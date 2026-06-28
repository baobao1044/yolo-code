// Session/task types and the status lifecycle (File 03 §3.2).
//
// Sprint 1 scope (L1-001…L1-003): the session/task model with the full status
// lifecycle, the per-task undo stack, named checkpoints with restore, the
// interrupted-task-resumes-as-PAUSED safety rule, and cancel vs. pause. The
// git snapshot primitive and the SQLite persistence schema are stubbed here and
// handed off to the Patch Engine (File 10) and Memory (File 11) respectively.

package session

import "time"

// ID identifies a session (a conversation).
type ID string

// TaskID identifies a task within a session. Allocated monotonically (I2,
// File 04 §4.2.1); a transition for an older task is a stale event, dropped.
type TaskID string

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	// StatusPending: task allocated, runtime has not picked it up yet.
	StatusPending TaskStatus = "PENDING"
	// StatusRunning: the runtime is actively driving this task.
	StatusRunning TaskStatus = "RUNNING"
	// StatusPaused: halted at a safe boundary; resumable. HITL approval waits
	// and explicit user pauses land here.
	StatusPaused TaskStatus = "PAUSED"
	// StatusDone: verify passed and the task committed; terminal.
	StatusDone TaskStatus = "DONE"
	// StatusFailed: retries exhausted or a hard error; terminal.
	StatusFailed TaskStatus = "FAILED"
	// StatusCancelled: user cancelled and rolled back to the last checkpoint;
	// terminal.
	StatusCancelled TaskStatus = "CANCELLED"
)

// IsTerminal reports whether status is a terminal state (no further
// transitions except the error-recovery reset, File 04 §4.2 T20).
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case StatusDone, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// EntryKind classifies a history entry by what it recorded.
type EntryKind string

const (
	KindPatch      EntryKind = "patch"
	KindBash       EntryKind = "bash"
	KindFileWrite  EntryKind = "file-write"
	KindCheckpoint EntryKind = "checkpoint"
)

// SnapshotRef is an opaque handle to a restorable state (a git tree, File 10,
// or a shadow copy). Sprint 1 treats it as an opaque string; the Patch Engine
// gives it meaning.
type SnapshotRef string

// TokenUse is the running token tally the Cost Controller (File 07) reads.
// Stubbed in Sprint 1; wired in Sprint 3.
type TokenUse struct {
	Input  int
	Output int
}

// Session is a conversation: the framing that owns one or more tasks.
type Session struct {
	ID        ID        `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"` // auto-derived from first user message
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Model     string    `json:"model,omitempty"` // which model this session used
	Tasks     []TaskID  `json:"tasks"`           // ordered; one session may run many tasks
}

// Task is the unit of work, with status, retry state, and an undo-able
// history stack (File 03 §3.2.1).
type Task struct {
	ID         TaskID         `json:"id"`
	SessionID  ID             `json:"session_id"`
	Goal       string         `json:"goal"`       // the user's request for this task
	Status     TaskStatus     `json:"status"`     // PENDING | RUNNING | PAUSED | DONE | FAILED | CANCELLED
	Checkpoint string         `json:"checkpoint"` // name of the last safe checkpoint, e.g. "patch_03"
	Retry      int            `json:"retry"`      // reflection-driven retries so far
	RetryMax   int            `json:"retry_max"`  // cap (default from the Cost Controller, File 07)
	StartedAt  time.Time      `json:"started_at"`
	EndedAt    *time.Time     `json:"ended_at,omitempty"`
	Tokens     TokenUse       `json:"tokens"`
	History    []HistoryEntry `json:"history"` // undo-able changes, see §3.3
}

// HistoryEntry records one applied change so it can be undone (File 03 §3.3.1).
// It is the link between "the model did something" and "the user can undo it".
type HistoryEntry struct {
	Seq        int         `json:"seq"`        // monotonic per task
	Kind       EntryKind   `json:"kind"`       // patch | bash | file-write | checkpoint
	Snapshot   SnapshotRef `json:"snapshot"`   // git snapshot id (File 10) or shadow copy
	Summary    string      `json:"summary"`    // 1-line, for the undo menu
	Paths      []string    `json:"paths"`      // affected files
	Reversible bool        `json:"reversible"` // false only for truly irreversible ops (denied by default)
	At         time.Time   `json:"at"`
}

// Errors returned by the Manager.
var (
	// ErrUnknownSession is returned when an operation references a session the
	// manager has no record of.
	ErrUnknownSession = errStr("session: unknown session")
	// ErrUnknownTask is returned when an operation references a task the
	// manager has no record of.
	ErrUnknownTask = errStr("session: unknown task")
	// ErrNothingToUndo is returned by Undo when the task's history is empty.
	ErrNothingToUndo = errStr("session: nothing to undo")
	// ErrNotReversible is returned by Undo when the top history entry is marked
	// irreversible.
	ErrNotReversible = errStr("session: entry is not reversible")
	// ErrUnknownCheckpoint is returned by Restore when the named checkpoint is
	// not in the task's history.
	ErrUnknownCheckpoint = errStr("session: unknown checkpoint")
	// ErrTaskNotCancellable is returned when Cancel is called on an already
	// terminal task.
	ErrTaskNotCancellable = errStr("session: task is not cancellable (already terminal)")
)

// errStr is a tiny helper so the error vars above read as values, not casts.
func errStr(s string) error { return &errString{s} }

type errString struct{ s string }

func (e *errString) Error() string { return e.s }
