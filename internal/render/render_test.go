package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"molt/internal/analyze"
)

func sample() *analyze.Report {
	return &analyze.Report{
		Module:           "github.com/example/orders",
		GoVersion:        "1.24",
		GoFiles:          4,
		DirectDeps:       9,
		IndirectDeps:     4,
		SumModules:       203,
		Removable:        1,
		Advisory:         1,
		Orphans:          1,
		CorpusRows:       23,
		CorpusMechanical: 5,
		Findings: []analyze.Finding{
			{
				Kind:   analyze.KindMigration,
				Module: "golang.org/x/exp/slices",
				Target: "slices",
				Since:  "go1.21",
				Auto:   true,
				Files:  []string{"store/store.go"},
				Why:    "slices entered the standard library in Go 1.21.",
				Symbols: []analyze.SymbolUse{
					{Name: "Sort", Count: 2, First: "store/store.go:18"},
				},
			},
			{
				Kind:     analyze.KindMigration,
				Module:   "github.com/sirupsen/logrus",
				Target:   "log/slog",
				Since:    "go1.21",
				Auto:     false,
				Blockers: []string{"the replacement changes the shape of the code"},
				Files:    []string{"main.go"},
				Why:      "log/slog is structured logging in the standard library.",
				Guidance: "logrus.Info becomes slog.Info.",
				Note:     "Hooks have no direct equivalent.",
				Symbols: []analyze.SymbolUse{
					{Name: "Info", Count: 3, First: "main.go:14"},
					{Name: "WithFields", Count: 1, Blocked: "field builder has no direct form", First: "main.go:20"},
				},
			},
			{
				Kind:   analyze.KindOrphan,
				Module: "github.com/spf13/viper",
				Note:   "molt does not read build-tagged files.",
			},
		},
		Kept:    []analyze.Kept{{Module: "github.com/aws/aws-sdk-go-v2", Symbols: 18, Refs: 94}},
		Skipped: []analyze.SkipEntry{{Path: "gen/big.go", Reason: "larger than 4194304 bytes"}},
	}
}

func TestTextContainsEachSection(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, sample(), Plain(), false); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		"github.com/example/orders",
		"REMOVABLE",
		"NEEDS A HUMAN",
		"UNUSED",
		"golang.org/x/exp/slices",
		"github.com/sirupsen/logrus",
		"github.com/spf13/viper",
		"stdlib since go1.21",
		"1 removable",
		"corpus: 23 rows, 5 mechanical",
		"203",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q\n%s", want, body)
		}
	}
	// Amplification must be shown, since it is the number that lands.
	if !strings.Contains(body, "22.6x") {
		t.Errorf("amplification ratio missing:\n%s", body)
	}
}

// Compact mode must stay compact: no rationale, no per-symbol detail.
func TestCompactModeOmitsDetail(t *testing.T) {
	var buf bytes.Buffer
	Text(&buf, sample(), Plain(), false)
	body := buf.String()
	if strings.Contains(body, "logrus.Info becomes slog.Info") {
		t.Error("guidance leaked into compact output")
	}
	if strings.Contains(body, "main.go:14") {
		t.Error("per-symbol detail leaked into compact output")
	}
	if strings.Contains(body, "KEEP") {
		t.Error("the keep list is verbose-only")
	}
}

func TestVerboseModeAddsDetail(t *testing.T) {
	var buf bytes.Buffer
	Text(&buf, sample(), Plain(), true)
	body := buf.String()
	for _, want := range []string{
		"logrus.Info becomes slog.Info",
		"Hooks have no direct equivalent",
		"main.go:14",
		"field builder has no direct form",
		"KEEP",
		"github.com/aws/aws-sdk-go-v2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("verbose output missing %q\n%s", want, body)
		}
	}
}

// Plain style must never emit an escape sequence, or piped output and golden
// files would be full of control characters.
func TestPlainStyleEmitsNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	Text(&buf, sample(), Plain(), true)
	if bytes.Contains(buf.Bytes(), []byte{0x1b}) {
		t.Error("plain output contains an ANSI escape")
	}
}

// A non-terminal writer must never get colour. This is the check that keeps
// molt's output clean when redirected to a file.
func TestStyleForNonTerminal(t *testing.T) {
	if StyleFor(&bytes.Buffer{}).color {
		t.Error("colour enabled for a non-file writer")
	}
}

func TestNoColorEnvDisablesColour(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if StyleFor(&bytes.Buffer{}).color {
		t.Error("colour enabled despite NO_COLOR")
	}
}

func TestCleanReportSaysSo(t *testing.T) {
	r := &analyze.Report{Module: "molt", GoFiles: 11, CorpusRows: 23, CorpusMechanical: 5}
	var buf bytes.Buffer
	Text(&buf, r, Plain(), false)
	if !strings.Contains(buf.String(), "Nothing to remove") {
		t.Errorf("a clean report must say so:\n%s", buf.String())
	}
}

func TestReportWithoutGoMod(t *testing.T) {
	r := &analyze.Report{GoFiles: 2, CorpusRows: 23, CorpusMechanical: 5}
	var buf bytes.Buffer
	Text(&buf, r, Plain(), false)
	if !strings.Contains(buf.String(), "no go.mod found") {
		t.Errorf("missing go.mod should be stated:\n%s", buf.String())
	}
}

func TestJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var back analyze.Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("emitted JSON does not parse: %v", err)
	}
	if back.Module != "github.com/example/orders" {
		t.Errorf("Module = %q", back.Module)
	}
	if len(back.Findings) != 3 {
		t.Errorf("Findings = %d, want 3", len(back.Findings))
	}
	if back.SumModules != 203 || back.Removable != 1 {
		t.Errorf("counts lost in round trip: %+v", back)
	}
}

// Both renderers must be byte-stable across runs.
func TestRenderersAreDeterministic(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		var a, b bytes.Buffer
		Text(&a, sample(), Plain(), verbose)
		Text(&b, sample(), Plain(), verbose)
		if a.String() != b.String() {
			t.Errorf("Text output differs between runs (verbose=%v)", verbose)
		}
	}
	var a, b bytes.Buffer
	JSON(&a, sample())
	JSON(&b, sample())
	if a.String() != b.String() {
		t.Error("JSON output differs between runs")
	}
}

// A dependency with many symbols must not flood the compact report.
func TestSymbolListIsTruncated(t *testing.T) {
	r := sample()
	var many []analyze.SymbolUse
	for _, name := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
		many = append(many, analyze.SymbolUse{Name: name, Count: 1})
	}
	r.Findings[0].Symbols = many

	var buf bytes.Buffer
	Text(&buf, r, Plain(), false)
	if !strings.Contains(buf.String(), "and 3 more") {
		t.Errorf("long symbol list not truncated:\n%s", buf.String())
	}
}

func TestSkippedListing(t *testing.T) {
	var buf bytes.Buffer
	Skipped(&buf, sample(), Plain())
	body := buf.String()
	if !strings.Contains(body, "SKIPPED") || !strings.Contains(body, "gen/big.go") {
		t.Errorf("skipped listing wrong:\n%s", body)
	}

	buf.Reset()
	Skipped(&buf, &analyze.Report{}, Plain())
	if buf.Len() != 0 {
		t.Errorf("empty skip list should print nothing, got %q", buf.String())
	}
}

// failWriter fails after n successful writes.
type failWriter struct {
	remaining int
}

func (f *failWriter) Write(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, errors.New("disk full")
	}
	f.remaining--
	return len(p), nil
}

// Text and Skipped return an error when the underlying writer fails. Without the
// latching writer they would report success after a failed write.
func TestWriteErrorsPropagate(t *testing.T) {
	for _, n := range []int{0, 1, 5} {
		if err := Text(&failWriter{remaining: n}, sample(), Plain(), true); err == nil {
			t.Errorf("Text returned nil after the writer failed (allowing %d writes)", n)
		}
	}
	if err := Skipped(&failWriter{}, sample(), Plain()); err == nil {
		t.Error("Skipped returned nil after the writer failed")
	}
	// Nothing to write means nothing to fail.
	if err := Skipped(&failWriter{}, &analyze.Report{}, Plain()); err != nil {
		t.Errorf("Skipped on an empty list = %v, want nil", err)
	}
}
