// Tests for the patch.applied event (File 10 + File 05 §5.4.4): on a successful
// apply the engine publishes patch.applied carrying the diff summary — files
// touched, insertions, deletions — so the transcript/TUI can show what changed
// (Sprint 5 exit bar: "the transcript shows the diff summary"). The summary is
// computed by a deterministic line diff of original vs next; patch may import
// event (matrix), so the engine holds a *event.Bus like exec does.

package patch

import (
	"context"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

func TestSummarizeCountsAddedAndRemovedLines(t *testing.T) {
	orig := "package main\n\nfunc a() {}\nfunc b() {}\n"
	next := "package main\n\nfunc a() {}\nfunc c() {}\nfunc d() {}\n"

	s := Summarize([]Change{{Path: "a.go", Original: orig, Next: next}})

	if len(s.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(s.Files))
	}
	f := s.Files[0]
	if f.Path != "a.go" {
		t.Errorf("Path = %q, want a.go", f.Path)
	}
	// orig has "func b() {}" removed; next has "func c() {}" and "func d() {}"
	// added. The common lines (package main, blank, func a) are context.
	if f.Insertions != 2 {
		t.Errorf("Insertions = %d, want 2", f.Insertions)
	}
	if f.Deletions != 1 {
		t.Errorf("Deletions = %d, want 1", f.Deletions)
	}
	if f.New {
		t.Errorf("New = true, want false (existing file)")
	}
	if s.Insertions != 2 || s.Deletions != 1 {
		t.Errorf("totals = +%d/-%d, want +2/-1", s.Insertions, s.Deletions)
	}
}

func TestSummarizeNewFileAllInsertions(t *testing.T) {
	next := "package main\n\nfunc fresh() {}\n"

	s := Summarize([]Change{{Path: "fresh.go", Original: "", Next: next}})

	if len(s.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(s.Files))
	}
	f := s.Files[0]
	if !f.New {
		t.Error("New = false, want true (new file)")
	}
	// 2 non-empty content lines (package main, func fresh() {}); the blank line
	// is framing and doesn't count.
	if f.Insertions != 2 {
		t.Errorf("Insertions = %d, want 2", f.Insertions)
	}
	if f.Deletions != 0 {
		t.Errorf("Deletions = %d, want 0", f.Deletions)
	}
}

func TestSummarizePureDeletion(t *testing.T) {
	orig := "a\nb\nc\n"
	next := "a\nc\n"

	s := Summarize([]Change{{Path: "x.txt", Original: orig, Next: next}})

	f := s.Files[0]
	if f.Insertions != 0 || f.Deletions != 1 {
		t.Errorf("= +%d/-%d, want +0/-1", f.Insertions, f.Deletions)
	}
}

func TestSummarizeDeterministicFileOrder(t *testing.T) {
	// Files must come out in a stable order (path-sorted), not map/iteration
	// order, so transcripts are byte-identical (S5 determinism).
	changes := []Change{
		{Path: "z.go", Original: "a\n", Next: "b\n"},
		{Path: "a.go", Original: "a\n", Next: "b\n"},
		{Path: "m.go", Original: "a\n", Next: "b\n"},
	}
	s := Summarize(changes)
	got := []string{s.Files[0].Path, s.Files[1].Path, s.Files[2].Path}
	want := []string{"a.go", "m.go", "z.go"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Files[%d].Path = %q, want %q (sorted)", i, got[i], want[i])
		}
	}
}

func TestEnginePublishesPatchAppliedOnAccept(t *testing.T) {
	bus := event.New()
	subs := bus.Subscribe("patch.applied")
	defer bus.Close()

	fs := &memFS{files: map[string]string{"a.go": "package main\n\nfunc a() {}\n"}}
	ck := &memCheckpointer{}
	e := NewEngine(Deps{FS: fs, Checkpoint: ck, Validator: NewValidator(), Bus: bus})

	blocks := []Block{{Search: "func a() {}", Replace: "func b() {}\nfunc c() {}"}}
	res, err := e.Apply(context.Background(), Op{Task: "t_9", Seq: 1, Path: "a.go", Blocks: blocks})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("res = %+v, want Accepted", res)
	}

	// Receive with a timeout — a missing publish must fail the test fast, not
	// hang forever (a publish regression would otherwise block <-subs).
	select {
	case env, ok := <-subs:
		if !ok {
			t.Fatal("subscriber channel closed: no patch.applied event published")
		}
		pe, ok := env.Evt.(*event.PatchAppliedEvent)
		if !ok {
			t.Fatalf("event type = %T, want *PatchAppliedEvent", env.Evt)
		}
		if string(pe.Task) != "t_9" {
			t.Errorf("Task = %q, want t_9", pe.Task)
		}
		if string(pe.Snapshot) == "" {
			t.Error("Snapshot empty, want the checkpoint ref")
		}
		if len(pe.Files) != 1 || pe.Files[0].Path != "a.go" {
			t.Errorf("Files = %+v, want one entry for a.go", pe.Files)
		}
		// "func a() {}" → 1 deletion; "func b() {}" + "func c() {}" → 2 insertions.
		if pe.Files[0].Insertions != 2 || pe.Files[0].Deletions != 1 {
			t.Errorf("file stat = +%d/-%d, want +2/-1", pe.Files[0].Insertions, pe.Files[0].Deletions)
		}
		if pe.Insertions != 2 || pe.Deletions != 1 {
			t.Errorf("totals = +%d/-%d, want +2/-1", pe.Insertions, pe.Deletions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no patch.applied event within 2s (publish regression)")
	}
}

func TestEngineDoesNotPublishPatchAppliedOnReject(t *testing.T) {
	bus := event.New()
	subs := bus.Subscribe("patch.applied")
	defer bus.Close()

	fs := &memFS{files: map[string]string{"a.go": "package main\n\nfunc a() {}\n"}}
	ck := &memCheckpointer{}
	e := NewEngine(Deps{FS: fs, Checkpoint: ck, Validator: NewValidator(), Bus: bus})

	// Search not present → rejected; no event.
	blocks := []Block{{Search: "func missing() {}", Replace: "func x() {}"}}
	_, _ = e.Apply(context.Background(), Op{Task: "t_9", Seq: 2, Path: "a.go", Blocks: blocks})

	select {
	case env := <-subs:
		t.Fatalf("patch.applied published on reject: %+v", env.Evt)
	default:
		// expected: no event
	}
}
