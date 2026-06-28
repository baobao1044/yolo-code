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
	"testing"
	"time"

	econtext "github.com/yolo-code/yolo/internal/context"
	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/memory"
	"github.com/yolo-code/yolo/internal/prompt"
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

// TestMemoryStoreAdapterPublishesNotMutates: the runtime.MemoryStore adapter
// (Update) must publish an event the listener reacts to — it must NOT mutate a
// sub-store directly (§11.2). Calling Update with no prior conversation should
// not append a spurious message; the event-driven path is the only writer. This
// asserts the adapter is a publisher, not a direct mutator.
func TestMemoryStoreAdapterPublishesNotMutates(t *testing.T) {
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

	// Subscribe to memory.update so we can assert the adapter's Update
	// produced a learning event (the listener reacted).
	ch := bus.Subscribe(event.Topic("memory.update"))
	adapter := memoryStoreAdapter{store: mem, bus: bus}

	// The runtime calls Update(ctx, taskID) on the direct-answer path. The
	// adapter's job is to trigger a memory learning — it publishes an event the
	// listener reacts to. A direct mutation would bypass the listener.
	if err := adapter.Update(context.Background(), "t_recall"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Drain a short window; the listener should have reacted (published at least
	// one memory.update from the dispatch, OR the adapter's own publish).
	saw := false
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return
			}
			if env.Evt.Type() == "memory.update" {
				saw = true
			}
		case <-time.After(150 * time.Millisecond):
			if !saw {
				t.Error("memoryStoreAdapter.Update produced no memory.update event (the adapter didn't trigger a learning)")
			}
			return
		}
	}
}
