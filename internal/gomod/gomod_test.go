package gomod

import (
	"errors"
	"strings"
	"testing"
)

func TestParseBlockAndInline(t *testing.T) {
	src := `module github.com/example/app

go 1.24

toolchain go1.25.1

require github.com/google/uuid v1.6.0

require (
	github.com/sirupsen/logrus v1.9.3
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842 // indirect
	golang.org/x/sys v0.20.0 // indirect ; extra words
)

replace github.com/old/pkg => github.com/new/pkg v1.0.0
`
	f, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Module != "github.com/example/app" {
		t.Errorf("Module = %q", f.Module)
	}
	if f.GoVersion != "1.24" {
		t.Errorf("GoVersion = %q", f.GoVersion)
	}
	if f.Toolchain != "go1.25.1" {
		t.Errorf("Toolchain = %q", f.Toolchain)
	}
	if got := len(f.Requires); got != 4 {
		t.Fatalf("Requires = %d, want 4", got)
	}
	if got := len(f.Direct()); got != 2 {
		t.Errorf("Direct = %d, want 2", got)
	}
	if got := len(f.Indirect()); got != 2 {
		t.Errorf("Indirect = %d, want 2", got)
	}
	// replace is parsed structurally now, not just counted.
	if len(f.Unknown) != 0 {
		t.Errorf("Unknown = %v, want none", f.Unknown)
	}
	r, ok := f.Replaced("github.com/old/pkg")
	if !ok || r.New != "github.com/new/pkg" || r.NewVersion != "v1.0.0" {
		t.Errorf("Replaced(github.com/old/pkg) = %+v, ok=%v", r, ok)
	}
	// Requires must be sorted so that reports are deterministic.
	for i := 1; i < len(f.Requires); i++ {
		if f.Requires[i-1].Path > f.Requires[i].Path {
			t.Fatalf("Requires not sorted: %v", f.Requires)
		}
	}
}

func TestParseNoRequireBlock(t *testing.T) {
	f, err := Parse(strings.NewReader("module molt\n\ngo 1.25\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Requires) != 0 {
		t.Errorf("Requires = %v, want none", f.Requires)
	}
}

func TestParseMissingModule(t *testing.T) {
	_, err := Parse(strings.NewReader("go 1.25\n"))
	if !errors.Is(err, ErrNoModule) {
		t.Fatalf("err = %v, want ErrNoModule", err)
	}
}

func TestParseTolerance(t *testing.T) {
	// A truncated require line, a bare word, and a stray paren must not stop
	// molt from reporting the requirements it could read.
	src := `module m
require (
	github.com/a/b v1.0.0
	github.com/broken
	nonsense
)
require
`
	f, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Requires) != 1 || f.Requires[0].Path != "github.com/a/b" {
		t.Fatalf("Requires = %v, want only github.com/a/b", f.Requires)
	}
}

func TestParseQuotedPath(t *testing.T) {
	f, err := Parse(strings.NewReader("module \"github.com/ex/app\"\nrequire \"github.com/a/b\" \"v1.0.0\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Module != "github.com/ex/app" {
		t.Errorf("Module = %q", f.Module)
	}
	if len(f.Requires) != 1 || f.Requires[0].Version != "v1.0.0" {
		t.Errorf("Requires = %v", f.Requires)
	}
}

func TestParseCommentOnlyIndirectWord(t *testing.T) {
	// "// indirectly needed" must not be read as the indirect marker.
	f, err := Parse(strings.NewReader("module m\nrequire github.com/a/b v1.0.0 // indirectly needed\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Requires[0].Indirect {
		t.Error("Indirect = true, want false")
	}
}

func TestSumModules(t *testing.T) {
	src := `github.com/a/b v1.0.0 h1:abc=
github.com/a/b v1.0.0/go.mod h1:def=
github.com/c/d v2.0.0 h1:ghi=
malformed line
`
	mods, err := SumModules(strings.NewReader(src))
	if err != nil {
		t.Fatalf("SumModules: %v", err)
	}
	if len(mods) != 2 || mods[0] != "github.com/a/b" || mods[1] != "github.com/c/d" {
		t.Fatalf("mods = %v", mods)
	}
}

func TestParseReplaceSingleLine(t *testing.T) {
	f, err := Parse(strings.NewReader("module m\n\nreplace github.com/old/pkg => github.com/new/pkg v1.2.3\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, ok := f.Replaced("github.com/old/pkg")
	if !ok {
		t.Fatal("Replaced returned false for a directly-parsed replace")
	}
	if r.New != "github.com/new/pkg" || r.NewVersion != "v1.2.3" {
		t.Errorf("Replace = %+v", r)
	}
}

func TestParseReplaceLocalFork(t *testing.T) {
	// A local filesystem replacement has no version on the right-hand side.
	f, err := Parse(strings.NewReader("module m\n\nreplace github.com/pkg/errors => ../local-fork\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, ok := f.Replaced("github.com/pkg/errors")
	if !ok {
		t.Fatal("Replaced returned false")
	}
	if r.New != "../local-fork" || r.NewVersion != "" {
		t.Errorf("Replace = %+v", r)
	}
}

func TestParseReplaceBlock(t *testing.T) {
	src := `module m

replace (
	github.com/a/b => github.com/a/b v1.0.1
	github.com/c/d v1.0.0 => ../fork
)
`
	f, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Replaces) != 2 {
		t.Fatalf("Replaces = %+v, want 2", f.Replaces)
	}
	r1, ok := f.Replaced("github.com/a/b")
	if !ok || r1.NewVersion != "v1.0.1" {
		t.Errorf("github.com/a/b replace = %+v, ok=%v", r1, ok)
	}
	r2, ok := f.Replaced("github.com/c/d")
	if !ok || r2.OldVersion != "v1.0.0" || r2.New != "../fork" {
		t.Errorf("github.com/c/d replace = %+v, ok=%v", r2, ok)
	}
}

func TestReplacedMiss(t *testing.T) {
	f, err := Parse(strings.NewReader("module m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Replaced("github.com/not/replaced"); ok {
		t.Error("Replaced invented a redirect")
	}
}

func TestGoVersionAtLeast(t *testing.T) {
	cases := []struct {
		have, want string
		ok         bool
	}{
		{"1.27", "go1.27", true},
		{"1.28", "go1.27", true},
		{"1.26", "go1.27", false},
		{"1.27.3", "go1.27", true},
		{"2.0", "go1.27", true},
		{"", "go1.27", false},
		{"", "go1.0", true}, // go1.0 imposes no real floor
		{"1.0", "go1.0", true},
		{"garbage", "go1.27", false},
		{"1.21", "go1.21", true},
		{"1.20", "go1.21", false},
	}
	for _, c := range cases {
		if got := GoVersionAtLeast(c.have, c.want); got != c.ok {
			t.Errorf("GoVersionAtLeast(%q, %q) = %v, want %v", c.have, c.want, got, c.ok)
		}
	}
}

func TestSatisfiesGo(t *testing.T) {
	f := &File{GoVersion: "1.24"}
	if f.SatisfiesGo("go1.27") {
		t.Error("a go1.24 module satisfies go1.27, want false")
	}
	if !f.SatisfiesGo("go1.21") {
		t.Error("a go1.24 module does not satisfy go1.21, want true")
	}
}

// A replace directive redirects a module, and a module can contain many
// packages. Redirecting golang.org/x/exp must also redirect the packages
// inside it, like golang.org/x/exp/slices, even though the directive never
// names that path.
func TestReplacedCoversSubpackages(t *testing.T) {
	f, err := Parse(strings.NewReader("module m\n\nreplace golang.org/x/exp => ../fork\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Replaced("golang.org/x/exp/slices"); !ok {
		t.Error("Replaced(golang.org/x/exp/slices) = false, want true: it is a package inside the replaced module")
	}
	if _, ok := f.Replaced("golang.org/x/expfoo"); ok {
		t.Error("Replaced(golang.org/x/expfoo) = true, want false: a bare prefix is not a subpackage")
	}
}
