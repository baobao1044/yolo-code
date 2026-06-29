// Runtime port interfaces (File 04 §4.6 + import matrix File 15 §15.15.2).
//
// The runtime may import session (concrete) and event, but the other layers
// (context, prompt, cognitive, exec, verify, patch, memory) are accessed via
// interfaces defined HERE, in the runtime package. This keeps the runtime
// buildable with only session+event present and lets a headless demo inject
// stubs. Each interface is the minimal surface the drive loop (§4.3) needs; the
// owning layer implements it and Sprint-3+ wires the real type.

package runtime

import (
	"context"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// ContextPackage is the assembled, budgeted context the Context Engine (File 06)
// produces and the Prompt Compiler consumes. Opaque to the runtime — it passes
// it through. Typed as any so the real context.ContextPackage struct (Sprint 2)
// flows without the runtime importing context.
type ContextPackage = any

// Prompt is the compiled prompt the Cognitive Core consumes. Opaque to runtime;
// any so the real prompt shape (Sprint 2+) flows through.
type Prompt = any

// ToolCall is a tool the Planner chose (File 07 §5.4.3 shape, collapsed).
type ToolCall struct {
	Tool   string
	Args   []byte // json.RawMessage
	Reason string
	Task   event.TaskID // causal task id, threaded by the runtime before dispatch
}

// Observation is a tool's result (File 08). Opaque payload; Files lists the
// paths the tool/patch touched so VERIFY knows what to check; Checkpoint is the
// checkpoint name a patch tool recorded, the runtime Restores on a verify
// failure. Tool carries the tool name so the runtime can feed results back
// to the cognitive core for multi-turn agent loops.
type Observation struct {
	FromPatch  bool
	Payload    []byte // json.RawMessage
	Files      []string
	Checkpoint string // set by a patch tool; the runtime rolls back here on fail
	Stdout     string
	Summary    string
	Tool       string // the tool name that produced this observation
}

// VerifyPolicy is the runtime's opaque view of cognitive.VerificationPolicy
// (the runtime may not import cognitive, File 15 §15.15.2; the composition root
// translates). It's passed to the Verifier so the engine runs only the stages
// the policy requires. Kept as a plain struct the adapter fills.
type VerifyPolicy struct {
	RequireAST       bool
	RequireFormat    bool
	RequireLint      bool
	RequireTypeCheck bool
	RequireBuild     bool
	RequireTests     bool
	LintLevel        string
	TestTimeout      time.Duration
}

// Verdict is the verification pipeline's result (File 09). Pass is false iff a
// stage failed; Stage names the failing stage; Severity is "pass"/"warn"/"fail";
// Reason is the one-line summary the runtime/Reflection reads. Hint is an
// optional short classifier the Scope Controller uses to decide whether to
// expand/contract the search scope (e.g. "missing_import" → widen to repo
// scope). Empty Hint means "no hint".
type Verdict struct {
	Pass     bool
	Stage    string
	Severity string
	Reason   string
	Hint     string
}

// ReflectionDecision is the Cognitive Core's answer to a verify failure (File 07
// §7.3.1): Replan (re-architect, →PLAN), Patch (propose a corrective patch,
// →PATCH), or Abort (give up, →CANCELLED). Exactly one of Replan/Patch/Abort.
type ReflectionDecision struct {
	Replan bool
	Patch  PatchOp
	Abort  bool
	Note   string
}

// PatchOp is a patch the engine applies (File 10). Task + Seq name the
// checkpoint and let the patch engine publish a causal snapshot; Path is the
// optional target file carried by the tool-call args when the body is raw
// SEARCH/REPLACE blocks.
type PatchOp struct {
	Task session.TaskID
	Seq  int
	Path string
	Body []byte
}

// PatchResult is what the Patch Engine returns (File 10 §10.6). Checkpoint is
// the human-readable checkpoint id ("patch_3") the runtime Restores on a verify
// failure; Snapshot is the opaque ref for the event.
type PatchResult struct {
	Accepted   bool
	Reason     string
	Checkpoint string
	Snapshot   []byte // json.RawMessage snapshot ref
}

// ContextBuilder builds a ContextPackage from a task + session (File 06).
type ContextBuilder interface {
	Build(ctx context.Context, req ContextRequest) (ContextPackage, error)
}

// ContextRequest is the input to ContextBuilder.Build.
type ContextRequest struct {
	Task    *session.Task
	Session *session.Session
}

// PromptCompiler turns a ContextPackage into a budgeted Prompt (File 06).
type PromptCompiler interface {
	Compile(pkg ContextPackage) Prompt
}

// CognitiveTurn is one Planner turn (File 07): either a final answer or a set
// of tool calls.
type CognitiveTurn struct {
	Final     bool
	Text      string
	ToolCalls []ToolCall
}

// CognitiveCore drives planning, reflection, and tool selection (File 07).
// Think is the Planner turn; HasMore decides VERIFY→PLAN vs VERIFY→DONE; Reflect
// is the verify-failure handoff (File 07 §7.3) — the runtime calls it when a
// Verdict fails and acts on the Replan/Patch/Abort decision. RecordToolResult
// feeds a tool's output back into the conversation so the next Think sees it.
type CognitiveCore interface {
	Think(ctx context.Context, msgs Prompt) (CognitiveTurn, error)
	HasMore(task *session.Task) bool
	Reflect(ctx context.Context, task *session.Task, v Verdict, obs Observation) ReflectionDecision
	RecordToolResult(toolName, result string)
}

// Executor dispatches tools under the sandbox (File 08).
type Executor interface {
	NeedsApproval(call ToolCall) bool
	Dispatch(ctx context.Context, call ToolCall) (Observation, error)
}

// Verifier runs the verification pipeline (File 09). The VerifyPolicy selects
// the required stages; the Observation carries the touched files; the task
// gives the causal id. The runtime calls this on the VERIFY state.
type Verifier interface {
	Verify(ctx context.Context, obs Observation, task *session.Task, pol VerifyPolicy) (Verdict, error)
}

// Patcher applies a patch (File 10).
type Patcher interface {
	Apply(ctx context.Context, op PatchOp) (PatchResult, error)
}

// Restorer rolls the task's tree back to a named checkpoint (File 10 §10.5.4):
// when VERIFY fails, the runtime Restores the patch checkpoint so the file is
// unchanged before Reflection proposes a corrective patch. The composition root
// wires this to session.Manager.Restore (the runtime imports session concretely
// but a seam keeps the port substitutable in tests).
type Restorer interface {
	Restore(ctx context.Context, tid session.TaskID, name string) error
}

// MemoryStore records learnings (File 11). Minimal for Sprint 1: a no-op.
type MemoryStore interface {
	Update(ctx context.Context, taskID session.TaskID) error
}

// --- Scope Loop Engineering ports (sibling package internal/scope) ---
//
// The runtime may not import internal/scope (import matrix File 15 §15.15.2),
// so the scope controller is a port here with runtime-local mirror types. The
// composition root (cmd/yolo) wires an adapter that translates between these
// and the real scope.* types. A nil Scope port means "no scope control" — the
// drive loop falls back to its pre-scope behaviour (see drive.go StateVerify).

// ScopeLevel mirrors scope.Level: the granularity of the current search scope.
// It is an int so the runtime can compare/transition without importing scope.
type ScopeLevel int

// ScopeVerdict is the minimal verdict shape the Scope Controller needs to
// suggest an expansion/contraction. It mirrors scope.Verdict (Pass/Stage/Hint).
type ScopeVerdict struct {
	Pass   bool
	Stage  string
	Hint   string
	Reason string
}

// ScopeAction is what the Scope Controller wants the runtime to do with the
// scope (stay / expand / contract). It mirrors scope.Action.
type ScopeAction int

const (
	ScopeActionNoOp     ScopeAction = iota // stay
	ScopeActionExpand                      // widen the search scope
	ScopeActionContract                    // narrow the search scope
	ScopeActionStay                        // stay in scope, repair
)

// ScopeTransition is a suggested scope move: the target level + the action +
// a human-readable reason. Mirrors scope.Transition.
type ScopeTransition struct {
	TargetLevel ScopeLevel
	Action      ScopeAction
	Reason      string
}

// ScopeController owns the per-task scope state machine (File: Scope Loop
// Engineering). The runtime consults it in the VERIFY arm to decide whether to
// widen or narrow the search scope on a failure, and gates tool access by the
// current level. All methods are best-effort and MUST be safe to call from the
// drive goroutine (the scope controller owns no goroutine).
type ScopeController interface {
	Current() ScopeLevel
	Enter(level ScopeLevel, reason string)
	Exit() ScopeLevel
	CanUseTool(tool string) bool
	SuggestTransition(v ScopeVerdict) ScopeTransition
	RecordFact(fact string)
	RecordFailedHypothesis(h string)
	RecordPatch(seq int, summary string, accepted bool)
}

// --- Dynamic Workflow ports (sibling package internal/workflow) ---
//
// The runtime may not import internal/workflow; the workflow engine is a port
// here with runtime-local mirror types. A nil Workflow port means "use the
// legacy fixed FSM flow" — the drive loop behaves exactly as before.

// WFPhase names a phase within a dynamic workflow (e.g. "LOCALIZE", "REPAIR").
type WFPhase string

// WFState is the runtime's view of a workflow's progress. It mirrors
// workflow.State (Phase + hypothesis/candidate counters).
type WFState struct {
	Phase      WFPhase
	Hypotheses []string
	Candidates int
	Retries    int
}

// WFEventKind names a workflow event the runtime emits to the engine (e.g. a
// verify pass/fail, a context-needed signal, a timeout).
type WFEventKind int

const (
	WFEventVerifyPass WFEventKind = iota
	WFEventVerifyFail
	WFEventContextNeeded
	WFEventTimeout
)

// WFEvent is a single workflow event handed to the engine.
type WFEvent struct {
	Kind    WFEventKind
	Payload string
}

// WFActionKind names a workflow action the engine returns for the runtime to
// perform (localize, generate a patch, run multi-hypothesis, verify, repair,
// contract scope, submit, degrade the model).
type WFActionKind int

const (
	WFActionLocalize WFActionKind = iota
	WFActionGenerate
	WFActionMultiHyp
	WFActionVerify
	WFActionRepair
	WFActionContract
	WFActionSubmit
	WFActionDegrade
)

// WFAction is a workflow action the engine wants the runtime to take.
type WFAction struct {
	Kind WFActionKind
	Note string
}

// WorkflowEngine selects and drives a dynamic workflow per task (File: Dynamic
// Workflow). The runtime calls Next in the PLAN/VERIFY arms to route based on
// the task type and recent feedback. A nil engine means "no dynamic workflow"
// — the legacy fixed FSM flow applies.
type WorkflowEngine interface {
	Next(goal string, state *WFState, ev WFEvent) (WFAction, error)
}

// Deps are the runtime's collaborators (File 04 §4.6). Bus and Session are
// required; the rest default to no-op stubs (wireDeps fills them) so a Sprint 1
// stubbed single-turn loop builds with only event+session present. Scope and
// Workflow are optional — a nil value disables scope control / dynamic
// workflow and the runtime falls back to its legacy fixed FSM flow.
type Deps struct {
	Bus       *event.Bus
	Session   *session.Manager
	Context   ContextBuilder
	Prompt    PromptCompiler
	Cognitive CognitiveCore
	Exec      Executor
	Verify    Verifier
	Patch     Patcher
	Restore   Restorer
	Memory    MemoryStore
	Scope     ScopeController // optional; nil → no scope control
	Workflow  WorkflowEngine  // optional; nil → legacy fixed FSM flow
}
