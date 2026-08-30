package analyze

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"molt/internal/gomod"
	"molt/internal/scan"
)

// fixture writes a module to disk and returns its parsed go.mod and scan result.
func fixture(t *testing.T, goMod string, files map[string]string) (*gomod.File, *scan.Result) {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var mod *gomod.File
	if goMod != "" {
		var err error
		mod, err = gomod.Parse(strings.NewReader(goMod))
		if err != nil {
			t.Fatal(err)
		}
	}
	src, err := scan.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	return mod, src
}

func find(r *Report, module string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].Module == module {
			return &r.Findings[i]
		}
	}
	return nil
}

func TestMechanicalMigrationIsAuto(t *testing.T) {
	mod, src := fixture(t, "module m\n\nrequire golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842\n", map[string]string{
		"a.go": `package a

import "golang.org/x/exp/slices"

func f(s []int) { slices.Sort(s) }
`,
	})
	r := Run(mod, src)

	f := find(r, "golang.org/x/exp/slices")
	if f == nil {
		t.Fatal("no finding for x/exp/slices")
	}
	if !f.Auto {
		t.Errorf("Auto = false, blockers %v", f.Blockers)
	}
	if f.Target != "slices" || f.Since != "go1.21" {
		t.Errorf("Target = %q, Since = %q", f.Target, f.Since)
	}
	if len(f.Symbols) != 1 || f.Symbols[0].Name != "Sort" || f.Symbols[0].Count != 1 {
		t.Errorf("Symbols = %+v", f.Symbols)
	}
	if r.Removable != 1 {
		t.Errorf("Removable = %d, want 1", r.Removable)
	}
}

// A blocked symbol anywhere in the module downgrades the whole migration.
func TestBlockedSymbolDowngradesToAdvisory(t *testing.T) {
	mod, src := fixture(t, "module m\n", map[string]string{
		"a.go": `package a

import "golang.org/x/exp/slices"

func f(s []int) {
	slices.Sort(s)
	slices.SortFunc(s, func(x, y int) bool { return x < y })
}
`,
	})
	r := Run(mod, src)

	f := find(r, "golang.org/x/exp/slices")
	if f == nil {
		t.Fatal("no finding")
	}
	if f.Auto {
		t.Error("Auto = true despite SortFunc")
	}
	var blocked *SymbolUse
	for i := range f.Symbols {
		if f.Symbols[i].Name == "SortFunc" {
			blocked = &f.Symbols[i]
		}
	}
	if blocked == nil || blocked.Blocked == "" {
		t.Fatalf("SortFunc not marked blocked: %+v", f.Symbols)
	}
	if len(f.Blockers) == 0 {
		t.Error("no blockers recorded")
	}
	if r.Advisory != 1 || r.Removable != 0 {
		t.Errorf("Advisory = %d, Removable = %d, want 1 and 0", r.Advisory, r.Removable)
	}
}

func TestShadowingDowngradesToAdvisory(t *testing.T) {
	mod, src := fixture(t, "module m\n", map[string]string{
		"a.go": `package a

import "golang.org/x/exp/slices"

func f() {
	slices := []int{1}
	_ = slices
}
`,
	})
	r := Run(mod, src)
	f := find(r, "golang.org/x/exp/slices")
	if f == nil {
		t.Fatal("no finding; a shadowed import is still worth reporting")
	}
	if f.Auto {
		t.Error("Auto = true for a shadowed package name")
	}
	joined := strings.Join(f.Blockers, " ")
	if !strings.Contains(joined, "shadowed") {
		t.Errorf("blockers = %v, want one mentioning shadowing", f.Blockers)
	}
}

func TestOrphanDetection(t *testing.T) {
	mod, src := fixture(t, `module m

require (
	github.com/used/pkg v1.0.0
	github.com/unused/pkg v1.0.0
)
`, map[string]string{
		"a.go": `package a

import "github.com/used/pkg"

var _ = pkg.Thing
`,
	})
	r := Run(mod, src)

	f := find(r, "github.com/unused/pkg")
	if f == nil || f.Kind != KindOrphan {
		t.Fatalf("unused dependency not reported as an orphan: %+v", r.Findings)
	}
	if f.Note == "" {
		t.Error("an orphan finding must warn about build-tagged files")
	}
	if find(r, "github.com/used/pkg") != nil {
		t.Error("an imported dependency was reported as an orphan")
	}
	if r.Orphans != 1 {
		t.Errorf("Orphans = %d, want 1", r.Orphans)
	}
}

// A prefix match alone would report github.com/a/bc as satisfying
// github.com/a/b. It must not.
func TestOrphanPrefixIsNotEnough(t *testing.T) {
	mod, src := fixture(t, "module m\n\nrequire github.com/a/b v1.0.0\n", map[string]string{
		"a.go": `package a

import "github.com/a/bc"

var _ = bc.Thing
`,
	})
	r := Run(mod, src)
	if find(r, "github.com/a/b") == nil {
		t.Error("github.com/a/b should be an orphan; github.com/a/bc is a different module")
	}
}

func TestSubpackageSatisfiesRequire(t *testing.T) {
	mod, src := fixture(t, "module m\n\nrequire github.com/a/b v1.0.0\n", map[string]string{
		"a.go": `package a

import "github.com/a/b/sub"

var _ = sub.Thing
`,
	})
	r := Run(mod, src)
	if f := find(r, "github.com/a/b"); f != nil && f.Kind == KindOrphan {
		t.Error("importing a subpackage must satisfy the require")
	}
}

func TestKeptListsUnknownDependencies(t *testing.T) {
	mod, src := fixture(t, "module m\n\nrequire github.com/aws/aws-sdk-go-v2 v1.30.3\n", map[string]string{
		"a.go": `package a

import "github.com/aws/aws-sdk-go-v2/aws"

var _ = aws.Config{}
var _ = aws.NewConfig
`,
	})
	r := Run(mod, src)
	if len(r.Kept) != 1 || r.Kept[0].Module != "github.com/aws/aws-sdk-go-v2" {
		t.Fatalf("Kept = %+v", r.Kept)
	}
	if r.Kept[0].Refs == 0 {
		t.Error("Kept entry records no uses")
	}
}

func TestAmplification(t *testing.T) {
	r := &Report{DirectDeps: 10, SumModules: 200}
	if got := r.Amplification(); got != 20 {
		t.Errorf("Amplification = %v, want 20", got)
	}
	// Missing data must give zero rather than a divide by zero or a lie.
	if got := (&Report{DirectDeps: 0, SumModules: 200}).Amplification(); got != 0 {
		t.Errorf("Amplification with no direct deps = %v, want 0", got)
	}
	if got := (&Report{DirectDeps: 10, SumModules: 0}).Amplification(); got != 0 {
		t.Errorf("Amplification with no go.sum = %v, want 0", got)
	}
}

func TestNoGoModStillAnalysesSource(t *testing.T) {
	_, src := fixture(t, "", map[string]string{
		"a.go": `package a

import "golang.org/x/exp/slices"

func f(s []int) { slices.Sort(s) }
`,
	})
	r := Run(nil, src)
	if r.GoFiles != 1 {
		t.Errorf("GoFiles = %d", r.GoFiles)
	}
	if find(r, "golang.org/x/exp/slices") == nil {
		t.Error("source findings must work without a go.mod")
	}
	if r.DirectDeps != 0 || r.Orphans != 0 {
		t.Error("dependency counts must be zero with no go.mod")
	}
}

func TestCleanModule(t *testing.T) {
	mod, src := fixture(t, "module m\n\ngo 1.25\n", map[string]string{
		"a.go": "package a\n\nimport \"fmt\"\n\nvar _ = fmt.Println\n",
	})
	r := Run(mod, src)
	if !r.Clean() {
		t.Errorf("Clean = false for a stdlib-only module: %+v", r.Findings)
	}
}

// Findings are ordered: actionable first, then advisory, then orphans, each
// alphabetically. Output determinism depends on this being a total order.
func TestFindingOrderIsDeterministic(t *testing.T) {
	goMod := `module m

require (
	github.com/gorilla/mux v1.8.1
	github.com/sirupsen/logrus v1.9.3
	github.com/unused/one v1.0.0
	github.com/unused/two v1.0.0
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842
)
`
	files := map[string]string{
		"a.go": `package a

import (
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
)

func f(s []int) {
	slices.Sort(s)
	_ = mux.NewRouter()
	logrus.Info("x")
}
`,
	}
	mod, src := fixture(t, goMod, files)

	var first []string
	for i := 0; i < 8; i++ {
		r := Run(mod, src)
		var order []string
		for _, f := range r.Findings {
			order = append(order, f.Kind+":"+f.Module)
		}
		if first == nil {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("finding order varies:\n%v\n%v", first, order)
		}
	}

	// The actionable migration must come before the advisory ones, and orphans last.
	if first[0] != "migration:golang.org/x/exp/slices" {
		t.Errorf("first finding = %q, want the auto-applicable migration", first[0])
	}
	if !strings.HasPrefix(first[len(first)-1], "orphan:") {
		t.Errorf("last finding = %q, want an orphan", first[len(first)-1])
	}
}

func TestSkippedFilesAreReported(t *testing.T) {
	_, src := fixture(t, "", map[string]string{
		"good.go": "package a\n",
		"bad.go":  "package a\n\nfunc ( {{{\n",
	})
	r := Run(nil, src)
	if len(r.Skipped) != 1 || r.Skipped[0].Path != "bad.go" {
		t.Fatalf("Skipped = %+v", r.Skipped)
	}
	if r.Skipped[0].Reason == "" {
		t.Error("skipped file has no reason")
	}
}
