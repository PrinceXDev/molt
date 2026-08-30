package diff

import (
	"strings"
	"testing"
)

func TestIdenticalInputsProduceNoDiff(t *testing.T) {
	src := "a\nb\nc\n"
	if got := Unified("x.go", []byte(src), []byte(src)); got != "" {
		t.Errorf("Unified = %q, want empty", got)
	}
}

func TestSingleLineChange(t *testing.T) {
	a := "one\ntwo\nthree\n"
	b := "one\nTWO\nthree\n"
	got := Unified("x.go", []byte(a), []byte(b))

	if !strings.Contains(got, "--- a/x.go") || !strings.Contains(got, "+++ b/x.go") {
		t.Errorf("missing file headers:\n%s", got)
	}
	if !strings.Contains(got, "-two") || !strings.Contains(got, "+TWO") {
		t.Errorf("change not represented:\n%s", got)
	}
	// The unchanged lines must appear as context, once each, unprefixed.
	if !strings.Contains(got, " one") || !strings.Contains(got, " three") {
		t.Errorf("context lines missing:\n%s", got)
	}
	// The changed line appears exactly twice: once removed, once added.
	if n := strings.Count(strings.ToLower(got), "two"); n != 2 {
		t.Errorf("changed line appears %d times, want 2:\n%s", n, got)
	}
}

func TestInsertionAndDeletion(t *testing.T) {
	got := Unified("x.go", []byte("a\nb\n"), []byte("a\nx\ny\nb\n"))
	if !strings.Contains(got, "+x") || !strings.Contains(got, "+y") {
		t.Errorf("insertions missing:\n%s", got)
	}
	if strings.Contains(got, "-a") || strings.Contains(got, "-b") {
		t.Errorf("unchanged lines marked as deleted:\n%s", got)
	}

	got = Unified("x.go", []byte("a\nb\nc\n"), []byte("a\nc\n"))
	if !strings.Contains(got, "-b") {
		t.Errorf("deletion missing:\n%s", got)
	}
}

// The trimming of common prefix and suffix is what keeps the LCS table small.
// A change deep inside a large file must still produce a small, correct diff.
func TestChangeInsideLargeFile(t *testing.T) {
	var a, b []string
	for i := 0; i < 5000; i++ {
		a = append(a, "line")
		b = append(b, "line")
	}
	b[2500] = "changed"

	got := Unified("big.go", []byte(strings.Join(a, "\n")+"\n"), []byte(strings.Join(b, "\n")+"\n"))
	if !strings.Contains(got, "+changed") {
		t.Errorf("change not found:\n%s", got)
	}
	// Without prefix and suffix trimming this would be enormous.
	if n := strings.Count(got, "\n"); n > 20 {
		t.Errorf("diff has %d lines; trimming is not working", n)
	}
}

func TestEmptyInputs(t *testing.T) {
	got := Unified("x.go", nil, []byte("new\n"))
	if !strings.Contains(got, "+new") {
		t.Errorf("addition to an empty file:\n%s", got)
	}
	got = Unified("x.go", []byte("old\n"), nil)
	if !strings.Contains(got, "-old") {
		t.Errorf("emptying a file:\n%s", got)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	// Source files without a trailing newline must not gain a phantom empty
	// line in the diff.
	got := Unified("x.go", []byte("a\nb"), []byte("a\nc"))
	if strings.Contains(got, "- \n") || strings.HasSuffix(got, "+\n") {
		t.Errorf("phantom empty line:\n%q", got)
	}
	if !strings.Contains(got, "-b") || !strings.Contains(got, "+c") {
		t.Errorf("change not represented:\n%s", got)
	}
}

func TestDeterministic(t *testing.T) {
	a := []byte("one\ntwo\nthree\nfour\n")
	b := []byte("one\n2\nthree\n4\n")
	first := Unified("x.go", a, b)
	for i := 0; i < 5; i++ {
		if got := Unified("x.go", a, b); got != first {
			t.Fatal("Unified is not deterministic")
		}
	}
}

// A diff is only useful if applying it conceptually reproduces the target: every
// non-deleted line, in order, must equal the new file.
func TestDiffReconstructsTarget(t *testing.T) {
	a := "package x\n\nimport (\n\t\"golang.org/x/exp/slices\"\n)\n\nvar _ = slices.Sort\n"
	b := "package x\n\nimport (\n\t\"slices\"\n)\n\nvar _ = slices.Sort\n"

	got := Unified("x.go", []byte(a), []byte(b))
	var rebuilt []string
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "-"):
			continue
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, " "):
			rebuilt = append(rebuilt, line[1:])
		}
	}
	want := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	if strings.Join(rebuilt, "\n") != strings.Join(want, "\n") {
		t.Errorf("diff does not reconstruct the target:\ngot  %q\nwant %q", rebuilt, want)
	}
}
