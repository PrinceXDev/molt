package corpus

import (
	"strings"
	"testing"
)

// The corpus is data, and wrong data is the one thing that would make molt
// dangerous. These tests are the schema.
func TestEveryRowIsWellFormed(t *testing.T) {
	for _, m := range All() {
		t.Run(m.Module, func(t *testing.T) {
			if m.Module == "" || m.Target == "" {
				t.Fatal("Module and Target are required")
			}
			if m.Pkg == "" {
				t.Error("Pkg is required: molt cannot attribute selectors without the package identifier")
			}
			if !strings.HasPrefix(m.Since, "go1.") {
				t.Errorf("Since = %q, want a go1.x version", m.Since)
			}
			if m.Why == "" {
				t.Error("Why is required: every row must be able to explain itself")
			}
			if m.Guidance == "" && m.Advisory {
				t.Error("an advisory row with no Guidance tells the user nothing")
			}
			if m.Advisory && m.Verified {
				t.Error("Advisory and Verified are contradictory")
			}
			// A mechanical row must be able to replace something, or it would
			// rewrite nothing and only churn imports.
			if m.Mechanical() && m.Symbols == nil && m.Target == "" {
				t.Error("mechanical row has neither a symbol table nor a target path")
			}
		})
	}
}

func TestRowsAreSorted(t *testing.T) {
	rows := All()
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Module >= rows[i].Module {
			t.Fatalf("rows not sorted: %q then %q", rows[i-1].Module, rows[i].Module)
		}
	}
}

func TestNoDuplicateModules(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range All() {
		if seen[m.Module] {
			t.Errorf("duplicate row for %s", m.Module)
		}
		seen[m.Module] = true
	}
}

// A symbol cannot be both replaceable and blocked; that ambiguity would make
// Replacement's answer depend on evaluation order.
func TestSymbolsAndBlockedAreDisjoint(t *testing.T) {
	for _, m := range All() {
		for name := range m.Symbols {
			if _, blocked := m.Blocked[name]; blocked {
				t.Errorf("%s: %s is both replaceable and blocked", m.Module, name)
			}
		}
	}
}

// Every blocked symbol must say why. "Blocked" with no reason is the kind of
// output that makes a user distrust a tool.
func TestBlockedSymbolsExplainThemselves(t *testing.T) {
	for _, m := range All() {
		for name, reason := range m.Blocked {
			if len(strings.TrimSpace(reason)) < 10 {
				t.Errorf("%s: %s has no useful reason (%q)", m.Module, name, reason)
			}
		}
	}
}

func TestMechanicalRowsTargetKnownPackages(t *testing.T) {
	// rewrite.pkgIdent only knows a fixed set of stdlib packages. A mechanical
	// row pointing anywhere else would be refused at rewrite time, which would
	// be a silent contradiction between the two files.
	known := map[string]bool{
		"context": true, "errors": true, "fmt": true,
		"maps": true, "os": true, "slices": true, "uuid": true,
	}
	for _, m := range All() {
		if !m.Mechanical() {
			continue
		}
		targets := map[string]bool{m.Target: true}
		for _, r := range m.Symbols {
			if r.Target != "" {
				targets[r.Target] = true
			}
		}
		for target := range targets {
			if !known[target] {
				t.Errorf("%s: mechanical row targets %q, which rewrite cannot resolve", m.Module, target)
			}
		}
	}
}

func TestReplacement(t *testing.T) {
	errs, ok := Lookup("github.com/pkg/errors")
	if !ok {
		t.Fatal("pkg/errors missing from corpus")
	}

	target, name, ok := errs.Replacement("Is")
	if !ok || target != "errors" || name != "Is" {
		t.Errorf("Replacement(Is) = (%q, %q, %v), want (errors, Is, true)", target, name, ok)
	}
	target, name, ok = errs.Replacement("Unwrap")
	if !ok || target != "errors" || name != "Unwrap" {
		t.Errorf("Replacement(Unwrap) = (%q, %q, %v), want (errors, Unwrap, true)", target, name, ok)
	}
	// New and Errorf both capture a stack trace stdlib errors/fmt do not, so
	// they must stay blocked rather than resolve as a rename.
	if _, _, ok := errs.Replacement("New"); ok {
		t.Error("Replacement(New) succeeded; pkg/errors.New captures a stack trace stdlib errors.New does not")
	}
	if _, _, ok := errs.Replacement("Errorf"); ok {
		t.Error("Replacement(Errorf) succeeded; pkg/errors.Errorf captures a stack trace fmt.Errorf does not")
	}
	if _, _, ok := errs.Replacement("Wrap"); ok {
		t.Error("Replacement(Wrap) succeeded; Wrap changes argument shape and must be blocked")
	}
	if reason := errs.BlockReason("Wrap"); reason == "" {
		t.Error("BlockReason(Wrap) is empty")
	}
	if _, _, ok := errs.Replacement("SomethingInvented"); ok {
		t.Error("an unknown symbol must not resolve")
	}
}

// A row with no symbol table swaps the import path and leaves every symbol
// alone, which is how x/net/context works.
func TestReplacementWithNilSymbolTable(t *testing.T) {
	ctx, ok := Lookup("golang.org/x/net/context")
	if !ok {
		t.Fatal("x/net/context missing from corpus")
	}
	if ctx.Symbols != nil {
		t.Skip("row now has a symbol table; this test no longer applies")
	}
	target, name, ok := ctx.Replacement("WithTimeout")
	if !ok || target != "context" || name != "WithTimeout" {
		t.Errorf("Replacement = (%q, %q, %v)", target, name, ok)
	}
}

func TestLookupMiss(t *testing.T) {
	if _, ok := Lookup("github.com/not/in/the/corpus"); ok {
		t.Error("Lookup invented a row")
	}
}

func TestCount(t *testing.T) {
	total, mechanical := Count()
	if total != len(All()) {
		t.Errorf("total = %d, want %d", total, len(All()))
	}
	if mechanical == 0 {
		t.Error("no mechanical rows; molt could not rewrite anything")
	}
	if mechanical > total {
		t.Errorf("mechanical %d exceeds total %d", mechanical, total)
	}
}

// The two traps molt exists to know about. If either of these rows loses its
// Blocked entry, molt would start corrupting code, so they are pinned.
func TestTrapsArePinned(t *testing.T) {
	slices, ok := Lookup("golang.org/x/exp/slices")
	if !ok {
		t.Fatal("x/exp/slices missing")
	}
	for _, sym := range []string{"SortFunc", "SortStableFunc", "BinarySearchFunc"} {
		if _, blocked := slices.Blocked[sym]; !blocked {
			t.Errorf("slices.%s must stay blocked: the comparison changed from less-bool to cmp-int", sym)
		}
	}

	maps, ok := Lookup("golang.org/x/exp/maps")
	if !ok {
		t.Fatal("x/exp/maps missing")
	}
	for _, sym := range []string{"Keys", "Values"} {
		if _, blocked := maps.Blocked[sym]; !blocked {
			t.Errorf("maps.%s must stay blocked: the return type changed from a slice to an iterator", sym)
		}
	}

	// slices.SortStable does not exist in the standard library at all (only
	// SortStableFunc does). Listing it as safe would emit code that fails to
	// compile, which is worse than any other mistake this corpus can make.
	if _, blocked := slices.Blocked["SortStable"]; !blocked {
		t.Error("slices.SortStable must stay blocked: it does not exist in the standard library's slices package")
	}
	if _, safe := slices.Symbols["SortStable"]; safe {
		t.Error("slices.SortStable must not be listed as a safe rename")
	}

	// pkg/errors.New and Errorf both capture a stack trace the stdlib
	// equivalents do not. That is a feature removal, not a rename.
	errs, ok := Lookup("github.com/pkg/errors")
	if !ok {
		t.Fatal("pkg/errors missing")
	}
	for _, sym := range []string{"New", "Errorf"} {
		if _, blocked := errs.Blocked[sym]; !blocked {
			t.Errorf("pkg/errors.%s must stay blocked: it captures a stack trace the stdlib replacement does not", sym)
		}
	}

	// go-homedir.Dir caches its result; os.UserHomeDir does not. Signature
	// equality is not enough to make this mechanical.
	homedir, ok := Lookup("github.com/mitchellh/go-homedir")
	if !ok {
		t.Fatal("go-homedir missing")
	}
	if homedir.Mechanical() {
		t.Error("go-homedir must not be mechanical: caching behaviour differs from os.UserHomeDir")
	}
	if !homedir.Advisory {
		t.Error("go-homedir must be marked Advisory")
	}
}

// TestCheatSheetTableIsCovered pins molt's corpus to the "Instead of installing
// it" table published for Go 1.27 at https://zerodepshack.com/cheatsheets.
//
// That table is the corpus's source of record. If a row there is missing here,
// molt is not doing the job it claims to do, so this test is the contract.
func TestCheatSheetTableIsCovered(t *testing.T) {
	// Every row of the published table: the module a reader would install, the
	// stdlib answer, and the release it landed in.
	table := []struct {
		modules []string
		target  string
		since   string
	}{
		{[]string{"github.com/google/uuid"}, "uuid", "go1.27"},
		{[]string{"github.com/json-iterator/go"}, "encoding/json/v2", "go1.27"},
		{[]string{"github.com/gorilla/mux", "github.com/go-chi/chi", "github.com/go-chi/chi/v5"}, "net/http", "go1.22"},
		{[]string{"github.com/sirupsen/logrus", "go.uber.org/zap"}, "log/slog", "go1.21"},
		{[]string{"github.com/stretchr/testify/assert", "github.com/stretchr/testify/require"}, "testing + testing/synctest", "go1.25"},
		{[]string{"github.com/gorilla/csrf"}, "net/http", "go1.25"},
		{[]string{"github.com/spf13/cobra"}, "flag", "go1.0"},
		{[]string{"github.com/gocarina/gocsv"}, "encoding/csv", "go1.0"},
	}

	for _, row := range table {
		for _, module := range row.modules {
			m, ok := Lookup(module)
			if !ok {
				t.Errorf("cheat-sheet row %q is missing from the corpus", module)
				continue
			}
			if m.Target != row.target {
				t.Errorf("%s: Target = %q, cheat-sheet says %q", module, m.Target, row.target)
			}
			if m.Since != row.since {
				t.Errorf("%s: Since = %q, cheat-sheet says %q", module, m.Since, row.since)
			}
		}
	}
}
