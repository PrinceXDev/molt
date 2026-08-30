// Package gomod parses the subset of the go.mod grammar molt needs: module, go,
// toolchain and require.
//
// Unrecognised directives are counted rather than dropped, so a caller can tell
// "absent" from "unparsed".
package gomod

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ErrNoModule reports a go.mod with no module directive.
var ErrNoModule = errors.New("go.mod: no module directive")

// Require is one entry of a require block.
type Require struct {
	Path     string
	Version  string
	Indirect bool
	Line     int
}

// File is the parsed subset of a go.mod.
// Replace is one replace directive: Old is redirected to New, optionally at
// NewVersion. NewVersion is empty for a local filesystem replacement.
type Replace struct {
	Old        string
	OldVersion string
	New        string
	NewVersion string
	Line       int
}

type File struct {
	Module    string
	GoVersion string
	Toolchain string
	Requires  []Require
	Replaces  []Replace

	// Unknown counts directives this package does not model, by name.
	Unknown map[string]int
}

// Replaced reports whether importPath is redirected by a replace directive,
// either directly or because it names a package inside a replaced module. A
// replace directive operates on a module path, and a module can contain many
// packages: "replace golang.org/x/exp => ../fork" redirects
// golang.org/x/exp/slices too, even though the directive never names it.
//
// A replace means the code behind this import path is not necessarily the
// code the corpus verified: it could be a local fork, a patched version, or
// an unrelated module entirely. Callers must treat a replaced import as
// unverified regardless of what the corpus says about the original path.
func (f *File) Replaced(importPath string) (Replace, bool) {
	for _, r := range f.Replaces {
		if r.Old == importPath || strings.HasPrefix(importPath, r.Old+"/") {
			return r, true
		}
	}
	return Replace{}, false
}

// SatisfiesGo reports whether this module's declared go directive is at
// least since (for example "go1.21"). A missing or unparsable go directive
// does not satisfy any requirement above go1.0 — conservative on purpose,
// since rewriting into a standard-library API the module's own toolchain
// floor cannot yet provide would produce a build that does not compile.
func (f *File) SatisfiesGo(since string) bool {
	return GoVersionAtLeast(f.GoVersion, since)
}

// GoVersionAtLeast reports whether have (a go.mod go directive, or "") is at
// least want (for example "go1.21"). want == "go1.0" is treated as always
// satisfied, since no real Go toolchain is older than that.
func GoVersionAtLeast(have, want string) bool {
	wMaj, wMin, ok := parseGoVersion(want)
	if !ok {
		return false
	}
	if wMaj == 1 && wMin == 0 {
		return true
	}
	hMaj, hMin, ok := parseGoVersion(have)
	if !ok {
		return false
	}
	if hMaj != wMaj {
		return hMaj > wMaj
	}
	return hMin >= wMin
}

// parseGoVersion extracts the major and minor numbers from a version string
// like "go1.21", "1.21", or "1.21.3". A patch component, if present, is
// ignored: go.mod's go directive has only ever gated stdlib API availability
// at minor-version granularity.
func parseGoVersion(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "go")
	if v == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// Direct returns requires not marked "// indirect".
func (f *File) Direct() []Require {
	var out []Require
	for _, r := range f.Requires {
		if !r.Indirect {
			out = append(out, r)
		}
	}
	return out
}

// Indirect returns requires marked "// indirect".
func (f *File) Indirect() []Require {
	var out []Require
	for _, r := range f.Requires {
		if r.Indirect {
			out = append(out, r)
		}
	}
	return out
}

// maxLines bounds a hostile or accidentally enormous file.
const maxLines = 200_000

// Parse reads a go.mod, skipping malformed require lines rather than failing
// the file. Only a missing module directive is fatal.
func Parse(r io.Reader) (*File, error) {
	f := &File{Unknown: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	inRequireBlock := false
	line := 0

	for sc.Scan() {
		line++
		if line > maxLines {
			return nil, fmt.Errorf("go.mod: exceeds %d lines", maxLines)
		}

		raw := sc.Text()
		text, indirect := splitComment(raw)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if inRequireBlock {
			if text == ")" {
				inRequireBlock = false
				continue
			}
			if req, ok := parseRequire(text, indirect, line); ok {
				f.Requires = append(f.Requires, req)
			}
			continue
		}

		verb, rest := firstField(text)
		switch verb {
		case "module":
			f.Module = unquote(rest)
		case "go":
			f.GoVersion = unquote(rest)
		case "toolchain":
			f.Toolchain = unquote(rest)
		case "require":
			if rest == "(" {
				inRequireBlock = true
				continue
			}
			if req, ok := parseRequire(rest, indirect, line); ok {
				f.Requires = append(f.Requires, req)
			}
		case "replace":
			if rest == "(" {
				if err := parseReplaceBlock(sc, &line, f); err != nil {
					return nil, err
				}
				continue
			}
			if r, ok := parseReplace(rest, line); ok {
				f.Replaces = append(f.Replaces, r)
			}
		case "exclude", "retract", "godebug", "tool", "ignore":
			// Counted, not resolved: these do not redirect a module's code, so
			// they carry no correctness risk for molt the way replace does.
			f.Unknown[verb]++
			if rest == "(" {
				if err := skipBlock(sc, &line); err != nil {
					return nil, err
				}
			}
		default:
			f.Unknown[verb]++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}
	if f.Module == "" {
		return nil, ErrNoModule
	}
	sort.SliceStable(f.Requires, func(i, j int) bool {
		return f.Requires[i].Path < f.Requires[j].Path
	})
	return f, nil
}

// skipBlock consumes lines to the closing paren of an unmodelled block.
func skipBlock(sc *bufio.Scanner, line *int) error {
	for sc.Scan() {
		*line++
		if *line > maxLines {
			return fmt.Errorf("go.mod: exceeds %d lines", maxLines)
		}
		if strings.TrimSpace(sc.Text()) == ")" {
			return nil
		}
	}
	return sc.Err()
}

// splitComment strips a trailing comment and reports whether it marked the
// requirement indirect. go.mod has no block comment form.
func splitComment(s string) (text string, indirect bool) {
	i := strings.Index(s, "//")
	if i < 0 {
		return s, false
	}
	comment := strings.TrimSpace(s[i+2:])
	for _, field := range strings.Fields(comment) {
		if field == "indirect" {
			indirect = true
			break
		}
	}
	return s[:i], indirect
}

func firstField(s string) (first, rest string) {
	i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' })
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// parseReplace accepts "OLD [OLDVER] => NEW [NEWVER]". NEWVER is absent for a
// local filesystem path on the right-hand side.
func parseReplace(s string, line int) (Replace, bool) {
	parts := strings.SplitN(s, "=>", 2)
	if len(parts) != 2 {
		return Replace{}, false
	}
	left := strings.Fields(parts[0])
	right := strings.Fields(parts[1])
	if len(left) == 0 || len(right) == 0 {
		return Replace{}, false
	}
	r := Replace{Old: unquote(left[0]), New: unquote(right[0]), Line: line}
	if len(left) >= 2 {
		r.OldVersion = unquote(left[1])
	}
	if len(right) >= 2 {
		r.NewVersion = unquote(right[1])
	}
	return r, true
}

// parseReplaceBlock reads a parenthesised replace block line by line.
func parseReplaceBlock(sc *bufio.Scanner, line *int, f *File) error {
	for sc.Scan() {
		*line++
		if *line > maxLines {
			return fmt.Errorf("go.mod: exceeds %d lines", maxLines)
		}
		text, _ := splitComment(sc.Text())
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if text == ")" {
			return nil
		}
		if r, ok := parseReplace(text, *line); ok {
			f.Replaces = append(f.Replaces, r)
		}
	}
	return sc.Err()
}

// parseRequire accepts "path version". Anything else is skipped.
func parseRequire(s string, indirect bool, line int) (Require, bool) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return Require{}, false
	}
	path := unquote(fields[0])
	version := unquote(fields[1])
	if path == "" || version == "" {
		return Require{}, false
	}
	return Require{Path: path, Version: version, Indirect: indirect, Line: line}, true
}

// unquote handles the quoted path form the grammar permits.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' {
		if v, err := strconv.Unquote(s); err == nil {
			return v
		}
	}
	return s
}

// SumModules returns the distinct module paths in a go.sum. Each one is a
// codebase that can contribute bytes to a build, so the count measures real
// third-party surface better than the require block does.
func SumModules(r io.Reader) ([]string, error) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if line > maxLines {
			return nil, fmt.Errorf("go.sum: exceeds %d lines", maxLines)
		}
		fields := strings.Fields(sc.Text())
		if len(fields) != 3 {
			continue
		}
		seen[fields[0]] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("go.sum: %w", err)
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}
