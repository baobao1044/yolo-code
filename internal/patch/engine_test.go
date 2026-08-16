// Tests for the patch engine (File 10 §10.5/§10.6): the orchestrator that ties
// L9-001..004 together — read → checkpoint → apply → AST validate → write —
// with two-layer rollback so a failed patch leaves the tree clean (§10.5.4).
// The seams (Filesystem, Checkpointer) are patch-local interfaces; these tests
// wire fakes. The Validator is intra-package (ast.go), so the engine uses the
// real one.

package patch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// memFS is an in-memory Filesystem: Read returns the stored content or
// ErrFileNotExist; Write stores content. writes records every Write call.
type memFS struct {
	files  map[string]string
	writes int
	// writeErr, if set, makes the next Write store the content then return
	// this error (simulating a partial/corrupt write the rollback must clean).
	writeErr error
}

func (f *memFS) Read(_ context.Context, path string) (string, error) {
	s, ok := f.files[path]
	if !ok {
		return "", ErrFileNotExist
	}
	return s, nil
}

func (f *memFS) Write(_ context.Context, path, content string) error {
	f.writes++
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = content // partial write is still on disk → rollback needed
	if f.writeErr != nil {
		return f.writeErr
	}
	return nil
}

// memCheckpointer records Checkpoint/Restore calls. Checkpoint mints an opaque
// ref from the name; Restore records that the named checkpoint was rolled back.
type memCheckpointer struct {
	checkpoints []memCkptCall
	restored    []string
	restoreErr  error
}

type memCkptCall struct {
	task, name string
	paths      []string
}

func (c *memCheckpointer) Checkpoint(_ context.Context, task, name string, paths []string) (SnapshotRef, error) {
	c.checkpoints = append(c.checkpoints, memCkptCall{task: task, name: name, paths: append([]string(nil), paths...)})
	return SnapshotRef("snap-" + name), nil
}

func (c *memCheckpointer) Restore(_ context.Context, task, name string) error {
	c.restored = append(c.restored, name)
	return c.restoreErr
}

func newTestEngine(fs *memFS, ck *memCheckpointer) *Engine {
	return NewEngine(Deps{
		FS:         fs,
		Checkpoint: ck,
		Validator:  NewValidator(),
	})
}

func TestEngineApplyWritesOnSuccess(t *testing.T) {
	fs := &memFS{files: map[string]string{
		"a.go": "package main\n\nfunc a() {}\n",
	}}
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	blocks := []Block{{Search: "func a() {}", Replace: "func b() {}"}}
	res, err := e.Apply(context.Background(), Op{Task: "t_1", Seq: 3, Path: "a.go", Blocks: blocks})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Accepted || res.Rejected {
		t.Fatalf("res = %+v, want Accepted", res)
	}
	if got := fs.files["a.go"]; !strings.Contains(got, "func b() {}") || strings.Contains(got, "func a()") {
		t.Errorf("disk = %q, want the block applied (func b, not func a)", got)
	}
	if fs.writes != 1 {
		t.Errorf("writes = %d, want 1", fs.writes)
	}
	if len(ck.checkpoints) != 1 {
		t.Errorf("checkpoints = %d, want 1", len(ck.checkpoints))
	}
	if ck.checkpoints[0].name != "patch_3" {
		t.Errorf("checkpoint name = %q, want patch_3", ck.checkpoints[0].name)
	}
}

func TestEngineApplyRejectsApplyFailureLeavesTreeClean(t *testing.T) {
	orig := "package main\n\nfunc a() {}\n"
	fs := &memFS{files: map[string]string{"a.go": orig}}
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	// Search text not present → Apply returns ErrNotFound → reject, no write.
	blocks := []Block{{Search: "func missing() {}", Replace: "func x() {}"}}
	res, err := e.Apply(context.Background(), Op{Task: "t_1", Seq: 1, Path: "a.go", Blocks: blocks})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Rejected || res.Accepted {
		t.Fatalf("res = %+v, want Rejected", res)
	}
	if fs.writes != 0 {
		t.Errorf("writes = %d, want 0 (no write on apply failure)", fs.writes)
	}
	if got := fs.files["a.go"]; got != orig {
		t.Errorf("disk = %q, want unchanged original (tree clean)", got)
	}
}

func TestEngineApplyRejectsASTFailureNoWrite(t *testing.T) {
	orig := "package main\n\nfunc a() {}\n"
	fs := &memFS{files: map[string]string{"a.go": orig}}
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	// Replace leaves an unbalanced function → AST validate fails → no write.
	blocks := []Block{{Search: "func a() {}", Replace: "func b() {"}}
	res, err := e.Apply(context.Background(), Op{Task: "t_1", Seq: 2, Path: "a.go", Blocks: blocks})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Rejected {
		t.Fatalf("res = %+v, want Rejected (AST failure)", res)
	}
	if !strings.Contains(res.Reason, "ast") {
		t.Errorf("Reason = %q, want it to flag the AST/parse failure", res.Reason)
	}
	if fs.writes != 0 {
		t.Errorf("writes = %d, want 0 (in-memory rollback, file never written)", fs.writes)
	}
	if got := fs.files["a.go"]; got != orig {
		t.Errorf("disk = %q, want unchanged (tree clean)", got)
	}
}

func TestEngineApplyNewFileWithFullContent(t *testing.T) {
	fs := &memFS{files: map[string]string{}} // path absent
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	content := "package main\n\nfunc new() {}\n"
	res, err := e.Apply(context.Background(), Op{Task: "t_1", Seq: 4, Path: "new.go", FullContent: content})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("res = %+v, want Accepted (new file)", res)
	}
	if got := fs.files["new.go"]; got != content {
		t.Errorf("disk = %q, want the full content", got)
	}
	if fs.writes != 1 {
		t.Errorf("writes = %d, want 1", fs.writes)
	}
}

func TestEngineApplyNewFileEmptyContentRejects(t *testing.T) {
	fs := &memFS{files: map[string]string{}} // path absent
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	res, err := e.Apply(context.Background(), Op{Task: "t_1", Seq: 5, Path: "empty.go", FullContent: ""})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Rejected {
		t.Fatalf("res = %+v, want Rejected (new file with empty content)", res)
	}
	if fs.writes != 0 {
		t.Errorf("writes = %d, want 0 (nothing written)", fs.writes)
	}
}

func TestEngineApplyRollsBackOnWriteFailure(t *testing.T) {
	orig := "package main\n\nfunc a() {}\n"
	fs := &memFS{
		files:    map[string]string{"a.go": orig},
		writeErr: errors.New("disk full"),
	}
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	blocks := []Block{{Search: "func a() {}", Replace: "func b() {}"}}
	_, err := e.Apply(context.Background(), Op{Task: "t_1", Seq: 6, Path: "a.go", Blocks: blocks})
	if err == nil {
		t.Fatal("Apply: want error on write failure")
	}
	// The engine must restore the checkpoint so the half-written file is
	// cleaned (§10.5: failed patch leaves the tree clean).
	if len(ck.restored) != 1 || ck.restored[0] != "patch_6" {
		t.Errorf("restored = %v, want [patch_6] (rollback on write failure)", ck.restored)
	}
}

func TestEngineAcceptedCarriesCheckpointForRuntimeRestore(t *testing.T) {
	fs := &memFS{files: map[string]string{"a.go": "package main\n\nfunc a() {}\n"}}
	ck := &memCheckpointer{}
	e := newTestEngine(fs, ck)

	blocks := []Block{{Search: "func a() {}", Replace: "func b() {}"}}
	res, _ := e.Apply(context.Background(), Op{Task: "t_7", Seq: 9, Path: "a.go", Blocks: blocks})

	// The runtime runs Verify (File 09) after this; on fail it calls
	// Checkpointer.Restore(task, name). The result must carry both so that's
	// possible (§10.5.4).
	if res.Checkpoint != "patch_9" {
		t.Errorf("Checkpoint = %q, want patch_9", res.Checkpoint)
	}
	if res.Snapshot == "" {
		t.Error("Snapshot is empty, want an opaque ref for the user/event")
	}
}

// TestApplyWithoutBusDoesNotRenderTheDiff pins fix (a) of the diff-cost pass:
// PatchAppliedEvent.Diff is the only consumer of UnifiedDiff, so with no bus
// wired the render is pure waste — it used to run anyway and throw the string
// away. The probe is a 1 000-line full rewrite, whose changed region is under
// the maxDiffCells cap and therefore genuinely expensive to render (~1.2 MiB,
// ~6 ms measured); the accept path without a bus must stay well under that.
func TestApplyWithoutBusDoesNotRenderTheDiff(t *testing.T) {
	const n = 1000
	var orig, next strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&orig, "old %d aaaa\n", i)
		fmt.Fprintf(&next, "new %d bbbb\n", i)
	}

	apply := func() {
		fs := &memFS{files: map[string]string{"a.txt": orig.String()}}
		e := NewEngine(Deps{FS: fs, Checkpoint: &memCheckpointer{}, Bus: nil})
		res, err := e.Apply(context.Background(), Op{
			Task: "t_diff", Seq: 1, Path: "a.txt", FullContent: next.String(),
		})
		if err != nil || !res.Accepted {
			t.Fatalf("Apply = %+v, %v; want accepted", res, err)
		}
	}

	used := allocBytes(apply, 3)

	// Rendering the diff costs ~1.2 MiB on its own with the current renderer and
	// ~8.5 MiB with the pre-fix full table. Everything else on the accept path
	// (read, Summarize's rolling-row LCS, the write) is tens of KiB.
	const budget = 512 << 10
	if used > budget {
		t.Errorf("Apply with a nil bus allocated %d bytes, want <= %d "+
			"(the discarded UnifiedDiff render is the difference)", used, budget)
	}
}
