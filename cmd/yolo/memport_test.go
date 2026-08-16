// Tests for L10-006 — memory feeds back into the L4 context builder (File 06
// §6.1 + File 11 §11.8). The composition root (cmd/yolo) bridges memory into
// both seams: a memoryStoreAdapter satisfies runtime.MemoryStore.Update
// (publishes an event the listener reacts to — never mutates a sub-store
// directly, §11.2), and a contextMemoryAdapter satisfies context.Memory
// (translates memory.Part → context.Part). The sprint exit bar (roadmap §15.10.2):
// a recalled memory surfaces in the prompt. The test seeds a preference, runs
// headless with real memory wired, and asserts the compiled prompt carries the
// preference text — a recalled memory reaches the model.

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/memory"
	"github.com/baobao1044/yolo-code/internal/prompt"
)

// TestRecalledPreferenceSurfacesInPrompt is the Sprint 7 exit bar: a preference
// stored in memory is recalled and surfaces in the compiled prompt the model
// sees. This wires the real memory.Store behind both seams via adapters and
// asserts the assertCognitive core saw the preference text in its prompt.
func TestRecalledPreferenceSurfacesInPrompt(t *testing.T) {
	dir := t.TempDir()

	// Seed the preference in session A (cross-session recall from L10-005).
	seed, err := memory.Open(memory.Deps{Root: dir})
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if err := seed.Preferences().Set(context.Background(), "test-style", "I prefer table-driven tests"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_ = seed.Close()

	// Session B: a fresh memory.Store over the same root eager-loads the
	// preference (L10-005). Wire it behind the context.Memory seam via the
	// adapter; the Context Engine's preference group reads it.
	bus := event.New()
	mem, err := memory.Open(memory.Deps{Root: dir, Bus: bus})
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	// Cleanup order matters (L10-002): the listener's drain goroutine only
	// exits when the bus closes its subscriber channel, and Store.Close waits
	// on that. So close the bus BEFORE the store — a single deferred closure
	// guarantees the order (two separate defers would run LIFO: mem.Close
	// first → deadlock waiting for a bus that closes after it).
	defer func() {
		_ = bus.Close()
		_ = mem.Close()
	}()

	// A real Context Engine whose Memory seam is the memory adapter. The repo
	// has no open files / AGENTS.md — the only content that can reach the prompt
	// is the recalled preference.
	eng := econtext.New(econtext.Deps{
		Bus:    bus,
		Repo:   dir,
		Memory: contextMemoryAdapter{store: mem},
	})
	comp := prompt.New(nil, nil)
	// The asserting core checks the compiled prompt carries the preference.
	cog := &assertCognitive{want: "I prefer table-driven tests", answer: "saw the preference"}

	_, err = runHeadlessDeps(context.Background(), bytes.NewBufferString("write a test\n"), 0,
		&headlessDeps{
			context: contextAdapter{eng: eng},
			prompt:  promptAdapter{comp: comp},
			cog:     cog,
			memory:  mem,
			bus:     bus,
		})
	if err != nil {
		t.Fatalf("runHeadlessDeps: %v", err)
	}
	if !cog.saw {
		t.Fatal("assertCognitive.Think was never called; the stub core received no prompt")
	}
	if !cog.ok {
		t.Error("compiled prompt did NOT contain the recalled preference; a recalled memory did not surface in the prompt")
	}
}

// TestMemoryPortDoesNotDuplicateTaskCompleted: wiring the runtime's MemoryStore
// port must not add a second task.completed to the run.
//
// The adapter used to implement "record what this task learned" by publishing a
// task.completed of its own, to make the memory listener persist. But look at
// where the runtime calls it (internal/runtime/core.go, direct-answer arm):
//
//	if c.memory != nil {
//	    _ = c.memory.Update(ctx, h.id)   // adapter publishes task.completed
//	}
//	_ = c.session.CompleteTask(ctx, h.id) // manager.go:236 publishes task.completed
//
// Two lines apart, the same event twice. Every consumer sees it: the TUI folds a
// second completion, the durability log records one that never happened, infra's
// observers count two tasks for one. Worse, the listener's task.completed arm
// launches its persist on a goroutine, so two of them run concurrently and write
// conversations/<id>.json and exec/<id>.json at the same time.
//
// The port itself is not the problem — the fabricated event is. Memory persists
// this task either way, off the genuine task.completed the session manager
// publishes, which is exactly why the duplicate bought nothing.
func TestMemoryPortDoesNotDuplicateTaskCompleted(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	mem, err := memory.Open(memory.Deps{Root: dir, Bus: bus})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		_ = bus.Close()
		_ = mem.Close()
	}()

	out, err := runHeadlessDeps(context.Background(), bytes.NewBufferString("say hi\n"), 0,
		&headlessDeps{memory: mem, bus: bus})
	if err != nil {
		t.Fatalf("runHeadlessDeps: %v", err)
	}

	got := strings.Count(out, `"type":"task.completed"`)
	if got != 1 {
		t.Errorf("transcript carries %d task.completed events, want exactly 1 — "+
			"the memory port fabricated a lifecycle event the runtime publishes for real\n%s",
			got, out)
	}
}

// TestMemoryStoreAdapterNeitherPublishesNorMutates pins both halves of the
// adapter's contract (§11.2 and the duplicate above).
//
// The original test asserted only the first half — the adapter must not write a
// sub-store directly, because the listener is memory's only writer — and it
// checked that by looking for the memory.update the listener emits when it
// reacts. That made "the adapter published something the listener consumed" the
// pass condition, so the fabricated task.completed read as the feature working.
//
// So assert the other half too, and assert it on the bus rather than through the
// listener: Update is a seam with no work at its call site, and a seam with no
// work publishes nothing. A root subscriber sees anything it did emit.
func TestMemoryStoreAdapterNeitherPublishesNorMutates(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	mem, err := memory.Open(memory.Deps{Root: dir, Bus: bus})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Same cleanup-order discipline as the test above (L10-002): bus before
	// store, in one deferred closure, so Store.Close never deadlocks waiting
	// for a drain that needs the bus closed first.
	defer func() {
		_ = bus.Close()
		_ = mem.Close()
	}()

	ch := bus.Subscribe(event.Topic(">"))
	adapter := memoryStoreAdapter{store: mem, bus: bus}

	if err := adapter.Update(context.Background(), "t_recall"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	select {
	case env := <-ch:
		t.Errorf("Update published %q; the runtime publishes this task's completion "+
			"through the session manager one line later, so anything the adapter "+
			"emits here is a duplicate", env.Evt.Type())
	case <-time.After(150 * time.Millisecond):
	}

	// The other half, unchanged in substance: no direct sub-store write. A
	// spurious conversation entry is what a direct mutation would leave behind.
	if msgs := mem.Conversation().Messages("t_recall"); len(msgs) != 0 {
		t.Errorf("Update wrote %d conversation entries directly; the listener is "+
			"memory's only writer (§11.2)", len(msgs))
	}
}
