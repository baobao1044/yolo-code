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

// TaskFailedEvent announces the third terminal outcome: the task stopped
// because something went wrong, not because it finished (task.completed) and
// not because anyone asked it to stop (task.cancelled).
//
// It was specified from the start — File 03 §3.2 draws RUNNING --> FAILED on
// "retries exhausted / hard error", §5.4.1 lists task.failed among the task
// lifecycle topics, docs/user/commands.md documents it to users, and
// memory/knowledge.go already names "task.failed" as a valid insight Source —
// but it was never added here, so nothing in the tree could emit it and the
// runtime's error path left tasks recorded as still running.
//
// Reason is the cause as the runtime saw it. It is the only field that
// distinguishes one failure from another to a log reader, which is why it is
// carried rather than left to the separate ErrorEvent: that one is published by
// every layer for every kind of trouble and says nothing about task lifecycle.
type TaskFailedEvent struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

func (e *TaskFailedEvent) Type() Topic      { return "task.failed" }
func (e *TaskFailedEvent) CausalID() TaskID { return TaskID(e.Task) }

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
	Task       TaskID `json:"task"`
	ApprovalID string `json:"approval_id"`
	Tool       string `json:"tool"`
	Summary    string `json:"summary"`
	Preview    string `json:"preview"`
	Risk       Risk   `json:"risk"`
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

// VerificationStageEvent is the per-stage advisory (File 09 §9.4.2): each
// completed stage publishes its pass/warn/fail so the TUI shows a green check
// / red cross per stage. Detail carries the one-line reason (the failed stage's
// diagnostic summary, the warning's list). Status is "pass"/"warn"/"fail"/
// "skip" — the same strings verify.Severity.String returns.
type VerificationStageEvent struct {
	Task   TaskID `json:"task"`
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func (e *VerificationStageEvent) Type() Topic      { return "verification.stage" }
func (e *VerificationStageEvent) CausalID() TaskID { return e.Task }

type ReflectionEvent struct {
	Task TaskID `json:"task"`
	Note string `json:"note"`
}

func (e *ReflectionEvent) Type() Topic      { return "reflection.note" }
func (e *ReflectionEvent) CausalID() TaskID { return e.Task }

// PatchFile is one file's contribution to a patch.applied summary: how many
// lines were added/removed and whether the file was newly created. The list is
// ordered (path-sorted) for deterministic transcripts (S5).
type PatchFile struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	New        bool   `json:"new"`
}

// PatchAppliedEvent announces a successful apply (File 05 §5.4.4, File 10
// §10.6): the diff summary — files touched, insertions, deletions — so the
// transcript/TUI can show what changed. The per-file Files list is
// path-sorted (deterministic); Insertions/Deletions are the totals. Diff is
// the rendered unified-diff string (Phase D) for the TUI diff viewer; it's
// omitempty so headless/stub consumers that don't read it stay byte-identical.
type PatchAppliedEvent struct {
	Task       TaskID          `json:"task"`
	Snapshot   json.RawMessage `json:"snapshot"`
	Files      []PatchFile     `json:"files"`
	Insertions int             `json:"insertions"`
	Deletions  int             `json:"deletions"`
	Diff       string          `json:"diff,omitempty"`
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
	PlanID    string   `json:"plan_id"`
	TodoID    string   `json:"todo_id"`
	Agent     string   `json:"agent"`
	Brief     string   `json:"brief"`
	Context   []string `json:"context"`
	Artifacts []string `json:"artifacts"`
}

func (e *TaskAssignEvent) Type() Topic      { return "coord.task.assign" }
func (e *TaskAssignEvent) CausalID() TaskID { return TaskID(e.PlanID) }

type PlanReadyEvent struct {
	PlanID string          `json:"plan_id"`
	Plan   json.RawMessage `json:"plan"`
}

func (e *PlanReadyEvent) Type() Topic      { return "coord.plan.ready" }
func (e *PlanReadyEvent) CausalID() TaskID { return TaskID(e.PlanID) }

// PlanDoneEvent signals the orchestrator has finished all todos (and, when a
// verifier is wired, the merge/re-verify step passed).
type PlanDoneEvent struct {
	PlanID string `json:"plan_id"`
	Done   bool   `json:"done"`
	// Canceled reports that the run ended because its context was canceled
	// (Ctrl-C, shutdown) rather than because the work failed. Done is false
	// either way; this is the flag that tells the two apart. A consumer that
	// only cares about success can keep reading Done alone.
	Canceled bool   `json:"canceled"`
	Merged   bool   `json:"merged"`
	Summary  string `json:"summary"`
}

func (e *PlanDoneEvent) Type() Topic      { return "coord.plan.done" }
func (e *PlanDoneEvent) CausalID() TaskID { return TaskID(e.PlanID) }

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

// --- Cost: degradation ladder + hard abort (File 07 §7.6.2/§7.6.3) ---

// CostDegradedEvent signals the auto-degradation ladder stepped down a rung:
// after MaxLoops reflection loops the Core disables reflection (only-verify
// mode); further failure autosubmits. The TUI's cost meter (File 14) renders
// the rung live.
type CostDegradedEvent struct {
	Task  TaskID `json:"task"`
	Stage string `json:"stage"` // "reflection_disabled" | "autosubmit"
}

func (e *CostDegradedEvent) Type() Topic      { return "cost.degraded" }
func (e *CostDegradedEvent) CausalID() TaskID { return e.Task }

// CostAbortEvent signals a hard cap (spend or time) was hit and the task
// aborts, surfacing to the user (File 07 §7.6.2).
type CostAbortEvent struct {
	Task   TaskID `json:"task"`
	Reason string `json:"reason"` // "spend cap" | "time cap"
}

func (e *CostAbortEvent) Type() Topic      { return "cost.abort" }
func (e *CostAbortEvent) CausalID() TaskID { return e.Task }

// CostIncurredEvent is published whenever a tool call consumes budget. This
// is the real accrual event used by the coord plan budget (Sprint 13).
type CostIncurredEvent struct {
	Task    TaskID  `json:"task"`
	Tool    string  `json:"tool"`
	Dollars float64 `json:"dollars"`
	Reason  string  `json:"reason"`
}

func (e *CostIncurredEvent) Type() Topic      { return "cost.incurred" }
func (e *CostIncurredEvent) CausalID() TaskID { return e.Task }

// --- L-scope: Scope (File 15) ---

// ScopeEnterEvent is published by the scope controller whenever it enters a new
// level (File 15 §15.x). Task is the causal id; Level and Reason carry the
// human-readable move. The struct lives in package event (not scope) so the
// durability catalog can reconstruct it on replay without an import cycle.
type ScopeEnterEvent struct {
	Task   string `json:"task"`
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

func (e *ScopeEnterEvent) Type() Topic      { return "scope.enter" }
func (e *ScopeEnterEvent) CausalID() TaskID { return TaskID(e.Task) }

// ScopeTransitionEvent is published when the controller applies a W3
// expansion/contraction: the from/to levels, the action, and the reason.
type ScopeTransitionEvent struct {
	Task      string `json:"task"`
	FromLevel string `json:"from_level"`
	ToLevel   string `json:"to_level"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
}

func (e *ScopeTransitionEvent) Type() Topic      { return "scope.transition" }
func (e *ScopeTransitionEvent) CausalID() TaskID { return TaskID(e.Task) }

// --- L-workflow: Dynamic Workflow (File 15 §15.15.2) ---

// WorkflowSelectedEvent is published by the workflow engine whenever it selects a
// workflow for a goal (File 15 §15.x). Task is the causal id (empty today; the
// runtime threads the real id when it owns the engine), Goal is the classified
// goal, and Workflow is the chosen workflow's Name() (e.g. "bugfix"). The struct
// lives in package event (not workflow) so the durability catalog can reconstruct
// it on replay without an import cycle — the same cycle-avoidance scope uses.
type WorkflowSelectedEvent struct {
	Task     string `json:"task"`
	Goal     string `json:"goal"`
	Workflow string `json:"workflow"`
}

func (e *WorkflowSelectedEvent) Type() Topic      { return "workflow.selected" }
func (e *WorkflowSelectedEvent) CausalID() TaskID { return TaskID(e.Task) }

// --- L5: Prompt Compiler (File 06 §6.6.3) ---

// TokenBudgetEvent reports what the Prompt Compiler's budget stage did to one
// compile: the window it worked against, the tokens the finished prompt
// occupies, and how many parts each group lost to trimming.
//
// It exists because its absence was load-bearing. Nothing anywhere recorded
// prompt size or trimming decisions, so two budget defects — the Preferences
// group being emitted but never counted, and allocate()'s reserve floor zeroing
// every group cap on small windows — ran unnoticed until an audit read the
// arithmetic. A model silently receiving none of the context it asked for is
// indistinguishable, from outside, from one that received all of it.
//
// Window is 0 on the unbudgeted path (no window configured); Used is measured
// either way, so a zero window never has to double as "not measured". Dropped
// is keyed by group name ("retrieved files", "retrieved chunks", "recalled
// preferences", "conversation turns") and omitted entirely when nothing was
// trimmed, which is the common case.
//
// The struct lives in package event rather than internal/prompt so the
// durability catalog can reconstruct it on replay without an import cycle —
// the same reason scope and workflow put theirs here.
type TokenBudgetEvent struct {
	Task    TaskID         `json:"task"`
	Window  int            `json:"window"`
	Used    int            `json:"used"`
	Dropped map[string]int `json:"dropped,omitempty"`
}

func (e *TokenBudgetEvent) Type() Topic      { return "prompt.budget" }
func (e *TokenBudgetEvent) CausalID() TaskID { return e.Task }

// --- L-memory: user preference (File 11 §11.5.2) ---

// UserPreferenceEvent is published when the agent (or user) records a
// preference to remember (e.g. "always use conventional commits"). Key is the
// preference name; Value the stored text. The memory listener subscribes and
// routes it to the Preference store (the one user-editable sub-store, §11.2).
type UserPreferenceEvent struct {
	Task  string `json:"task,omitempty"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (e *UserPreferenceEvent) Type() Topic      { return "user.preference" }
func (e *UserPreferenceEvent) CausalID() TaskID { return TaskID(e.Task) }

// --- L-user: slash commands (TUI interactive) ---

// UserCommandEvent carries a parsed slash command (e.g. /model, /provider) from
// the TUI to the driver, which performs the runtime action (swap provider) and
// responds with a CommandResponseEvent. The TUI intercepts /help, /clear,
// /theme locally (no event); /model, /provider, /status go through the bus so
// the driver can rebuild the cognitive Core. Command is the verb ("model",
// "provider", "status"); Args is the argument (model name, provider name, or
// empty for "list").
type UserCommandEvent struct {
	Command string `json:"command"`
	Args    string `json:"args,omitempty"`
}

func (e *UserCommandEvent) Type() Topic      { return "user.command" }
func (e *UserCommandEvent) CausalID() TaskID { return "" }

// CommandResponseEvent carries the driver's text response to a slash command
// (e.g. "model: gpt-4o", "provider: groq (llama-3.3-70b-versatile)"). The TUI
// folds it into the chat pane as a system-role message. Topic is
// "command.response" (NOT under "user." — this is a driver→TUI response, not a
// user action; the TUI subscribes to it separately so it doesn't loop back
// through the driver's "user.>" subscription).
type CommandResponseEvent struct {
	Text string `json:"text"`
}

func (e *CommandResponseEvent) Type() Topic      { return "command.response" }
func (e *CommandResponseEvent) CausalID() TaskID { return "" }
