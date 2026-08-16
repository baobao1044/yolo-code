// Race reproduction for the orchestrator's shared lifecycle state (4.16c).
//
// Run blocks ("It blocks the caller"), so the documented usage is to run it on
// its own goroutine and observe it from the owner's via Stop/Done. Start —
// which Run calls implicitly ("Run calls it implicitly if the caller didn't")
// — used to CREATE o.done, so the event-loop goroutine wrote the field while
// the owner read it. That is an unsynchronised read/write on a shared field of
// the shared *Orchestrator.
//
// This test exercises exactly that interleaving; it fails under -race before
// the fix (done is created in NewOrchestrator, so nothing writes it after
// construction) and passes after.

package coord

import (
	"context"
	"testing"
	"time"
)

// TestOrchestratorLifecycleRace runs Run on its own goroutine while the owner
// polls Done() and then Stops — the documented Run/Stop split. No shared field
// may be written by Run's goroutine and read by the owner's.
func TestOrchestratorLifecycleRace(t *testing.T) {
	o, bus, _, _ := newTestOrchestrator(t, []string{"implement X"}, true, 0, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// NOTE: no o.Start(ctx) here — Run starts itself, which is the documented
	// contract and the path the existing tests never take.
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = o.Run(ctx, "refactor X, add tests, fix CI")
	}()

	// The owner observes the lifecycle while Run is starting. Done() reads the
	// same field Start writes; a nil channel simply falls through the default.
	for i := 0; i < 5000; i++ {
		select {
		case <-o.Done():
		default:
		}
	}
	_ = o.Stop(ctx)
	<-runDone
	_ = bus.Close()
}
