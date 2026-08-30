package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"-version"}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.HasPrefix(out.String(), "molt "+version) {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unknown flag", []string{"-nope"}, exitUsage},
		{"two directories", []string{".", ".."}, exitUsage},
		{"apply and diff together", []string{"-apply", "-diff", "."}, exitUsage},
		{"missing path", []string{filepath.Join(t.TempDir(), "absent")}, exitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := run(tc.args, &out, &errBuf); code != tc.want {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, tc.want, errBuf.String())
			}
		})
	}
}

// A file where a directory is expected must be a usage error, not a panic.
func TestPathIsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(file, []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	if code := run([]string{file}, &out, &errBuf); code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
}

// A directory with no go.mod is reported on, not rejected.
func TestNoGoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	if code := run([]string{dir}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "no go.mod") {
		t.Errorf("expected a note on stderr, got %q", errBuf.String())
	}
}

func TestJSONIsValidAndPopulated(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"-json", filepath.Join("..", "..", "testdata", "example-app")}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errBuf.String())
	}

	var report struct {
		Module    string `json:"module"`
		GoFiles   int    `json:"go_files"`
		Removable int    `json:"removable"`
		Findings  []struct {
			Kind   string `json:"kind"`
			Module string `json:"module"`
			Auto   bool   `json:"auto"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if report.Module != "github.com/example/orders" {
		t.Errorf("module = %q", report.Module)
	}
	if report.GoFiles == 0 || len(report.Findings) == 0 || report.Removable == 0 {
		t.Errorf("report looks empty: %+v", report)
	}
}

// The -exit-code flag is what a CI job gates on.
func TestExitCodeFlag(t *testing.T) {
	dirty := filepath.Join("..", "..", "testdata", "example-app")
	var out, errBuf bytes.Buffer
	if code := run([]string{"-exit-code", dirty}, &out, &errBuf); code != exitFindings {
		t.Errorf("exit = %d for a module with findings, want %d", code, exitFindings)
	}

	// molt's own module has no dependencies, so it is clean.
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-exit-code", filepath.Join("..", "..")}, &out, &errBuf); code != exitOK {
		t.Errorf("exit = %d for a clean module, want %d", code, exitOK)
	}
}

// Two runs over the same tree must produce identical bytes. Determinism is what
// the reproducible-build claim rests on, so it is asserted rather than assumed.
func TestOutputIsDeterministic(t *testing.T) {
	for _, args := range [][]string{
		{filepath.Join("..", "..", "testdata", "example-app")},
		{"-v", filepath.Join("..", "..", "testdata", "example-app")},
		{"-json", filepath.Join("..", "..", "testdata", "example-app")},
	} {
		var first, second bytes.Buffer
		var discard bytes.Buffer
		run(args, &first, &discard)
		run(args, &second, &discard)
		if first.String() != second.String() {
			t.Errorf("output differs between runs for %v", args)
		}
	}
}

// -diff must not touch the tree.
func TestDiffWritesNothing(t *testing.T) {
	dir := copyFixture(t, "tidy-app")
	before := snapshot(t, dir)

	var out, errBuf bytes.Buffer
	if code := run([]string{"-diff", dir}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "--- a/main.go") {
		t.Errorf("expected a patch on stdout, got:\n%s", out.String())
	}
	if after := snapshot(t, dir); after != before {
		t.Error("-diff modified the tree")
	}
}

// The payoff test. Applying every mechanical migration to the tidy-app fixture
// must leave a module that compiles and passes its tests with no third-party
// code at all.
//
// GOPROXY is set to off and GOFLAGS to -mod=mod, so a build that still needed a
// module download would fail rather than quietly fetch one. That is what makes
// this a zero-dependency proof and not just a compile check.
func TestApplyProducesBuildableZeroDepModule(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the go toolchain")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	dir := copyFixture(t, "tidy-app")

	var out, errBuf bytes.Buffer
	if code := run([]string{"-apply", dir}, &out, &errBuf); code != exitOK {
		t.Fatalf("apply exit = %d (stderr: %s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "rewrote") {
		t.Fatalf("nothing was rewritten:\n%s\n%s", out.String(), errBuf.String())
	}

	// molt deliberately does not edit go.mod; it tells the operator to run
	// go mod tidy. Removing the requires here is that step, done explicitly so
	// the test does not depend on the network that go mod tidy would want.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module github.com/example/tidy\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"build", "./..."}, {"test", "./..."}} {
		cmd := exec.Command(goBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod")
		combined, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s failed after molt -apply: %v\n%s",
				strings.Join(args, " "), err, combined)
		}
	}

	// Confirm the rewritten source really has no third-party imports left.
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, banned := range []string{"github.com/", "golang.org/x/"} {
			if bytes.Contains(body, []byte(banned)) {
				t.Errorf("%s still imports %s", d.Name(), banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// -apply must leave alone the files it refuses. example-app uses gorilla/mux and
// logrus, which are advisory, so those files must come out byte-identical.
func TestApplyLeavesAdvisoryFilesUntouched(t *testing.T) {
	dir := copyFixture(t, "example-app")
	beforeMain, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := run([]string{"-apply", dir}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errBuf.String())
	}

	afterMain, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeMain, afterMain) {
		t.Error("main.go was modified, but mux and logrus are advisory-only")
	}

	// store.go carries three mechanical migrations, so all three must land.
	store, err := os.ReadFile(filepath.Join(dir, "store", "store.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(store)
	for _, gone := range []string{
		"github.com/pkg/errors",
		"github.com/google/uuid",
		"golang.org/x/exp/slices",
	} {
		if strings.Contains(body, gone) {
			t.Errorf("%s survived a mechanical migration", gone)
		}
	}
	for _, want := range []string{`"errors"`, `"fmt"`, `"slices"`, `"uuid"`} {
		if !strings.Contains(body, want) {
			t.Errorf("store.go missing stdlib import %s\n%s", want, body)
		}
	}
}

func TestCorpusListing(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"-corpus"}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	body := out.String()
	for _, want := range []string{
		"github.com/pkg/errors",
		"golang.org/x/exp/slices",
		"mechanical",
		"advisory",
		"blocked symbols",
		"zerodepshack.com/cheatsheets",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("corpus listing missing %q", want)
		}
	}
}

// copyFixture copies a testdata fixture into a temp dir so tests can mutate it.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", name)
	dst := filepath.Join(t.TempDir(), name)

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// snapshot concatenates every file's contents, so a single comparison detects
// any change anywhere in the tree.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sb.WriteString(path)
		sb.WriteByte(0)
		if _, err := io.Copy(&sb, f); err != nil {
			return err
		}
		sb.WriteByte(0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}
