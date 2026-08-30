// Command molt reports which of a Go module's dependencies the standard library
// has replaced, and rewrites the ones it can do safely.
//
// The CLI is built on flag. cobra would give subcommand trees and generated
// completions; molt does one job and takes one path, so a flag set and a
// positional argument is the whole surface.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"molt/internal/analyze"
	"molt/internal/corpus"
	"molt/internal/diff"
	"molt/internal/gomod"
	"molt/internal/render"
	"molt/internal/rewrite"
	"molt/internal/scan"
)

// version is set here rather than injected at build time. A linker-injected
// timestamp or commit hash would make the binary differ between builds, and the
// reproducible-build claim is worth more than a version string.
const version = "0.1.0"

// Exit codes. Documented in README under "Exit codes".
const (
	exitOK       = 0 // success
	exitFindings = 1 // findings present, only with -exit-code
	exitUsage    = 2 // bad flags or unusable path
	exitInternal = 3 // molt could not complete
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	json     bool
	verbose  bool
	showDiff bool
	apply    bool
	exitCode bool
	corpused bool
	version  bool
}

func run(args []string, stdout, stderr io.Writer) int {
	var opt options
	fs := flag.NewFlagSet("molt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opt.json, "json", false, "emit the report as JSON")
	fs.BoolVar(&opt.verbose, "v", false, "show rationale, guidance and per-symbol detail")
	fs.BoolVar(&opt.showDiff, "diff", false, "print the patch molt would apply, without writing")
	fs.BoolVar(&opt.apply, "apply", false, "rewrite files in place")
	fs.BoolVar(&opt.exitCode, "exit-code", false, "exit 1 when there are findings, for CI")
	fs.BoolVar(&opt.corpused, "corpus", false, "print the migration table and exit")
	fs.BoolVar(&opt.version, "version", false, "print the version and exit")
	fs.Usage = func() { usage(stderr, fs) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	switch {
	case opt.version:
		fmt.Fprintf(stdout, "molt %s\n", version)
		return exitOK
	case opt.corpused:
		printCorpus(stdout)
		return exitOK
	}

	if opt.apply && opt.showDiff {
		fmt.Fprintln(stderr, "molt: -apply and -diff are mutually exclusive; -diff shows what -apply would do")
		return exitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(stderr, "molt: expected at most one directory, got %d\n", fs.NArg())
		return exitUsage
	}

	root := "."
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	return scanRoot(root, opt, stdout, stderr)
}

func scanRoot(root string, opt options, stdout, stderr io.Writer) int {
	info, err := os.Stat(root)
	if err != nil {
		fmt.Fprintf(stderr, "molt: %v\n", err)
		return exitUsage
	}
	if !info.IsDir() {
		fmt.Fprintf(stderr, "molt: %s is not a directory\n", root)
		return exitUsage
	}

	mod, err := readGoMod(root)
	if err != nil {
		fmt.Fprintf(stderr, "molt: %v\n", err)
		return exitInternal
	}
	if mod == nil {
		fmt.Fprintf(stderr, "molt: no go.mod in %s; reporting on source only\n", root)
	}

	// A missing or unreadable go.sum costs one line of the report, not the run.
	sum, err := readGoSum(root)
	if err != nil {
		fmt.Fprintf(stderr, "molt: go.sum: %v\n", err)
	}

	src, err := scan.Dir(root)
	if err != nil {
		fmt.Fprintf(stderr, "molt: %v\n", err)
		return exitInternal
	}

	report := analyze.Run(mod, src)
	report.SumModules = len(sum)

	if opt.json {
		if err := render.JSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "molt: %v\n", err)
			return exitInternal
		}
	} else {
		style := render.StyleFor(stdout)
		if err := render.Text(stdout, report, style, opt.verbose); err != nil {
			fmt.Fprintf(stderr, "molt: %v\n", err)
			return exitInternal
		}
		if opt.verbose {
			if err := render.Skipped(stdout, report, style); err != nil {
				fmt.Fprintf(stderr, "molt: %v\n", err)
				return exitInternal
			}
		}
	}

	if opt.showDiff || opt.apply {
		changed, err := rewriteAll(root, src, opt.apply, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "molt: %v\n", err)
			return exitInternal
		}
		if opt.apply && changed > 0 {
			fmt.Fprintf(stdout, "\nRewrote %s. Run go mod tidy to drop the requires, then go test ./... to confirm.\n",
				plural(changed, "file", "files"))
		}
		if opt.showDiff && changed == 0 {
			fmt.Fprintln(stdout, "\nNo mechanical migration applies to this module.")
		}
	}

	if opt.exitCode && !report.Clean() {
		return exitFindings
	}
	return exitOK
}

// rewriteAll walks the scanned files and applies every mechanical migration each
// one is eligible for. With apply false it prints diffs and writes nothing.
func rewriteAll(root string, src *scan.Result, apply bool, stdout, stderr io.Writer) (int, error) {
	mechanical := make([]corpus.Migration, 0, 8)
	for _, m := range corpus.All() {
		if m.Mechanical() {
			mechanical = append(mechanical, m)
		}
	}

	changed := 0
	for _, f := range src.Files {
		// Only bother with files importing something molt can act on.
		applicable := mechanical[:0:0]
		for _, m := range mechanical {
			for _, imp := range f.Imports {
				if imp.Path == m.Module {
					applicable = append(applicable, m)
					break
				}
			}
		}
		if len(applicable) == 0 {
			continue
		}

		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		before, err := os.ReadFile(abs)
		if err != nil {
			fmt.Fprintf(stderr, "molt: %s: %v\n", f.Path, err)
			continue
		}
		res, err := rewrite.File(f.Path, before, applicable)
		if err != nil {
			// A file molt cannot rewrite cleanly is left exactly as it was.
			fmt.Fprintf(stderr, "molt: %s: %v\n", f.Path, err)
			continue
		}
		if !res.Changed() {
			continue
		}

		if apply {
			// Preserve the original mode rather than imposing one.
			mode := os.FileMode(0o644)
			if info, err := os.Stat(abs); err == nil {
				mode = info.Mode().Perm()
			}
			if err := os.WriteFile(abs, res.Source, mode); err != nil {
				return changed, fmt.Errorf("%s: %w", f.Path, err)
			}
			fmt.Fprintf(stdout, "  rewrote %s\n", f.Path)
		} else {
			if patch := diff.Unified(f.Path, before, res.Source); patch != "" {
				fmt.Fprintln(stdout)
				fmt.Fprint(stdout, patch)
			}
		}
		changed++
	}
	return changed, nil
}

func readGoMod(root string) (*gomod.File, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return gomod.Parse(f)
}

func readGoSum(root string) ([]string, error) {
	f, err := os.Open(filepath.Join(root, "go.sum"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return gomod.SumModules(f)
}

func printCorpus(w io.Writer) {
	rows := corpus.All()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Module < rows[j].Module })
	total, mech := corpus.Count()
	fmt.Fprintf(w, "molt corpus: %d rows, %d mechanical\n", total, mech)
	fmt.Fprintf(w, "source: https://zerodepshack.com/cheatsheets\n\n")
	for _, m := range rows {
		kind := "advisory"
		if m.Mechanical() {
			kind = "mechanical"
		}
		fmt.Fprintf(w, "%s\n  -> %s  (stdlib since %s, %s)\n", m.Module, m.Target, m.Since, kind)
		if m.Why != "" {
			fmt.Fprintf(w, "  %s\n", m.Why)
		}
		if len(m.Blocked) > 0 {
			names := make([]string, 0, len(m.Blocked))
			for k := range m.Blocked {
				names = append(names, k)
			}
			sort.Strings(names)
			fmt.Fprintf(w, "  blocked symbols: %s\n", strings.Join(names, ", "))
		}
		fmt.Fprintln(w)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `molt - find the dependencies the Go standard library has replaced

Usage:
  molt [flags] [directory]

The directory defaults to the current one and must contain a go.mod for the
dependency counts to mean anything. Source analysis works without it.

Flags:
`)
	fs.PrintDefaults()
	fmt.Fprint(w, `
Exit codes:
  0  success
  1  findings present, with -exit-code
  2  bad flags, or the path is not a directory
  3  molt could not complete

Examples:
  molt .                      report on the current module
  molt -v .                   include rationale and per-symbol detail
  molt -diff .                show the patch molt would apply
  molt -apply . && go mod tidy && go test ./...
  molt -json . > report.json  machine-readable output
  molt -exit-code .           fail a CI job when findings exist
`)
}
