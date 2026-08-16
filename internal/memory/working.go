// Working memory — in-process, per-turn, the fastest tier (§11.3.1). No I/O:
// the source the Context Engine reads during a turn. A turn forks the
// conversation so a canceled turn can't corrupt the shared list; the fork
// merges back only on success (L10-002 wires the merge; L10-001 ships the
// fork + history read).
//
// Concurrency: File 04's Invariant I1 makes the drive loop the sole writer of a
// turn's working memory, and this used to be documented as reason enough to
// carry no mutex. It isn't, for one concrete reason: task/state are written
// from the memory LISTENER goroutine (listener.go dispatch — task.started,
// state.change, task.completed), not from the drive loop, so any reader on any
// other goroutine is already a data race. The mutex below costs one uncontended
// atomic per access on a struct touched a handful of times per turn.

package memory

import "sync"

// WorkingMemory holds the live conversation being mutated this turn. It forks
// a Conversation view so a turn's tentative appends don't touch the parent
// until the fork is committed. It also carries the current task + state
// (§11.3.1): task.created → SetTask, state.change → SetState, task.completed
// → Clear (the listener drives these, §11.2). The Context Engine reads these
// as the highest-priority context input.
//
// mu guards every field: the listener goroutine writes task/state/conv while
// any other goroutine may read them (see the file header).
type WorkingMemory struct {
	mu    sync.RWMutex
	conv  *Conversation
	task  string
	state string
}

// SetTask records the current task (§11.3.1 — on task.created). Nil-safe.
func (w *WorkingMemory) SetTask(t string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.task = t
}

// Task returns the current task (§11.3.1). Empty for a nil/empty working memory.
func (w *WorkingMemory) Task() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.task
}

// SetState records the current runtime state (§11.3.1 — on state.change).
// Nil-safe.
func (w *WorkingMemory) SetState(s string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = s
}

// State returns the current runtime state (§11.3.1).
func (w *WorkingMemory) State() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// Clear resets the task + state + live conversation (§11.3.1 — on
// task.completed). Nil-safe.
func (w *WorkingMemory) Clear() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.task = ""
	w.state = ""
	w.conv = nil
}

// Append adds a message to the working memory's live conversation.
func (w *WorkingMemory) Append(m Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conv == nil {
		w.conv = &Conversation{}
	}
	w.conv.Messages = append(w.conv.Messages, m)
}

// History returns the conversation as Parts for the Context Engine (§11.3.1).
// Each turn becomes a Part labeled "turn#<seq>" with the role in Attr, so the
// prompt compiler can rebuild role-tagged messages (File 06 §6.6.2 order()).
func (w *WorkingMemory) History() []Part {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.conv == nil {
		return nil
	}
	return w.conv.parts()
}

// Fork returns a new Conversation carrying the parent's history plus a new user
// turn, without mutating the parent (§11.3.1: a canceled turn doesn't corrupt
// the shared list). The composition root commits the fork on turn success.
func (w *WorkingMemory) Fork(text string) *Conversation {
	w.mu.RLock()
	base := []Message(nil)
	if w.conv != nil {
		base = append(base, w.conv.Messages...)
	}
	w.mu.RUnlock()
	return &Conversation{Messages: append(base, Message{Role: RoleUser, Text: text})}
}
