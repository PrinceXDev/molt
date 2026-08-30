// Package diff renders a unified diff of two byte slices.
//
// The algorithm is a longest-common-subsequence table over lines, run only on
// the region left after trimming the common prefix and suffix. That trimming is
// what keeps an O(n*m) table affordable on large files.
package diff

import (
	"fmt"
	"strings"
)

// contextLines is the number of unchanged lines shown around each hunk.
const contextLines = 3

// maxTable bounds the LCS table at 4 million cells.
const maxTable = 2000

// Unified renders the changes from a to b as a unified diff. The result is empty
// when the inputs are identical.
func Unified(name string, a, b []byte) string {
	if string(a) == string(b) {
		return ""
	}
	oldLines := splitLines(string(a))
	newLines := splitLines(string(b))

	// Trim the identical head and tail; only the middle needs the table.
	head := 0
	for head < len(oldLines) && head < len(newLines) && oldLines[head] == newLines[head] {
		head++
	}
	tail := 0
	for tail < len(oldLines)-head && tail < len(newLines)-head &&
		oldLines[len(oldLines)-1-tail] == newLines[len(newLines)-1-tail] {
		tail++
	}

	midOld := oldLines[head : len(oldLines)-tail]
	midNew := newLines[head : len(newLines)-tail]

	var ops []op
	if len(midOld) > maxTable || len(midNew) > maxTable {
		// Too large to table: report the region as wholly replaced. True, but
		// coarser than an LCS would be.
		for _, line := range midOld {
			ops = append(ops, op{kind: '-', text: line})
		}
		for _, line := range midNew {
			ops = append(ops, op{kind: '+', text: line})
		}
	} else {
		ops = lcsOps(midOld, midNew)
	}

	// Re-attach enough of the trimmed head and tail to give the hunk context.
	preStart := head - contextLines
	if preStart < 0 {
		preStart = 0
	}
	var full []op
	for _, line := range oldLines[preStart:head] {
		full = append(full, op{kind: ' ', text: line})
	}
	full = append(full, ops...)
	postEnd := len(oldLines) - tail + contextLines
	if postEnd > len(oldLines) {
		postEnd = len(oldLines)
	}
	for _, line := range oldLines[len(oldLines)-tail : postEnd] {
		full = append(full, op{kind: ' ', text: line})
	}

	oldCount, newCount := 0, 0
	for _, o := range full {
		switch o.kind {
		case ' ':
			oldCount++
			newCount++
		case '-':
			oldCount++
		case '+':
			newCount++
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n", name)
	fmt.Fprintf(&sb, "+++ b/%s\n", name)
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", preStart+1, oldCount, preStart+1, newCount)
	for _, o := range full {
		sb.WriteByte(o.kind)
		sb.WriteString(o.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

type op struct {
	kind byte // ' ', '-' or '+'
	text string
}

// lcsOps walks the LCS table backwards to produce an edit script.
func lcsOps(a, b []string) []op {
	n, m := len(a), len(b)
	// table[i][j] is the LCS length of a[i:] and b[j:].
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}

	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{kind: ' ', text: a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{kind: '-', text: a[i]})
			i++
		default:
			ops = append(ops, op{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{kind: '-', text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{kind: '+', text: b[j]})
	}
	return ops
}

// splitLines splits on newline without a trailing empty element for text that
// ends in one, as source files do.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
