// Package rewrite performs molt's source edits with go/ast and go/format.
//
// File is a pure function of bytes and never touches the filesystem, so the
// CLI can show a diff before anything is written.
package rewrite

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"molt/internal/corpus"
	"molt/internal/scan"
)

// Edit records one applied change, for reporting.
type Edit struct {
	// From is the import path that was replaced.
	From string
	// To lists the stdlib import paths that replaced it.
	To []string
	// Symbols counts the selector expressions rewritten.
	Symbols int
	// Renames lists the symbol renames performed, as "old -> new".
	Renames []string
}

// Refusal records a migration molt declined to perform on a file, and why.
type Refusal struct {
	Module string
	Reason string
}

// Result is the outcome of rewriting one file.
type Result struct {
	// Source is the rewritten file. It equals the input when no edit applied.
	Source []byte
	// Edits lists what changed.
	Edits []Edit
	// Refusals lists migrations that matched the file but were declined.
	Refusals []Refusal
}

// Changed reports whether any edit was made.
func (r *Result) Changed() bool { return len(r.Edits) > 0 }

// File applies every mechanical migration in migs to src. Migrations that are
// advisory, unverified, or obstructed in this file become refusals and leave the
// source untouched.
func File(filename string, src []byte, migs []corpus.Migration) (*Result, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	res := &Result{Source: src}
	declared := scan.DeclaredNames(af)
	existing := importedPaths(af)

	var applied []Edit
	for _, m := range migs {
		spec := findImport(af, m.Module)
		if spec == nil {
			continue
		}
		if !m.Mechanical() {
			res.Refusals = append(res.Refusals, Refusal{m.Module, "not a mechanical migration"})
			continue
		}

		local, alias := localName(spec, m.Pkg)
		if local == "." || local == "_" {
			res.Refusals = append(res.Refusals, Refusal{m.Module, "dot or blank import"})
			continue
		}
		if declared[local] {
			res.Refusals = append(res.Refusals, Refusal{m.Module, "package name " + local + " is shadowed in this file"})
			continue
		}

		edit, err := applyOne(af, spec, m, local, alias, existing)
		if err != nil {
			res.Refusals = append(res.Refusals, Refusal{m.Module, err.Error()})
			continue
		}
		applied = append(applied, *edit)
		for _, path := range edit.To {
			existing[path] = true
		}
	}

	if len(applied) == 0 {
		return res, nil
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, af); err != nil {
		return nil, fmt.Errorf("format %s: %w", filename, err)
	}
	// Never hand back source the parser rejects.
	if _, err := parser.ParseFile(token.NewFileSet(), filename, buf.Bytes(), parser.SkipObjectResolution); err != nil {
		return nil, fmt.Errorf("rewrite of %s did not reparse, refusing to write: %w", filename, err)
	}

	out := buf.Bytes()
	// go/printer packs every spec into one group, which mixes stdlib and
	// third-party paths.
	if grouped, ok := groupImports(filename, out); ok {
		out = grouped
	}

	res.Source = out
	res.Edits = applied
	return res, nil
}

// groupImports splits a file's import block into a sorted stdlib group and a
// sorted third-party group.
//
// The block is regenerated textually: forcing a blank line between two specs
// through go/printer means fabricating token positions. Returns false when the
// block carries comments, which regeneration would drop.
func groupImports(filename string, src []byte) ([]byte, bool) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, false
	}

	var decl *ast.GenDecl
	for _, d := range af.Decls {
		gen, ok := d.(*ast.GenDecl)
		if ok && gen.Tok == token.IMPORT && gen.Lparen.IsValid() {
			decl = gen
			break
		}
	}
	if decl == nil || len(decl.Specs) < 2 {
		return nil, false
	}

	// A comment anywhere in the block makes regeneration lossy.
	for _, group := range af.Comments {
		if group.Pos() > decl.Pos() && group.End() < decl.End() {
			return nil, false
		}
	}

	var std, other []string
	for _, spec := range decl.Specs {
		imp, ok := spec.(*ast.ImportSpec)
		if !ok {
			return nil, false
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, false
		}
		line := "\t" + imp.Path.Value
		if imp.Name != nil {
			line = "\t" + imp.Name.Name + " " + imp.Path.Value
		}
		if isStdlibPath(path) {
			std = append(std, line)
		} else {
			other = append(other, line)
		}
	}
	sort.Strings(std)
	sort.Strings(other)

	var b bytes.Buffer
	b.WriteString("import (\n")
	b.WriteString(strings.Join(std, "\n"))
	if len(std) > 0 && len(other) > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(strings.Join(other, "\n"))
	b.WriteString("\n)")

	start := fset.Position(decl.Pos()).Offset
	end := fset.Position(decl.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		return nil, false
	}

	var out bytes.Buffer
	out.Write(src[:start])
	out.Write(b.Bytes())
	out.Write(src[end:])

	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, false
	}
	return formatted, true
}

// isStdlibPath reports whether a path names a standard-library package, using
// the goimports test: no dot in the first element, since every module path
// outside the stdlib starts with a domain name.
func isStdlibPath(path string) bool {
	first := path
	if i := strings.IndexByte(path, '/'); i >= 0 {
		first = path[:i]
	}
	return !strings.Contains(first, ".")
}

// applyOne rewrites one migration's selectors and repoints its import. Targets
// already present in existing are reused rather than duplicated.
func applyOne(af *ast.File, spec *ast.ImportSpec, m corpus.Migration, local string, alias bool, existing map[string]bool) (*Edit, error) {
	// Resolve everything before mutating anything, so an unreplaceable symbol
	// cannot leave the file half-edited.
	type change struct {
		sel    *ast.SelectorExpr
		target string
		name   string
	}
	var changes []change
	targets := map[string]bool{}

	var resolveErr error
	ast.Inspect(af, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != local {
			return true
		}
		target, name, ok := m.Replacement(sel.Sel.Name)
		if !ok {
			resolveErr = fmt.Errorf("%s.%s has no verified replacement", local, sel.Sel.Name)
			return false
		}
		changes = append(changes, change{sel: sel, target: target, name: name})
		targets[target] = true
		return true
	})
	if resolveErr != nil {
		return nil, resolveErr
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("no uses of %s found in this file", local)
	}

	// An alias is a single local name, so it cannot span two packages.
	if alias && len(targets) > 1 {
		return nil, fmt.Errorf("aliased import cannot be split across %d stdlib packages", len(targets))
	}

	edit := &Edit{From: m.Module, Symbols: len(changes)}
	renames := map[string]bool{}
	// Mutate names in place. ast.NewIdent carries token.NoPos, and go/printer
	// reads the gap to the next position as a line break, so replacing the
	// qualifier node splits "slices.Sort(s)" across two lines.
	for _, c := range changes {
		if !alias {
			pkg, err := pkgIdent(c.target)
			if err != nil {
				return nil, err
			}
			c.sel.X.(*ast.Ident).Name = pkg
		}
		if c.sel.Sel.Name != c.name {
			renames[c.sel.Sel.Name+" -> "+c.name] = true
			c.sel.Sel.Name = c.name
		}
	}
	for r := range renames {
		edit.Renames = append(edit.Renames, r)
	}
	sort.Strings(edit.Renames)

	sorted := make([]string, 0, len(targets))
	for t := range targets {
		sorted = append(sorted, t)
	}
	sort.Strings(sorted)
	edit.To = sorted

	// Repoint the old spec at the first missing target and add the rest; if
	// every target is already imported, drop the spec.
	var needed []string
	for _, t := range sorted {
		if !existing[t] {
			needed = append(needed, t)
		}
	}
	switch {
	case len(needed) == 0:
		deleteImport(af, spec)
	default:
		spec.Path.Value = strconv.Quote(needed[0])
		spec.EndPos = 0
		for _, t := range needed[1:] {
			addImport(af, t)
		}
	}
	return edit, nil
}

// findImport returns the spec importing path, or nil.
func findImport(af *ast.File, path string) *ast.ImportSpec {
	for _, spec := range af.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err == nil && p == path {
			return spec
		}
	}
	return nil
}

func importedPaths(af *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, spec := range af.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err == nil {
			out[p] = true
		}
	}
	return out
}

// localName returns the identifier the file uses to qualify the package, and
// whether it came from an explicit alias.
func localName(spec *ast.ImportSpec, pkg string) (name string, alias bool) {
	if spec.Name != nil {
		return spec.Name.Name, true
	}
	return pkg, false
}

// pkgIdent returns the identifier a stdlib path declares. Rewrites only ever
// target this fixed set, so an unknown path is an error rather than a guess.
func pkgIdent(path string) (string, error) {
	known := map[string]string{
		"context": "context",
		"errors":  "errors",
		"fmt":     "fmt",
		"maps":    "maps",
		"os":      "os",
		"slices":  "slices",
		"uuid":    "uuid",
	}
	if name, ok := known[path]; ok {
		return name, nil
	}
	return "", fmt.Errorf("no known package identifier for stdlib path %q", path)
}

// addImport inserts a new import spec into the file's first import declaration,
// creating the parenthesised form if the declaration is a single-line import.
func addImport(af *ast.File, path string) {
	spec := &ast.ImportSpec{
		Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(path)},
	}
	for _, decl := range af.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		// go/printer only emits parentheses when Lparen is a valid position, so
		// a single-line "import x" must be promoted before a spec can be added.
		if !gen.Lparen.IsValid() {
			gen.Lparen = gen.TokPos + token.Pos(len("import"))
			gen.Rparen = gen.Lparen
		}
		gen.Specs = append(gen.Specs, spec)
		sortSpecs(gen)
		af.Imports = append(af.Imports, spec)
		return
	}
}

// deleteImport removes a spec, and the whole declaration if it becomes empty.
func deleteImport(af *ast.File, target *ast.ImportSpec) {
	for i, decl := range af.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		for j, spec := range gen.Specs {
			if spec != ast.Spec(target) {
				continue
			}
			gen.Specs = append(gen.Specs[:j:j], gen.Specs[j+1:]...)
			if len(gen.Specs) == 0 {
				af.Decls = append(af.Decls[:i:i], af.Decls[i+1:]...)
			}
			for k, imp := range af.Imports {
				if imp == target {
					af.Imports = append(af.Imports[:k:k], af.Imports[k+1:]...)
					break
				}
			}
			return
		}
	}
}

// sortSpecs orders a declaration's import paths, so output does not depend on
// insertion order.
func sortSpecs(gen *ast.GenDecl) {
	specs := gen.Specs
	sort.SliceStable(specs, func(i, j int) bool {
		return specPath(specs[i]) < specPath(specs[j])
	})
	// Out-of-order positions make go/printer emit the specs on one line.
	for _, spec := range specs {
		imp, ok := spec.(*ast.ImportSpec)
		if !ok {
			continue
		}
		imp.Path.ValuePos = token.NoPos
		imp.EndPos = 0
		if imp.Name != nil {
			imp.Name.NamePos = token.NoPos
		}
	}
}

func specPath(spec ast.Spec) string {
	imp, ok := spec.(*ast.ImportSpec)
	if !ok {
		return ""
	}
	p, err := strconv.Unquote(imp.Path.Value)
	if err != nil {
		return imp.Path.Value
	}
	return p
}
