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
	"strings"
)

// ErrConflict signals that two todos touched the same file and the merge
// cannot combine them without the Patch Engine's three-way resolution.
var ErrConflict = errors.New("coord: merge conflict — todos overlap on the same file")

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
// summary. An empty / all-failed plan yields an empty patch that trivially
// verifies (no verifier call).
func Merge(ctx context.Context, plan *Plan, diffs map[string]string, v Verifier) (MergedPatch, error) {
	var mp MergedPatch

	// Collect the Done todos' diffs + artifacts, and tally the summary.
	fileOwners := make(map[string][]string) // file -> todos touching it
	var combined []string
	for i := range plan.Todos {
		td := &plan.Todos[i]
		if td.Status == Done {
			mp.Summary.Done++
			if d, ok := diffs[td.ID]; ok {
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

	ok, err := v.Verify(ctx, mp.CombinedDiff)
	if err != nil {
		return mp, err
	}
	mp.Verified = ok
	if !ok {
		return mp, errors.New("coord: merged patch failed verification")
	}
	return mp, nil
}
