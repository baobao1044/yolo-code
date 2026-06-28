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

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
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
}

// Observation is a tool's result (File 08). Opaque payload; Files lists the
// paths the tool/patch touched so VERIFY knows what to check; Checkpoint is the
// checkpoint name a patch tool recorded, the runtime Restores on a verify
// failure.
type Observation struct {
	FromPatch  bool
	Payload    []byte // json.RawMessage
	Files      []string
	Checkpoint string // set by a patch tool; the runtime rolls back here on fail
	Stdout     string
	Summary    string
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
// Reason is the one-line summary the runtime/Reflection reads.
type Verdict struct {
	Pass     bool
	Stage    string
	Severity string
	Reason   string
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

// PatchOp is a patch the engine applies (File 10).
type PatchOp struct {
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
// Verdict fails and acts on the Replan/Patch/Abort decision.
type CognitiveCore interface {
	Think(ctx context.Context, msgs Prompt) (CognitiveTurn, error)
	HasMore(task *session.Task) bool
	Reflect(ctx context.Context, task *session.Task, v Verdict, obs Observation) ReflectionDecision
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

// Deps are the runtime's collaborators (File 04 §4.6). Bus and Session are
// required; the rest default to no-op stubs (wireDeps fills them) so a Sprint 1
// stubbed single-turn loop builds with only event+session present.
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
}
