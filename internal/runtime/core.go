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
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// Core is the runtime: it owns the scheduler and the per-task FSM handles,
// and drives the active task through its states.
type Core struct {
	bus     *event.Bus
	session *session.Manager

	// userEvents is the bus subscription userEventLoop drains. It is created by
	// New, NOT by the goroutine, and that ordering is the whole point: a
	// subscription made inside the goroutine does not exist until the scheduler
	// runs it, so every user.* event published in the gap was delivered to
	// nobody and the drive loop parked forever waiting for an answer that had
	// already been given. The bus closes this channel on Close, which is what
	// ends the goroutine.
	userEvents <-chan event.Envelope

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
	cost     CostLedger      // nil → noopCostLedger (no loop/time cap)

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
	approvals   int                // approval prompts published; numbers the task-scoped ApprovalIDs
	approvalID  string             // the id of the prompt awaiting an answer; "" when none is up
	events      chan userCmd       // user-driven events (approve/reject/pause/resume/cancel)
	ctx         context.Context    // task-scoped; cancel cascades into drive + ports
	cancel      context.CancelFunc // attached to the Session Manager
	// driving is true from the moment Submit builds the handle until Submit has
	// finished its post-drive cancel sweep. It is the only way userEventLoop can
	// tell "a goroutine owns this task and will persist a cancel for me" from
	// "the loop is long gone and I am the last one who can". Handles are never
	// removed from c.tasks (a late user event must still find its task to be
	// dropped deliberately rather than by absence), so presence in the map says
	// nothing about liveness. Guarded by Core.mu, NOT by the drive goroutine:
	// two goroutines read it.
	driving bool
}

// userCmd is a user action routed from the UI into the runtime's FSM.
//
// approvalID carries the ApprovalID the publisher answered with, for the
// approve/reject kinds only. It is compared against the prompt the task
// actually has outstanding — by applyUserCmd, on the drive goroutine, because
// taskHandle.approvalID is task state and invariant I1 says only that goroutine
// may read or write it. Comparing in userEventLoop would be a data race and
// would also be wrong: the loop routes for every task, and only the drive
// goroutine knows which prompt is up right now.
type userCmd struct {
	kind       string
	taskID     session.TaskID
	approvalID string
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
		// Subscribe HERE, not in the goroutine. `go f()` only makes f runnable;
		// until the scheduler picks it up the subscription does not exist, and
		// the bus delivers an event only to subscribers registered at publish
		// time. Anything answered in that window — a user.approve for a task
		// that parked immediately, which is the common case under load — was
		// dropped, and the drive loop then blocked forever on h.events waiting
		// for an answer the operator had already given. Creating the
		// subscription before New returns closes the window by construction:
		// there is no instant at which a caller holds a *Core that is not yet
		// listening.
		c.userEvents = c.bus.Subscribe(
			event.Topic("user.approve"),
			event.Topic("user.reject"),
			event.Topic("user.pause"),
			event.Topic("user.resume"),
			event.Topic("user.cancel"),
		)
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
	c.cost = noopCostLedger{}
	if d.Cost != nil {
		c.cost = d.Cost
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
	// Snapshot rather than Resume. This used to be Resume, which does two
	// things Submit does not want. It re-reads the session and the last task
	// off disk on every submit, although StartTask just wrote both and the
	// session is already in the Manager's map (StartTask would have returned
	// ErrUnknownSession otherwise). And it hands back the LIVE *Session — the
	// object StartTask appends to under m.mu — which the drive loop below then
	// retains for the whole task and passes into every ContextRequest. A second
	// submit on the same session therefore wrote Session.Tasks and UpdatedAt
	// while this loop was reading them: the same defect SnapshotTask closed for
	// *Task two lines down, in the same function, for the same caller.
	sess := c.session.SnapshotSession(sid)
	if sess == nil {
		return "", session.ErrUnknownSession
	}
	// LoadTaskPublic hands back the live *Task out of the Manager's own
	// mutex-protected map, and the Manager keeps writing to it: Cancel and
	// CompleteTask set Status/EndedAt under m.mu, and every save reads all of it
	// under m.mu via (*Task).clone. Retaining that pointer for the length of the
	// task puts the drive loop inside their critical section without their lock
	// — and the drive loop does not merely read it, it hands the task to
	// Reflect, whose shipped implementation increments task.Retry
	// (internal/cognitive/reflection.go:112). Pressing esc during a reflection
	// is enough to collide the two. So the runtime takes its own copy here and
	// never touches the Manager's object again.
	//
	// Nothing is lost by the copy. Everything the loop reads is fixed for the
	// task's lifetime (ID, Goal, RetryMax) and the one field it writes (Retry)
	// is read by nobody but reflection itself, so the counter still bounds the
	// retry loop exactly as before. The fields the Manager goes on changing —
	// Status, EndedAt — are never read outside internal/session.
	//
	// The copy is taken inside internal/session, not here. Copying after
	// LoadTaskPublic returned would leave a hairline window — the copy is
	// itself an unsynchronized read, so a cancel arriving between StartTask and
	// it could still collide. SnapshotTask takes it under m.mu, which is the
	// only place that window can be closed.
	task := c.session.SnapshotTask(tid)

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
		driving:   true,
	}
	c.mu.Lock()
	c.tasks[tid] = h
	c.mu.Unlock()
	// Open the cost ledger entry now so the MaxTime deadline is measured from
	// when the task began, not from the first thing that happens to read it
	// (File 07 §7.6.1).
	c.cost.RegisterTask(tid)
	// A new task starts a fresh conversation: without this the previous task's
	// transcript leaks into this one (and a repeated goal is merged into what is
	// already there instead of being genuinely re-asked). Safe here for the same
	// reason SetProvider is safe from the driver — the caller serializes
	// submissions (cmd/yolo's d.busy guard), so no drive goroutine is reading
	// the history yet; the loop below is the only reader from now on.
	c.cog.Reset()
	c.drive(taskCtx, h, sess)
	c.settleCancel(h, taskCtx)
	return tid, nil
}

// settleCancel is the last thing Submit does for a task, and it is what makes
// "Submit returned" mean "if this task was cancelled, CANCELLED is on disk".
//
// The drive loop reaches session.Cancel on the ordinary cancel routes
// (applyUserCmd on the cancel command, handleCancel on the ctx.Done arms), but
// not on all of them: a cascade that arrives while LOAD_CONTEXT or VERIFY is
// inside a port call surfaces as a plain port error, and those arms go straight
// to toError and return without ever consulting ctx.Err(). Before the cancel
// was split, the off-loop caller happened to cover that gap — badly, on a
// goroutine nobody joined, which is the defect being fixed. Sweeping here
// covers every exit from drive at once instead of enumerating them, and does it
// on the goroutine the caller is already waiting on. Cancel's terminal guard
// makes the redundant call on the ordinary routes a no-op, so no second
// task.cancelled is published.
//
// Clearing driving under the same lock acquisition that reads ctx.Err() is what
// closes the handoff to userEventLoop: a cascade that lands before this point
// is seen here, and one that lands after it finds driving already false and is
// persisted by userEventLoop itself. There is no interleaving in which both
// sides decide the other will do it.
func (c *Core) settleCancel(h *taskHandle, taskCtx context.Context) {
	c.mu.Lock()
	cancelled := taskCtx.Err() != nil
	h.driving = false
	c.mu.Unlock()
	if cancelled {
		c.handleCancel(h)
	}
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
			// Submit already put the session and task snapshots in the handle;
			// nothing left to load, so advance.
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
			// Cost caps (File 07 §7.6), sampling point one of two. PLAN is the
			// loop's only unbounded cycle, so it is where a runaway run is
			// COUNTED — but it is not sufficient to stop one: a single turn's
			// tool queue drains EXECUTE→WAIT_TOOL→VERIFY→EXECUTE (T22) without
			// coming back through here, so every call after the first ran with
			// the cap unconsulted. EXECUTE samples it again for exactly that
			// reason. IncLoop drives the ledger's own degradation ladder
			// (cost.degraded at MaxLoops) — that rung means "carry on with less",
			// so it deliberately does NOT end the task. Only a hard cap does:
			// spend, or the wall-clock deadline. With no ledger wired the noop
			// stub never trips, so nothing changes.
			c.cost.IncLoop(h.id)
			if over, cap := c.cost.HardCapExceeded(h.id); over {
				c.abortOverBudget(ctx, h, cap)
				return
			}
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
			// Bill the turn, but only when the provider actually reported its
			// usage. UsageKnown false means nobody counted, and 0/0 fed to the
			// ledger would price a real turn at zero — the spend cap would then
			// never fire for exactly the providers whose cost is unknown.
			if turn.UsageKnown {
				c.cost.AddTokens(h.id, turn.TokensIn, turn.TokensOut)
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
			// advance (T5). One turn may emit several; EXECUTE drains them one at
			// a time, VERIFY routing back to EXECUTE (T22) while any remain.
			//
			// This assignment CAN overwrite an undrained remainder, on the one
			// path that re-enters PLAN with a non-empty queue: T14, verify-fail
			// -replan. That is intended, not an accident of ordering. A replan
			// says the plan those calls belonged to was wrong, and VERIFY has
			// just rolled the checkpoint back — the calls behind the failed one
			// were chosen assuming it succeeded, so running them now would apply
			// half a plan to a tree that no longer matches it. The drain path
			// (T22) is the one that must not lose calls, and it never routes
			// through here. (The verify-fail-patch arm below overwrites the same
			// queue with the corrective patch, for the same reason.)
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
			// Cost caps, sampling point two (File 07 §7.6). Everything that
			// actually spends — a tool dispatch — passes through here, and the
			// T22 drain reaches it without re-entering PLAN, so this is what
			// makes YOLO_MAX_TIME bound a run rather than just its inter-turn
			// boundaries. Checked BEFORE the approval gate on purpose: asking a
			// human to authorise work the budget will not let us run wastes
			// their answer and their attention.
			//
			// A task parked in WAIT_USER is deliberately NOT evicted. It passes
			// through no state while it waits, and that is the honest reading:
			// the clock ran out during the human's turn, and killing a prompt
			// somebody is mid-way through answering is hostile. The cap is
			// enforced the moment the loop is spending again — the answer
			// returns here, and this check fires before the call dispatches.
			if over, cap := c.cost.HardCapExceeded(h.id); over {
				c.abortOverBudget(ctx, h, cap)
				return
			}
			call := h.pending[0]
			call.Task = event.TaskID(h.id)
			// Scope-gated tool access (File: Scope Loop Engineering, W2): when a
			// scope controller is wired AND it disallows the tool at the current
			// level, try to BROADEN the scope to a level that permits it before
			// dispatching (a tool call the Planner emitted is a signal the work
			// must happen; we don't silently drop it, which would loop forever).
			// The noop controller allows every tool, so this is a no-op there.
			//
			// Only when the W2 table actually names the tool. For one it does
			// not, there is no level to broaden to, and moving anyway would
			// hand the loop a level that does not permit the call either while
			// destroying the one it had. The call dispatches from where it is,
			// which is what happened before and what the paragraph above wants.
			if c.scope != nil && !c.scope.CanUseTool(call.Tool) {
				if lvl, known := scopeLevelForTool(call.Tool); known {
					c.scope.Enter(lvl, "tool requires broader scope")
				}
			}
			if c.exec.NeedsApproval(call) && !h.approved {
				// Wait for human approval before dispatching this call.
				from, to, err := h.fsm.transition(SigNeedsApproval, "approval")
				if err != nil {
					return
				}
				// Ask the human before announcing the park. Parking without
				// asking is a silent hang: the TUI's approval pane is driven
				// solely by approval.request, and the only other thing that
				// answers — the headless resolver — watches state.change, so
				// publishing the prompt first keeps it ahead of its own answer.
				c.publishApprovalRequest(ctx, h, call)
				c.publishTransition(ctx, h.id, from, to, "approval")
				continue
			}
			// Either no approval needed or the user already approved. Carry the
			// yes into the call so the Executor's own gate doesn't re-ask the
			// question this loop just answered, then reset the flag so the next
			// tool is re-evaluated from scratch.
			call.PreApproved = h.approved
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
			// Key the observation to the call it answers. An Executor that can
			// complete calls out of dispatch order sets CallID itself and we keep
			// it; a synchronous one leaves it empty, and then the call we just
			// dispatched is by definition the one this answers.
			if obs.CallID == "" {
				obs.CallID = call.ID
			}
			h.lastObs = obs
			h.pending = h.pending[1:]
			from, to, err := h.fsm.transition(SigDispatched, "dispatched")
			if err != nil {
				return
			}
			c.publishTransition(ctx, h.id, from, to, "dispatched")

		case StateWaitTool:
			// The observation is in (T10) → drive VERIFY. Each dispatched call
			// gets its own verify (the effect of that call is what's checked); a
			// multi-tool turn returns from VERIFY to EXECUTE (T22) for the next
			// queued call.
			// Feed the tool result into the cognitive core's conversation history
			// so the next Think() sees it (multi-turn agent loop). The call id
			// goes with it: two calls to the same tool differ only by id, so
			// without it the core pairs results to calls by name and a turn whose
			// results land out of order reasons on swapped output.
			c.cog.RecordToolResult(h.lastObs.CallID, h.lastObs.Tool, h.lastObs.Stdout)
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
				//
				// This is the second, ungated route to a model-authored write.
				// StateExecute asks the Executor's NeedsApproval and parks in
				// WAIT_USER; PATCH calls the Patcher directly and asks nobody,
				// so the body of a reflection note reaches disk with no human in
				// the loop. AST validation and the checkpoint do not close that
				// — they make a bad write undoable, not unasked-for.
				//
				// The gate is deliberately narrow rather than blanket. A
				// corrective patch exists to repair the verdict that just
				// failed, so the files that verdict was rendered on are already
				// in play and re-editing them continues an act the run has
				// already authorised. A patch that names some OTHER file is not
				// a correction, it is a new write to a file this task never
				// established it was working on — and there is already a door
				// for that with a lock on it, the ordinary tool-call path
				// through EXECUTE, where the Executor classes a patch high-risk
				// and the human is asked. So a target outside the failing change
				// is refused here and the task replans (T14): the write is not
				// blocked, it is redirected to the route that asks.
				patchCall := patchToolCall(dec.Patch)
				if !patchTargetAllowed(pathFromPatchArgs(patchCall.Args), h.lastObs.Files) {
					c.refuseOutOfScopePatch(ctx, h, pathFromPatchArgs(patchCall.Args))
					from, to, err := h.fsm.transition(SigVerifyFailReplan, "patch_out_of_scope")
					if err != nil {
						return
					}
					c.publishTransition(ctx, h.id, from, to, "patch_out_of_scope")
					continue
				}
				h.pending = []ToolCall{patchCall}
				from, to, err := h.fsm.transition(SigVerifyFailPatch, "verify_fail_patch")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "verify_fail_patch")
				continue
			}
			// Pass: this turn's remaining tool calls → back to EXECUTE (T22).
			// The Planner asked for several tools in one turn and each is
			// dispatched-verified in sequence; re-entering PLAN here would both
			// clobber the queue and burn an LLM round-trip per tool call.
			if len(h.pending) > 0 {
				from, to, err := h.fsm.transition(SigVerifyPassDrain, "verify_pass_drain")
				if err != nil {
					return
				}
				c.publishTransition(ctx, h.id, from, to, "verify_pass_drain")
				continue
			}
			// Queue drained. More to do → PLAN (T11); else DONE (T12).
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
			// Last line before the bytes hit disk. VERIFY is the only entrance
			// to this state and it already refused an out-of-scope target, so
			// reaching here means a queue this loop did not build — belt and
			// braces on the one call in the runtime that writes to the repo
			// without asking anyone.
			if !patchTargetAllowed(op.Path, h.lastObs.Files) {
				c.toError(ctx, h, errUngatedPatch{path: op.Path})
				return
			}
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
			// Name the files this patch touched. T15 hands this observation
			// straight to VERIFY, and the composition root copies
			// Observation.Files into verify.Change.Files verbatim — so an empty
			// list makes the pipeline run zero stages and report a pass. That
			// turned the corrective-patch path, the one place verification
			// matters most, into the one place it was guaranteed not to run: a
			// broken fix would verify clean and complete the task over a repo
			// that was still broken.
			files := patchedFiles(op, h.lastObs.Files)
			if len(files) == 0 {
				// The patch landed but nothing here can say what it changed, so
				// VERIFY has nothing to check. Passing an empty change on would
				// certify the patch by default; erroring says so out loud.
				c.toError(ctx, h, errUnverifiablePatch{checkpoint: res.Checkpoint})
				return
			}
			h.lastObs = Observation{FromPatch: true, Files: files}
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
			// remain blocked. The ctx arm is what makes Ctrl-C work while a
			// prompt is up: the top-of-loop ctx.Err() check is unreachable from
			// here, so without it a cancelled task waits forever for an answer
			// nobody is going to give.
			select {
			case cmd, ok := <-h.events:
				if !ok {
					return
				}
				c.applyUserCmd(ctx, h, cmd)
			case <-ctx.Done():
				c.cancelFromContext(h)
				return
			}

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
		// The verdict has to answer the prompt that is actually outstanding.
		// The runtime minted a task-scoped ApprovalID and then never read one
		// back, so ANY user.approve resolved whichever gate happened to be up:
		// replaying the id of a prompt the human already answered approved the
		// NEXT gated call in the same turn, and call.PreApproved then told the
		// Executor's own gate not to ask either. No attacker is needed — the
		// TUI clears its approval pane on the bus echo of the verdict, so a key
		// repeat republishes the id that is still in its model.
		if !h.answersOutstandingPrompt(cmd.approvalID, false) {
			c.rejectStaleVerdict(h, cmd)
			return
		}
		sig = SigUserApprove
	case "reject":
		// Same gate on the deny path. A stale deny is the smaller blast radius
		// — it cancels a call the human approved rather than running one they
		// did not — but it is the same defect, and an unexplained cancel is
		// just as opaque to the operator as an unexplained hang.
		if !h.answersOutstandingPrompt(cmd.approvalID, true) {
			c.rejectStaleVerdict(h, cmd)
			return
		}
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
	// Any accepted command leaves WAIT_USER (approve/reject) or abandons the
	// park (pause/cancel), so whatever prompt was up is no longer awaiting an
	// answer. Clearing here — rather than only on approve/reject — is what
	// stops a paused-then-resumed task from still recognising the id of a
	// prompt whose tool call PLAN has since replaced.
	h.approvalID = ""
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

// answersOutstandingPrompt reports whether a user verdict answers the approval
// prompt this task currently has up. Both the id and the existence of a prompt
// are checked: with nothing outstanding there is nothing a verdict can resolve.
//
// blankOK is the one asymmetry, and it is deliberate rather than an oversight.
// Every production publisher of user.approve echoes the id it was asked with
// (internal/tui/input.go sends m.approval.id on `y`), so a blank approve can
// only come from a replay or a client that never saw a prompt — and approve is
// the direction that runs the command, so it fails closed. user.reject has a
// second, legitimate publisher that sends no id at all: cmd/yolo's headless
// resolver refuses on the absent operator's behalf ("headless: no human to
// approve"), and it answers a park it noticed on state.change, an event that
// does not carry the ApprovalID. Failing closed there would hang every
// unattended run on its first gated call — strictly worse than the bug being
// fixed — so a blank deny is accepted. A spurious deny only cancels a task,
// which is the safe direction to be wrong in.
func (h *taskHandle) answersOutstandingPrompt(id string, blankOK bool) bool {
	if h.approvalID == "" {
		return false
	}
	if id == "" {
		return blankOK
	}
	return id == h.approvalID
}

// rejectStaleVerdict handles a verdict that does not answer the outstanding
// prompt. Dropping it silently would be safe and unusable: the gate stays up,
// and the TUI has already cleared its approval pane on the bus echo of the very
// verdict being ignored, so the operator sees a task that has simply stopped
// with nothing on screen explaining why. Two things go out instead — a warning
// naming both ids, and a re-publish of the SAME approval.request, which puts
// the pane back. "Ignored, please answer again" and "frozen" then look
// different, which is the whole point.
//
// With no prompt outstanding there is nothing to re-ask and nothing is stuck:
// that is the ordinary late-echo case (a second `y` at a prompt already
// answered), which the FSM dropped before this change too, so it stays quiet.
func (c *Core) rejectStaleVerdict(h *taskHandle, cmd userCmd) {
	if h.approvalID == "" || len(h.pending) == 0 {
		return
	}
	// The task's own context may be fine, but this path is also reachable from
	// the WAIT_USER select where a cancel is racing; background matches what
	// the rest of applyUserCmd publishes with.
	ctx := context.Background()
	_ = c.bus.Publish(ctx, &event.ErrorEvent{
		Task:  event.TaskID(h.id),
		Layer: "runtime",
		Code:  "approval_id_mismatch",
		Msg: "ignored a " + cmd.kind + " for approval " + strconv.Quote(cmd.approvalID) +
			": the prompt awaiting an answer is " + strconv.Quote(h.approvalID) +
			". Re-asking — answer the prompt now on screen.",
		Retry: true,
	})
	c.emitApprovalRequest(ctx, h, h.pending[0])
}

// userEventLoop drains the user.* subscription New created and routes each
// event to the addressed task's command channel. It runs for the lifetime of
// the Core and ends when the bus closes the channel (Bus.Close closes every
// subscriber channel), so a Core whose bus outlives it holds one subscription
// and one parked goroutine — the same lifetime the pre-hoist code had, since
// the goroutine's `range` was over the same channel.
//
// It deliberately does NOT interpret ApprovalID: that comparison is task state
// and belongs to the drive goroutine (see userCmd).
func (c *Core) userEventLoop() {
	for env := range c.userEvents {
		switch e := env.Evt.(type) {
		case *event.UserApproveEvent:
			tid := session.TaskID(e.Task)
			c.sendUserCmd(tid, userCmd{kind: "approve", taskID: tid, approvalID: e.ApprovalID})
		case *event.UserRejectEvent:
			tid := session.TaskID(e.Task)
			c.sendUserCmd(tid, userCmd{kind: "reject", taskID: tid, approvalID: e.ApprovalID})
		case *event.UserPauseEvent:
			tid := session.TaskID(string(e.Task))
			c.sendUserCmd(tid, userCmd{kind: "pause", taskID: tid})
		case *event.UserResumeEvent:
			tid := session.TaskID(string(e.Task))
			c.sendUserCmd(tid, userCmd{kind: "resume", taskID: tid})
		case *event.UserCancelEvent:
			tid := session.TaskID(string(e.Task))
			// Cascade first and cascade from here: a drive loop blocked in a
			// port call cannot cancel itself, and this is the only step that
			// unblocks it. It must also happen before the driving check below,
			// so that a Submit still running is guaranteed to observe it.
			//
			// Only the cascade, though. This goroutine used to run the whole of
			// Cancel, which put the rollback, the CANCELLED store write and
			// task.cancelled behind a step that had already released the drive
			// loop — Submit returned with the terminal status still in flight
			// on a goroutine nobody joins (see session.CancelSignal). The
			// persisting half now belongs to whoever owns the task's goroutine.
			c.session.CancelSignal(tid)
			c.mu.Lock()
			h, ok := c.tasks[tid]
			driving := ok && h.driving
			c.mu.Unlock()
			if driving {
				c.sendUserCmd(tid, userCmd{kind: "cancel", taskID: tid})
				continue
			}
			// No Submit is running this task, so there is no goroutine to hand
			// the persist to and dropping it here would lose the cancel
			// outright — strictly worse than the ordering bug above.
			//
			// Handles outlive their drive loop, so this is reachable whenever a
			// loop exits without ending its task. It used to be reachable by the
			// ERROR path too, which left the task non-terminal; toError records
			// FAILED now, so that route is closed and a cancel arriving after it
			// is correctly refused as already-terminal rather than dropped. What
			// still reaches here is the set of transition failures that return
			// from the drive loop bare — no status, no event, nothing — which is
			// the quietest way this runtime can abandon a task.
			_ = c.session.Cancel(context.Background(), tid, "user")
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

// publishApprovalRequest emits approval.request for the call the FSM just
// parked on (File 04 §4.2, T6). This is the only thing that puts the question
// in front of an interactive user: internal/tui builds its approval pane from
// this event and nothing else, and the y/n keys echo ApprovalID straight back
// as user.approve / user.reject.
//
// The id is task-scoped and carries a "rt-" prefix so it can never collide with
// an exec-issued one ("appr-N") — the two gates run in sequence for the same
// call and a shared id would let one gate's answer resolve the other's prompt.
// It is recorded on the handle as the ONE id that can resolve this park;
// applyUserCmd refuses anything else. That was the missing half: the id was
// minted, published, and then never read back, so the collision argument above
// described a property nothing enforced.
//
// Risk comes from the Executor when it implements RiskClassifier — the runtime
// has no risk model of its own, and a class it guessed would be worse than none.
// An executor that doesn't classify leaves Risk empty and the TUI omits the line
// rather than showing a wrong one.
func (c *Core) publishApprovalRequest(ctx context.Context, h *taskHandle, call ToolCall) {
	h.approvals++
	h.approvalID = "rt-" + string(h.id) + "-" + strconv.Itoa(h.approvals)
	c.emitApprovalRequest(ctx, h, call)
}

// emitApprovalRequest publishes the prompt for the id already on the handle.
// Split out from publishApprovalRequest so a re-ask (rejectStaleVerdict) puts
// out the SAME question with the SAME id — minting a fresh one there would
// invalidate the answer the operator is in the middle of giving and turn one
// mis-timed keypress into an unanswerable prompt.
func (c *Core) emitApprovalRequest(ctx context.Context, h *taskHandle, call ToolCall) {
	var risk event.Risk
	if rc, ok := c.exec.(RiskClassifier); ok {
		risk = rc.RiskOf(call)
	}
	_ = c.bus.Publish(ctx, &event.ApprovalRequestEvent{
		Task:       event.TaskID(h.id),
		ApprovalID: h.approvalID,
		Tool:       call.Tool,
		Summary:    call.Reason,
		Preview:    approvalPreview(call.Args),
		Risk:       risk,
	})
}

// approvalPreview renders a tool call's arguments for the prompt's preview
// line, capped so a large patch body can't flood the rail. Cut on a rune
// boundary — the args are JSON but may carry non-ASCII payloads.
func approvalPreview(args []byte) string {
	const maxPreview = 200
	r := []rune(string(args))
	if len(r) > maxPreview {
		return string(r[:maxPreview]) + "…"
	}
	return string(r)
}

// abortOverBudget stops a task whose cost budget is exhausted (File 07 §7.6.2).
// It reuses the retry-cap idiom rather than inventing a second failure path:
// cost.abort states the cause, then SigUserCancel lands the FSM in the terminal
// CANCELLED state and the Session Manager publishes task.cancelled with the
// reason, which is what the TUI's status bar and banner read.
// The cap name comes from the ledger rather than being guessed here: "over
// budget" without saying which budget leaves the operator unable to tell a
// ten-minute timeout from a dollar limit they can raise.
func (c *Core) abortOverBudget(ctx context.Context, h *taskHandle, cap string) {
	if cap == "" {
		cap = "cost cap"
	}
	_ = c.bus.Publish(ctx, &event.CostAbortEvent{
		Task: event.TaskID(h.id), Reason: cap,
	})
	from, to, err := h.fsm.transition(SigUserCancel, "cost_cap")
	if err == nil {
		c.publishTransition(ctx, h.id, from, to, "cost_cap")
	}
	_ = c.session.Cancel(ctx, h.id, cap+" reached")
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

// scopeLevelForTool returns the scope level that permits a tool, mirroring the
// W2 permission table (File: Scope Loop Engineering). When a tool call the
// Planner emitted is disallowed at the current level, the runtime broadens to
// this level before dispatching so the work proceeds instead of looping forever.
//
// The bool reports whether the table names the tool at all. It used to answer
// LevelRepo for anything unrecognised, called a "safe read-mostly default", and
// neither word held: LevelRepo does not permit the unknown tool either (the W2
// levels are exclusive sets, not a ladder), so the move never made the call
// legal, and for a loop already at Edit or Verify it was a NARROWING dressed up
// as "tool requires broader scope" — the real level went onto the controller's
// history stack and read-only came out. "patch" is the live case: cmd/yolo's
// routableTools admits it on purpose, it has a dispatch path to patch.Engine,
// and it writes to disk. Every model-emitted patch demoted the scope loop to
// read-only and then wrote.
//
// This mirror is hand-kept because internal/runtime may not import
// internal/scope (§15.15.2). Drift is therefore the expected failure mode, and
// the honest answer to a name it does not carry is "I don't know" — never a
// guess the caller cannot tell apart from knowledge.
func scopeLevelForTool(tool string) (ScopeLevel, bool) {
	switch tool {
	case "list_files", "grep", "read_file":
		return ScopeLevel(1), true // LevelRepo (Task=0, Repo=1, File=2, Function=3, Edit=4, Verify=5)
	case "view_function", "call_graph":
		return ScopeLevel(3), true // LevelFunction
	case "edit_file", "write_file":
		return ScopeLevel(4), true // LevelEdit
	case "run_test", "bash", "git_diff":
		return ScopeLevel(5), true // LevelVerify
	default:
		return ScopeLevel(0), false
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
//
// A ToolCall has no path field, so a target the Reflection named used to be
// dropped on the way to the PATCH arm — leaving the Patcher adapter with
// nothing to apply to and VERIFY with nothing to check. Folding it into the
// JSON args shape both the adapter (opTargetAndBody) and patchOpFromCall
// already read keeps it, without widening the port for one caller.
func patchToolCall(p PatchOp) ToolCall {
	args := p.Body
	if p.Path != "" && pathFromPatchArgs(p.Body) == "" {
		if b, err := json.Marshal(struct {
			Path string `json:"path"`
			Body string `json:"body"`
		}{Path: p.Path, Body: string(p.Body)}); err == nil {
			args = b
		}
	}
	return ToolCall{Tool: "patch", Args: args, Task: event.TaskID(p.Task), Reason: "corrective patch"}
}

// patchOpFromCall unpacks a patch tool call back into a PatchOp for the Patcher.
// It preserves the task ID threaded through the tool call, and recovers Path
// from the args when they carry one; Seq stays default — the PATCH arm
// re-injects it.
func patchOpFromCall(call ToolCall) PatchOp {
	return PatchOp{
		Body: call.Args,
		Task: session.TaskID(call.Task),
		Path: pathFromPatchArgs(call.Args),
	}
}

// pathFromPatchArgs reads the "path" field out of a patch tool call's JSON args.
// A reflection body is often raw prose or bare SEARCH/REPLACE blocks rather than
// JSON, so a failed unmarshal is the ordinary case and means "no path stated",
// not an error.
func pathFromPatchArgs(args []byte) string {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return ""
	}
	return a.Path
}

// patchedFiles is the file list VERIFY checks after a patch is applied.
//
// It does not come from PatchResult.Snapshot, and cannot: SnapshotRef is an
// opaque restore handle (a git tree or shadow-copy id), not an inventory — the
// old filesFromSnapshot placeholder returned nil for every input, which is what
// made the post-patch verify a no-op. The two real sources are the op the
// Patcher was just handed, and the files the failed verify was already about.
// The fallback is not a guess: a corrective patch exists to fix the verdict that
// just failed, so it targets the files that verdict was rendered on, and
// re-checking them is exactly what has to happen.
func patchedFiles(op PatchOp, prior []string) []string {
	if op.Path != "" {
		return []string{op.Path}
	}
	return prior
}

// patchTargetAllowed reports whether a corrective patch may write to the path
// it names, without a human being asked again.
//
// scope is the file set the failing verdict was rendered on. An unnamed target
// is allowed: patchedFiles then aims the patch at exactly that set, so there is
// nothing new to authorise. A named target must be in it. Both sides are
// cleaned so "./a.go" and "a.go" are the same file and a "../" escape cannot
// spell its way past a literal comparison — though the sandbox would refuse
// that write anyway; this is about permission, not confinement.
func patchTargetAllowed(target string, scope []string) bool {
	if target == "" {
		return true
	}
	want := filepath.Clean(target)
	for _, f := range scope {
		if f != "" && filepath.Clean(f) == want {
			return true
		}
	}
	return false
}

// refuseOutOfScopePatch tells the operator (and the transcript) that a
// reflection asked to write somewhere it had no standing to write, and what the
// run is doing about it. Silence here would be the worst of both worlds: the
// write does not happen, the model keeps proposing it, and the retry cap ends
// the task with no record of why it never converged.
func (c *Core) refuseOutOfScopePatch(ctx context.Context, h *taskHandle, target string) {
	_ = c.bus.Publish(ctx, &event.ErrorEvent{
		Task:  event.TaskID(h.id),
		Layer: "runtime",
		Code:  "patch_out_of_scope",
		Msg: "refused a corrective patch targeting " + strconv.Quote(target) +
			": the failed verification covered " + strconv.Quote(strings.Join(h.lastObs.Files, ", ")) +
			". A reflection may repair the files it broke; writing anywhere else has to go through " +
			"the approval-gated tool-call path. Replanning.",
		Retry: true,
	})
}

// errUngatedPatch is the PATCH arm's refusal: a patch reached the Patcher with a
// target the failing verdict never covered, which VERIFY should have caught.
type errUngatedPatch struct{ path string }

func (e errUngatedPatch) Error() string {
	return "runtime: refusing to apply a corrective patch to " + strconv.Quote(e.path) +
		" — outside the files the failed verification covered, and this path asks no one for approval"
}

// errUnverifiablePatch is returned when a patch was accepted but nothing names
// a file it touched, so VERIFY would be handed an empty change. An empty change
// verifies clean, which would certify the patch by not looking at it.
type errUnverifiablePatch struct{ checkpoint string }

func (e errUnverifiablePatch) Error() string {
	return "runtime: patch " + e.checkpoint + " applied but no touched file could be named; " +
		"refusing to verify an empty change, which would pass without checking anything"
}

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

// toError ends the task: it transitions the FSM to ERROR (T19), publishes an
// error event, and records the task FAILED through the Session Manager.
//
// The last of those was missing. toError moved the FSM and the bus and never
// touched the session, so the only durable record of the task — the one Resume
// reads on the next launch — still said the task had not finished. Every one of
// the eight callers returns immediately after calling this, so the task is over
// in fact whether or not anything writes it down; handleCancel, five lines
// below, has always gone through the Session Manager for exactly this reason.
//
// The session write happens even when the FSM refuses the edge. It used to be
// gated behind that transition, which inverted the priority: a state with no
// SigHardError edge is precisely when a dying task most needs a terminal status
// recorded, because nothing else is going to write one, and instead it was the
// one case that recorded nothing at all — no status, no state.change, not even
// the error event. cancelFromContext already takes this shape (transition if it
// can, record the outcome regardless).
//
// The persist runs on a background context, as handleCancel's does: some
// callers reach here with the task's own context already dead, and a cancelled
// context would fail the store write and put us back where we started.
func (c *Core) toError(ctx context.Context, h *taskHandle, cause error) {
	from, to, err := h.fsm.transition(SigHardError, "error")
	if err == nil {
		c.publishTransition(ctx, h.id, from, to, "error")
	}
	_ = c.bus.Publish(ctx, &event.ErrorEvent{
		Task: event.TaskID(h.id), Layer: "runtime", Msg: cause.Error(),
	})
	_ = c.session.Fail(context.Background(), h.id, cause.Error())
}

// cancelFromContext unparks a task whose context was cancelled while it waited
// on the user. A user cancel arrives twice — the Session Manager cascades the
// context AND a "cancel" command lands on the channel — so the two arms of the
// WAIT_USER select race. Landing the same CANCELLED transition + state.change
// applyUserCmd would have landed keeps the observable outcome identical
// whichever arm wins. The publish uses a background context because the task's
// own is, by definition, already cancelled.
func (c *Core) cancelFromContext(h *taskHandle) {
	from, to, err := h.fsm.transition(SigUserCancel, "cancel")
	if err == nil {
		c.publishTransition(context.Background(), h.id, from, to, "cancel")
	}
	c.handleCancel(h)
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
