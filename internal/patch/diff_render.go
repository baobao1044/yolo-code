// The diff renderer (Phase D): produces a unified-diff-style string from an
// original→next content pair so the TUI diff viewer (TUI-004) can show real
// hunks instead of just "+N -N" counts. The renderer is a line-level
// longest-common-subsequence diff: matched lines are context (prefixed " "),
// old-only lines are deletions ("-"), new-only lines are insertions ("+").
//
// stdlib-only (patch imports only event + stdlib, File 15 §15.15.2). The TUI
// never imports this package — the engine computes the diff string once and
// carries it on PatchAppliedEvent.Diff, so the TUI reads a string, not a
// patch symbol (the import matrix holds).
//
// Determinism (S5): the LCS table is walked deterministically (tie-break:
// prefer a diagonal match, then the up cell, then the left cell) so two
// identical original→next pairs produce byte-identical diff strings across
// runs.

package patch

import "strings"

// UnifiedDiff returns a unified-diff-style string for original→next. Every
// line is prefixed: " " context, "-" deletion, "+" insertion. No file-path
// headers or hunk headers — the TUI already shows the path (diffView.files)
// and adds line numbers at render time (view.go diff render). An unchanged
// pair returns "" (no diff to show); the TUI treats an empty Diff as "no
// hunks".
func UnifiedDiff(original, next string) string {
	a := splitLines(original)
	b := splitLines(next)

	// Fast path: identical content → no diff.
	if equalSlices(a, b) {
		return ""
	}

	// Fast path: new file (empty original) → all insertions.
	if original == "" {
		var sb strings.Builder
		for _, line := range b {
			sb.WriteString("+")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		return strings.TrimSuffix(sb.String(), "\n")
	}

	// Fast path: deletion to empty (original→"") → all deletions.
	if next == "" {
		var sb strings.Builder
		for _, line := range a {
			sb.WriteString("-")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		return strings.TrimSuffix(sb.String(), "\n")
	}

	// General case: LCS table + backtrack.
	table := lcsTable(a, b)
	ops := backtrack(a, b, table)

	var sb strings.Builder
	for _, op := range ops {
		switch op.kind {
		case opEqual:
			sb.WriteString(" ")
			sb.WriteString(op.line)
		case opDelete:
			sb.WriteString("-")
			sb.WriteString(op.line)
		case opInsert:
			sb.WriteString("+")
			sb.WriteString(op.line)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// diffOp is one line of the rendered diff: kind + the (un-prefixed) line text.
type diffOp struct {
	kind int    // opEqual / opDelete / opInsert
	line string // the line content without the " "/"-"/"+" prefix
}

const (
	opEqual  = 0 // context line (present in both)
	opDelete = 1 // line only in original
	opInsert = 2 // line only in next
)

// splitLines splits s on newlines without a trailing empty element (the
// artifact of a string ending in "\n"). An empty string yields an empty
// slice (mirrors how splitDiffLines works in diff.go).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// equalSlices reports whether two string slices are element-wise equal.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lcsTable builds the full (n+1)×(m+1) LCS length table. O(n*m) time + space —
// file diffs are small, and we need the full table to backtrack (the rolling-
// row optimization in lcsLen only gives the length, not the sequence).
func lcsTable(a, b []string) [][]int {
	n, m := len(a), len(b)
	tbl := make([][]int, n+1)
	for i := range tbl {
		tbl[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				tbl[i][j] = tbl[i-1][j-1] + 1
			} else if tbl[i-1][j] >= tbl[i][j-1] {
				tbl[i][j] = tbl[i-1][j]
			} else {
				tbl[i][j] = tbl[i][j-1]
			}
		}
	}
	return tbl
}

// backtrack walks the LCS table from (n,m) back to (0,0), emitting diffOps in
// reverse, then reverses them into reading order. Tie-break (when the up and
// left cells tie, i.e. tbl[i-1][j] == tbl[i][j-1]): prefer a diagonal match if
// the lines are equal; otherwise prefer the left cell (insertion) so that
// after reversal deletions precede insertions at the same position (matches
// git diff ordering: old lines before new lines).
func backtrack(a, b []string, tbl [][]int) []diffOp {
	var ops []diffOp
	i, j := len(a), len(b)
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && a[i-1] == b[j-1]:
			ops = append(ops, diffOp{kind: opEqual, line: a[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || tbl[i][j-1] >= tbl[i-1][j]):
			// Insertion (left cell). On a tie, prefer this so deletions end
			// up before insertions after reversal (git diff ordering).
			ops = append(ops, diffOp{kind: opInsert, line: b[j-1]})
			j--
		default: // i > 0 && (j == 0 || tbl[i-1][j] > tbl[i][j-1])
			ops = append(ops, diffOp{kind: opDelete, line: a[i-1]})
			i--
		}
	}
	// Reverse into reading order.
	for lo, hi := 0, len(ops)-1; lo < hi; lo, hi = lo+1, hi-1 {
		ops[lo], ops[hi] = ops[hi], ops[lo]
	}
	return ops
}
