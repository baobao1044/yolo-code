// The event listener — the ONLY writer to the memory sub-stores (besides the
// user-editable Preference store) (File 11 §11.2). Open starts a goroutine that
// subscribes to patch.applied, tool.result, task.completed, assistant.message
// and dispatches each by type to the sub-store's handler. Every reaction
// publishes a memory.update event so the transcript shows the learning. The
// listener is idempotent on Env.Seq (File 05 §5.6.1): a replayed event (same
// seq, possible across restart) is dropped — never double-applied.
//
// Lifecycle: subscribe BEFORE driving (File 05 §5.6 — no event missed); drain
// via `for env := range ch`; the bus's Close closes all subscriber channels so
// the range ends naturally. Close (on Store.Close) is idempotent.

package memory

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/event"
)

// listenTopics is the set the memory listener subscribes to. §11.2 lists the
// full event→memory mapping; L10-006 widens it beyond the L10-002 set to cover
// the working/knowledge/exec lifecycle: task.started/state.change drive Working
// memory; verification.* feed Knowledge insights; user.preference routes to
// the Preference store; task.completed clears Working + persists exec/knowledge.
var listenTopics = []event.Topic{
	event.Topic("patch.applied"),
	event.Topic("tool.result"),
	event.Topic("task.started"),
	event.Topic("task.completed"),
	event.Topic("state.change"),
	event.Topic("assistant.message"),
	event.Topic("verification.failed"),
	event.Topic("verification.stage"),
	event.Topic("user.preference"),
}

// listen subscribes to the memory topics and runs the drain goroutine. Called
// by Open when a Bus is wired. Safe to call once; a second call is a no-op.
func (s *Store) listen(bus *event.Bus) {
	if s == nil || bus == nil || s.listening {
		return
	}
	s.bus = bus
	s.ch = bus.Subscribe(listenTopics...)
	s.done = make(chan struct{})
	s.listening = true
	go s.drain()
}

// drain is the listener loop: read envelopes, dispatch each via deliver (which
// applies the event to the sub-stores, idempotent on Seq). Exits when the bus
// closes the subscriber channel (Close → range ends).
func (s *Store) drain() {
	defer close(s.done)
	for env := range s.ch {
		s.deliver(env)
	}
}

// deliver applies one envelope to the sub-stores, idempotent on Env.Seq. A
// replayed seq (already in the seen-set) is dropped. On a fresh event, it
// dispatches by event type, records the seq, and publishes a memory.update
// event naming the store that learned. Exposed (package-private) so the
// idempotency test can deliver hand-built envelopes with a fixed seq.
func (s *Store) deliver(env event.Envelope) {
	if s.alreadySeen(env.Seq) {
		return
	}
	store, items := s.dispatch(env.Evt)
	if store == "" {
		return // not a memory-relevant event (shouldn't happen — only subscribed topics)
	}
	s.publishUpdate(env.Evt.CausalID(), store, items)
}

// dispatch routes an event to its sub-store handler and returns the store name
// + how many items it learned (for the memory.update event). Returns ("", 0)
// for events the listener doesn't handle (defensive — the subscription is
// filtered, so this shouldn't fire).
func (s *Store) dispatch(e event.Event) (store string, items int) {
	ctx := context.Background()
	switch ev := e.(type) {
	case *event.AssistantMessageEvent:
		s.conversation.AppendAssistant(ctx, string(ev.Task), Message{
			Role: RoleAssistant, Text: ev.Text,
		})
		return "conversation", 1
	case *event.ToolResultEvent:
		s.exec.Append(ctx, string(ev.Task), ExecEntry{
			Kind: "tool", Summary: ev.Tool + " result",
		})
		return "exec", 1
	case *event.PatchAppliedEvent:
		paths := make([]string, 0, len(ev.Files))
		for _, f := range ev.Files {
			paths = append(paths, f.Path)
		}
		s.repo.Invalidate(paths) // fast (in-memory)
		// Reindex reads via FS + embeds — offload so the drain stays fast and
		// the listener channel doesn't fill (which would block the drive loop's
		// fan-out holding fanoutMu, deadlocking publishUpdate). A nil FS makes
		// Reindex a no-op, so this is safe to fire-and-forget. Tracked on bg so
		// Close waits for it (no racing a temp-dir cleanup).
		pathsCopy := append([]string(nil), paths...)
		s.bg.Add(1)
		go func() {
			defer s.bg.Done()
			for _, p := range pathsCopy {
				s.knowledge.Reindex(context.Background(), p, nil) // §11.7.5
			}
		}()
		return "repo", len(paths)
	case *event.TaskStartedEvent:
		// §11.3.1: task.created → Working Memory sets the task.
		s.working.SetTask(ev.Goal)
		s.working.SetState("") // fresh task, no state yet
		return "working", 1
	case *event.StateChangeEvent:
		// §11.3.1: state.change → Working Memory updates the state.
		s.working.SetState(ev.To)
		return "working", 1
	case *event.VerificationFailedEvent:
		// §11.5.1: verify.fail → record the failure insight. Reason is the
		// lesson text; Source tags the teaching event so a reader can attribute.
		s.insights.Record(ctx, ev.Reason, "verify.fail")
		return "knowledge", 1
	case *event.VerificationStageEvent:
		// §11.5.1: a passing/failing stage is a teaching signal. Skip neutral
		// statuses and empty detail (no insight to record).
		if ev.Status != "pass" && ev.Status != "fail" {
			return "", 0
		}
		text := ev.Stage + ": " + ev.Detail
		if ev.Detail == "" {
			text = ev.Stage
		}
		s.insights.Record(ctx, text, "verify."+ev.Status)
		return "knowledge", 1
	case *event.UserPreferenceEvent:
		// §11.5.2: user.preference → Preference store (the one user-editable
		// sub-store; agent-originated updates are still event-driven, §11.2).
		if err := s.pref.Set(ctx, ev.Key, ev.Value); err == nil {
			return "preference", 1
		}
		return "", 0
	case *event.TaskCompletedEvent:
		// §11.3.1 + §11.5.1: task.completed → clear Working (fast), record a
		// Knowledge success pattern (fast, in-memory embed), then offload the
		// slow file I/O (Persist conversation/exec/knowledge) so the drain
		// stays fast and publishUpdate doesn't deadlock on a full channel.
		// Tracked on bg so Close waits for it (no racing a temp-dir cleanup).
		s.working.Clear()
		s.insights.Record(ctx, "task.completed: "+string(ev.Task), "task.completed")
		tid := string(ev.Task)
		s.bg.Add(1)
		go func() {
			defer s.bg.Done()
			_ = s.conversation.Persist(context.Background(), tid)
			_ = s.exec.Persist(context.Background(), tid)
			_ = s.insights.Persist(context.Background())
		}()
		return "working", 1
	}
	return "", 0
}

// publishUpdate emits the memory.update event naming the store that learned
// and how many items (File 11 §5.4.5). Best-effort: a nil bus (unit test) or a
// dropped event is survivable — the sub-store already mutated.
//
// This is SYNCHRONOUS (not a goroutine) so the event's Seq is deterministic
// relative to the spine — the golden transcript is byte-identical across runs
// (S5). The reentrant-backpressure deadlock this could cause (the drain holds
// the channel while Publish wants fanoutMu, which the drive loop holds while
// blocked on that same full channel) is avoided upstream in dispatch: slow
// I/O reactions (Persist, Reindex) are offloaded to background goroutines, so
// the drain loop stays fast, the listener channel never fills, and the drive
// loop's fan-out never blocks on it → fanoutMu is released before publishUpdate
// runs here.
func (s *Store) publishUpdate(task event.TaskID, store string, items int) {
	if s.bus == nil {
		return
	}
	_ = s.bus.Publish(context.Background(), &event.MemoryUpdateEvent{
		Task:  task,
		Store: store,
		Items: items,
	})
}

// alreadySeen reports whether the seq was applied before, recording it if not.
// Guarded by the seen-mu so concurrent delivers (not expected — single
// listener goroutine — but safe) don't race the map.
func (s *Store) alreadySeen(seq uint64) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if s.seen == nil {
		s.seen = make(map[uint64]bool)
	}
	if s.seen[seq] {
		return true
	}
	s.seen[seq] = true
	return false
}
