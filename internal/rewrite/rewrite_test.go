package rewrite

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"molt/internal/corpus"
)

func mig(t *testing.T, module string) corpus.Migration {
	t.Helper()
	m, ok := corpus.Lookup(module)
	if !ok {
		t.Fatalf("corpus has no row for %s", module)
	}
	return m
}

// apply runs File and fails the test on error, returning the new source.
func apply(t *testing.T, src string, modules ...string) *Result {
	t.Helper()
	var migs []corpus.Migration
	for _, m := range modules {
		migs = append(migs, mig(t, m))
	}
	return applyMigs(t, src, migs...)
}

// applyMigs is apply for callers that already have Migration values in hand,
// which the synthetic fakes below need.
func applyMigs(t *testing.T, src string, migs ...corpus.Migration) *Result {
	t.Helper()
	res, err := File("input.go", []byte(src), migs)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	// Whatever molt emits must be valid Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", res.Source, parser.SkipObjectResolution); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, res.Source)
	}
	return res
}

// fakeRename is a synthetic single-target migration with a real symbol
// rename, used to exercise the rewriter's rename mechanics independently of
// which real corpus row currently renames a symbol. None does at present:
// pkg/errors.New/Errorf were removed for dropping stack traces (see
// corpus.go), and every remaining mechanical row keeps its symbol names
// unchanged.
var fakeRename = corpus.Migration{
	Module:   "example.com/fake/rename",
	Pkg:      "rename",
	Target:   "os",
	Verified: true,
	Symbols: map[string]corpus.Repl{
		"Dir": {Symbol: "UserHomeDir"},
	},
}

// fakeSplit is a synthetic multi-target migration used to exercise the
// rewriter's split-across-packages machinery independently of which real
// corpus row currently splits. pkg/errors no longer does: New and Errorf,
// its only two-target symbols, were both removed for silently dropping a
// stack trace (see corpus.go).
var fakeSplit = corpus.Migration{
	Module:   "example.com/fake/split",
	Pkg:      "split",
	Target:   "errors",
	Verified: true,
	Symbols: map[string]corpus.Repl{
		"A": {Target: "errors", Symbol: "New"},
		"B": {Target: "fmt", Symbol: "Errorf"},
	},
}

func TestImportPathSwapOnly(t *testing.T) {
	res := apply(t, `package main

import (
	"fmt"

	"golang.org/x/exp/slices"
)

func main() {
	s := []int{3, 1, 2}
	slices.Sort(s)
	fmt.Println(slices.Contains(s, 2))
}
`, "golang.org/x/exp/slices")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if strings.Contains(got, "golang.org/x/exp/slices") {
		t.Errorf("old import survived:\n%s", got)
	}
	if !strings.Contains(got, "\"slices\"") {
		t.Errorf("stdlib import missing:\n%s", got)
	}
	// The call sites keep the same qualifier and symbol.
	if !strings.Contains(got, "slices.Sort(s)") || !strings.Contains(got, "slices.Contains(s, 2)") {
		t.Errorf("call sites changed unexpectedly:\n%s", got)
	}
	if res.Edits[0].Symbols != 2 {
		t.Errorf("Symbols = %d, want 2", res.Edits[0].Symbols)
	}
}

// The Func variants changed comparison semantics. molt must refuse the file
// outright rather than swap the import and silently break the sort.
func TestRefusesBlockedSymbol(t *testing.T) {
	res := apply(t, `package main

import "golang.org/x/exp/slices"

func main() {
	s := []int{3, 1, 2}
	slices.SortFunc(s, func(a, b int) bool { return a < b })
}
`, "golang.org/x/exp/slices")

	if res.Changed() {
		t.Fatalf("molt edited a file using SortFunc:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "SortFunc") {
		t.Fatalf("Refusals = %+v, want one naming SortFunc", res.Refusals)
	}
	if string(res.Source) == "" {
		t.Error("source was emptied")
	}
}

// maps.Keys kept its name and changed its return type from a slice to an
// iterator. Refusing is the only safe answer.
func TestRefusesIteratorChange(t *testing.T) {
	res := apply(t, `package main

import "golang.org/x/exp/maps"

func main() {
	m := map[string]int{"a": 1}
	_ = maps.Keys(m)
}
`, "golang.org/x/exp/maps")

	if res.Changed() {
		t.Fatalf("molt edited a file using maps.Keys:\n%s", res.Source)
	}
	if len(res.Refusals) == 0 || !strings.Contains(res.Refusals[0].Reason, "Keys") {
		t.Fatalf("Refusals = %+v, want one naming Keys", res.Refusals)
	}
}

func TestSymbolRename(t *testing.T) {
	res := applyMigs(t, `package main

import "example.com/fake/rename"

func main() {
	dir, err := rename.Dir()
	_, _ = dir, err
}
`, fakeRename)

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if !strings.Contains(got, "os.UserHomeDir()") {
		t.Errorf("call not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "\"os\"") || strings.Contains(got, "fake/rename") {
		t.Errorf("import not repointed:\n%s", got)
	}
	if len(res.Edits[0].Renames) != 1 || res.Edits[0].Renames[0] != "Dir -> UserHomeDir" {
		t.Errorf("Renames = %v", res.Edits[0].Renames)
	}
}

// A migration can split across two stdlib packages. This is the hardest
// mechanical case molt handles. No real corpus row does this today (see
// fakeSplit's doc comment), so a synthetic migration exercises the mechanics.
func TestSplitAcrossTwoStdlibPackages(t *testing.T) {
	res := applyMigs(t, `package main

import (
	"os"

	"example.com/fake/split"
)

func run() error {
	if _, err := os.Open("x"); err != nil {
		return split.B("open failed: %v", err)
	}
	return split.A("nothing to do")
}
`, fakeSplit)

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	for _, want := range []string{`"errors"`, `"fmt"`, `"os"`, "errors.New(", "fmt.Errorf("} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fake/split") {
		t.Errorf("old import survived:\n%s", got)
	}
	edit := res.Edits[0]
	if len(edit.To) != 2 || edit.To[0] != "errors" || edit.To[1] != "fmt" {
		t.Errorf("To = %v, want [errors fmt]", edit.To)
	}
}

// When the file already imports one of the targets under its canonical
// unaliased name, molt must reuse it rather than emit a duplicate import.
func TestReusesExistingTargetImport(t *testing.T) {
	res := applyMigs(t, `package main

import (
	"fmt"

	"example.com/fake/split"
)

func run() error {
	fmt.Println("working")
	return split.B("failed %d times", 3)
}
`, fakeSplit)

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if n := strings.Count(got, `"fmt"`); n != 1 {
		t.Errorf(`"fmt" appears %d times, want 1:%s`, n, got)
	}
	if strings.Contains(got, "fake/split") {
		t.Errorf("old import survived:\n%s", got)
	}
	if !strings.Contains(got, "fmt.Errorf(") {
		t.Errorf("call not rewritten:\n%s", got)
	}
}

// Reuse must be refused, not silently corrupted, when the existing import of
// a target path uses an incompatible qualifier — the bug an automated review
// caught: a file already importing fmt under an alias would otherwise end up
// with call sites qualified "fmt" and no unaliased fmt import binding that
// name at all.
func TestRefusesReuseOfIncompatibleQualifier(t *testing.T) {
	res := applyMigs(t, `package main

import (
	f "fmt"

	"example.com/fake/split"
)

func run() error {
	f.Println("working")
	return split.B("failed %d times", 3)
}
`, fakeSplit)

	if res.Changed() {
		t.Fatalf("molt reused an incompatibly aliased import:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "already imported as") {
		t.Fatalf("Refusals = %+v, want one about the conflicting alias", res.Refusals)
	}
}

// The qualifier a migration would introduce must not collide with a local
// binding elsewhere in the file — the second bug an automated review caught:
// rewriting to fmt.Errorf when a local variable named fmt exists would bind
// to that variable, not the package.
func TestRefusesTargetQualifierCollision(t *testing.T) {
	res := applyMigs(t, `package main

import "example.com/fake/split"

func run() error {
	fmt := 5
	_ = fmt
	return split.B("failed")
}
`, fakeSplit)

	if res.Changed() {
		t.Fatalf("molt introduced a qualifier colliding with a local binding:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "shadowed") {
		t.Fatalf("Refusals = %+v, want one about the shadowed qualifier", res.Refusals)
	}
}

// Promoting a single-line import to the parenthesised form is a go/printer
// detail that silently produces one-line garbage if Lparen is left invalid.
func TestPromotesSingleLineImport(t *testing.T) {
	res := applyMigs(t, `package main

import "example.com/fake/split"

func run() error {
	if err := split.A("x"); err != nil {
		return split.B("wrapped: %v", err)
	}
	return nil
}
`, fakeSplit)

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if !strings.Contains(got, "import (") {
		t.Errorf("import block not promoted to parenthesised form:\n%s", got)
	}
	if !strings.Contains(got, `"errors"`) || !strings.Contains(got, `"fmt"`) {
		t.Errorf("both targets not imported:\n%s", got)
	}
}

func TestAliasKeepsLocalName(t *testing.T) {
	res := apply(t, `package main

import xslices "golang.org/x/exp/slices"

func main() {
	s := []int{2, 1}
	xslices.Sort(s)
}
`, "golang.org/x/exp/slices")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if !strings.Contains(got, "xslices.Sort(s)") {
		t.Errorf("aliased call site must keep its qualifier:\n%s", got)
	}
	if !strings.Contains(got, `xslices "slices"`) {
		t.Errorf("alias not preserved on the new path:\n%s", got)
	}
}

// An aliased import cannot be split across two packages: there is only one
// local name to go around.
func TestRefusesAliasedSplit(t *testing.T) {
	res := applyMigs(t, `package main

import fs "example.com/fake/split"

func run() error {
	if err := fs.A("x"); err != nil {
		return fs.B("wrapped: %v", err)
	}
	return nil
}
`, fakeSplit)

	if res.Changed() {
		t.Fatalf("molt split an aliased import:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "aliased") {
		t.Fatalf("Refusals = %+v", res.Refusals)
	}
}

func TestRefusesShadowedName(t *testing.T) {
	res := apply(t, `package main

import "golang.org/x/exp/slices"

func main() {
	slices := []int{1, 2}
	_ = slices
}
`, "golang.org/x/exp/slices")

	if res.Changed() {
		t.Fatalf("molt edited a file where the package name is shadowed:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "shadowed") {
		t.Fatalf("Refusals = %+v", res.Refusals)
	}
}

// google/uuid.New and the stdlib's have identical signatures, so this is a pure
// import swap.
func TestUUIDImportSwap(t *testing.T) {
	res := apply(t, `package main

import (
	"fmt"

	"github.com/google/uuid"
)

func main() {
	id := uuid.New()
	parsed, err := uuid.Parse(id.String())
	fmt.Println(parsed, err)
}
`, "github.com/google/uuid")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if strings.Contains(got, "github.com/google/uuid") {
		t.Errorf("old import survived:\n%s", got)
	}
	if !strings.Contains(got, `"uuid"`) {
		t.Errorf("stdlib uuid not imported:\n%s", got)
	}
	// Call sites are unchanged: the names match exactly.
	if !strings.Contains(got, "uuid.New()") || !strings.Contains(got, "uuid.Parse(") {
		t.Errorf("call sites altered:\n%s", got)
	}
}

// uuid.Nil is a package variable in google/uuid and a function in the standard
// library, so a blind import swap produces code that does not compile. This is
// the subtlest trap in the corpus and the refusal is pinned here.
func TestRefusesUUIDNilVarToFunc(t *testing.T) {
	res := apply(t, `package main

import "github.com/google/uuid"

func main() {
	id := uuid.New()
	if id == uuid.Nil {
		panic("nil uuid")
	}
}
`, "github.com/google/uuid")

	if res.Changed() {
		t.Fatalf("molt rewrote a file comparing against uuid.Nil:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "Nil") {
		t.Fatalf("Refusals = %+v, want one naming Nil", res.Refusals)
	}
}

// uuid.NewRandom returns (UUID, error) in google/uuid; the stdlib's NewV4
// returns a UUID alone, so the arity of the call site changes.
func TestRefusesUUIDArityChange(t *testing.T) {
	res := apply(t, `package main

import "github.com/google/uuid"

func main() {
	id, err := uuid.NewRandom()
	_, _ = id, err
}
`, "github.com/google/uuid")

	if res.Changed() {
		t.Fatalf("molt rewrote a NewRandom call:\n%s", res.Source)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "NewRandom") {
		t.Fatalf("Refusals = %+v, want one naming NewRandom", res.Refusals)
	}
}

func TestCommentsSurvive(t *testing.T) {
	res := apply(t, `// Package main does a thing.
package main

import (
	// sorting helpers
	"golang.org/x/exp/slices"
)

// main is the entry point.
func main() {
	s := []int{2, 1}
	slices.Sort(s) // sort in place
}
`, "golang.org/x/exp/slices")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	for _, want := range []string{
		"// Package main does a thing.",
		"// main is the entry point.",
		"// sort in place",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comment lost: %s\n%s", want, got)
		}
	}
}

func TestNoMatchingImportIsNoOp(t *testing.T) {
	src := `package main

import "fmt"

func main() { fmt.Println("hi") }
`
	res := apply(t, src, "golang.org/x/exp/slices")
	if res.Changed() {
		t.Error("edit reported for a file that does not import the module")
	}
	if string(res.Source) != src {
		t.Error("source modified")
	}
}

func TestUnparseableInputErrors(t *testing.T) {
	_, err := File("bad.go", []byte("package main\n\nfunc ( {{{\n"), []corpus.Migration{mig(t, "golang.org/x/exp/slices")})
	if err == nil {
		t.Fatal("File accepted unparseable input")
	}
}

// Two independent migrations in one file must both land.
func TestMultipleMigrationsOneFile(t *testing.T) {
	res := apply(t, `package main

import (
	"golang.org/x/exp/slices"
	"golang.org/x/net/context"
)

func main(ctx context.Context) {
	s := []int{2, 1}
	slices.Sort(s)
	_ = ctx
}
`, "golang.org/x/exp/slices", "golang.org/x/net/context")

	if len(res.Edits) != 2 {
		t.Fatalf("Edits = %d, want 2; refusals %+v", len(res.Edits), res.Refusals)
	}
	got := string(res.Source)
	if strings.Contains(got, "golang.org/x") {
		t.Errorf("an x/ import survived:\n%s", got)
	}
}
