// Tests for the L10-006 memory lifecycle extensions: Working memory task/
// state fields (§11.3.1) and Exec history rolling window (§11.4.1).

package memory

import (
	"context"
	"testing"
)

// TestWorkingMemoryTaskAndState: SetTask/SetState/Task/State/Clear drive the
// §11.3.1 lifecycle (task.created → set task, state.change → set state,
// task.completed → clear all).
func TestWorkingMemoryTaskAndState(t *testing.T) {
	var w WorkingMemory
	if w.Task() != "" || w.State() != "" {
		t.Fatalf("fresh Working: Task=%q State=%q, want empty", w.Task(), w.State())
	}
	w.SetTask("write a fibonacci CLI")
	if w.Task() != "write a fibonacci CLI" {
		t.Errorf("Task = %q, want the set goal", w.Task())
	}
	w.SetState("exec")
	if w.State() != "exec" {
		t.Errorf("State = %q, want exec", w.State())
	}
	w.Clear()
	if w.Task() != "" || w.State() != "" {
		t.Errorf("after Clear: Task=%q State=%q, want empty", w.Task(), w.State())
	}
}

// TestWorkingMemoryNilSafe: methods on a nil WorkingMemory return zero values
// without panicking (the listener guards, but the store shouldn't either).
func TestWorkingMemoryNilSafe(t *testing.T) {
	var w *WorkingMemory
	if w.Task() != "" || w.State() != "" {
		t.Error("nil Working returned non-empty Task/State")
	}
	w.SetTask("x") // must not panic
	w.SetState("y")
	w.Clear()
}

// TestExecHistoryRollingWindow: appending beyond execWindow (50) drops the
// oldest entries so the slice stays bounded (§11.4.1 "last 50 results").
func TestExecHistoryRollingWindow(t *testing.T) {
	s := NewExecHistoryStore(t.TempDir())
	ctx := context.Background()
	for i := 0; i < execWindow+5; i++ {
		s.Append(ctx, "t1", ExecEntry{Kind: "tool", Summary: "result"})
	}
	entries := s.Entries("t1")
	if len(entries) != execWindow {
		t.Fatalf("Entries len = %d, want %d (rolling window)", len(entries), execWindow)
	}
	// Seq is monotonic per task (tracked in seqCounter, not slice length), so
	// the rolling window keeps the LAST 50 entries with seqs 6..55 (the first
	// 5 were dropped from the slice but their seqs are not reused).
	firstSeq := entries[0].Seq
	wantFirst := execWindow + 5 - execWindow + 1 // 6
	if firstSeq != wantFirst {
		t.Errorf("first kept Seq = %d, want %d (oldest dropped, seq stays monotonic)", firstSeq, wantFirst)
	}
	lastSeq := entries[len(entries)-1].Seq
	if lastSeq != execWindow+5 {
		t.Errorf("last kept Seq = %d, want %d", lastSeq, execWindow+5)
	}
}
