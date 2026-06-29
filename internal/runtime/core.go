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
	"encoding/json"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// Core is the runtime: it owns the scheduler and the per-task FSM handles,
// and drives the active task through its states.
type Core struct {
	bus     *event.Bus
	session *session.Manager

	ctxBldr  ContextBuilder
	prompt   PromptCompiler
	cog      CognitiveCore
	exec     Executor
	verify   Verifier
	patch    Patcher
	restore  Restorer
	memory   MemoryStore
	scope    ScopeController // nil → noopScopeController (no scope control)
	workflow WorkflowEngine  // nil → noopWorkflowEngine (legacy fixed FSM)

	mu    sync.Mutex // guards tasks map (I1: state itself is single-writer)
	tasks map[session.TaskID]*taskHandle
}

// taskHandle is the per-task drive-loop state (File 04 §4.6). Only the runtime
// goroutine running drive() touches fsm/lastObs/pendingCalls; the mutex guards
// the map membership only.
type taskHandle struct {
	id          session.TaskID
	sessionID   session.ID
	fsm         *fsm
	task        *session.Task
	pkg         ContextPackage
	lastObs     Observation
	lastVerdict Verdict            // last verify result; consulted by the Scope Controller
	ckptName    string             // the patch checkpoint to Restore on a verify failure
	retries     int                // PATCH→VERIFY→fail cycles; capped to stop a spin
	pending     []ToolCall         // tool calls the Planner emitted this turn, awaiting EXECUTE
	approved    bool               // true when the head pending call has been user-approved
	events      chan userCmd       // user-driven events (approve/reject/pause/resume/cancel)
	ctx         context.Context    // task-scoped; cancel cascades into drive + ports
	cancel      context.CancelFunc // attached to the Session Manager
}

// userCmd is a user action routed from the UI into the runtime's FSM.
type userCmd struct {
	kind   string
	taskID session.TaskID
}

// New wires a Core from Deps, filling absent ports with no-op stubs so the
// Sprint 1 stubbed loop runs without the real layers.
func New(d Deps) *Core {
	c := &Core{
		bus:     d.Bus,
		session: d.Session,
		tasks:   make(map[session.TaskID]*taskHandle),
	}
	if c.bus != nil {
		go c.userEventLoop()
	}
	c.ctxBldr = orContext(d.Context, noopContextBuilder{})
	c.prompt = orPrompt(d.Prompt, noopPromptCompiler{})
	c.cog = orCognitive(d.Cognitive, StubCognitive{Answer: "hello"})
	c.exec = orExec(d.Exec)
	c.verify = orVerify(d.Verify)
	c.patch = orPatch(d.Patch)
	c.restore = orRestore(d.Restore)
	c.memory = d.Memory
	c.scope = &noopScopeController{}
	if d.Scope != nil {
		c.scope = d.Scope
	}
	c.workflow = &noopWorkflowEngine{}
	if d.Workflow != nil {
		c.workflow = d.Workflow
	}
	return c
}

// Submit opens a session task and starts driving it. Sprint 1 drives inline
// (single task); the scheduler (File 04 §4.4) is added later.
//
// The task runs under a child context derived from ctx: canceling ctx (or the
// child) cascades into the drive loop and every port call (L2-004). The child's
// CancelFunc is attached to the Session Manager so its Cancel (user cancel,
// File 04 §4.5) can cascade too.
func (c *Core) Submit(ctx context.Context, sid session.ID, goal string) (session.TaskID, error) {
	// StartTask uses the parent ctx so a not-yet-canceled task always allocates;
	// the task's own context (below) governs the drive loop + ports.
	tid, err := c.session.StartTask(ctx, sid, goal)
	if err != nil {
		return "", err
	}
	sess, _, err := c.session.Resume(ctx, sid)
	if err != nil {
		return "", err
	}
	task := c.session.LoadTaskPublic(tid)

	taskCtx, taskCancel := context.WithCancel(ctx)
	c.session.AttachCancel(tid, taskCancel)

	h := &taskHandle{
		id:        tid,
		sessionID: sid,
		fsm:       newFSM(StateInit),
		task:      task,
		events:    make(chan userCmd, 16),
		ctx:       taskCtx,
		cancel:    taskCancel,
	}
	c.mu.Lock()
	c.tasks[tid] = h
	c.mu.Unlock()
	c.drive(taskCtx, h, sess)
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
			c.handleCancel(h)
			return
		}

		// Drain any user-driven command (approval/pause/resume/cancel) before
		// handling the next state. WAIT_USER and PAUSED block on this channel
		// instead of spinning.
		c.drainUserEvents(ctx, h)

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
			// Dynamic Workflow: consult the workflow engine for the routing
			// decision given the task goal + the last feedback event (File:
			// Dynamic Workflow). The engine's action is advisory — it does not
			// override the FSM, so it is NOT published as a state.change (that
			// would desync event counts); it primes the workflow state for any
			// future routing seam. When no engine is wired, the noop engine
			// returns Submit and this block is a no-op.
			if c.workflow != nil {
				wfState := &WFState{Phase: WFPhase("PLAN"), Retries: h.retries}
				wfEvent := WFEvent{Kind: WFEventVerifyFail}
				if h.lastVerdict.Pass {
					wfEvent.Kind = WFEventVerifyPass
				}
				_, _ = c.workflow.Next(h.task.Goal, wfState, wfEvent)
			}
			prompt := c.prompt.Compile(h.pkg)
			turn, err := c.cog.Think(ctx, prompt)
			if err != nil {
				// A cancel that reached the cognitive core surfaces as
				// ctx.Err(); treat it as cancellation, not a hard error.
				if ctx.Err() != nil {
					c.handleCancel(h)
					return
				}
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
			// Tool path: stash the turn's tool calls for EXECUTE to dispatch, then
			// advance (T5). One turn may emit several; EXECUTE drains them.
			h.pending = turn.ToolCalls
			from, to, err := h.fsm.transition(SigPlannerToolCall, "tool_call")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "tool_call")

		case StateExecute:
			// Dispatch the next pending tool call (T6/T7). If none remain this turn,
			// the Planner is done — back to PLAN for the next turn (T21).
			if len(h.pending) == 0 {
				from, to, err := h.fsm.transition(SigTurnDone, "turn_done")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "turn_done")
				continue
			}
			call := h.pending[0]
			call.Task = event.TaskID(h.id)
			// Scope-gated tool access (File: Scope Loop Engineering, W2): when a
			// scope controller is wired AND it disallows the tool at the current
			// level, try to BROADEN the scope to a level that permits it before
			// dispatching (a tool call the Planner emitted is a signal the work
			// must happen; we don't silently drop it, which would loop forever).
			// The noop controller allows every tool, so this is a no-op there.
			if c.scope != nil && !c.scope.CanUseTool(call.Tool) {
				c.scope.Enter(scopeLevelForTool(call.Tool), "tool requires broader scope")
			}
			if c.exec.NeedsApproval(call) && !h.approved {
				// Wait for human approval before dispatching this call.
				from, to, err := h.fsm.transition(SigNeedsApproval, "approval")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "approval")
				continue
			}
			// Either no approval needed or the user already approved. Reset the
			// flag after dispatching so the next tool is re-evaluated.
			h.approved = false
			obs, err := c.exec.Dispatch(ctx, call)
			if err != nil {
				if ctx.Err() != nil {
					c.handleCancel(h)
					return
				}
				c.toError(ctx, h, err)
				return
			}
			h.lastObs = obs
			h.pending = h.pending[1:]
			from, to, err := h.fsm.transition(SigDispatched, "dispatched")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "dispatched")

		case StateWaitTool:
			// The observation is in (T10) → drive VERIFY. If more pending tools
			// remain this turn, the verify still runs once per turn (the patch's
			// effect is what's checked); a multi-tool turn dispatches the rest in
			// the next EXECUTE.
			// Feed the tool result into the cognitive core's conversation history
			// so the next Think() sees it (multi-turn agent loop).
			c.cog.RecordToolResult(h.lastObs.Tool, h.lastObs.Stdout)
			c.publishObservation(ctx, h.id, h.lastObs)
			from, to, err := h.fsm.transition(SigObservation, "observation")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "observation")

		case StateVerify:
			// Run the pipeline (T11/T12/T13/T14). A fail rolls the patch back and
			// hands the verdict to Reflection (File 07 §7.3); the decision routes
			// to PLAN (replan), PATCH (corrective), or CANCELLED (abort).
			verdict, err := c.verify.Verify(ctx, h.lastObs, h.task, c.policyFor(h.task))
			if err != nil {
				c.toError(ctx, h, err)
				return
			}
			h.lastVerdict = verdict
			// Scope Loop Engineering: consult the scope controller to widen or
			// narrow the search scope based on this verdict (File: Scope Loop
			// Engineering, W3). On a fail, the controller may suggest expanding
			// (e.g. a missing import → repo scope) or contracting (re-examine a
			// narrower scope). When a wired controller suggests a move, the
			// runtime records it so subsequent PLAN turns see the adjusted scope;
			// the noop stub returns NoOp and this is a no-op. A pass records a
			// confirmed fact so the scope memory remembers what worked.
			if c.scope != nil {
				if verdict.Pass {
					c.scope.RecordFact("verify passed at " + verdict.Stage)
				} else {
					c.scope.RecordFailedHypothesis(verdict.Reason)
				}
				tr := c.scope.SuggestTransition(scopeVerdictFrom(verdict))
				if tr.Action != ScopeActionNoOp && tr.Action != ScopeActionStay {
					c.scope.Enter(tr.TargetLevel, tr.Reason)
				}
			}
			if !verictPass(verdict) {
				c.publishVerificationFailed(ctx, h.id, verdict)
				// Roll back the patch's checkpoint so the file is unchanged before
				// Reflection proposes a corrective patch (File 10 §10.5.4).
				if h.ckptName == "" {
					h.ckptName = h.lastObs.Checkpoint
				}
				if h.ckptName != "" {
					_ = c.restore.Restore(ctx, h.id, h.ckptName)
					_ = c.bus.Publish(ctx, &event.RestoredEvent{Task: string(h.id), Name: h.ckptName})
				}
				dec := c.cog.Reflect(ctx, h.task, verdict, h.lastObs)
				if dec.Abort {
					from, to, err := h.fsm.transition(SigUserCancel, "reflection_abort")
					if err == nil {
						c.publishTransition(ctx, h.id, from, to, "reflection_abort")
					}
					_ = c.session.Cancel(ctx, h.id, "reflection aborted")
					return
				}
				if dec.Replan {
					from, to, err := h.fsm.transition(SigVerifyFailReplan, "verify_fail_replan")
					if err != nil {
						return
					}
					c.publishTransition(ctx, h.id, from, to, "verify_fail_replan")
					continue
				}
				// Patch: store the corrective patch and drive PATCH (T13).
				h.pending = []ToolCall{patchToolCall(dec.Patch)}
				from, to, err := h.fsm.transition(SigVerifyFailPatch, "verify_fail_patch")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "verify_fail_patch")
				continue
			}
			// Pass: more to do → PLAN (T11); else DONE (T12).
			if c.cog.HasMore(h.task) {
				from, to, err := h.fsm.transition(SigVerifyPassMore, "verify_pass_more")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "verify_pass_more")
				continue
			}
			from, to, err := h.fsm.transition(SigVerifyPassDone, "verify_pass_done")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "verify_pass_done")
			c.publishAssistant(ctx, h.id, "verified")
			_ = c.session.CompleteTask(ctx, h.id)

		case StatePatch:
			// Apply the pending corrective patch (T15 → VERIFY). The patch's
			// checkpoint name is recorded for the next verify failure to Restore.
			if len(h.pending) == 0 {
				from, to, err := h.fsm.transition(SigVerifyFailReplan, "no_patch_replan")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "no_patch_replan")
				continue
			}
			h.retries++
			if h.retries > maxVerifyRetries {
				from, to, err := h.fsm.transition(SigUserCancel, "retry_cap")
				if err == nil {
					c.publishTransition(ctx, h.id, from, to, "retry_cap")
				}
				_ = c.session.Cancel(ctx, h.id, "verify retry cap reached")
				return
			}
			op := patchOpFromCall(h.pending[0])
			op.Task = h.id
			op.Seq = h.retries
			res, err := c.patch.Apply(ctx, op)
			if err != nil {
				c.toError(ctx, h, err)
				return
			}
			if !res.Accepted {
				from, to, err := h.fsm.transition(SigVerifyFailReplan, "patch_rejected")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "patch_rejected")
				continue
			}
			h.ckptName = res.Checkpoint
			h.lastObs = Observation{FromPatch: true, Files: filesFromSnapshot(res.Snapshot)}
			h.pending = nil
			from, to, err := h.fsm.transition(SigPatchApplied, "patch_applied")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "patch_applied")

		case StateDone, StateCancelled:
			// Terminal: the loop reaches here only if a transition landed on a
			// terminal state without an early return; stop.
			return

		case StateWaitUser, StatePaused:
			// Block until the user issues a command. If the command is not valid
			// for this state (e.g. pause while paused), it is dropped and we
			// remain blocked.
			cmd, ok := <-h.events
			if !ok {
				return
			}
			c.applyUserCmd(ctx, h, cmd)

		default:
			// States requiring real layers (EXECUTE/WAIT_TOOL/VERIFY/PATCH/
			// ERROR) are not driven in Sprint 1's stubbed loop. Hitting one
			// means a stub was wired that advanced past PLAN; stop rather than
			// spin, and surface it as an error transition.
			c.toError(ctx, h, errUnimplementedState{state: h.fsm.current()})
			return
		}
	}
}

// drainUserEvents consumes any queued user commands for this task from the
// event channel. It is non-blocking so normal states only react when a
// command has already arrived.
func (c *Core) drainUserEvents(ctx context.Context, h *taskHandle) {
	select {
	case cmd := <-h.events:
		c.applyUserCmd(ctx, h, cmd)
	default:
	}
}

// applyUserCmd translates a user command into the matching FSM signal.
func (c *Core) applyUserCmd(ctx context.Context, h *taskHandle, cmd userCmd) {
	if cmd.taskID != "" && cmd.taskID != h.id {
		return
	}
	sig := Signal("")
	switch cmd.kind {
	case "approve":
		sig = SigUserApprove
	case "reject":
		sig = SigUserReject
	case "pause":
		sig = SigUserPause
	case "resume":
		sig = SigUserResume
	case "cancel":
		sig = SigUserCancel
	default:
		return
	}
	from, to, err := h.fsm.transition(sig, cmd.kind)
	if err != nil {
		// Command doesn't apply to current state (e.g. approve when not in
		// WAIT_USER). Drop it.
		return
	}
	// Use a background context for the transition event so that cancelling the
	// task context doesn't race with fan-out to state.change subscribers.
	pubCtx := context.Background()
	c.publishTransition(pubCtx, h.id, from, to, cmd.kind)
	if sig == SigUserApprove {
		h.approved = true
	}
	if sig == SigUserReject || sig == SigUserCancel {
		_ = c.session.Cancel(pubCtx, h.id, "user")
	}
}

// userEventLoop subscribes to user.* events on the bus and routes them to the
// active task's command channel. It runs for the lifetime of the Core.
func (c *Core) userEventLoop() {
	ch := c.bus.Subscribe(
		event.Topic("user.approve"),
		event.Topic("user.reject"),
		event.Topic("user.pause"),
		event.Topic("user.resume"),
		event.Topic("user.cancel"),
	)
	for env := range ch {
		switch e := env.Evt.(type) {
		case *event.UserApproveEvent:
			tid := session.TaskID(e.Task)
			c.sendUserCmd(tid, userCmd{kind: "approve", taskID: tid})
		case *event.UserRejectEvent:
			tid := session.TaskID(e.Task)
			c.sendUserCmd(tid, userCmd{kind: "reject", taskID: tid})
		case *event.UserPauseEvent:
			tid := session.TaskID(string(e.Task))
			c.sendUserCmd(tid, userCmd{kind: "pause", taskID: tid})
		case *event.UserResumeEvent:
			tid := session.TaskID(string(e.Task))
			c.sendUserCmd(tid, userCmd{kind: "resume", taskID: tid})
		case *event.UserCancelEvent:
			tid := session.TaskID(string(e.Task))
			// Cancel immediately cascades context cancellation so long-running
			// tools stop, even if the drive loop is blocked in a port call.
			_ = c.session.Cancel(context.Background(), tid, "user")
			c.sendUserCmd(tid, userCmd{kind: "cancel", taskID: tid})
		}
	}
}

// sendUserCmd delivers a user command to a task's channel if it exists.
func (c *Core) sendUserCmd(tid session.TaskID, cmd userCmd) {
	c.mu.Lock()
	h, ok := c.tasks[tid]
	c.mu.Unlock()
	if !ok || h.events == nil {
		return
	}
	select {
	case h.events <- cmd:
	default:
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

// publishObservation emits observation.received so the trace shows the tool's
// result before VERIFY inspects it.
func (c *Core) publishObservation(ctx context.Context, tid session.TaskID, obs Observation) {
	_ = c.bus.Publish(ctx, &event.ObservationEvent{
		Task: event.TaskID(tid),
		Obs:  mustMarshalRuntimeObs(obs),
	})
}

// publishVerificationFailed emits verification.failed with the failing stage's
// reason (File 09 §9.4.2). Surfaces the verify failure to the TUI/Reflection.
func (c *Core) publishVerificationFailed(ctx context.Context, tid session.TaskID, v Verdict) {
	_ = c.bus.Publish(ctx, &event.VerificationFailedEvent{
		Task:   event.TaskID(tid),
		Reason: v.Reason,
	})
}

// policyFor picks the verification policy for a task. Sprint 6 uses a single
// full policy (the runtime can't import cognitive; the composition root wires a
// per-task selector later, File 07 §7.5.2). Kept as a method so a future
// adapter can override it.
func (c *Core) policyFor(_ *session.Task) VerifyPolicy {
	return fullVerifyPolicy()
}

// fullVerifyPolicy is the §7.5.2 default: all stages except tests, lint at
// error, 30s test timeout. A real per-task selector (cognitive layer) replaces
// this; Sprint 6 wires a single policy so VERIFY runs a meaningful pipeline.
func fullVerifyPolicy() VerifyPolicy {
	return VerifyPolicy{
		RequireAST:       true,
		RequireFormat:    true,
		RequireLint:      true,
		RequireTypeCheck: true,
		RequireBuild:     true,
		RequireTests:     true,
		LintLevel:        "error",
		TestTimeout:      30e9,
	}
}

// verictPass reports whether a Verdict is a pass (Pass true OR severity pass).
// Kept defensive against stubs that set one field.
func verictPass(v Verdict) bool {
	return v.Pass || v.Severity == "pass"
}

// scopeVerdictFrom projects a runtime Verdict into the minimal shape the Scope
// Controller needs (Pass/Stage/Hint/Reason). The Hint field is the scope
// controller's primary routing signal (empty = no hint).
func scopeVerdictFrom(v Verdict) ScopeVerdict {
	return ScopeVerdict{Pass: v.Pass, Stage: v.Stage, Hint: v.Hint, Reason: v.Reason}
}

// scopeLevelForTool returns the broadest-but-most-restrictive scope level that
// permits a tool, mirroring the W2 permission table (File: Scope Loop
// Engineering). When a tool call the Planner emitted is disallowed at the
// current level, the runtime broadens to this level before dispatching so the
// work proceeds instead of looping forever. Unknown tools broaden to LevelRepo
// (read-mostly exploration) as a safe default.
func scopeLevelForTool(tool string) ScopeLevel {
	switch tool {
	case "list_files", "grep", "read_file":
		return ScopeLevel(1) // LevelRepo (Task=0, Repo=1, File=2, Function=3, Edit=4, Verify=5)
	case "view_function", "call_graph":
		return ScopeLevel(3) // LevelFunction
	case "edit_file", "write_file":
		return ScopeLevel(4) // LevelEdit
	case "run_test", "bash", "git_diff":
		return ScopeLevel(5) // LevelVerify
	default:
		return ScopeLevel(1) // LevelRepo: safe read-mostly default
	}
}

// maxVerifyRetries caps PATCH→VERIFY→fail cycles so a broken patch + a
// reflection that keeps proposing the same fix can't spin forever (File 07
// §7.3.2). Sprint 6 hard-codes 3; the cost controller's MaxReflections is the
// real source (wired when File 07's cost ledger plugs in).
const maxVerifyRetries = 3

// patchToolCall wraps a corrective patch body as a tool call the PATCH arm
// applies via the Patcher. Kept as "patch" so the composition root's adapter
// routes it to the patch engine.
func patchToolCall(p PatchOp) ToolCall {
	return ToolCall{Tool: "patch", Args: p.Body, Task: event.TaskID(p.Task), Reason: "corrective patch"}
}

// patchOpFromCall unpacks a patch tool call back into a PatchOp for the Patcher.
// It preserves the task ID threaded through the tool call; Path/Seq stay
// defaults because JSON args don't carry them — the PATCH arm re-injects them.
func patchOpFromCall(call ToolCall) PatchOp {
	return PatchOp{Body: call.Args, Task: session.TaskID(call.Task)}
}

// filesFromSnapshot is a placeholder: a real patch returns the touched paths in
// the observation; Sprint 6 carries them via the Patcher's richer result when
// the composition root wires it. For now the snapshot ref names the checkpoint
// and the files come from the observation the tool produced.
func filesFromSnapshot(_ []byte) []string { return nil }

// mustMarshalRuntimeObs serializes the runtime's Observation to JSON for the
// observation.received event. A marshal failure (shouldn't happen — plain
// fields) yields an empty payload rather than skipping the event.
func mustMarshalRuntimeObs(obs Observation) []byte {
	type wire struct {
		FromPatch bool     `json:"fromPatch"`
		Files     []string `json:"files,omitempty"`
		Summary   string   `json:"summary,omitempty"`
	}
	b, err := json.Marshal(wire{FromPatch: obs.FromPatch, Files: obs.Files, Summary: obs.Summary})
	if err != nil {
		return []byte(`{}`)
	}
	return b
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
// §4.5.3). Called when the drive context is canceled. It uses a fresh
// background context so the cancel cleanup itself is not canceled.
func (c *Core) handleCancel(h *taskHandle) {
	ctx := context.Background()
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

// orExec/orVerify/orPatch/orRestore fill the loop ports with no-op stubs when
// nil, so a stubbed Deps doesn't nil-panic once the drive loop drives those
// states. The composition root always wires the real ports.
func orExec(p Executor) Executor {
	if p != nil {
		return p
	}
	return noopExecutor{}
}
func orVerify(p Verifier) Verifier {
	if p != nil {
		return p
	}
	return noopVerifier{}
}
func orPatch(p Patcher) Patcher {
	if p != nil {
		return p
	}
	return noopPatcher{}
}
func orRestore(p Restorer) Restorer {
	if p != nil {
		return p
	}
	return noopRestorer{}
}
