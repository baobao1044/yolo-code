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

// Observation is a tool's result (File 08). Opaque payload.
type Observation struct {
	FromPatch bool
	Payload   []byte // json.RawMessage
}

// Verdict is the verification pipeline's result (File 09).
type Verdict struct {
	Pass   bool
	Reason string
}

// PatchOp is a patch the engine applies (File 10).
type PatchOp struct {
	Body []byte
}

// PatchResult is what the Patch Engine returns (File 10 §10.6).
type PatchResult struct {
	Accepted bool
	Reason   string
	Snapshot []byte // json.RawMessage snapshot ref
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
type CognitiveCore interface {
	Think(ctx context.Context, msgs Prompt) (CognitiveTurn, error)
	HasMore(task *session.Task) bool
}

// Executor dispatches tools under the sandbox (File 08).
type Executor interface {
	NeedsApproval(call ToolCall) bool
	Dispatch(ctx context.Context, call ToolCall) (Observation, error)
}

// Verifier runs the verification pipeline (File 09).
type Verifier interface {
	Verify(ctx context.Context, obs Observation, task *session.Task) (Verdict, error)
}

// Patcher applies a patch (File 10).
type Patcher interface {
	Apply(ctx context.Context, op PatchOp) (PatchResult, error)
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
	Memory    MemoryStore
}
