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
	event.Topic("task.failed"),
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
		// Redacted on the way in: this text is persisted to
		// conversations/<sid>.json, which outlives the session (§11.3.3).
		s.conversation.AppendAssistant(ctx, string(ev.Task), Message{
			Role: RoleAssistant, Text: s.redact(ev.Text),
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
		// the listener channel doesn't fill, which would push backpressure onto
		// the drive loop's fan-out. A nil FS makes Reindex a no-op, so this is
		// safe to fire-and-forget. Tracked on bg so Close waits for it (no
		// racing a temp-dir cleanup).
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
		// Redacted first: verify runs its own commands and never passes through
		// exec's output normalizer, so this text is raw at birth and would reach
		// knowledge.json verbatim.
		s.insights.Record(ctx, s.redact(ev.Reason), "verify.fail")
		return "knowledge", 1
	case *event.TaskFailedEvent:
		// docs/rag/memory-lifecycle.md maps task.failed → Knowledge → record
		// insight, and knowledge.go has always listed "task.failed" among the
		// valid Source values. Neither could fire: no such event existed. The
		// reason is redacted on the way in for the same reason verify's is —
		// it is a raw error string from whatever blew up, not something that
		// passed through exec's output normalizer, and it lands in
		// knowledge.json which outlives the session.
		s.insights.Record(ctx, s.redact(ev.Reason), "task.failed")
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
		s.insights.Record(ctx, s.redact(text), "verify."+ev.Status)
		return "knowledge", 1
	case *event.UserPreferenceEvent:
		// §11.5.2: user.preference → Preference store (the one user-editable
		// sub-store; agent-originated updates are still event-driven, §11.2).
		//
		// This is the only arm that reports its own failure, and the asymmetry
		// is deliberate rather than an oversight. Every other arm records
		// something derived — an insight inferred from a verification, a
		// working-memory field tracked off a state change — and losing one
		// degrades recall quietly. A preference is a direct instruction the
		// user just typed. Dropping it in silence means the system accepted an
		// order and discarded it, and the user finds out only much later, when
		// the agent keeps doing the thing they asked it to stop doing. Set
		// writes the JSON file, so the error is reachable (a full or read-only
		// disk), not theoretical.
		//
		// TryPublish, not Publish: this runs on the listener's own drain
		// goroutine, the same place publishUpdate posts from, and a blocking
		// send back into the bus from here would be a self-deadlock the moment
		// a subscriber is slow.
		if err := s.pref.Set(ctx, ev.Key, ev.Value); err != nil {
			if s.bus != nil {
				_, _ = s.bus.TryPublish(&event.ErrorEvent{
					Task:  event.TaskID(ev.Task),
					Layer: "memory",
					Code:  "preference_write_failed",
					Msg:   "could not save preference " + ev.Key + ": " + err.Error(),
				})
			}
			return "", 0
		}
		return "preference", 1
	case *event.TaskCompletedEvent:
		// §11.3.1: task.completed → clear Working (fast), then offload the slow
		// file I/O (Persist conversation/exec/knowledge) so the drain stays fast
		// and the listener's channel doesn't back up into the drive loop.
		// Tracked on bg so Close waits for it (no racing a temp-dir cleanup).
		//
		// This deliberately records NO Knowledge insight. It used to write
		// "task.completed: <task id>", which is a lesson in name only: the task
		// id is unique per task, so the text can never dedupe, can never match a
		// future query (no shared tokens with anything the agent will ask), and
		// exists only to be re-embedded at every Open. It was the store's main
		// source of unbounded growth and carried no recallable signal. The
		// insights that do carry signal come from verification.failed /
		// verification.stage above, which describe what went wrong rather than
		// which id it went wrong under.
		s.working.Clear()
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
// (S5). TryPublish keeps that (it stamps and fsyncs under the same lock Publish
// does) while removing the reason the synchronous call was dangerous.
//
// It is a TryPublish and not a Publish because this call is issued from inside
// the listener's own drain loop. A blocking Publish here waits for a
// memory.update subscriber to make room in its 64-slot buffer, and while it
// waits the goroutine that drains *this* store's channel is the one parked — so
// the listener's own channel fills behind it and the drive loop's fan-out
// blocks on that. A slow renderer becomes a stalled agent, and Store.Close's
// `<-s.done` waits on a drain that cannot advance.
//
// (The older note here explained this in terms of holding fanoutMu across
// delivery. That is no longer how the bus works: stamping and ticket
// reservation happen under fanoutMu and the delivery itself runs with the lock
// released, so there is no process-wide lock to deadlock on. The hazard that
// survived the rewrite is plain per-subscriber backpressure, which offloading
// Persist/Reindex does not help with at all — those keep the *drain* fast, but
// this publish is what parks it.)
//
// Dropping under backpressure is the right trade for this event specifically:
// it is observational telemetry, not spine. The headless transcript filters
// memory.update out by name to stay deterministic; the TUI's only use is a
// cosmetic "+N <store>" flash (tui/fold.go); the wildcard subscribers are
// infra's observers and the JSONL transcripts. Nothing reads it to decide
// anything, nothing accumulates it, and no consumer needs at-least-once. The
// loss is not silent either — the bus counts it in Stats().Dropped, which
// cmd/yolo already reports on stderr — and the envelope is still in the
// durability log, because TryPublish stamps and appends before fan-out.
func (s *Store) publishUpdate(task event.TaskID, store string, items int) {
	if s.bus == nil {
		return
	}
	_, _ = s.bus.TryPublish(&event.MemoryUpdateEvent{
		Task:  task,
		Store: store,
		Items: items,
	})
}

// redact masks secrets in text the listener is about to hand to a sub-store.
// A Store opened with no Deps.Redactor passes text through unchanged — the
// nil-tolerant seam the package's own tests rely on.
func (s *Store) redact(text string) string {
	if s == nil || s.redactor == nil {
		return text
	}
	return s.redactor.Redact(text)
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
