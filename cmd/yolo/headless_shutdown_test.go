// Shutdown-ordering test for runHeadlessDeps. The bus, the memory Store and
// the infra aggregate have to be closed in that order, and the ordering has to
// survive an early return — which is where it was getting lost.

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/memory"
	"github.com/baobao1044/yolo-code/internal/session"
)

// errSessionStoreFull stands in for the disk being full or read-only, which is
// what makes session.Manager.OpenSession fail in the field.
var errSessionStoreFull = errors.New("no space left on device")

// brokenSessionStore fails the one call OpenSession makes. Embedding the
// interface keeps the fake to the method under test; anything else the code
// starts calling panics loudly rather than passing silently.
type brokenSessionStore struct{ session.Store }

func (brokenSessionStore) CreateSession(context.Context, *session.Session) error {
	return errSessionStoreFull
}

// ListSessions is the other call OpenSession makes (via seedCounters, to pick
// the next free id). An empty listing is what a fresh store returns.
func (brokenSessionStore) ListSessions(context.Context, string) ([]*session.Session, error) {
	return nil, nil
}

// TestHeadlessReturnsWhenTheSessionStoreFails pins the teardown order against
// an early return.
//
// memStore.Close does <-s.done, and done closes only when the memory
// listener's `range s.ch` ends — which needs the bus closed. The memory Close
// used to be its own defer, registered *after* the bus/infra defer, so LIFO ran
// it *first*. On the success path that was invisible: the explicit bus.Close at
// the end of the run had already happened. On this path — OpenSession failing —
// the function returns before that close, so the memory Close waited for a
// drain that could only end after a bus.Close the outer defer would never
// reach. `yolo --headless` wedged silently instead of reporting why it could
// not open a session.
//
// The watchdog is the point of the test: without it a regression hangs the
// package's test binary until the 10-minute panic, which is a worse failure
// than a red line.
func TestHeadlessReturnsWhenTheSessionStoreFails(t *testing.T) {
	bus := event.New()
	root := t.TempDir()
	memStore, err := memory.Open(memory.Deps{Root: root, Bus: bus})
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	t.Cleanup(func() {
		_ = bus.Close()
		_ = memStore.Close()
	})

	// Something worth keeping, queued before the run. The close chain is the
	// only thing that will ever flush it, so its presence on disk afterwards is
	// how we tell "closed in the right order" from "skipped the close".
	const lesson = "the operator asked for tabs, not spaces"
	if err := bus.Publish(context.Background(), &event.AssistantMessageEvent{
		Task: "s_9", Text: lesson, Final: true,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// An injected bus is the harder case: it nominally belongs to the caller, so
	// the teardown used to leave it open — and then nothing could ever release
	// the memory listener.
	deps := &headlessDeps{
		bus:      bus,
		memory:   memStore,
		memDir:   root,
		sessions: brokenSessionStore{},
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := runHeadlessDeps(context.Background(),
			bytes.NewBufferString("say hi\n"), 0, deps)
		done <- result{out, err}
	}()

	select {
	case got := <-done:
		if !errors.Is(got.err, errSessionStoreFull) {
			t.Fatalf("err = %v, want the session-store failure to reach the caller", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runHeadlessDeps hung on the OpenSession error path: the memory " +
			"Close is waiting on a listener drain that needs a bus.Close the " +
			"teardown never performs")
	}

	// The close chain ran, and ran in an order that let the listener finish its
	// work: the queued message is on disk, not lost with the process.
	path := filepath.Join(root, "conversations", "s_9.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (the teardown returned without flushing memory)", path, err)
	}
	if !strings.Contains(string(raw), lesson) {
		t.Errorf("%s does not carry the queued message:\n%s", path, raw)
	}
}
