package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModule materialises a throwaway module on disk. Fixtures live in the test
// rather than in testdata because scan skips directories named testdata, the
// same as the go command does.
func writeModule(t *testing.T, files map[string]string) string {
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
	return root
}

func TestUsageOfCollectsSymbols(t *testing.T) {
	root := writeModule(t, map[string]string{
		"main.go": `package main

import "github.com/google/uuid"

func main() {
	a := uuid.New()
	b := uuid.New()
	_ = uuid.NewString()
	_, _ = a, b
}
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(res.Files))
	}
	u := res.UsageOf("github.com/google/uuid", "uuid")
	got := u.SymbolNames()
	if len(got) != 2 || got[0] != "New" || got[1] != "NewString" {
		t.Fatalf("SymbolNames = %v, want [New NewString]", got)
	}
	if n := len(u.Symbols["New"]); n != 2 {
		t.Errorf("New refs = %d, want 2", n)
	}
	if u.TotalRefs() != 3 {
		t.Errorf("TotalRefs = %d, want 3", u.TotalRefs())
	}
	if ok, why := u.Rewritable(); !ok {
		t.Errorf("Rewritable = false (%s), want true", why)
	}
}

// A local variable named like the package makes selector attribution unsound.
// molt must notice and refuse, not guess.
func TestUsageOfDetectsShadowing(t *testing.T) {
	root := writeModule(t, map[string]string{
		"main.go": `package main

import "github.com/google/uuid"

type row struct{ uuid string }

func main() {
	uuid := row{uuid: "not-the-package"}
	_ = uuid.uuid
}
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	u := res.UsageOf("github.com/google/uuid", "uuid")
	if len(u.ShadowedFiles) != 1 {
		t.Fatalf("ShadowedFiles = %v, want main.go", u.ShadowedFiles)
	}
	if len(u.Symbols) != 0 {
		t.Errorf("Symbols = %v, want none attributed in a shadowed file", u.Symbols)
	}
	ok, why := u.Rewritable()
	if ok {
		t.Fatal("Rewritable = true, want false for a shadowed package name")
	}
	if why == "" {
		t.Error("Rewritable gave no reason")
	}
}

func TestUsageOfDotImport(t *testing.T) {
	root := writeModule(t, map[string]string{
		"main.go": `package main

import . "github.com/google/uuid"

func main() { _ = New() }
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	u := res.UsageOf("github.com/google/uuid", "uuid")
	if len(u.DotFiles) != 1 {
		t.Fatalf("DotFiles = %v, want main.go", u.DotFiles)
	}
	if ok, _ := u.Rewritable(); ok {
		t.Error("Rewritable = true, want false for a dot import")
	}
}

func TestUsageOfBlankImport(t *testing.T) {
	root := writeModule(t, map[string]string{
		"main.go": `package main

import _ "github.com/lib/pq"

func main() {}
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	u := res.UsageOf("github.com/lib/pq", "pq")
	if len(u.BlankFiles) != 1 {
		t.Fatalf("BlankFiles = %v", u.BlankFiles)
	}
	if ok, _ := u.Rewritable(); ok {
		t.Error("Rewritable = true, want false for a side-effect-only import")
	}
}

func TestUsageOfAlias(t *testing.T) {
	root := writeModule(t, map[string]string{
		"main.go": `package main

import guid "github.com/google/uuid"

func main() { _ = guid.New() }
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	u := res.UsageOf("github.com/google/uuid", "uuid")
	if len(u.AliasedFiles) != 1 {
		t.Fatalf("AliasedFiles = %v", u.AliasedFiles)
	}
	if got := u.SymbolNames(); len(got) != 1 || got[0] != "New" {
		t.Fatalf("SymbolNames = %v, want [New] resolved through the alias", got)
	}
}

func TestDirSkipsIgnoredDirs(t *testing.T) {
	body := "package x\n\nimport \"github.com/a/b\"\n\nvar _ = b.C\n"
	root := writeModule(t, map[string]string{
		"keep.go":             body,
		"vendor/dep.go":       body,
		"testdata/fixture.go": body,
		".hidden/h.go":        body,
		"_ignored/i.go":       body,
		"sub/keep.go":         body,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 2 {
		var paths []string
		for _, f := range res.Files {
			paths = append(paths, f.Path)
		}
		t.Fatalf("Files = %v, want keep.go and sub/keep.go only", paths)
	}
}

// Unparseable Go is a fact about the repository. molt records it and carries on.
func TestDirRecordsParseErrors(t *testing.T) {
	root := writeModule(t, map[string]string{
		"good.go": "package x\n",
		"bad.go":  "package x\n\nfunc ( {{{ \n",
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatalf("Dir returned an error for one bad file: %v", err)
	}
	if len(res.Files) != 1 {
		t.Errorf("Files = %d, want 1", len(res.Files))
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Path != "bad.go" {
		t.Fatalf("Skipped = %v, want bad.go", res.Skipped)
	}
}

func TestDirEmptyAndMissing(t *testing.T) {
	res, err := Dir(t.TempDir())
	if err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if len(res.Files) != 0 {
		t.Errorf("Files = %d, want 0", len(res.Files))
	}
	if _, err := Dir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("Dir on a missing path returned no error")
	}
}

func TestDirIncludesTestFiles(t *testing.T) {
	root := writeModule(t, map[string]string{
		"x_test.go": `package x

import "github.com/stretchr/testify/assert"

func TestX(t *testing.T) { assert.Equal(t, 1, 1) }
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || !res.Files[0].IsTest {
		t.Fatalf("test files must be scanned: %+v", res.Files)
	}
	u := res.UsageOf("github.com/stretchr/testify/assert", "assert")
	if got := u.SymbolNames(); len(got) != 1 || got[0] != "Equal" {
		t.Fatalf("SymbolNames = %v, want [Equal]", got)
	}
}

func TestPkgNameFor(t *testing.T) {
	cases := map[string]string{
		"github.com/google/uuid":        "uuid",
		"github.com/sirupsen/logrus":    "logrus",
		"gopkg.in/yaml.v3":              "yaml",
		"github.com/go-chi/chi/v5":      "chi",
		"golang.org/x/exp/slices":       "slices",
		"github.com/dustin/go-humanize": "humanize",
		"github.com/mattn/go-sqlite3":   "sqlite3",
		"github.com/json-iterator/go":   "jsoniter",
		"errors":                        "errors",
	}
	for path, want := range cases {
		// json-iterator is the known exception: the directory is "go" and the
		// package is "jsoniter". The corpus carries the real name; the guess is
		// only a fallback, so record the gap rather than pretend it works.
		got := PkgNameFor(path)
		if path == "github.com/json-iterator/go" {
			if got == want {
				t.Errorf("PkgNameFor(%q) unexpectedly guessed %q; the corpus is supposed to cover this case", path, got)
			}
			continue
		}
		if got != want {
			t.Errorf("PkgNameFor(%q) = %q, want %q", path, got, want)
		}
	}
}

// FileSymbols drives the per-file eligibility decision, so it must attribute
// each symbol to the right file rather than pooling them.
func TestFileSymbols(t *testing.T) {
	root := writeModule(t, map[string]string{
		"clean.go": `package a

import "github.com/google/uuid"

func f() { _ = uuid.New() }
`,
		"awkward.go": `package a

import "github.com/google/uuid"

func g() {
	_ = uuid.New()
	_ = uuid.Nil
}
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	byFile := res.UsageOf("github.com/google/uuid", "uuid").FileSymbols()

	if got := byFile["clean.go"]; len(got) != 1 || got[0] != "New" {
		t.Errorf("clean.go = %v, want [New]", got)
	}
	// Sorted, so the report and any rewrite decision are deterministic.
	if got := byFile["awkward.go"]; len(got) != 2 || got[0] != "New" || got[1] != "Nil" {
		t.Errorf("awkward.go = %v, want [New Nil]", got)
	}
	if len(byFile) != 2 {
		t.Errorf("FileSymbols covered %d files, want 2", len(byFile))
	}
}

func TestFileSymbolsEmpty(t *testing.T) {
	root := writeModule(t, map[string]string{"a.go": "package a\n"})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.UsageOf("github.com/nothing/here", "here").FileSymbols(); len(got) != 0 {
		t.Errorf("FileSymbols = %v, want empty", got)
	}
}

// Obstructed answers per file, where Rewritable answers for the whole module.
func TestObstructed(t *testing.T) {
	root := writeModule(t, map[string]string{
		"fine.go": `package a

import "github.com/google/uuid"

func f() { _ = uuid.New() }
`,
		"dotted.go": `package a

import . "github.com/google/uuid"

func g() { _ = New() }
`,
		"shadowed.go": `package a

import "github.com/google/uuid"

func h() {
	uuid := 1
	_ = uuid
}
`,
	})
	res, err := Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	u := res.UsageOf("github.com/google/uuid", "uuid")

	if obstructed, why := u.Obstructed("fine.go"); obstructed {
		t.Errorf("fine.go obstructed (%s), want clear", why)
	}
	for file, want := range map[string]string{
		"dotted.go":   "dot-imported",
		"shadowed.go": "shadowed",
	} {
		obstructed, why := u.Obstructed(file)
		if !obstructed {
			t.Errorf("%s not obstructed", file)
			continue
		}
		if !strings.Contains(why, want) {
			t.Errorf("%s reason = %q, want it to mention %q", file, why, want)
		}
	}
	// A file that does not import the path at all is not obstructed.
	if obstructed, _ := u.Obstructed("absent.go"); obstructed {
		t.Error("a file not importing the path was reported obstructed")
	}
}
