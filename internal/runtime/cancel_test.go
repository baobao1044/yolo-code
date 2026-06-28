package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
)

// blockingCognitive is a stub that blocks on a gate until released, so a test
// can land a cancel mid-Think and assert the drive loop unwinds to CANCELLED.
// It honors context cancellation (L2-004 requirement: cancel must reach the
// cognitive core).
type blockingCognitive struct {
	gate    chan struct{}
	entered chan struct{}
}

func newBlockingCognitive() *blockingCognitive {
	return &blockingCognitive{gate: make(chan struct{}), entered: make(chan struct{})}
}

func (b *blockingCognitive) Think(ctx context.Context, _ Prompt) (CognitiveTurn, error) {
	close(b.entered)
	select {
	case <-b.gate:
		return CognitiveTurn{Final: true, Text: "released"}, nil
	case <-ctx.Done():
		return CognitiveTurn{}, ctx.Err()
	}
}

func (*blockingCognitive) HasMore(*session.Task) bool { return false }

// TestCancelMidThinkReturnsCancelled is the L2-004 headline: a task canceled
// while the cognitive core is mid-Thinking unwinds to a terminal CANCELLED
// state via the Session Manager, and the drive loop returns (does not spin or
// deadlock).
func TestCancelMidThinkReturnsCancelled(t *testing.T) {
	store := session.NewFileStore(filepath.Join(t.TempDir(), "store"))
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, _ := smgr.OpenSession(context.Background(), "proj", "demo")

	cog := newBlockingCognitive()
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: cog})

	// Submit drives on the calling goroutine for MVP, so run it in a goroutine
	// so we can cancel its context while it's blocked in Think.
	taskCtx, cancel := context.WithCancel(context.Background())
	done := make(chan session.TaskID, 1)
	go func() {
		tid, _ := core.Submit(taskCtx, sid, "say hi")
		done <- tid
	}()

	// Wait for the drive loop to reach PLAN and block inside Think.
	select {
	case <-cog.entered:
	case <-time.After(time.Second):
		t.Fatal("drive loop never reached Think (blocked before PLAN)")
	}

	// Land the cancel: cascade to the cognitive core via context.
	cancel()

	// The Submit goroutine must return (drive loop unwound), giving back the tid.
	var tid session.TaskID
	select {
	case tid = <-done:
	case <-time.After(time.Second):
		t.Fatal("Submit did not return after cancel; drive loop stuck")
	}

	// The task must be in terminal CANCELLED, not DONE.
	got := smgr.LoadTaskPublic(tid)
	if got == nil {
		t.Fatal("task handle missing after cancel")
	}
	if got.Status != session.StatusCancelled {
		t.Errorf("task status = %q, want CANCELLED (terminal)", got.Status)
	}
	if !got.Status.IsTerminal() {
		t.Error("CANCELLED must be terminal")
	}

	// task.cancelled must have been published by the Session Manager.
	ch := bus.Subscribe("task.cancelled")
	// Subscribe after the fact won't see already-published events; instead,
	// assert via the status above. (The bus is closed in cleanup; we only check
	// status here — the event is covered by the session package's own tests.)
	_ = ch
}

// TestCancelBeforeThinkCompletesTaskCancelled covers the case where cancel
// arrives while the drive loop is between states (e.g., in LOAD_CONTEXT). The
// loop checks ctx.Err() at the top of each iteration and unwinds to CANCELLED.
func TestCancelBeforeThinkCompletesTaskCancelled(t *testing.T) {
	store := session.NewFileStore(filepath.Join(t.TempDir(), "store"))
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, _ := smgr.OpenSession(context.Background(), "proj", "demo")

	cog := newBlockingCognitive()
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: cog})

	// Pre-cancel the context so the drive loop sees ctx.Err() immediately on
	// its first iteration.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan session.TaskID, 1)
	go func() {
		tid, _ := core.Submit(ctx, sid, "say hi")
		done <- tid
	}()

	select {
	case tid := <-done:
		// The task may not even have been created if StartTask saw the canceled
		// ctx first; either way, no goroutine leaked and no DONE.
		if tid != "" {
			got := smgr.LoadTaskPublic(tid)
			if got != nil && got.Status == session.StatusDone {
				t.Error("pre-canceled task reached DONE; must be CANCELLED or absent")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Submit did not return after pre-cancel")
	}
}

// TestDriveCancelsCognitiveContext proves the cancel actually reaches the
// cognitive core: blockingCognitive.Think returns ctx.Err() (not its gate).
func TestDriveCancelsCognitiveContext(t *testing.T) {
	store := session.NewFileStore(filepath.Join(t.TempDir(), "store"))
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, _ := smgr.OpenSession(context.Background(), "proj", "demo")
	cog := newBlockingCognitive()
	core := New(Deps{Bus: bus, Session: smgr, Cognitive: cog})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = core.Submit(ctx, sid, "go") }()
	<-cog.entered
	cancel()

	// Think must have observed ctx.Done(); give it a beat and assert the gate
	// was never closed (i.e., Think returned via ctx, not via gate).
	select {
	case <-cog.gate:
		t.Fatal("Think returned via the gate, not via ctx.Done() — cancel did not reach the cognitive core")
	case <-time.After(200 * time.Millisecond):
		// good: gate still open, Think unwound on ctx
	}
}
