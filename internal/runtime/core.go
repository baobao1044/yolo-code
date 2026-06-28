// The runtime Core (File 04 §4.6) owns the scheduler and drives one task at a
// time through the FSM. Sprint 1 implements the drive loop (§4.3) for the
// direct-answer path (PLAN→DONE) against stub ports; the tool/verify/patch
// branches are scaffolded and filled by Sprints 4–6.
//
// Invariant I1 (File 04 §4.2.1): only the runtime goroutine mutates a task's
// state. The Core drives a task on a single goroutine (drive runs inline from
// Run for MVP); other layers communicate via events, never by touching state.

package runtime

import (
	"context"
	"sync"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
)

// Core is the runtime: it owns the scheduler and the per-task FSM handles,
// and drives the active task through its states.
type Core struct {
	bus     *event.Bus
	session *session.Manager

	ctxBldr ContextBuilder
	prompt  PromptCompiler
	cog     CognitiveCore
	exec    Executor
	verify  Verifier
	patch   Patcher
	memory  MemoryStore

	mu    sync.Mutex // guards tasks map (I1: state itself is single-writer)
	tasks map[session.TaskID]*taskHandle
}

// taskHandle is the per-task drive-loop state (File 04 §4.6). Only the runtime
// goroutine running drive() touches fsm/lastObs/pendingCalls; the mutex guards
// the map membership only.
type taskHandle struct {
	id        session.TaskID
	sessionID session.ID
	fsm       *fsm
	task      *session.Task
	pkg       ContextPackage
	lastObs   Observation
}

// New wires a Core from Deps, filling absent ports with no-op stubs so the
// Sprint 1 stubbed loop runs without the real layers.
func New(d Deps) *Core {
	c := &Core{
		bus:     d.Bus,
		session: d.Session,
		tasks:   make(map[session.TaskID]*taskHandle),
	}
	c.ctxBldr = orContext(d.Context, noopContextBuilder{})
	c.prompt = orPrompt(d.Prompt, noopPromptCompiler{})
	c.cog = orCognitive(d.Cognitive, StubCognitive{Answer: "hello"})
	c.exec = d.Exec
	c.verify = d.Verify
	c.patch = d.Patch
	c.memory = d.Memory
	return c
}

// Submit opens a session task and starts driving it. Sprint 1 drives inline
// (single task); the scheduler (File 04 §4.4) is added later.
func (c *Core) Submit(ctx context.Context, sid session.ID, goal string) (session.TaskID, error) {
	tid, err := c.session.StartTask(ctx, sid, goal)
	if err != nil {
		return "", err
	}
	sess, _, err := c.session.Resume(ctx, sid)
	if err != nil {
		return "", err
	}
	task := c.session.LoadTaskPublic(tid)
	h := &taskHandle{
		id:        tid,
		sessionID: sid,
		fsm:       newFSM(StateInit),
		task:      task,
	}
	c.mu.Lock()
	c.tasks[tid] = h
	c.mu.Unlock()
	c.drive(ctx, h, sess)
	return tid, nil
}

// drive walks the FSM for one task (File 04 §4.3). Each state delegates to the
// port that owns the work; every transition publishes state.change. The loop
// exits when the FSM reaches a terminal state (DONE/CANCELLED) or a no-edge
// signal (ErrNoTransition from a terminal state).
func (c *Core) drive(ctx context.Context, h *taskHandle, sess *session.Session) {
	for {
		// Cancellation: a canceled context unwinds the loop (File 04 §4.5).
		if err := ctx.Err(); err != nil {
			c.handleCancel(ctx, h)
			return
		}

		switch h.fsm.current() {

		case StateInit:
			from, to, err := h.fsm.transition(SigStartTask, "start")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "start")

		case StateLoadSession:
			// Resume already loaded the session+task into the handle; advance.
			from, to, err := h.fsm.transition(SigSessionLoaded, "session_loaded")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "session_loaded")

		case StateLoadContext:
			pkg, err := c.ctxBldr.Build(ctx, ContextRequest{Task: h.task, Session: sess})
			if err != nil {
				c.toError(ctx, h, err)
				return
			}
			h.pkg = pkg
			from, to, err := h.fsm.transition(SigContextBuilt, "context_built")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "context_built")

		case StatePlan:
			prompt := c.prompt.Compile(h.pkg)
			turn, err := c.cog.Think(ctx, prompt)
			if err != nil {
				c.toError(ctx, h, err)
				return
			}
			if turn.Final {
				from, to, err := h.fsm.transition(SigPlannerAnswer, "direct_answer")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "direct_answer")
				c.publishAssistant(ctx, h.id, turn.Text)
				if c.memory != nil {
					_ = c.memory.Update(ctx, h.id)
				}
				_ = c.session.CompleteTask(ctx, h.id)
				return // DONE is terminal
			}
			// Tool path is scaffolded for Sprints 4–6; the stub never takes it.
			from, to, err := h.fsm.transition(SigPlannerToolCall, "tool_call")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "tool_call")

		case StateDone, StateCancelled:
			// Terminal: the loop reaches here only if a transition landed on a
			// terminal state without an early return; stop.
			return

		default:
			// States requiring real layers (EXECUTE/WAIT_TOOL/VERIFY/PATCH/
			// WAIT_USER/PAUSED/ERROR) are not driven in Sprint 1's stubbed loop.
			// Hitting one means a stub was wired that advanced past PLAN; stop
			// rather than spin, and surface it as an error transition.
			c.toError(ctx, h, errUnimplementedState{state: h.fsm.current()})
			return
		}
	}
}

// publishTransition emits a state.change event for a (from, to, why) triple
// (L2-003, File 04 §4.2.2). Called on every transition so the TUI and Infra
// always know where the task is.
func (c *Core) publishTransition(ctx context.Context, tid session.TaskID, from, to State, why string) {
	_ = c.bus.Publish(ctx, &event.StateChangeEvent{
		Task: event.TaskID(tid), From: string(from), To: string(to), Why: why,
	})
}

// publishAssistant emits an assistant.message (the canned final answer).
func (c *Core) publishAssistant(ctx context.Context, tid session.TaskID, text string) {
	_ = c.bus.Publish(ctx, &event.AssistantMessageEvent{
		Task: event.TaskID(tid), Text: text, Final: true,
	})
}

// toError transitions the FSM to ERROR (T19) and publishes an error event.
func (c *Core) toError(ctx context.Context, h *taskHandle, cause error) {
	from, to, err := h.fsm.transition(SigHardError, "error")
	if err != nil {
		return
	}
	c.publishTransition(ctx, h.id, from, to, "error")
	_ = c.bus.Publish(ctx, &event.ErrorEvent{
		Task: event.TaskID(h.id), Layer: "runtime", Msg: cause.Error(),
	})
}

// handleCancel transitions the active task to CANCELLED (T18) via the Session
// Manager, which cascades the cancel and rolls back the checkpoint (File 04
// §4.5.3). Called when the drive context is canceled.
func (c *Core) handleCancel(ctx context.Context, h *taskHandle) {
	_ = c.session.Cancel(ctx, h.id, "context_canceled")
}

// errUnimplementedState is returned when the stubbed drive loop reaches a
// state that needs a real layer not yet wired.
type errUnimplementedState struct{ state State }

func (e errUnimplementedState) Error() string {
	return "runtime: state " + string(e.state) + " not driven in stubbed loop"
}

// orContext/orPrompt/orCognitive return the provided port or a default stub.
// Kept as small helpers so New reads cleanly.
func orContext(p ContextBuilder, d ContextBuilder) ContextBuilder {
	if p != nil {
		return p
	}
	return d
}
func orPrompt(p PromptCompiler, d PromptCompiler) PromptCompiler {
	if p != nil {
		return p
	}
	return d
}
func orCognitive(p CognitiveCore, d CognitiveCore) CognitiveCore {
	if p != nil {
		return p
	}
	return d
}
