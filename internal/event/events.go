// Package event — the event catalog (L3-006, File 05 §5.4).
//
// Every event the bus can carry, grouped by publisher. Each struct implements
// Event (Type + CausalID) and is JSON-marshalable. Fields that reference
// cross-layer domain objects (snapshots, observations, plans, attachments)
// are json.RawMessage: the bus carries them as opaque payloads, and the
// owning layer owns their schema. This keeps internal/event a pure wire
// contract that imports no other layer (File 02 §2.2, File 15 §15.15.2).
//
// Versioning (§5.4.10): every envelope carries "v":1. Within v1, additive
// optional fields are allowed; removing or renaming a field is a breaking
// change requiring a log-reader migration.

package event

import "encoding/json"

// Risk is the risk classification of an action the runtime surfaces for HITL.
// A string alias so the wire contract stays in event; L7/L12 refine its values.
type Risk string

// --- L1: Session & task (File 03, §5.4.1) ---

type TaskStartedEvent struct {
	Task    string `json:"task"`
	Session string `json:"session"`
	Goal    string `json:"goal"`
}

func (e *TaskStartedEvent) Type() Topic      { return "task.started" }
func (e *TaskStartedEvent) CausalID() TaskID { return TaskID(e.Task) }

type TaskCompletedEvent struct {
	Task string `json:"task"`
}

func (e *TaskCompletedEvent) Type() Topic      { return "task.completed" }
func (e *TaskCompletedEvent) CausalID() TaskID { return TaskID(e.Task) }

type TaskCancelledEvent struct {
	Task    string `json:"task"`
	Reason  string `json:"reason"`
	Partial string `json:"partial"`
}

func (e *TaskCancelledEvent) Type() Topic      { return "task.cancelled" }
func (e *TaskCancelledEvent) CausalID() TaskID { return TaskID(e.Task) }

type TaskPausedEvent struct {
	Task string `json:"task"`
}

func (e *TaskPausedEvent) Type() Topic      { return "task.paused" }
func (e *TaskPausedEvent) CausalID() TaskID { return TaskID(e.Task) }

type CheckpointEvent struct {
	Task     string          `json:"task"`
	Name     string          `json:"name"`
	Snapshot json.RawMessage `json:"snapshot"`
}

func (e *CheckpointEvent) Type() Topic      { return "task.checkpoint" }
func (e *CheckpointEvent) CausalID() TaskID { return TaskID(e.Task) }

type RestoredEvent struct {
	Task string `json:"task"`
	Name string `json:"name"`
}

func (e *RestoredEvent) Type() Topic      { return "task.restored" }
func (e *RestoredEvent) CausalID() TaskID { return TaskID(e.Task) }

type UndoneEvent struct {
	Task  string          `json:"task"`
	Entry json.RawMessage `json:"entry"`
}

func (e *UndoneEvent) Type() Topic      { return "task.undone" }
func (e *UndoneEvent) CausalID() TaskID { return TaskID(e.Task) }

// --- L2: Runtime FSM (File 04, §5.4.2) ---

type StateChangeEvent struct {
	Task TaskID `json:"task"`
	From string `json:"from"`
	To   string `json:"to"`
	Why  string `json:"why"`
}

func (e *StateChangeEvent) Type() Topic      { return "state.change" }
func (e *StateChangeEvent) CausalID() TaskID { return e.Task }

type ContextBuiltEvent struct {
	Task TaskID `json:"task"`
}

func (e *ContextBuiltEvent) Type() Topic      { return "context.built" }
func (e *ContextBuiltEvent) CausalID() TaskID { return e.Task }

type ApprovalRequestEvent struct {
	Task    TaskID `json:"task"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	Preview string `json:"preview"`
	Risk    Risk   `json:"risk"`
}

func (e *ApprovalRequestEvent) Type() Topic      { return "approval.request" }
func (e *ApprovalRequestEvent) CausalID() TaskID { return e.Task }

type ObservationEvent struct {
	Task TaskID          `json:"task"`
	Tool string          `json:"tool"`
	Obs  json.RawMessage `json:"obs"`
}

func (e *ObservationEvent) Type() Topic      { return "observation.received" }
func (e *ObservationEvent) CausalID() TaskID { return e.Task }

type VerificationFailedEvent struct {
	Task   TaskID `json:"task"`
	Reason string `json:"reason"`
}

func (e *VerificationFailedEvent) Type() Topic      { return "verification.failed" }
func (e *VerificationFailedEvent) CausalID() TaskID { return e.Task }

type ReflectionEvent struct {
	Task TaskID `json:"task"`
	Note string `json:"note"`
}

func (e *ReflectionEvent) Type() Topic      { return "reflection.note" }
func (e *ReflectionEvent) CausalID() TaskID { return e.Task }

type PatchAppliedEvent struct {
	Task     TaskID          `json:"task"`
	Snapshot json.RawMessage `json:"snapshot"`
}

func (e *PatchAppliedEvent) Type() Topic      { return "patch.applied" }
func (e *PatchAppliedEvent) CausalID() TaskID { return e.Task }

// --- L6: Cognitive (File 07, §5.4.3) ---

type TokenEvent struct {
	Task  TaskID `json:"task"`
	Delta string `json:"delta"`
}

func (e *TokenEvent) Type() Topic      { return "llm.token" }
func (e *TokenEvent) CausalID() TaskID { return e.Task }

type ThinkingEvent struct {
	Task  TaskID `json:"task"`
	Delta string `json:"delta"`
}

func (e *ThinkingEvent) Type() Topic      { return "llm.thinking" }
func (e *ThinkingEvent) CausalID() TaskID { return e.Task }

type AssistantMessageEvent struct {
	Task  TaskID `json:"task"`
	Text  string `json:"text"`
	Final bool   `json:"final"`
}

func (e *AssistantMessageEvent) Type() Topic      { return "assistant.message" }
func (e *AssistantMessageEvent) CausalID() TaskID { return e.Task }

type ToolCallEvent struct {
	Task   TaskID          `json:"task"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
	Reason string          `json:"reason"`
}

func (e *ToolCallEvent) Type() Topic      { return "tool.call" }
func (e *ToolCallEvent) CausalID() TaskID { return e.Task }

// --- L7: Execution (File 08, §5.4.4) ---

type ToolResultEvent struct {
	Task TaskID          `json:"task"`
	Tool string          `json:"tool"`
	Obs  json.RawMessage `json:"obs"`
}

func (e *ToolResultEvent) Type() Topic      { return "tool.result" }
func (e *ToolResultEvent) CausalID() TaskID { return e.Task }

// --- L10: Memory (File 11, §5.4.5) ---

type MemoryUpdateEvent struct {
	Task  TaskID `json:"task"`
	Store string `json:"store"`
	Items int    `json:"items"`
}

func (e *MemoryUpdateEvent) Type() Topic      { return "memory.update" }
func (e *MemoryUpdateEvent) CausalID() TaskID { return e.Task }

// --- User: published by TUI, subscribed by L2 (§5.4.6) ---

type UserSubmitEvent struct {
	Text        string            `json:"text"`
	Attachments []json.RawMessage `json:"attachments"`
}

func (e *UserSubmitEvent) Type() Topic      { return "user.submit" }
func (e *UserSubmitEvent) CausalID() TaskID { return "" }

type UserCancelEvent struct {
	Task TaskID `json:"task"`
}

func (e *UserCancelEvent) Type() Topic      { return "user.cancel" }
func (e *UserCancelEvent) CausalID() TaskID { return e.Task }

type UserApproveEvent struct {
	Task       string `json:"task"`
	ApprovalID string `json:"approval_id"`
}

func (e *UserApproveEvent) Type() Topic      { return "user.approve" }
func (e *UserApproveEvent) CausalID() TaskID { return TaskID(e.Task) }

type UserRejectEvent struct {
	Task       string `json:"task"`
	ApprovalID string `json:"approval_id"`
	Reason     string `json:"reason"`
}

func (e *UserRejectEvent) Type() Topic      { return "user.reject" }
func (e *UserRejectEvent) CausalID() TaskID { return TaskID(e.Task) }

type UserPauseEvent struct {
	Task TaskID `json:"task"`
}

func (e *UserPauseEvent) Type() Topic      { return "user.pause" }
func (e *UserPauseEvent) CausalID() TaskID { return e.Task }

type UserResumeEvent struct {
	Task TaskID `json:"task"`
}

func (e *UserResumeEvent) Type() Topic      { return "user.resume" }
func (e *UserResumeEvent) CausalID() TaskID { return e.Task }

type UserQuitEvent struct{}

func (e *UserQuitEvent) Type() Topic      { return "user.quit" }
func (e *UserQuitEvent) CausalID() TaskID { return "" }

// --- L11: Coordination (File 12, §5.4.7) ---

type TaskAssignEvent struct {
	PlanID  string   `json:"plan_id"`
	TodoID  string   `json:"todo_id"`
	Agent   string   `json:"agent"`
	Brief   string   `json:"brief"`
	Context []string `json:"context"`
}

func (e *TaskAssignEvent) Type() Topic      { return "coord.task.assign" }
func (e *TaskAssignEvent) CausalID() TaskID { return TaskID(e.PlanID) }

type PlanReadyEvent struct {
	PlanID string          `json:"plan_id"`
	Plan   json.RawMessage `json:"plan"`
}

func (e *PlanReadyEvent) Type() Topic      { return "coord.plan.ready" }
func (e *PlanReadyEvent) CausalID() TaskID { return TaskID(e.PlanID) }

type CodeReadyEvent struct {
	PlanID     string `json:"plan_id"`
	TodoID     string `json:"todo_id"`
	Diff       string `json:"diff"`
	SelfReport string `json:"self_report"`
}

func (e *CodeReadyEvent) Type() Topic      { return "coord.code.ready" }
func (e *CodeReadyEvent) CausalID() TaskID { return TaskID(e.PlanID) }

type ReviewVerdictEvent struct {
	PlanID   string   `json:"plan_id"`
	TodoID   string   `json:"todo_id"`
	Approved bool     `json:"approved"`
	Comments []string `json:"comments"`
}

func (e *ReviewVerdictEvent) Type() Topic      { return "coord.review.verdict" }
func (e *ReviewVerdictEvent) CausalID() TaskID { return TaskID(e.PlanID) }

type TestReportEvent struct {
	PlanID string `json:"plan_id"`
	TodoID string `json:"todo_id"`
	Passed bool   `json:"passed"`
	Output string `json:"output"`
}

func (e *TestReportEvent) Type() Topic      { return "coord.test.report" }
func (e *TestReportEvent) CausalID() TaskID { return TaskID(e.PlanID) }

// --- Error: any layer (§5.4.8) ---

type ErrorEvent struct {
	Task  TaskID `json:"task"`
	Layer string `json:"layer"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
	Retry bool   `json:"retry"`
}

func (e *ErrorEvent) Type() Topic      { return "error" }
func (e *ErrorEvent) CausalID() TaskID { return e.Task }
