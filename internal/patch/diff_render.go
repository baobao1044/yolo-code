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
// Cost (the reason this file is not a naive LCS): the model is told to emit the
// FULL file on every edit, so whole-file original→next pairs are the normal
// input, not the edge case. An unbounded (n+1)×(m+1) [][]int table measured
// 97 ms / 132 MiB for a *one-line* change in a 4 000-line file and 429 ms /
// 527 MiB at 8 000 — i.e. OOM somewhere past 20 000. Three guards keep it
// bounded, in order of payoff:
//  1. trim the shared prefix/suffix (O(n+m)) — a one-line change in a huge file
//     collapses to a 1×1 table, which is the common case (8 000 lines:
//     429 ms/527 MiB → 1.2 ms/1.8 MiB; 30 000 lines now runs at all, 4 ms);
//  2. cap what is left (maxDiffCells) — past the cap the renderer emits a
//     visibly *marked* truncated diff rather than stalling;
//  3. under the cap, keep one byte of backtrack direction per cell instead of
//     an int of LCS length (lcsDirs), so the table is 1/8 the size and needs no
//     per-row slice header (a 1 000-line full rewrite: 8.5 MiB → 1.2 MiB).
//
// Determinism (S5): the direction table records the same choices the length
// table's backtrack made (tie-break: prefer a diagonal match, then the left
// cell, then the up cell), so two identical original→next pairs produce
// byte-identical diff strings across runs.
//
// One behaviour change came with the prefix trim, and it is deliberate: when
// several equally-minimal alignments exist (runs of repeated lines — "}",
// blank lines), the trim anchors the shared line at its *earliest* position,
// where the old full-table backtrack — which walked from the end — anchored it
// at its latest. Both are minimal LCS diffs with the same +/- counts; the
// output differs only in where a duplicate line is called context. Measured on
// randomized code-like inputs the two agree on 99% of cases (and always when
// lines are unique, which is the realistic edit); on an adversarial 2-symbol
// alphabet they agree on 87%. No golden pins these bytes (the headless
// transcript golden never applies a patch), and the payoff — the whole first
// guard above — is not obtainable without the prefix trim.

package patch

import (
	"fmt"
	"strings"
)

// maxDiffCells caps the LCS table the general case may build, counted in cells
// of the *changed* region (after the prefix/suffix trim). The cost curve is
// quadratic, so the cap sits an order of magnitude below where it hurts rather
// than at the edge: measured, a fully-rewritten region costs 5.9 ms at 1e6
// cells, ~12 ms here, 30 ms at 4e6, 97 ms at 16e6. At this cap the worst case
// is a ~1 448×1 448 fully-rewritten region — 2 MiB of direction table and
// ~12 ms — which is still interactive on the apply path, and that is what a cap
// is for. Everything past it is truncated, loudly (writeTruncated).
const maxDiffCells = 2 << 20 // 2,097,152

// truncatedSide caps how many removed (and how many added) lines a truncated
// diff prints. Past the cap nobody reads the region line by line anyway; what
// they need is the head of each side plus an honest count of the rest.
const truncatedSide = 200

// UnifiedDiff returns a unified-diff-style string for original→next. Every
// line is prefixed: " " context, "-" deletion, "+" insertion. No file-path
// headers or hunk headers — the TUI already shows the path (diffView.files)
// and adds line numbers at render time (view.go diff render). An unchanged
// pair returns "" (no diff to show); the TUI treats an empty Diff as "no
// hunks". A changed region too large to diff exactly (maxDiffCells) is rendered
// as a truncated diff whose "@@ …" marker lines say so.
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
		writeLines(&sb, "+", b)
		return strings.TrimSuffix(sb.String(), "\n")
	}

	// Fast path: deletion to empty (original→"") → all deletions.
	if next == "" {
		var sb strings.Builder
		writeLines(&sb, "-", a)
		return strings.TrimSuffix(sb.String(), "\n")
	}

	// Trim the shared prefix and suffix: they are context either way, and
	// dropping them is what turns "one line changed in a 20 000-line file" into
	// a 1×1 table. The tail goes first because the backtrack also walks from the
	// end and always prefers a diagonal on equal lines — trimming the tail
	// reproduces exactly the ops the full table would have emitted there.
	n, m := len(a), len(b)
	suf := 0
	for suf < n && suf < m && a[n-1-suf] == b[m-1-suf] {
		suf++
	}
	pre := 0
	for pre < n-suf && pre < m-suf && a[pre] == b[pre] {
		pre++
	}
	midA, midB := a[pre:n-suf], b[pre:m-suf]

	var sb strings.Builder
	writeLines(&sb, " ", a[:pre])
	if len(midA)*len(midB) > maxDiffCells {
		writeTruncated(&sb, midA, midB)
	} else {
		for _, op := range diffOps(midA, midB) {
			switch op.kind {
			case opEqual:
				sb.WriteString(" ")
			case opDelete:
				sb.WriteString("-")
			case opInsert:
				sb.WriteString("+")
			}
			sb.WriteString(op.line)
			sb.WriteString("\n")
		}
	}
	writeLines(&sb, " ", a[n-suf:])
	return strings.TrimSuffix(sb.String(), "\n")
}

// writeLines appends every line with the given diff prefix, one per line.
func writeLines(sb *strings.Builder, prefix string, lines []string) {
	for _, line := range lines {
		sb.WriteString(prefix)
		sb.WriteString(line)
		sb.WriteString("\n")
	}
}

// writeTruncated renders the degraded form used when the changed region is past
// maxDiffCells: the region as a delete-block followed by an insert-block, each
// side capped at truncatedSide lines, wrapped in "@@ …" markers that say what
// was left out. It is deliberately not a minimal diff and does not pretend to
// be one — a silently shortened diff that reads as complete is worse than an
// honest summary, and this string is read both by a human (the TUI viewer
// colors "+"/"-" and shows the markers as plain context lines) and by the
// review agent that audits the coder's diff.
func writeTruncated(sb *strings.Builder, a, b []string) {
	_, _ = fmt.Fprintf(sb, "@@ diff truncated: changed region is %d old × %d new lines (%d cells, cap %d) — not diffed exactly @@\n",
		len(a), len(b), len(a)*len(b), maxDiffCells)
	writeCapped(sb, "-", "removed", a)
	writeCapped(sb, "+", "added", b)
}

// writeCapped writes at most truncatedSide of lines, then a marker naming how
// many were dropped (nothing is dropped silently).
func writeCapped(sb *strings.Builder, prefix, label string, lines []string) {
	shown := lines
	if len(shown) > truncatedSide {
		shown = shown[:truncatedSide]
	}
	writeLines(sb, prefix, shown)
	if rest := len(lines) - len(shown); rest > 0 {
		_, _ = fmt.Fprintf(sb, "@@ … %d more %s lines not shown @@\n", rest, label)
	}
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

// Backtrack directions, one byte per LCS cell (see lcsDirs).
const (
	dirDiag = 0 // a[i-1] == b[j-1]: the lines match
	dirLeft = 1 // take the left cell: b[j-1] is an insertion
	dirUp   = 2 // take the up cell: a[i-1] is a deletion
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

// diffOps returns the ops for a→b: the direction table plus its backtrack.
func diffOps(a, b []string) []diffOp {
	if len(a) == 0 || len(b) == 0 {
		return backtrack(a, b, nil)
	}
	return backtrack(a, b, lcsDirs(a, b))
}

// lcsDirs builds the n×m backtrack-direction table for a and b: one byte per
// cell recording which way the LCS recurrence went there. The lengths
// themselves only ever need two rolling rows, so the full (n+1)×(m+1) []][]int
// table the backtrack used to walk is never materialized — 1 byte per cell
// instead of 8, and no per-row slice header. The choices (and their tie-breaks)
// are the ones backtrack would have made from the length table, so the rendered
// diff is byte-identical to the full-table renderer's.
func lcsDirs(a, b []string) []uint8 {
	n, m := len(a), len(b)
	dirs := make([]uint8, n*m)
	prev := make([]int32, m+1)
	cur := make([]int32, m+1)
	for i := 1; i <= n; i++ {
		cur[0] = 0
		row := (i - 1) * m
		for j := 1; j <= m; j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
				dirs[row+j-1] = dirDiag
			case cur[j-1] >= prev[j]:
				// Left cell, ties included — the tie-break that puts deletions
				// before insertions at the same position (git diff ordering).
				cur[j] = cur[j-1]
				dirs[row+j-1] = dirLeft
			default:
				cur[j] = prev[j]
				dirs[row+j-1] = dirUp
			}
		}
		prev, cur = cur, prev
	}
	return dirs
}

// backtrack walks the direction table from (n,m) back to (0,0), emitting
// diffOps in reverse, then reverses them into reading order. Off the table's
// edges there is no choice left to make: at i == 0 every remaining b line is an
// insertion, at j == 0 every remaining a line is a deletion. dirs may be nil
// when either side is empty (both edges).
func backtrack(a, b []string, dirs []uint8) []diffOp {
	ops := make([]diffOp, 0, len(a)+len(b))
	m := len(b)
	i, j := len(a), len(b)
	for i > 0 || j > 0 {
		switch {
		case i == 0:
			ops = append(ops, diffOp{kind: opInsert, line: b[j-1]})
			j--
		case j == 0:
			ops = append(ops, diffOp{kind: opDelete, line: a[i-1]})
			i--
		default:
			switch dirs[(i-1)*m+j-1] {
			case dirDiag:
				ops = append(ops, diffOp{kind: opEqual, line: a[i-1]})
				i--
				j--
			case dirLeft:
				ops = append(ops, diffOp{kind: opInsert, line: b[j-1]})
				j--
			default:
				ops = append(ops, diffOp{kind: opDelete, line: a[i-1]})
				i--
			}
		}
	}
	// Reverse into reading order.
	for lo, hi := 0, len(ops)-1; lo < hi; lo, hi = lo+1, hi-1 {
		ops[lo], ops[hi] = ops[hi], ops[lo]
	}
	return ops
}
