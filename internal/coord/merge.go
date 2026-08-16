// merge.go — Merge + re-verify (File 12 §12.6).
//
// When all todos are Done or Failed, the orchestrator merges the per-todo
// diffs into a MergedPatch and re-verifies the combined diff through the
// Verifier seam. Overlap detection is via Todo.Artifacts (file paths): two
// todos touching the same file → a Conflict (the Patch Engine serializes
// concurrent overlaps, File 10 §10.5; Sprint 10 detects the overlap in-memory
// and surfaces it).
//
// Spec gap (Decision 2 + Sprint 10 design): the real combined diff reuses the
// Patch Engine's git snapshots (File 10 §10.5, "merge has no separate
// persistence mechanism — it aggregates existing checkpoints"). Sprint 10
// combines in-memory diff strings; the three-way git merge is the integration
// sprint. This function is the seam the integration sprint fills.

package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrConflict signals that two todos touched the same file and the merge
// cannot combine them without the Patch Engine's three-way resolution.
var ErrConflict = errors.New("coord: merge conflict — todos overlap on the same file")

// ErrNotVerified signals that the Verifier seam rejected the combined diff.
// Always wrapped with the merge's shape (done-todo count, diff size, leading
// bytes) — "failed verification" on its own tells the caller nothing about
// whether the patch was malformed, contentless, or merely rejected.
var ErrNotVerified = errors.New("coord: merged patch failed verification")

// MergedPatch is the orchestrator's merge output (File 12 §12.6): the combined
// diff, a done/failed summary, any conflicts, and whether the verifier passed.
type MergedPatch struct {
	// CombinedDiff is the concatenation of the Done todos' diffs (Failed
	// todos are skipped — their diffs are not merged).
	CombinedDiff string
	// Summary counts done/failed per todo (the §12.6 status table).
	Summary MergeSummary
	// Conflicts lists the (todo, file) pairs that collided. Non-empty means
	// Merge returned ErrConflict.
	Conflicts []MergeConflict
	// Verified is true iff the Verifier seam accepted the combined diff.
	Verified bool
}

// MergeSummary is the done/failed tally per todo (File 12 §12.6).
type MergeSummary struct {
	Done   int
	Failed int
}

// MergeConflict records one overlap: two todos touched the same file.
type MergeConflict struct {
	File  string
	Todos []string // the todo IDs that collided on File
}

// Merge combines the Done todos' diffs, detects same-file overlaps via
// Artifacts, re-verifies the combined diff through the Verifier seam, and
// returns the MergedPatch. Returns ErrConflict if any two Done todos share an
// artifact file; returns the verifier's error if re-verification fails.
//
// Failed todos are skipped (their diffs are not merged) but counted in the
// summary, as are Done todos whose diff is missing or blank. A plan that is
// empty, all-failed, or produced no patch at all yields an empty patch that
// trivially verifies (no verifier call).
//
// A nil Verifier combines without re-verifying: Verified stays false and no
// error is returned. That is the cancel path — the successful todos' work is
// still worth combining and reporting, but re-running a verifier (in
// production, the test suite) after the operator pressed Ctrl-C is not what
// they asked for. Callers must read Verified, not "err == nil", as the
// evidence that something checked the patch.
func Merge(ctx context.Context, plan *Plan, diffs map[string]string, v Verifier) (MergedPatch, error) {
	var mp MergedPatch

	// Collect the Done todos' diffs + artifacts, and tally the summary.
	fileOwners := make(map[string][]string) // file -> todos touching it
	var combined []string
	for i := range plan.Todos {
		td := &plan.Todos[i]
		if td.Status == Done {
			mp.Summary.Done++
			// Blank diffs are dropped, not joined. A Done todo that produced
			// no patch contributes nothing, and joining it in would fabricate
			// separator-only content no todo wrote ("\n\n" for three blank
			// diffs) — non-empty enough to skip the trivial-verify path below,
			// contentless enough for the verifier to then reject. "Carries no
			// patch" has to mean the same thing here, at that short-circuit,
			// and to the verifier.
			if d := diffs[td.ID]; strings.TrimSpace(d) != "" {
				combined = append(combined, d)
			}
			for _, f := range td.Artifacts {
				fileOwners[f] = append(fileOwners[f], td.ID)
			}
		} else if td.Status == Failed {
			mp.Summary.Failed++
		}
	}
	mp.CombinedDiff = strings.Join(combined, "\n")

	// Detect same-file overlaps among Done todos.
	for f, owners := range fileOwners {
		if len(owners) > 1 {
			mp.Conflicts = append(mp.Conflicts, MergeConflict{File: f, Todos: owners})
		}
	}
	if len(mp.Conflicts) > 0 {
		return mp, ErrConflict
	}

	// Re-verify the combined diff. An empty patch trivially verifies (no
	// verifier call) — the orchestrator merging an empty/failed plan should
	// not pay for a verifier round-trip.
	if mp.CombinedDiff == "" {
		mp.Verified = true
		return mp, nil
	}

	// No verifier wired: combine, report, and leave Verified false. Nothing
	// checked this patch, and saying otherwise is the lie the whole Verified
	// field exists to prevent.
	if v == nil {
		return mp, nil
	}

	ok, err := v.Verify(ctx, mp.CombinedDiff)
	if err != nil {
		return mp, fmt.Errorf("coord: verifier failed on %s: %w", mp.shape(), err)
	}
	mp.Verified = ok
	if !ok {
		return mp, fmt.Errorf("%w: verifier rejected %s", ErrNotVerified, mp.shape())
	}
	return mp, nil
}

// shape describes the combined diff for an error message: how many todos fed
// it, how big it is, and its leading bytes. Enough to tell a contentless patch
// apart from a genuinely rejected one without dumping a whole diff into a log.
func (mp MergedPatch) shape() string {
	const maxPreview = 120
	preview := mp.CombinedDiff
	suffix := ""
	if len(preview) > maxPreview {
		preview, suffix = preview[:maxPreview], "…"
	}
	return fmt.Sprintf("%d bytes from %d done todo(s), starting %q%s",
		len(mp.CombinedDiff), mp.Summary.Done, preview, suffix)
}
