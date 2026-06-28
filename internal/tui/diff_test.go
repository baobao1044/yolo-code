// Tests for TUI-004 — Diff viewer (File 14 §14.7.3). The diff viewer opens on
// patch.applied or verification.failed and shows the changed files + counts
// (hunk-colored). PatchAppliedEvent has NO diff-hunks text (spec gap: only
// Snapshot + Files []PatchFile + Insertions/Deletions), so the viewer renders
// the file list + counts, not hunks. VerificationFailedEvent carries a Reason
// the viewer displays. The viewer is display-only — it never edits; edits come
// only from patch.applied events (File 14 §14.1.1).

package tui

import (
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// TestFoldPatchAppliedOpensDiffViewer pins §14.7.3: a patch.applied event opens
// the diff viewer — m.diff is set with the file list + insertion/deletion
// counts, and focus moves to the diff pane. The mutation guard: if m.diff
// isn't set, the viewer never opens and the user can't review the change.
func TestFoldPatchAppliedOpensDiffViewer(t *testing.T) {
	m := newModelForTest()
	files := []event.PatchFile{
		{Path: "auth/login.go", Insertions: 3, Deletions: 1, New: false},
		{Path: "auth/login_test.go", Insertions: 5, Deletions: 0, New: true},
	}
	m, _ = fold(m, env(&event.PatchAppliedEvent{
		Task:       "t_1",
		Files:      files,
		Insertions: 8,
		Deletions:  1,
	}))

	if m.diff == nil {
		t.Fatal("m.diff = nil, want a diffView (patch.applied must open the viewer — mutation guard)")
	}
	if m.focus != paneDiff {
		t.Errorf("focus = %v, want paneDiff (the viewer takes focus on open)", m.focus)
	}
	if len(m.diff.files) != 2 {
		t.Fatalf("diff files = %d, want 2 (the file list from the event)", len(m.diff.files))
	}
	if m.diff.files[0].Path != "auth/login.go" {
		t.Errorf("diff files[0].Path = %q, want %q", m.diff.files[0].Path, "auth/login.go")
	}
	if m.diff.insertions != 8 {
		t.Errorf("diff insertions = %d, want 8 (the event's counts)", m.diff.insertions)
	}
	if m.diff.deletions != 1 {
		t.Errorf("diff deletions = %d, want 1", m.diff.deletions)
	}
}

// TestFoldVerificationFailedOpensDiffViewerWithReason pins §14.7.3: a
// verification.failed event opens the diff viewer focused on the failing file,
// with the reason staged. The viewer shows the reason so the user sees why
// verification broke.
func TestFoldVerificationFailedOpensDiffViewerWithReason(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.VerificationFailedEvent{Task: "t_1", Reason: "build error: undefined: x"}))

	if m.diff == nil {
		t.Fatal("m.diff = nil, want a diffView (verification.failed must open the viewer)")
	}
	if m.focus != paneDiff {
		t.Errorf("focus = %v, want paneDiff", m.focus)
	}
	if m.diff.reason != "build error: undefined: x" {
		t.Errorf("diff.reason = %q, want the verification failure reason", m.diff.reason)
	}
}

// TestFoldPatchAppliedReplacesPreviousDiff pins §14.6.1: a second patch.applied
// replaces the previous diff (the viewer shows the latest change, not a stack).
// The user dismisses; the next patch.applied swaps the content.
func TestFoldPatchAppliedReplacesPreviousDiff(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.PatchAppliedEvent{
		Task:  "t_1",
		Files: []event.PatchFile{{Path: "old.go", Insertions: 1, Deletions: 0}},
	}))
	m, _ = fold(m, env(&event.PatchAppliedEvent{
		Task:  "t_1",
		Files: []event.PatchFile{{Path: "new.go", Insertions: 2, Deletions: 0}},
	}))

	if m.diff == nil || len(m.diff.files) != 1 {
		t.Fatal("expected exactly one file in the replaced diff")
	}
	if m.diff.files[0].Path != "new.go" {
		t.Errorf("diff files[0].Path = %q, want %q (second patch replaces the first)", m.diff.files[0].Path, "new.go")
	}
}
