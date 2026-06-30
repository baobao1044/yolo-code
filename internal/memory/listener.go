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

// listenTopics is the set the memory listener subscribes to (§11.2).
var listenTopics = []event.Topic{
	event.Topic("patch.applied"),
	event.Topic("tool.result"),
	event.Topic("task.completed"),
	event.Topic("assistant.message"),
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
		s.repo.Invalidate(paths)
		for _, p := range paths {
			s.knowledge.Reindex(ctx, p, nil) // L10-004 passes the new content
		}
		return "repo", len(paths)
	case *event.TaskCompletedEvent:
		// Persist the conversation + exec history for the completed task so a
		// restart can resume (§11.3.3 — resume with integrity).
		if err := s.conversation.Persist(ctx, string(ev.Task)); err == nil {
			return "conversation", 1
		}
		return "", 0
	}
	return "", 0
}

// publishUpdate emits the memory.update event naming the store that learned
// and how many items (File 11 §5.4.5). Best-effort: a nil bus (unit test) or a
// dropped event is survivable — the sub-store already mutated.
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
