package event

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// init registers the test event under its topic so the durability log can
// unmarshal it. The full 16-topic catalog lands in L3-006.
func init() {
	Register("test.ping", func() Event { return &testEvent{typ: "test.ping"} })
}

// fakeAppender is a test seam for the durability sink: it records which seqs
// have been appended and in what order, without touching disk. It lets the
// durability-before-visibility test observe ordering precisely.
type fakeAppender struct {
	mu       sync.Mutex
	appended map[uint64]bool
}

func (f *fakeAppender) Append(env Envelope) error {
	f.mu.Lock()
	if f.appended == nil {
		f.appended = map[uint64]bool{}
	}
	f.appended[env.Seq] = true
	f.mu.Unlock()
	return nil
}

func (*fakeAppender) Close() error { return nil }

func (f *fakeAppender) has(seq uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appended[seq]
}

// TestDurabilityHappensBeforeVisibility is the P3 invariant: a subscriber
// must never see an envelope that is not yet durable. The unbuffered
// subscriber channel forces the send to block until the receiver runs, so the
// receiver can observe whether Append (which runs before the send in a correct
// Publish) has already recorded the seq. With fan-out-before-append the
// receiver would catch the event before it's durable.
func TestDurabilityHappensBeforeVisibility(t *testing.T) {
	fake := &fakeAppender{}
	bus := newBusWithAppender(fake)
	defer bus.Close()

	ch := bus.subscribe([]Topic{"test.>"}, 0) // unbuffered: send blocks until receive

	delivered := make(chan uint64, 8)
	go func() {
		for env := range ch {
			if !fake.has(env.Seq) {
				t.Errorf("seq %d delivered before it was appended (durability-before-visibility violated)", env.Seq)
			}
			delivered <- env.Seq
		}
	}()

	const n = 5
	for i := 0; i < n; i++ {
		if err := bus.Publish(context.Background(), ping("t")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	seen := make(map[uint64]bool, n)
	for i := 0; i < n; i++ {
		// Wait for each delivery rather than polling: Publish returns once the
		// unbuffered send to ch is accepted, but the receiver still has to move
		// the seq into `delivered`. A non-blocking poll here would race with
		// that move and flake; recvIn gives the receiver bounded time.
		select {
		case s := <-delivered:
			seen[s] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only %d/%d events delivered (timed out)", i, n)
		}
	}
	if len(seen) != n {
		t.Errorf("expected %d distinct deliveries, got %d", n, len(seen))
	}
}

// TestCrashRecoveryReplaysDurableEvents is the L3-004 headline exit: publish,
// then replay the on-disk log and recover the exact event stream. Because each
// Append fsyncs, a crash (kill -9) after the publishes leaves the same bytes;
// Close here only releases the file handle.
func TestCrashRecoveryReplaysDurableEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.log")

	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := []string{"t0", "t1", "t2"}
	for i, task := range want {
		if err := bus.Publish(context.Background(), ping(task)); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	envs, err := Replay(path)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(envs) != len(want) {
		t.Fatalf("replay got %d events, want %d", len(envs), len(want))
	}
	for i, env := range envs {
		if env.Seq != uint64(i+1) {
			t.Errorf("env %d Seq = %d, want %d", i, env.Seq, i+1)
		}
		if env.Evt.Type() != "test.ping" {
			t.Errorf("env %d Type = %q, want test.ping", i, env.Evt.Type())
		}
		if env.Evt.CausalID() != TaskID(want[i]) {
			t.Errorf("env %d CausalID = %q, want %q", i, env.Evt.CausalID(), want[i])
		}
	}
}

// TestReplayRoundTripsTheExactEventStream is the P4 contract: what the log
// gives back must be what the bus delivered, event for event and field for
// field. Comparing the re-marshaled envelopes catches anything Replay drops or
// mangles, and the mix deliberately includes topics the catalog used to be
// missing (plan.done, cost.incurred, user.preference, command.response), which
// Replay could not reconstruct at all.
func TestReplayRoundTripsTheExactEventStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := bus.Subscribe(">")

	stream := []Event{
		&TaskStartedEvent{Task: "t_01", Session: "s_1", Goal: "fix bug"},
		&PlanDoneEvent{PlanID: "p_1", Done: true, Merged: true, Summary: "all todos green"},
		&CostIncurredEvent{Task: "t_01", Tool: "shell", Dollars: 0.0125, Reason: "tool call"},
		&UserPreferenceEvent{Task: "t_01", Key: "commits", Value: "conventional"},
		&CommandResponseEvent{Text: "provider: groq"},
		&ScopeEnterEvent{Task: "t_01", Level: "L2", Reason: "multi-file"},
	}
	for i, e := range stream {
		if err := bus.Publish(context.Background(), e); err != nil {
			t.Fatalf("publish %d (%s): %v", i, e.Type(), err)
		}
	}

	// Capture exactly what a subscriber saw, to compare against the log.
	delivered := make([]Envelope, 0, len(stream))
	for i := range stream {
		env, ok := recv(t, sink)
		if !ok {
			t.Fatalf("event %d not delivered", i)
		}
		delivered = append(delivered, env)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	replayed, err := Replay(path)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != len(delivered) {
		t.Fatalf("replay returned %d envelopes, want %d", len(replayed), len(delivered))
	}
	for i := range delivered {
		want, err := json.Marshal(delivered[i])
		if err != nil {
			t.Fatalf("marshal delivered %d: %v", i, err)
		}
		got, err := json.Marshal(replayed[i])
		if err != nil {
			t.Fatalf("marshal replayed %d: %v", i, err)
		}
		if string(got) != string(want) {
			t.Errorf("event %d drifted through the log\n want %s\n got  %s", i, want, got)
		}
	}
}

// TestReplayRecoversRecordsBeforeATruncatedTail is the 4.17c driver. A crash
// between write and fsync leaves a half-written final line — which is exactly
// the situation crash recovery exists for. Replay used to return (nil, err) for
// such a log, throwing away every record that *had* been fsynced; the whole
// recovery was lost to the one torn record at the end.
func TestReplayRecoversRecordsBeforeATruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := []string{"t0", "t1", "t2"}
	for i, task := range want {
		if err := bus.Publish(context.Background(), ping(task)); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Simulate the torn write: a fourth record that stops mid-payload.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := f.WriteString(`{"v":1,"seq":4,"at":"2026-01-01T00:00:00Z","type":"test.ping","evt":{"tas`); err != nil {
		t.Fatalf("write torn record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close torn: %v", err)
	}

	envs, err := Replay(path)
	if !errors.Is(err, ErrTruncatedLog) {
		t.Fatalf("Replay err = %v, want ErrTruncatedLog", err)
	}
	if len(envs) != len(want) {
		t.Fatalf("recovered %d envelopes, want %d: a torn tail must not discard records that were already fsynced", len(envs), len(want))
	}
	for i, env := range envs {
		if env.Seq != uint64(i+1) {
			t.Errorf("env %d Seq = %d, want %d", i, env.Seq, i+1)
		}
		if env.Evt.CausalID() != TaskID(want[i]) {
			t.Errorf("env %d CausalID = %q, want %q", i, env.Evt.CausalID(), want[i])
		}
	}
}

// TestReplayRejectsUnknownTopic proves the codec refuses events it can't
// reconstruct — a corruption guard, not a silent skip.
func TestReplayRejectsUnknownTopic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Publish a test.ping (registered), then hand-write an unregistered topic
	// into the same file to simulate a log from a future/incompatible version.
	if err := bus.Publish(context.Background(), ping("ok")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Append a malformed (unknown-topic) envelope by hand.
	// We just write a JSON line with an unregistered type.
	if err := appendUnknownTopicLine(path, "totally.unknown"); err != nil {
		t.Fatalf("append unknown: %v", err)
	}

	if _, err := Replay(path); err == nil {
		t.Fatal("replay of an unregistered topic succeeded; expected an error")
	}
}

// appendUnknownTopicLine writes a single well-formed wire-envelope line with an
// unregistered topic to the log, simulating a log from an incompatible build.
func appendUnknownTopicLine(path, topic string) error {
	line := `{"v":1,"seq":99,"at":"` + time.Now().UTC().Format(time.RFC3339Nano) +
		`","type":"` + topic + `","evt":{}}` + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// TestReplayReconstructsARetiredTopicName is the migration half of a topic
// rename. Logs on disk are immutable and outlive the name that wrote them, and
// Replay resolves a topic through the registry with no fallback — one
// unrecognised name and TestReplayRejectsUnknownTopic's error path takes out
// the ENTIRE file, not just that record. So a rename without a legacyTopics
// entry silently orphans every transcript written before it.
//
// "plan.done" is the retired spelling of coord.plan.done. This writes the old
// name by hand, exactly as a pre-rename build would have, and asserts it comes
// back as a usable *PlanDoneEvent with its payload intact.
func TestReplayReconstructsARetiredTopicName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.log")
	line := `{"v":1,"seq":1,"at":"` + time.Now().UTC().Format(time.RFC3339Nano) +
		`","type":"plan.done","evt":{"plan_id":"p_1","done":true,"merged":true,"summary":"all todos green"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write legacy log: %v", err)
	}

	envs, err := Replay(path)
	if err != nil {
		t.Fatalf("replay of a pre-rename log failed: %v — every transcript written before the rename is now unreadable", err)
	}
	if len(envs) != 1 {
		t.Fatalf("replay returned %d envelopes, want 1", len(envs))
	}
	pd, ok := envs[0].Evt.(*PlanDoneEvent)
	if !ok {
		t.Fatalf("replayed event is %T, want *PlanDoneEvent", envs[0].Evt)
	}
	if pd.PlanID != "p_1" || !pd.Done || !pd.Merged || pd.Summary != "all todos green" {
		t.Errorf("replayed payload = %+v, want the fields the old log carried", *pd)
	}
	// The alias is for reading, not writing: the live event must emit the new
	// name, or the rename never actually happened.
	if got := pd.Type(); got != "coord.plan.done" {
		t.Errorf("PlanDoneEvent.Type() = %q, want coord.plan.done — the legacy alias must not become the name we publish", got)
	}
}
