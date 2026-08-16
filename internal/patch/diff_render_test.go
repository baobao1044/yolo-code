// Tests for the Phase D diff renderer: UnifiedDiff produces a unified-diff-
// style string from original→next. Each line is prefixed " " (context),
// "-" (deletion), "+" (insertion). Identical content returns "".

package patch

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// TestUnifiedDiffIdentical pins the fast path: identical original→next
// returns "" (no diff to show).
func TestUnifiedDiffIdentical(t *testing.T) {
	got := UnifiedDiff("hello\nworld\n", "hello\nworld\n")
	if got != "" {
		t.Errorf("identical content diff = %q, want \"\"", got)
	}
}

// TestUnifiedDiffEmptyOriginal pins the new-file path: an empty original →
// all lines are insertions (prefixed "+").
func TestUnifiedDiffEmptyOriginal(t *testing.T) {
	got := UnifiedDiff("", "package main\n\nfunc main() {}\n")
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "+") {
			t.Errorf("new-file diff line %q missing '+' prefix", line)
		}
	}
}

// TestUnifiedDiffEmptyNext pins the deletion-to-empty path: original→"" →
// all lines are deletions (prefixed "-").
func TestUnifiedDiffEmptyNext(t *testing.T) {
	got := UnifiedDiff("line one\nline two\n", "")
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "-") {
			t.Errorf("delete-to-empty diff line %q missing '-' prefix", line)
		}
	}
}

// TestUnifiedDiffMixed pins the general LCS case: a mix of context, deletion,
// and insertion lines with correct prefixes. Deletions precede insertions
// at the same position (git diff ordering).
func TestUnifiedDiffMixed(t *testing.T) {
	original := "package main\n\nfunc old() {}\n"
	next := "package main\n\nfunc new() {}\n"
	got := UnifiedDiff(original, next)

	lines := strings.Split(got, "\n")
	want := []string{
		" package main",  // context (unchanged)
		" ",              // context (empty line, unchanged)
		"-func old() {}", // deletion (old line)
		"+func new() {}", // insertion (new line)
	}
	if len(lines) != len(want) {
		t.Fatalf("diff = %d lines, want %d: %q", len(lines), len(want), got)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

// TestUnifiedDiffDeterministic pins S5: the same original→next pair produces
// byte-identical output across repeated calls (LCS backtrack is deterministic).
func TestUnifiedDiffDeterministic(t *testing.T) {
	original := "a\nb\nc\nd\ne\nf\n"
	next := "a\nx\nc\nd\ny\nf\n"
	first := UnifiedDiff(original, next)
	for i := 0; i < 5; i++ {
		got := UnifiedDiff(original, next)
		if got != first {
			t.Fatalf("non-deterministic diff on call %d:\nfirst: %q\ncall:  %q", i, first, got)
		}
	}
}

// TestUnifiedDiffContextPreserved pins that unchanged lines appear as context
// (prefix " ") — not dropped.
func TestUnifiedDiffContextPreserved(t *testing.T) {
	original := "keep1\nchange\nkeep2\n"
	next := "keep1\nchanged\nkeep2\n"
	got := UnifiedDiff(original, next)

	lines := strings.Split(got, "\n")
	// First and last lines are context; middle is a -/+ pair.
	if lines[0] != " keep1" {
		t.Errorf("first line = %q, want context ' keep1'", lines[0])
	}
	if lines[len(lines)-1] != " keep2" {
		t.Errorf("last line = %q, want context ' keep2'", lines[len(lines)-1])
	}
}

// linesFile builds an n-line file of distinct lines.
func linesFile(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "line %d of the file, some content here\n", i)
	}
	return sb.String()
}

// allocBytes reports the bytes f allocated, averaged over runs. It measures
// TotalAlloc rather than allocation *count* on purpose: the defect this pins is
// an (n+1)×(m+1) table, which is a handful of allocations of enormous size.
func allocBytes(f func(), runs int) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// TestUnifiedDiffOneLineChangeIsNotQuadratic is the memory guard. A one-line
// change in an 8 000-line file is the *normal* input (the system prompt tells
// the model to emit the full file), and the full-table renderer allocated the
// whole 8 001×8 001 table for it: 527 MiB and 429 ms measured, ~7 GiB and an
// OOM at 30 000 lines. The prefix/suffix trim collapses it to a 1×1 problem, so
// what is left is linear in the file: the split line slices and the output
// string. A text-only test passes with the OOM intact — this one does not.
func TestUnifiedDiffOneLineChangeIsNotQuadratic(t *testing.T) {
	const n = 8000
	original := linesFile(n)
	next := strings.Replace(original, "line 4000 of", "CHANGED 4000 of", 1)

	var got string
	used := allocBytes(func() { got = UnifiedDiff(original, next) }, 3)

	// The output alone is ~330 KiB of context lines, so the budget is generous
	// in absolute terms and still ~100x under the quadratic table's 527 MiB.
	const budget = 8 << 20
	if used > budget {
		t.Errorf("UnifiedDiff on a one-line change in %d lines allocated %d bytes, want <= %d "+
			"(a full (n+1)x(m+1) table would be ~%d)", n, used, budget, (n+1)*(n+1)*8)
	}

	// The diff must still be correct, not just cheap.
	lines := strings.Split(got, "\n")
	if len(lines) != n+1 { // n context lines, one of which became a -/+ pair
		t.Fatalf("diff = %d lines, want %d", len(lines), n+1)
	}
	if lines[4000] != "-line 4000 of the file, some content here" {
		t.Errorf("line 4000 = %q, want the deletion", lines[4000])
	}
	if lines[4001] != "+CHANGED 4000 of the file, some content here" {
		t.Errorf("line 4001 = %q, want the insertion", lines[4001])
	}
	if lines[0] != " line 0 of the file, some content here" {
		t.Errorf("line 0 = %q, want context", lines[0])
	}
	if lines[n] != " line 7999 of the file, some content here" {
		t.Errorf("last line = %q, want context", lines[n])
	}
}

// TestUnifiedDiffCapsTheChangedRegion pins the cap: a fully-rewritten region
// past maxDiffCells is not diffed exactly, is bounded in cost, and — the part
// that matters — says out loud that it was truncated. A silently shortened diff
// that reads as complete is worse than no diff.
func TestUnifiedDiffCapsTheChangedRegion(t *testing.T) {
	const n = 4000 // 16e6 cells, 8x over the cap
	var a, b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&a, "old %d aaaa\n", i)
		fmt.Fprintf(&b, "new %d bbbb\n", i)
	}
	original, next := a.String(), b.String()

	var got string
	used := allocBytes(func() { got = UnifiedDiff(original, next) }, 3)

	const budget = 4 << 20
	if used > budget {
		t.Errorf("capped diff allocated %d bytes, want <= %d (uncapped table = ~%d)",
			used, budget, n*n*8)
	}
	if !strings.Contains(got, "@@ diff truncated:") {
		t.Fatalf("truncated diff is not marked as truncated:\n%s", firstLines(got, 3))
	}
	if !strings.Contains(got, fmt.Sprintf("@@ … %d more removed lines not shown @@", n-truncatedSide)) {
		t.Errorf("elided removals not counted:\n%s", got[:min(len(got), 300)])
	}
	if !strings.Contains(got, fmt.Sprintf("@@ … %d more added lines not shown @@", n-truncatedSide)) {
		t.Errorf("elided insertions not counted")
	}
	// Both sides are shown, capped, and still carry their +/- prefixes so the
	// TUI colors them (view.go renders "@@ …" markers as plain context lines).
	if !strings.Contains(got, "\n-old 0 aaaa\n") || !strings.Contains(got, "\n+new 0 bbbb\n") {
		t.Error("truncated diff dropped the head of one side")
	}
	if shown := strings.Count(got, "\n-"); shown != truncatedSide {
		t.Errorf("removed lines shown = %d, want %d", shown, truncatedSide)
	}
}

// TestUnifiedDiffKeepsContextAroundACappedRegion pins that the trim still runs
// when the middle is capped: the shared prefix/suffix are exact context lines
// even though the region between them was too big to diff.
func TestUnifiedDiffKeepsContextAroundACappedRegion(t *testing.T) {
	const n = 2000
	var a, b strings.Builder
	a.WriteString("package main\n")
	b.WriteString("package main\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&a, "old %d\n", i)
		fmt.Fprintf(&b, "new %d\n", i)
	}
	a.WriteString("// trailer\n")
	b.WriteString("// trailer\n")

	got := UnifiedDiff(a.String(), b.String())
	lines := strings.Split(got, "\n")
	if lines[0] != " package main" {
		t.Errorf("first line = %q, want the trimmed prefix as context", lines[0])
	}
	if lines[len(lines)-1] != " // trailer" {
		t.Errorf("last line = %q, want the trimmed suffix as context", lines[len(lines)-1])
	}
	if !strings.Contains(got, "@@ diff truncated:") {
		t.Error("capped region not marked as truncated")
	}
}

// TestUnifiedDiffTrimAnchorsMatchesEarly pins the one output change the prefix
// trim brought: with several equally-minimal alignments (repeated lines), the
// shared line is context at its earliest position, not its latest. Both are
// minimal LCS diffs with identical +/- counts; this test exists so the next
// reader knows which one is intended rather than filing it as a bug.
func TestUnifiedDiffTrimAnchorsMatchesEarly(t *testing.T) {
	got := UnifiedDiff("b\n", "b\nb\na\n")
	want := " b\n+b\n+a" // the old full-table renderer produced "+b\n b\n+a"
	if got != want {
		t.Errorf("UnifiedDiff = %q, want %q", got, want)
	}
}

// firstLines returns the first n lines of s (for readable failure output).
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
