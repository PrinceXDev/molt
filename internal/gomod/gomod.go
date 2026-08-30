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
type File struct {
	Module    string
	GoVersion string
	Toolchain string
	Requires  []Require

	// Unknown counts directives this package does not model, by name.
	Unknown map[string]int
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
		case "replace", "exclude", "retract", "godebug", "tool", "ignore":
			// Counted, not resolved: a replace can redirect a module path, and
			// the report says so rather than following it.
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
