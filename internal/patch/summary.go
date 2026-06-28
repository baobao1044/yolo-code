// The patch.applied diff summary (File 10 §10.6 + File 05 §5.4.4): after a
// successful apply, the engine reports what changed — files touched,
// insertions, deletions — so the transcript/TUI can show the diff (Sprint 5
// exit bar). The summary is computed by a deterministic line diff of the
// original vs the next content; the per-file list is path-sorted so two
// identical patches produce byte-identical transcripts (S5 determinism — never
// map iteration order).
//
// The diff is a simple longest-common-subsequence line count, not a real
// unified diff: insertions = lines in next but not in original, deletions =
// lines in original but not in next, counted per changed run. It's a *summary*
// (the +/- numbers git shows), not the patch itself — good enough for the
// transcript and the cost/size heuristics; a full diff viewer is TUI-004.

package patch

import "sort"

// Change is one file's before/after content for Summarize to diff. Original is
// empty for a newly-created file; Next is the new on-disk content.
type Change struct {
	Path     string
	Original string
	Next     string
}

// FileStat is one file's contribution to the Summary (mirrors event.PatchFile
// without the event import — the engine copies these into the event).
type FileStat struct {
	Path       string
	Insertions int
	Deletions  int
	New        bool
}

// Summary is the aggregate diff summary published in patch.applied: the
// per-file stats (path-sorted) and the totals.
type Summary struct {
	Files      []FileStat
	Insertions int
	Deletions  int
}

// Summarize diffs each Change and returns the aggregate Summary. Files come
// out path-sorted (deterministic); totals are the sum of per-file counts. A
// new file (empty Original) counts every non-empty next line as an insertion;
// a deleted-to-empty file counts every original line as a deletion.
func Summarize(changes []Change) Summary {
	stats := make([]FileStat, 0, len(changes))
	for _, c := range changes {
		ins, del := lineDiff(c.Original, c.Next)
		stats = append(stats, FileStat{
			Path:       c.Path,
			Insertions: ins,
			Deletions:  del,
			New:        c.Original == "" && c.Next != "",
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Path < stats[j].Path })

	var s Summary
	s.Files = stats
	for _, f := range stats {
		s.Insertions += f.Insertions
		s.Deletions += f.Deletions
	}
	return s
}

// lineDiff counts the added/removed non-empty lines between old and new content.
// It uses a longest-common-subsequence over non-empty lines: a line present in
// both (matched in order) is context; an old line with no match is a deletion,
// a new line with no match is an insertion. Trailing/empty lines are framing,
// not content, so they don't count — a patch that only changes the trailing
// newline reports 0/0.
func lineDiff(old, next string) (insertions, deletions int) {
	a := nonEmptyLines(old)
	b := nonEmptyLines(next)
	// LCS length over the two line slices; the unmatched tails are the
	// insertions/deletions.
	lcs := lcsLen(a, b)
	deletions = len(a) - lcs
	insertions = len(b) - lcs
	if deletions < 0 {
		deletions = 0
	}
	if insertions < 0 {
		insertions = 0
	}
	return insertions, deletions
}

// nonEmptyLines splits s on newlines and drops empty lines (framing), returning
// the content lines that count toward the diff.
func nonEmptyLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		line := s[start:]
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// lcsLen returns the length of the longest common subsequence of two line
// slices. O(n*m) is fine here — file diffs are small; this runs once per apply.
func lcsLen(a, b []string) int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return 0
	}
	// Two rolling rows keep it O(min(n,m)) space.
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] > cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
		// reset cur for the next row (prev now holds the just-computed row)
		for j := range cur {
			cur[j] = 0
		}
	}
	return prev[m]
}
