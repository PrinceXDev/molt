// Package scan records, per file, which imports a module declares and which
// identifiers it selects from them.
//
// Imports and selectors are syntax, so go/parser and go/ast are enough; scan
// does not type-check. Cases that would need types are detected and refused
// rather than resolved. See STDLIB.md for the tradeoff.
package scan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxFileSize bounds a single source file; generated Go can be enormous.
const maxFileSize = 4 << 20

// Import is one import spec as written in the source.
type Import struct {
	Path  string // the import path, unquoted
	Local string // explicit local name, empty when the source gave none
	Line  int
}

// Blank reports the underscore form, imported for side effects only.
func (i Import) Blank() bool { return i.Local == "_" }

// Dot reports the dot form, which merges names into file scope. Selectors in
// such a file cannot be attributed.
func (i Import) Dot() bool { return i.Local == "." }

// Sel is one selection of a symbol from a qualifier, as in uuid.New.
type Sel struct {
	Symbol string
	Line   int
}

// File is the syntax molt extracted from one .go file.
type File struct {
	Path    string // slash-separated, relative to the scan root
	Imports []Import

	// Selectors maps a qualifier to the symbols selected from it. The qualifier
	// may be a package or an ordinary variable; the caller matches against
	// known import names.
	Selectors map[string][]Sel

	// Declared holds every identifier the file binds. An import name appearing
	// here is shadowed, so the file is not rewritable.
	Declared map[string]bool

	IsTest bool
}

// Skip records a file molt could not or would not read.
type Skip struct {
	Path   string
	Reason string
}

// Result is the outcome of scanning a module root.
type Result struct {
	Root    string
	Files   []*File
	Skipped []Skip
}

// Ref is one use site.
type Ref struct {
	File string
	Line int
}

// Usage aggregates one import path across the whole module.
type Usage struct {
	Path string

	// Symbols maps a selected symbol to every place it is used, sorted by file
	// then line.
	Symbols map[string][]Ref

	// Files lists every file importing the path, sorted.
	Files []string

	// DotFiles and BlankFiles list files using the dot and underscore forms.
	DotFiles   []string
	BlankFiles []string

	// ShadowedFiles lists files where the local name is also bound to something
	// else, making attribution unreliable.
	ShadowedFiles []string

	// AliasedFiles lists files importing the path under an explicit local name.
	AliasedFiles []string
}

// TotalRefs counts every use site across every symbol.
func (u *Usage) TotalRefs() int {
	n := 0
	for _, refs := range u.Symbols {
		n += len(refs)
	}
	return n
}

// SymbolNames returns the used symbols in sorted order, for deterministic output.
func (u *Usage) SymbolNames() []string {
	out := make([]string, 0, len(u.Symbols))
	for s := range u.Symbols {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Rewritable reports whether this import's files may be edited, and if not,
// the first reason found.
func (u *Usage) Rewritable() (bool, string) {
	switch {
	case len(u.DotFiles) > 0:
		return false, "dot-imported in " + u.DotFiles[0]
	case len(u.ShadowedFiles) > 0:
		return false, "package name shadowed in " + u.ShadowedFiles[0]
	case len(u.BlankFiles) > 0 && len(u.Symbols) == 0:
		return false, "imported for side effects only"
	}
	return true, ""
}

// Dir scans a module root, skipping the directories the go command ignores:
// vendor, testdata, and anything starting with a dot or underscore.
//
// filepath.WalkDir does not follow symlinks, so the walk cannot escape root.
func Dir(root string) (*Result, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", root)
	}

	res := &Result{Root: root}
	fset := token.NewFileSet()

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Record and step over: a partial scan beats none.
			res.Skipped = append(res.Skipped, Skip{Path: rel(root, path), Reason: err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if ignoredDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			res.Skipped = append(res.Skipped, Skip{Path: rel(root, path), Reason: err.Error()})
			return nil
		}
		if !fi.Mode().IsRegular() {
			res.Skipped = append(res.Skipped, Skip{Path: rel(root, path), Reason: "not a regular file"})
			return nil
		}
		if fi.Size() > maxFileSize {
			res.Skipped = append(res.Skipped, Skip{
				Path:   rel(root, path),
				Reason: fmt.Sprintf("larger than %d bytes", maxFileSize),
			})
			return nil
		}

		f, err := parseFile(fset, path, rel(root, path))
		if err != nil {
			// Unparseable Go is a fact about the repo, not a failure here.
			res.Skipped = append(res.Skipped, Skip{Path: rel(root, path), Reason: "parse error: " + err.Error()})
			return nil
		}
		res.Files = append(res.Files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Path < res.Skipped[j].Path })
	return res, nil
}

func ignoredDir(name string) bool {
	if name == "vendor" || name == "testdata" || name == "node_modules" {
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		r = path
	}
	return filepath.ToSlash(r)
}

func parseFile(fset *token.FileSet, abs, relPath string) (*File, error) {
	af, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	f := &File{
		Path:      relPath,
		Selectors: map[string][]Sel{},
		IsTest:    strings.HasSuffix(relPath, "_test.go"),
	}
	for _, spec := range af.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		imp := Import{Path: path, Line: fset.Position(spec.Pos()).Line}
		if spec.Name != nil {
			imp.Local = spec.Name.Name
		}
		f.Imports = append(f.Imports, imp)
	}
	collect(fset, af, f)
	return f, nil
}

// collect records selector expressions and every identifier the file binds.
func collect(fset *token.FileSet, af *ast.File, f *File) {
	f.Declared = DeclaredNames(af)
	ast.Inspect(af, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				f.Selectors[id.Name] = append(f.Selectors[id.Name], Sel{
					Symbol: sel.Sel.Name,
					Line:   fset.Position(sel.Sel.Pos()).Line,
				})
			}
		}
		return true
	})
}

// DeclaredNames returns every identifier a file binds: locals, parameters,
// receivers, types, functions, range variables, struct and interface members.
// An import name in this set is shadowed, so that file is left alone.
//
// The set is file-wide, not scope-aware. That over-reports shadowing and costs
// some safe rewrites; the opposite error would corrupt code.
func DeclaredNames(af *ast.File) map[string]bool {
	declared := map[string]bool{}
	declare := func(idents ...*ast.Ident) {
		for _, id := range idents {
			if id != nil && id.Name != "_" {
				declared[id.Name] = true
			}
		}
	}
	declareFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, field := range fl.List {
			declare(field.Names...)
		}
	}

	ast.Inspect(af, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			declare(node.Name)
			declareFields(node.Recv)
			if node.Type != nil {
				declareFields(node.Type.Params)
				declareFields(node.Type.Results)
			}
		case *ast.FuncLit:
			if node.Type != nil {
				declareFields(node.Type.Params)
				declareFields(node.Type.Results)
			}
		case *ast.ValueSpec:
			declare(node.Names...)
		case *ast.TypeSpec:
			declare(node.Name)
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE {
				for _, lhs := range node.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						declare(id)
					}
				}
			}
		case *ast.RangeStmt:
			if id, ok := node.Key.(*ast.Ident); ok {
				declare(id)
			}
			if id, ok := node.Value.(*ast.Ident); ok {
				declare(id)
			}
		case *ast.TypeSwitchStmt:
			if assign, ok := node.Assign.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
				for _, lhs := range assign.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						declare(id)
					}
				}
			}
		case *ast.StructType:
			declareFields(node.Fields)
		case *ast.InterfaceType:
			declareFields(node.Methods)
		}
		return true
	})
	return declared
}

// UsageOf aggregates every reference to importPath across the module. pkgName is
// the identifier the package declares, which molt cannot read from source it
// does not have; the corpus supplies it for known modules and PkgNameFor guesses
// it otherwise.
func (r *Result) UsageOf(importPath, pkgName string) *Usage {
	u := &Usage{Path: importPath, Symbols: map[string][]Ref{}}
	for _, f := range r.Files {
		var imp *Import
		for i := range f.Imports {
			if f.Imports[i].Path == importPath {
				imp = &f.Imports[i]
				break
			}
		}
		if imp == nil {
			continue
		}
		u.Files = append(u.Files, f.Path)

		switch {
		case imp.Dot():
			u.DotFiles = append(u.DotFiles, f.Path)
			continue
		case imp.Blank():
			u.BlankFiles = append(u.BlankFiles, f.Path)
			continue
		}

		name := pkgName
		if imp.Local != "" {
			name = imp.Local
			u.AliasedFiles = append(u.AliasedFiles, f.Path)
		}
		if f.Declared[name] {
			u.ShadowedFiles = append(u.ShadowedFiles, f.Path)
			continue
		}
		for _, sel := range f.Selectors[name] {
			u.Symbols[sel.Symbol] = append(u.Symbols[sel.Symbol], Ref{File: f.Path, Line: sel.Line})
		}
	}
	for sym := range u.Symbols {
		refs := u.Symbols[sym]
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].File != refs[j].File {
				return refs[i].File < refs[j].File
			}
			return refs[i].Line < refs[j].Line
		})
	}
	return u
}

// ImportPaths returns every distinct import path in the module, sorted.
func (r *Result) ImportPaths() []string {
	seen := map[string]bool{}
	for _, f := range r.Files {
		for _, imp := range f.Imports {
			seen[imp.Path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// PkgNameFor guesses the identifier an import path declares, for modules the
// corpus does not describe. The guess is the last path element with a major
// version suffix and common decorations removed. It is only ever used for
// reporting, never to decide a rewrite.
func PkgNameFor(importPath string) string {
	parts := strings.Split(importPath, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && isMajorVersion(last) {
		last = parts[len(parts)-2]
	}
	// gopkg.in carries the major version inside the element, as in yaml.v3.
	if i := strings.LastIndex(last, "."); i > 0 && isMajorVersion(last[i+1:]) {
		last = last[:i]
	}
	last = strings.TrimSuffix(last, ".go")
	last = strings.TrimPrefix(last, "go-")
	last = strings.TrimSuffix(last, "-go")
	last = strings.ReplaceAll(last, "-", "")
	last = strings.ReplaceAll(last, ".", "")
	return last
}

func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// FileSymbols returns, for each file importing the path, the sorted set of
// symbols that file selects from it.
//
// Eligibility for rewriting is a per-file question, not a per-module one: a
// module may use a blocked symbol in one file and only replaceable ones in
// twenty others. Those twenty are still safe to migrate.
func (u *Usage) FileSymbols() map[string][]string {
	byFile := map[string]map[string]bool{}
	for sym, refs := range u.Symbols {
		for _, ref := range refs {
			if byFile[ref.File] == nil {
				byFile[ref.File] = map[string]bool{}
			}
			byFile[ref.File][sym] = true
		}
	}
	out := make(map[string][]string, len(byFile))
	for file, syms := range byFile {
		names := make([]string, 0, len(syms))
		for s := range syms {
			names = append(names, s)
		}
		sort.Strings(names)
		out[file] = names
	}
	return out
}

// Obstructed reports whether a specific file cannot be rewritten, and why.
// Unlike Rewritable, which answers for the whole module, this is per file.
func (u *Usage) Obstructed(file string) (bool, string) {
	for _, f := range u.DotFiles {
		if f == file {
			return true, "dot-imported"
		}
	}
	for _, f := range u.ShadowedFiles {
		if f == file {
			return true, "package name shadowed"
		}
	}
	for _, f := range u.BlankFiles {
		if f == file {
			return true, "imported for side effects only"
		}
	}
	return false, ""
}
