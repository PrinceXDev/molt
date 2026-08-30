// Package analyze joins what a module declares against what its source uses,
// and reports where the standard library has caught up.
//
// Every function here is pure: no network, no module cache, no clock. That is
// what makes the output byte-identical across runs.
package analyze

import (
	"fmt"
	"sort"
	"strings"

	"molt/internal/corpus"
	"molt/internal/gomod"
	"molt/internal/scan"
)

// Kinds of finding.
const (
	KindMigration = "migration"
	KindOrphan    = "orphan"
)

// SymbolUse is one symbol a module's source selects from a dependency.
type SymbolUse struct {
	Name string `json:"name"`
	// Count is the number of use sites.
	Count int `json:"count"`
	// Replacement is the stdlib symbol to substitute, empty when blocked.
	Replacement string `json:"replacement,omitempty"`
	// Blocked explains why this symbol cannot be rewritten.
	Blocked string `json:"blocked,omitempty"`
	// First is the earliest use site, as file:line.
	First string `json:"first"`
}

// Finding is one actionable observation about a dependency.
type Finding struct {
	Kind   string `json:"kind"`
	Module string `json:"module"`
	Target string `json:"target,omitempty"`
	Since  string `json:"since,omitempty"`

	// Auto reports whether molt can migrate every file using this dependency.
	Auto bool `json:"auto"`
	// Partial reports whether molt can migrate some files but not all.
	Partial bool `json:"partial,omitempty"`
	// Blockers lists the reasons some or all files cannot be migrated.
	Blockers []string `json:"blockers,omitempty"`

	// AutoFiles and BlockedFiles split Files by whether molt can rewrite them.
	AutoFiles    []string `json:"auto_files,omitempty"`
	BlockedFiles []string `json:"blocked_files,omitempty"`

	Symbols []SymbolUse `json:"symbols,omitempty"`
	Files   []string    `json:"files,omitempty"`

	Why      string `json:"why,omitempty"`
	Guidance string `json:"guidance,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Kept is a dependency molt has nothing to say about.
type Kept struct {
	Module  string `json:"module"`
	Symbols int    `json:"symbols"`
	Refs    int    `json:"refs"`
}

// Report is the complete result of an analysis.
type Report struct {
	Module    string `json:"module"`
	GoVersion string `json:"go_version,omitempty"`

	GoFiles      int `json:"go_files"`
	DirectDeps   int `json:"direct_deps"`
	IndirectDeps int `json:"indirect_deps"`
	// SumModules counts distinct module paths in go.sum.
	SumModules int `json:"sum_modules"`

	Findings []Finding   `json:"findings"`
	Kept     []Kept      `json:"kept,omitempty"`
	Skipped  []SkipEntry `json:"skipped,omitempty"`

	// Removable counts findings molt can perform in full.
	Removable int `json:"removable"`
	// PartiallyRemovable counts findings molt can perform in some files.
	PartiallyRemovable int `json:"partially_removable"`
	// Advisory counts findings that need a human.
	Advisory int `json:"advisory"`
	// Orphans counts declared dependencies with no import anywhere in source.
	Orphans int `json:"orphans"`

	CorpusRows       int `json:"corpus_rows"`
	CorpusMechanical int `json:"corpus_mechanical"`
}

// SkipEntry is a file molt did not read.
type SkipEntry struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Amplification is total build modules over declared direct dependencies, or 0
// when there is no go.sum to measure against.
func (r *Report) Amplification() float64 {
	if r.DirectDeps == 0 || r.SumModules == 0 {
		return 0
	}
	return float64(r.SumModules) / float64(r.DirectDeps)
}

// Clean reports whether molt found nothing to do.
func (r *Report) Clean() bool { return len(r.Findings) == 0 }

// Run produces a report. mod may be nil, in which case dependency counts are
// zero but source analysis still runs.
func Run(mod *gomod.File, src *scan.Result) *Report {
	r := &Report{GoFiles: len(src.Files)}
	r.CorpusRows, r.CorpusMechanical = corpus.Count()

	if mod != nil {
		r.Module = mod.Module
		r.GoVersion = mod.GoVersion
		r.DirectDeps = len(mod.Direct())
		r.IndirectDeps = len(mod.Indirect())
	}
	for _, s := range src.Skipped {
		r.Skipped = append(r.Skipped, SkipEntry{Path: s.Path, Reason: s.Reason})
	}

	imported := src.ImportPaths()
	covered := map[string]bool{}

	for _, m := range corpus.All() {
		usage := src.UsageOf(m.Module, m.Pkg)
		if len(usage.Files) == 0 {
			continue
		}
		covered[m.Module] = true
		r.Findings = append(r.Findings, buildMigration(m, usage))
	}

	if mod != nil {
		r.Findings = append(r.Findings, orphans(mod, imported)...)
		r.Kept = kept(mod, src, covered)
	}

	sortFindings(r.Findings)
	for _, f := range r.Findings {
		switch {
		case f.Kind == KindOrphan:
			r.Orphans++
		case f.Auto:
			r.Removable++
		case f.Partial:
			r.PartiallyRemovable++
		default:
			r.Advisory++
		}
	}
	return r
}

// buildMigration turns a corpus row plus observed usage into a finding.
func buildMigration(m corpus.Migration, u *scan.Usage) Finding {
	f := Finding{
		Kind:     KindMigration,
		Module:   m.Module,
		Target:   m.Target,
		Since:    m.Since,
		Files:    u.Files,
		Why:      m.Why,
		Guidance: m.Guidance,
		Note:     m.Note,
	}

	if !m.Mechanical() {
		switch {
		case m.Advisory:
			f.Blockers = append(f.Blockers, "the replacement changes the shape of the code, not just its names")
		case !m.Verified:
			f.Blockers = append(f.Blockers, "replacement signatures not yet verified against a released toolchain")
		}
	} else {
		// Per file, not per module: one awkward symbol in one file must not
		// disqualify twenty clean ones. -apply works file by file too.
		perFile := u.FileSymbols()
		for _, file := range u.Files {
			if obstructed, why := u.Obstructed(file); obstructed {
				f.BlockedFiles = append(f.BlockedFiles, file)
				f.Blockers = append(f.Blockers, file+": "+why)
				continue
			}
			symbols := perFile[file]
			if len(symbols) == 0 {
				// No attributable selector, so nothing to claim.
				f.BlockedFiles = append(f.BlockedFiles, file)
				continue
			}
			eligible := true
			for _, sym := range symbols {
				if _, _, ok := m.Replacement(sym); !ok {
					eligible = false
					break
				}
			}
			if eligible {
				f.AutoFiles = append(f.AutoFiles, file)
			} else {
				f.BlockedFiles = append(f.BlockedFiles, file)
			}
		}
	}

	for _, name := range u.SymbolNames() {
		refs := u.Symbols[name]
		use := SymbolUse{Name: name, Count: len(refs)}
		if len(refs) > 0 {
			use.First = fmt.Sprintf("%s:%d", refs[0].File, refs[0].Line)
		}
		if _, repl, ok := m.Replacement(name); ok {
			if repl != name {
				use.Replacement = repl
			}
		} else if reason := m.BlockReason(name); reason != "" {
			use.Blocked = reason
		}
		f.Symbols = append(f.Symbols, use)
	}

	for _, s := range f.Symbols {
		if s.Blocked != "" {
			f.Blockers = append(f.Blockers, s.Name+": "+s.Blocked)
		}
	}

	// Auto is every file; Partial is some, which is still worth doing.
	f.Auto = len(f.AutoFiles) > 0 && len(f.BlockedFiles) == 0
	f.Partial = len(f.AutoFiles) > 0 && len(f.BlockedFiles) > 0

	sort.Strings(f.Blockers)
	f.Blockers = dedupe(f.Blockers)
	sort.Strings(f.AutoFiles)
	sort.Strings(f.BlockedFiles)
	return f
}

// orphans finds direct requirements imported nowhere in the scanned source.
func orphans(mod *gomod.File, imported []string) []Finding {
	var out []Finding
	for _, req := range mod.Direct() {
		if moduleIsImported(req.Path, imported) {
			continue
		}
		out = append(out, Finding{
			Kind:   KindOrphan,
			Module: req.Path,
			Why:    "Declared in go.mod but no package under it is imported by any scanned file.",
			Guidance: "Confirm it is unused, then remove the require line and run go mod tidy. " +
				"Check first for a build-tagged file, a tool directive, or a generated file molt skipped.",
			Note: "molt does not read files excluded by build constraints, so a dependency used only " +
				"behind a build tag will appear here. This finding is a prompt to look, not a verdict.",
		})
	}
	return out
}

// kept lists imported direct dependencies the corpus says nothing about.
func kept(mod *gomod.File, src *scan.Result, covered map[string]bool) []Kept {
	var out []Kept
	imported := src.ImportPaths()
	for _, req := range mod.Direct() {
		if covered[req.Path] || !moduleIsImported(req.Path, imported) {
			continue
		}
		// Count usage across every package under the module.
		symbols, refs := 0, 0
		for _, path := range imported {
			if !belongsTo(path, req.Path) {
				continue
			}
			if covered[path] {
				continue
			}
			u := src.UsageOf(path, scan.PkgNameFor(path))
			symbols += len(u.Symbols)
			refs += u.TotalRefs()
		}
		if symbols == 0 && refs == 0 {
			continue
		}
		out = append(out, Kept{Module: req.Path, Symbols: symbols, Refs: refs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

func moduleIsImported(module string, imported []string) bool {
	for _, path := range imported {
		if belongsTo(path, module) {
			return true
		}
	}
	return false
}

// belongsTo reports whether importPath is the module or a package inside it.
// A bare prefix test would match github.com/a/bc against github.com/a/b.
func belongsTo(importPath, module string) bool {
	return importPath == module || strings.HasPrefix(importPath, module+"/")
}

// sortFindings orders findings: applicable, partial, advisory, then orphans,
// each by module path. The order is total, so output is deterministic.
func sortFindings(f []Finding) {
	rank := func(x Finding) int {
		switch {
		case x.Kind == KindMigration && x.Auto:
			return 0
		case x.Kind == KindMigration && x.Partial:
			return 1
		case x.Kind == KindMigration:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(f, func(i, j int) bool {
		if ri, rj := rank(f[i]), rank(f[j]); ri != rj {
			return ri < rj
		}
		return f[i].Module < f[j].Module
	})
}

func dedupe(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
