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
	res := apply(t, `package main

import "github.com/mitchellh/go-homedir"

func main() {
	dir, err := homedir.Dir()
	_, _ = dir, err
}
`, "github.com/mitchellh/go-homedir")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if !strings.Contains(got, "os.UserHomeDir()") {
		t.Errorf("call not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "\"os\"") || strings.Contains(got, "go-homedir") {
		t.Errorf("import not repointed:\n%s", got)
	}
	if len(res.Edits[0].Renames) != 1 || res.Edits[0].Renames[0] != "Dir -> UserHomeDir" {
		t.Errorf("Renames = %v", res.Edits[0].Renames)
	}
}

// pkg/errors splits across two stdlib packages: New goes to errors, Errorf goes
// to fmt. This is the hardest mechanical case molt handles.
func TestSplitAcrossTwoStdlibPackages(t *testing.T) {
	res := apply(t, `package main

import (
	"os"

	"github.com/pkg/errors"
)

func run() error {
	if _, err := os.Open("x"); err != nil {
		return errors.Errorf("open failed: %v", err)
	}
	return errors.New("nothing to do")
}
`, "github.com/pkg/errors")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	for _, want := range []string{`"errors"`, `"fmt"`, `"os"`, "errors.New(", "fmt.Errorf("} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "github.com/pkg/errors") {
		t.Errorf("old import survived:\n%s", got)
	}
	edit := res.Edits[0]
	if len(edit.To) != 2 || edit.To[0] != "errors" || edit.To[1] != "fmt" {
		t.Errorf("To = %v, want [errors fmt]", edit.To)
	}
}

// When the file already imports one of the targets, molt must reuse it rather
// than emit a duplicate import.
func TestReusesExistingTargetImport(t *testing.T) {
	res := apply(t, `package main

import (
	"fmt"

	"github.com/pkg/errors"
)

func run() error {
	fmt.Println("working")
	return errors.Errorf("failed %d times", 3)
}
`, "github.com/pkg/errors")

	if !res.Changed() {
		t.Fatalf("no edit made; refusals: %+v", res.Refusals)
	}
	got := string(res.Source)
	if n := strings.Count(got, `"fmt"`); n != 1 {
		t.Errorf(`"fmt" appears %d times, want 1:%s`, n, got)
	}
	if strings.Contains(got, "github.com/pkg/errors") {
		t.Errorf("old import survived:\n%s", got)
	}
	if !strings.Contains(got, "fmt.Errorf(") {
		t.Errorf("call not rewritten:\n%s", got)
	}
}

// Promoting a single-line import to the parenthesised form is a go/printer
// detail that silently produces one-line garbage if Lparen is left invalid.
func TestPromotesSingleLineImport(t *testing.T) {
	res := apply(t, `package main

import "github.com/pkg/errors"

func run() error {
	if err := errors.New("x"); err != nil {
		return errors.Errorf("wrapped: %v", err)
	}
	return nil
}
`, "github.com/pkg/errors")

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
	res := apply(t, `package main

import errs "github.com/pkg/errors"

func run() error {
	if err := errs.New("x"); err != nil {
		return errs.Errorf("wrapped: %v", err)
	}
	return nil
}
`, "github.com/pkg/errors")

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
