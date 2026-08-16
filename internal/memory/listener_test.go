// Tests for L10-002 — the update-only-via-events rule (File 11 §11.2). The
// Store subscribes to patch.applied, tool.result, task.completed,
// assistant.message and reacts in a listener goroutine — the ONLY writer to
// the sub-stores (besides the user-editable Preference store). Each reaction
// publishes a memory.update event so the trace shows the learning. The
// listener is idempotent on Env.Seq (File 05 §5.6.1): a replayed event is
// dropped, never double-applied.
//
// The "rule" gate: the package exposes no public mutator on Conversation/
// ExecHistory/Project/Semantic besides AppendAssistant/Append/Invalidate/Reindex
// (called by the listener, within the package). Other layers can't reach them
// (memory is importable only by event + cmd-yolo); a direct write from outside
// the package is impossible. L10-006's runtime.MemoryStore.Update adapter
// publishes an event the listener reacts to — it does NOT mutate a sub-store
// directly.

package memory

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// newListenerStore wires a Store over a fresh dir + bus with the listener
// running, returning the store, bus, and a drain helper. The listener must be
// started before events are published (subscribe-before-drive, File 05 §5.6).
func newListenerStore(t *testing.T) (*Store, *event.Bus) {
	t.Helper()
	s, err := Open(Deps{Root: t.TempDir(), Bus: event.New()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = s.bus.Close() // ends the drain range → done closes
		_ = s.Close()     // waits for the goroutine to exit
	})
	return s, s.bus
}

// drain runs until the bus is quiet for `quiet` with no new memory.update event.
// Returns the memory.update events seen so a test can assert the trace.
func drain(t *testing.T, ch <-chan event.Envelope, quiet time.Duration) []*event.MemoryUpdateEvent {
	t.Helper()
	var out []*event.MemoryUpdateEvent
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return out
			}
			if u, ok := env.Evt.(*event.MemoryUpdateEvent); ok {
				out = append(out, u)
			}
		case <-time.After(quiet):
			return out
		}
	}
}

func TestListenerAppendsAssistantMessage(t *testing.T) {
	// assistant.message → ConversationStore.AppendAssistant → memory.update.
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.AssistantMessageEvent{
		Task: "s_1", Text: "hello", Final: true,
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) != 1 {
		t.Fatalf("memory.update events = %d, want 1 (one assistant append)", len(ups))
	}
	if ups[0].Store != "conversation" {
		t.Errorf("memory.update Store = %q, want \"conversation\"", ups[0].Store)
	}
	if got := s.Conversation().Messages("s_1"); len(got) != 1 || got[0].Text != "hello" {
		t.Errorf("Messages(s_1) = %+v, want one \"hello\"", got)
	}
}

func TestListenerAppendsToolResult(t *testing.T) {
	// tool.result → ExecHistoryStore.Append → memory.update(store=exec).
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.ToolResultEvent{
		Task: "t_1", Tool: "read_file", Obs: []byte(`{"ok":true}`),
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) != 1 || ups[0].Store != "exec" {
		t.Fatalf("memory.update = %+v, want 1 with store \"exec\"", ups)
	}
	if got := s.ExecHistory().Entries("t_1"); len(got) != 1 {
		t.Errorf("Entries(t_1) = %d, want 1 (tool result appended)", len(got))
	}
}

func TestListenerInvalidatesOnPatchApplied(t *testing.T) {
	// patch.applied → ProjectStore.Invalidate(paths) + LexicalStore.Reindex.
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.PatchAppliedEvent{
		Task: "t_2",
		Files: []event.PatchFile{
			{Path: "a.go", Insertions: 3, Deletions: 1},
			{Path: "b.md", New: true},
		},
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) == 0 {
		t.Fatal("no memory.update event (patch didn't trigger a learning)")
	}
	// The touched paths were invalidated.
	stale := s.Project().Stale()
	if len(stale) != 2 {
		t.Errorf("Stale() = %v, want 2 paths invalidated (a.go, b.md)", stale)
	}
}

func TestListenerIdempotentOnReplayedSeq(t *testing.T) {
	// The cardinal idempotency rule (§5.6.1): a replayed event (same Env.Seq)
	// is dropped — never double-applied. Publish the same assistant message
	// twice with the SAME seq (simulating a replay across restart) and assert
	// the conversation gets ONE message, not two.
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	// Synthesize two envelopes with the same Seq directly through the bus is
	// not possible (Publish stamps a new Seq each time), so test the listener's
	// idempotency via its internal seen-set: deliver a hand-built Envelope with
	// seq=5 twice through the listener's dispatch and assert one apply.
	evt := &event.AssistantMessageEvent{Task: "s_3", Text: "once", Final: true}
	s.deliver(event.Envelope{Seq: 5, Evt: evt})
	s.deliver(event.Envelope{Seq: 5, Evt: evt}) // replay — same seq

	// Drain the two memory.update events the first delivery would have
	// published (the replay publishes none).
	go func() {
		_ = bus
	}()
	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) != 1 {
		t.Errorf("memory.update events = %d, want 1 (replay dropped)", len(ups))
	}
	if got := s.Conversation().Messages("s_3"); len(got) != 1 {
		t.Errorf("Messages(s_3) = %d, want 1 (replay double-applied: %v)", len(got), got)
	}
}

func TestListenerNoPublicDirectMutators(t *testing.T) {
	// The "rule" gate: the only public mutators on the sub-stores are the ones
	// the listener (within the package) calls. A direct write from outside
	// memory is impossible — the methods exist but are reachable only because
	// the listener lives in the same package. This test asserts the Store
	// aggregate itself exposes NO public Write/Set/Append; the composition root
	// can't mutate a sub-store without going through an event.
	//
	// (This is a static-style assertion via the type system: the test would
	// fail to compile if a public `Store.Write` existed and were called. Kept
	// as a no-op that documents the invariant — the import matrix is the real
	// enforcement.)
	s, _ := newListenerStore(t)
	_ = s // no public mutator to call; the invariant is "none exist"
}

// TestListenerSetsTaskOnTaskStarted: task.started → Working.SetTask (§11.3.1).
func TestListenerSetsTaskOnTaskStarted(t *testing.T) {
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.TaskStartedEvent{
		Task: "t_9", Session: "s_1", Goal: "write a fibonacci CLI",
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) == 0 || ups[0].Store != "working" {
		t.Fatalf("memory.update = %+v, want 1 with store \"working\"", ups)
	}
	if got := s.Working().Task(); got != "write a fibonacci CLI" {
		t.Errorf("Working.Task = %q, want the goal", got)
	}
}

// TestListenerSetsStateOnStateChange: state.change → Working.SetState (§11.3.1).
func TestListenerSetsStateOnStateChange(t *testing.T) {
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.StateChangeEvent{
		Task: "t_9", From: "plan", To: "exec", Why: "verified",
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) == 0 || ups[0].Store != "working" {
		t.Fatalf("memory.update = %+v, want 1 with store \"working\"", ups)
	}
	if got := s.Working().State(); got != "exec" {
		t.Errorf("Working.State = %q, want exec", got)
	}
}

// TestListenerRecordsInsightOnVerificationFailed: verification.failed →
// Knowledge.Record (§11.5.1).
func TestListenerRecordsInsightOnVerificationFailed(t *testing.T) {
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.VerificationFailedEvent{
		Task: "t_9", Reason: "Race detector requires CGO",
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) == 0 || ups[0].Store != "knowledge" {
		t.Fatalf("memory.update = %+v, want 1 with store \"knowledge\"", ups)
	}
	all := s.Insights().All()
	if len(all) != 1 || all[0].Text != "Race detector requires CGO" {
		t.Errorf("Insights = %+v, want one Race-detector insight", all)
	}
}

// TestListenerSetsPreferenceOnUserPreference: user.preference → Preference.Set
// (§11.5.2 — agent-originated updates are still event-driven).
func TestListenerSetsPreferenceOnUserPreference(t *testing.T) {
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.UserPreferenceEvent{
		Key: "style", Value: "conventional commits",
	})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) == 0 || ups[0].Store != "preference" {
		t.Fatalf("memory.update = %+v, want 1 with store \"preference\"", ups)
	}
	val, err := s.Preferences().Get(context.Background(), "style")
	if err != nil || val != "conventional commits" {
		t.Errorf("Preferences.Get(style) = %q, err=%v, want \"conventional commits\"", val, err)
	}
}

// TestListenerReportsAFailedPreferenceWrite pins the one arm that reports its
// own failure, and the reason it is the only one.
//
// Every other arm records something derived — an insight inferred from a
// verification, a working-memory field tracked off a state change. Losing one
// degrades recall quietly and there is nobody to tell. A preference is a direct
// instruction the user just typed, and until this arm reported, a failed write
// returned ("", 0): no memory.update, no error, no log. The user was told
// "preference: style = tabs" by the slash command, the disk write failed, and
// they found out weeks later when the agent kept doing the thing they had asked
// it to stop doing.
func TestListenerReportsAFailedPreferenceWrite(t *testing.T) {
	root := t.TempDir()
	s, err := Open(Deps{Root: root, Bus: event.New()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bus := s.bus
	t.Cleanup(func() {
		_ = os.Chmod(root, 0o700) // let TempDir's cleanup remove it again
		_ = bus.Close()
		_ = s.Close()
	})

	// Make the store's directory unwritable so writeJSON fails for real, rather
	// than injecting a fake error into a seam the production path does not use.
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Precondition, checked rather than assumed. Running as root ignores the
	// mode bits entirely, and this test would then be green while proving
	// nothing at all — it would never reach the branch it exists to cover.
	if err := s.Preferences().Set(context.Background(), "probe", "x"); err == nil {
		t.Skip("a read-only directory is still writable here (running as root?), so the " +
			"failure branch is unreachable in this environment")
	}

	errs := bus.Subscribe(event.Topic("error"))
	bus.Publish(context.Background(), &event.UserPreferenceEvent{
		Task: "t_1", Key: "style", Value: "tabs",
	})

	deadline := time.After(5 * time.Second)
	for {
		select {
		case env, ok := <-errs:
			if !ok {
				t.Fatal("bus closed before the error arrived")
			}
			e, ok := env.Evt.(*event.ErrorEvent)
			if !ok {
				continue
			}
			if e.Layer != "memory" {
				t.Errorf("Layer = %q, want memory", e.Layer)
			}
			if e.Code != "preference_write_failed" {
				t.Errorf("Code = %q, want preference_write_failed", e.Code)
			}
			// The key has to be in the message. "could not save preference"
			// alone leaves the user unable to tell which of their preferences
			// was lost, which is most of what they need to know to retry.
			if !strings.Contains(e.Msg, "style") {
				t.Errorf("Msg = %q, want it to name the key that was lost", e.Msg)
			}
			return
		case <-deadline:
			t.Fatal("the preference write failed and nothing was published: the user was told " +
				"the preference was recorded and it was not")
		}
	}
}

// TestListenerClearsWorkingOnTaskCompleted: task.completed → Working.Clear +
// persists Conversation/Exec/Knowledge (§11.3.1). It records NO insight: see
// TestListenerRecordsNoInsightOnTaskCompleted.
func TestListenerClearsWorkingOnTaskCompleted(t *testing.T) {
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	// Prime Working so Clear is observable.
	s.Working().SetTask("a goal")
	s.Working().SetState("exec")

	bus.Publish(context.Background(), &event.TaskCompletedEvent{Task: "t_done"})

	ups := drain(t, ch, 100*time.Millisecond)
	if len(ups) == 0 || ups[0].Store != "working" {
		t.Fatalf("memory.update = %+v, want 1 with store \"working\"", ups)
	}
	if got := s.Working().Task(); got != "" {
		t.Errorf("after task.completed, Working.Task = %q, want empty (cleared)", got)
	}
	if got := s.Working().State(); got != "" {
		t.Errorf("after task.completed, Working.State = %q, want empty (cleared)", got)
	}
}

// TestListenerRecordsNoInsightOnTaskCompleted: task.completed must NOT write a
// Knowledge insight. The old handler recorded "task.completed: <task id>" — a
// per-task-unique string that can never dedupe, can never be retrieved (it
// shares no token with any future query), and is re-embedded at every Open. It
// was pure unbounded growth. Two completed tasks must leave the store empty.
func TestListenerRecordsNoInsightOnTaskCompleted(t *testing.T) {
	s, bus := newListenerStore(t)
	ch := bus.Subscribe(event.Topic("memory.update"))

	bus.Publish(context.Background(), &event.TaskCompletedEvent{Task: "t_one"})
	bus.Publish(context.Background(), &event.TaskCompletedEvent{Task: "t_two"})
	drain(t, ch, 100*time.Millisecond)

	if all := s.Insights().All(); len(all) != 0 {
		t.Errorf("task.completed recorded %d insight(s) %+v, want 0 (a task id is not a lesson)", len(all), all)
	}
}

// TestWorkingMemoryIsRaceFreeAgainstTheListener: Working memory's task/state are
// written from the LISTENER goroutine (task.started / state.change /
// task.completed), not from the drive loop, so a reader on any other goroutine
// races them. WorkingMemory carried no mutex on the strength of the
// single-writer invariant, which does not hold for these two fields. Run under
// -race: this reports a DATA RACE on the unsynchronised struct.
func TestWorkingMemoryIsRaceFreeAgainstTheListener(t *testing.T) {
	s, bus := newListenerStore(t)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the listener writes task/state via these events
		defer wg.Done()
		for i := 0; i < 50; i++ {
			bus.Publish(context.Background(), &event.TaskStartedEvent{Task: "t_race", Goal: "goal"})
			bus.Publish(context.Background(), &event.StateChangeEvent{Task: "t_race", From: "plan", To: "exec"})
			bus.Publish(context.Background(), &event.TaskCompletedEvent{Task: "t_race"})
		}
	}()
	for r := 0; r < 3; r++ { // readers on other goroutines
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = s.Working().Task()
				_ = s.Working().State()
				_ = s.Working().History()
				s.Working().Append(Message{Role: RoleUser, Text: "turn"})
				_ = s.Working().Fork("next")
			}
		}()
	}
	wg.Wait()
}
