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
	if f.Unknown["replace"] != 1 {
		t.Errorf("replace not recorded: %v", f.Unknown)
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
