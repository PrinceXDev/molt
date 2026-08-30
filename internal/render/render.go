// Package render writes reports as text or JSON, using text/tabwriter for
// alignment and raw ANSI for colour.
//
// Both renderers are deterministic: nothing here reads a clock, a random source,
// or any environment beyond the colour decision.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"molt/internal/analyze"
)

// The five escapes molt needs.
const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiGreen = "\x1b[32m"
	ansiAmber = "\x1b[33m"
	ansiBlue  = "\x1b[36m"
)

// errWriter latches the first write error so the renderers can read as a page
// of Fprintf calls without a check after each one.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

// Style decides whether output carries escapes.
type Style struct{ color bool }

// StyleFor returns the style appropriate to w. Colour is enabled only when w is
// the process's stdout, that stdout is a terminal, and NO_COLOR is unset.
func StyleFor(w io.Writer) Style {
	if os.Getenv("NO_COLOR") != "" {
		return Style{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return Style{}
	}
	info, err := f.Stat()
	if err != nil {
		return Style{}
	}
	return Style{color: info.Mode()&os.ModeCharDevice != 0}
}

// Plain returns a style that never emits escapes, for tests and for pipes.
func Plain() Style { return Style{} }

func (s Style) wrap(code, text string) string {
	if !s.color || text == "" {
		return text
	}
	return code + text + ansiReset
}

func (s Style) bold(t string) string  { return s.wrap(ansiBold, t) }
func (s Style) dim(t string) string   { return s.wrap(ansiDim, t) }
func (s Style) green(t string) string { return s.wrap(ansiGreen, t) }
func (s Style) amber(t string) string { return s.wrap(ansiAmber, t) }
func (s Style) blue(t string) string  { return s.wrap(ansiBlue, t) }

// JSON writes the report as indented JSON. The shape is the analyze.Report
// struct, documented in README under "JSON output".
func JSON(w io.Writer, r *analyze.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Text writes the human report.
func Text(dst io.Writer, r *analyze.Report, s Style, verbose bool) error {
	w := &errWriter{w: dst}
	name := r.Module
	if name == "" {
		name = "(no go.mod found)"
	}
	fmt.Fprintf(w, "%s %s\n\n", s.bold("molt"), name)

	writeCounts(w, r, s)

	auto, partial, advisory, orphans := partition(r.Findings)

	if len(auto) > 0 {
		fmt.Fprintf(w, "\n%s %s\n\n", s.green(s.bold("REMOVABLE")), s.dim("molt can apply these in full"))
		for _, f := range auto {
			writeFinding(w, f, s, verbose, true)
		}
	}
	if len(partial) > 0 {
		fmt.Fprintf(w, "\n%s %s\n\n", s.green(s.bold("PARTLY REMOVABLE")), s.dim("molt can apply these to some files"))
		for _, f := range partial {
			writeFinding(w, f, s, verbose, true)
		}
	}
	if len(advisory) > 0 {
		fmt.Fprintf(w, "\n%s %s\n\n", s.amber(s.bold("NEEDS A HUMAN")), s.dim("the replacement changes the shape of the code"))
		for _, f := range advisory {
			writeFinding(w, f, s, verbose, false)
		}
	}
	if len(orphans) > 0 {
		fmt.Fprintf(w, "\n%s %s\n\n", s.blue(s.bold("UNUSED")), s.dim("declared in go.mod, imported nowhere in scanned source"))
		for _, f := range orphans {
			fmt.Fprintf(w, "  %s\n", f.Module)
			if verbose {
				fmt.Fprintf(w, "    %s\n", s.dim(f.Note))
			}
		}
		fmt.Fprintf(w, "\n  %s\n", s.dim("molt skips files excluded by build constraints, so check before removing."))
	}
	if len(r.Kept) > 0 && verbose {
		fmt.Fprintf(w, "\n%s %s\n\n", s.bold("KEEP"), s.dim("no replacement in molt's corpus"))
		tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
		for _, k := range r.Kept {
			fmt.Fprintf(tw, "  %s\t%d symbols\t%d uses\n", k.Module, k.Symbols, k.Refs)
		}
		tw.Flush()
	}

	if r.Clean() {
		fmt.Fprintf(w, "\n%s\n", s.green("Nothing to remove. Every dependency molt knows about is either absent or still earning its place."))
	}

	writeSummary(w, r, s)
	return w.err
}

func writeCounts(w io.Writer, r *analyze.Report, s Style) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "  Go files scanned\t%d\n", r.GoFiles)
	if r.DirectDeps > 0 || r.IndirectDeps > 0 {
		fmt.Fprintf(tw, "  Direct requires\t%d\n", r.DirectDeps)
		fmt.Fprintf(tw, "  Indirect requires\t%d\n", r.IndirectDeps)
	}
	if r.SumModules > 0 {
		line := fmt.Sprintf("  Modules in go.sum\t%d", r.SumModules)
		if amp := r.Amplification(); amp > 0 {
			line += fmt.Sprintf("\t%s", s.dim(fmt.Sprintf("%.1fx your %d direct requires", amp, r.DirectDeps)))
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
}

// writeFinding prints one migration. The compact form is two lines; verbose adds
// the rationale, the guidance and the per-symbol detail.
func writeFinding(w io.Writer, f analyze.Finding, s Style, verbose, auto bool) {
	fmt.Fprintf(w, "  %s %s %s\n", s.bold(f.Module), s.dim("->"), s.bold(f.Target))

	uses := 0
	for _, sym := range f.Symbols {
		uses += sym.Count
	}
	fmt.Fprintf(w, "    %s\n", s.dim(fmt.Sprintf("stdlib since %s · %s, %s, %s",
		f.Since,
		count(len(f.Symbols), "symbol", "symbols"),
		count(uses, "use", "uses"),
		count(len(f.Files), "file", "files"))))

	if f.Partial {
		fmt.Fprintf(w, "    %s\n", s.green(fmt.Sprintf("molt can migrate %d of %d files now",
			len(f.AutoFiles), len(f.Files))))
	}

	if names := symbolLine(f, verbose); names != "" {
		fmt.Fprintf(w, "    %s\n", names)
	}

	if verbose {
		if f.Why != "" {
			fmt.Fprintf(w, "    %s %s\n", s.dim("why "), f.Why)
		}
		if f.Guidance != "" {
			fmt.Fprintf(w, "    %s %s\n", s.dim("do  "), f.Guidance)
		}
		if f.Note != "" {
			fmt.Fprintf(w, "    %s %s\n", s.dim("note"), f.Note)
		}
		for _, file := range f.AutoFiles {
			fmt.Fprintf(w, "    %s %s\n", s.green("will rewrite"), file)
		}
		for _, sym := range f.Symbols {
			detail := fmt.Sprintf("%s (%d)", sym.Name, sym.Count)
			if sym.Replacement != "" {
				detail += " -> " + sym.Replacement
			}
			if sym.First != "" {
				detail += "  " + s.dim(sym.First)
			}
			fmt.Fprintf(w, "      %s\n", detail)
			if sym.Blocked != "" {
				fmt.Fprintf(w, "        %s %s\n", s.amber("blocked:"), sym.Blocked)
			}
		}
	}
	if !auto && len(f.Blockers) > 0 && !verbose {
		fmt.Fprintf(w, "    %s %s\n", s.dim("blocked by"), f.Blockers[0])
	}
	fmt.Fprintln(w)
}

// symbolLine lists the used symbols, truncated in compact mode so a dependency
// with fifty call sites does not flood the report.
func symbolLine(f analyze.Finding, verbose bool) string {
	if verbose || len(f.Symbols) == 0 {
		return ""
	}
	const max = 6
	names := make([]string, 0, len(f.Symbols))
	for i, sym := range f.Symbols {
		if i == max {
			names = append(names, fmt.Sprintf("and %d more", len(f.Symbols)-max))
			break
		}
		names = append(names, sym.Name)
	}
	return strings.Join(names, ", ")
}

func writeSummary(w io.Writer, r *analyze.Report, s Style) {
	fmt.Fprintln(w)
	parts := []string{
		fmt.Sprintf("%d removable", r.Removable),
		fmt.Sprintf("%d partly removable", r.PartiallyRemovable),
		fmt.Sprintf("%d need a human", r.Advisory),
		fmt.Sprintf("%d unused", r.Orphans),
	}
	fmt.Fprintf(w, "  %s\n", s.bold(strings.Join(parts, " · ")))
	fmt.Fprintf(w, "  %s\n", s.dim(fmt.Sprintf(
		"corpus: %d rows, %d mechanical · source: zerodepshack.com/cheatsheets",
		r.CorpusRows, r.CorpusMechanical)))
	if len(r.Skipped) > 0 {
		fmt.Fprintf(w, "  %s\n", s.dim(fmt.Sprintf("%d files skipped (run with -v to list)", len(r.Skipped))))
	}
}

func partition(findings []analyze.Finding) (auto, partial, advisory, orphans []analyze.Finding) {
	for _, f := range findings {
		switch {
		case f.Kind == analyze.KindOrphan:
			orphans = append(orphans, f)
		case f.Auto:
			auto = append(auto, f)
		case f.Partial:
			partial = append(partial, f)
		default:
			advisory = append(advisory, f)
		}
	}
	return auto, partial, advisory, orphans
}

// Skipped writes the skipped-file list, for verbose runs.
func Skipped(dst io.Writer, r *analyze.Report, s Style) error {
	if len(r.Skipped) == 0 {
		return nil
	}
	w := &errWriter{w: dst}
	fmt.Fprintf(w, "\n%s\n\n", s.bold("SKIPPED"))
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	for _, sk := range r.Skipped {
		fmt.Fprintf(tw, "  %s\t%s\n", sk.Path, sk.Reason)
	}
	tw.Flush()
	return w.err
}

// count formats "1 file" and "3 files".
func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
