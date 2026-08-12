// Working memory — in-process, per-turn, the fastest tier (§11.3.1). No I/O:
// the source the Context Engine reads during a turn. A turn forks the
// conversation so a canceled turn can't corrupt the shared list; the fork
// merges back only on success (L10-002 wires the merge; L10-001 ships the
// fork + history read).
//
// Owned by the runtime goroutine (File 04 Invariant I1): the single writer is
// the drive loop, so no mutex here — concurrent access would need one if a
// future multi-task scheduler shares a WorkingMemory, which the spec rules out
// (one working memory per turn, owned by the driving goroutine).

package memory

// WorkingMemory holds the live conversation being mutated this turn. It forks
// a Conversation view so a turn's tentative appends don't touch the parent
// until the fork is committed. It also carries the current task + state
// (§11.3.1): task.created → SetTask, state.change → SetState, task.completed
// → Clear (the listener drives these, §11.2). The Context Engine reads these
// as the highest-priority context input.
type WorkingMemory struct {
	conv  *Conversation
	task  string
	state string
}

// SetTask records the current task (§11.3.1 — on task.created). Nil-safe.
func (w *WorkingMemory) SetTask(t string) {
	if w != nil {
		w.task = t
	}
}

// Task returns the current task (§11.3.1). Empty for a nil/empty working memory.
func (w *WorkingMemory) Task() string {
	if w == nil {
		return ""
	}
	return w.task
}

// SetState records the current runtime state (§11.3.1 — on state.change).
// Nil-safe.
func (w *WorkingMemory) SetState(s string) {
	if w != nil {
		w.state = s
	}
}

// State returns the current runtime state (§11.3.1).
func (w *WorkingMemory) State() string {
	if w == nil {
		return ""
	}
	return w.state
}

// Clear resets the task + state + live conversation (§11.3.1 — on
// task.completed). Nil-safe.
func (w *WorkingMemory) Clear() {
	if w != nil {
		w.task = ""
		w.state = ""
		w.conv = nil
	}
}

// Append adds a message to the working memory's live conversation. The runtime
// goroutine is the only writer (I1), so this is unsynchronized.
func (w *WorkingMemory) Append(m Message) {
	if w.conv == nil {
		w.conv = &Conversation{}
	}
	w.conv.Messages = append(w.conv.Messages, m)
}

// History returns the conversation as Parts for the Context Engine (§11.3.1).
// Each turn becomes a Part labeled "turn#<seq>" with the role in Attr, so the
// prompt compiler can rebuild role-tagged messages (File 06 §6.6.2 order()).
func (w *WorkingMemory) History() []Part {
	if w.conv == nil {
		return nil
	}
	return w.conv.parts()
}

// Fork returns a new Conversation carrying the parent's history plus a new user
// turn, without mutating the parent (§11.3.1: a canceled turn doesn't corrupt
// the shared list). The composition root commits the fork on turn success.
func (w *WorkingMemory) Fork(text string) *Conversation {
	base := []Message(nil)
	if w.conv != nil {
		base = append(base, w.conv.Messages...)
	}
	return &Conversation{Messages: append(base, Message{Role: RoleUser, Text: text})}
}
