// Tests for the Phase D diff renderer: UnifiedDiff produces a unified-diff-
// style string from original→next. Each line is prefixed " " (context),
// "-" (deletion), "+" (insertion). Identical content returns "".

package patch

import (
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
		" package main", // context (unchanged)
		" ",             // context (empty line, unchanged)
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
