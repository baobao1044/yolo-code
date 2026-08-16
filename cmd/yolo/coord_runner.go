// Runtime-backed AgentRunner for the multi-agent orchestrator (Sprint 12
// INT-006, extended in Sprint 13). Coder runs a real runtime.Core;
// Reviewer/Tester run lightweight real checks through the exec/verify adapters
// and publish the canonical coord.* events.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	econtext "github.com/baobao1044/yolo-code/internal/context"
	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/memory"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// runtimeAgentRunner implements coord.AgentRunner by spawning real agents. It
// owns per-agent session managers and port wiring so the orchestrator stays
// decoupled from runtime internals.
type runtimeAgentRunner struct {
	repo     string
	provider cognitive.Provider // default for coder when patches map is absent
	patches  map[string]string  // todoID -> patch body; test harness only
	bus      *event.Bus

	// The runner holds no costPublisher: the publisher subscribes tool.result
	// itself, so a run is metered regardless of who spawned it. A reference
	// would only be useful for checking a ceiling before spawning the next
	// todo, and that check belongs where the numbers live, not in the spawner.

	// adapters are built once and shared across roles and todos (see
	// buildAdapters). They used to be rebuilt on every role invocation, which
	// leaked a shadow temp dir and — now that the engine subscribes the bus for
	// approval verdicts — a subscriber goroutine per todo per role.
	adaptersOnce sync.Once
	execAd       *execAdapter
	verifyAd     *verifyAdapter
	patchAd      *patchAdapter
	restorer     runtime.Restorer
	snap         *shadowSnap // shadow tree behind patchAd+restorer; the runner owns close (a delete — see shadowSnap.close)
	adaptersErr  error

	// memStore is the durable memory Store shared by every todo (see memory).
	memOnce  sync.Once
	memStore *memory.Store
	memErr   error

	// sessMgr is the durable session Manager shared by every todo (see
	// sessions). It was a fresh Manager on a fresh temp dir per todo.
	sessOnce sync.Once
	sessMgr  *session.Manager
	sessErr  error
}

func newRuntimeAgentRunner(repo string, provider cognitive.Provider, bus *event.Bus) *runtimeAgentRunner {
	return &runtimeAgentRunner{repo: repo, provider: provider, bus: bus}
}

func (r *runtimeAgentRunner) withPatches(p map[string]string) *runtimeAgentRunner {
	r.patches = p
	return r
}

func (r *runtimeAgentRunner) Run(ctx context.Context, role coordpkg.Role, task event.TaskAssignEvent) error {
	switch role {
	case coordpkg.RoleCoder:
		return r.runCoder(ctx, task)
	case coordpkg.RoleReviewer:
		return r.runReviewer(ctx, task)
	case coordpkg.RoleTester:
		return r.runTester(ctx, task)
	default:
		return nil
	}
}

// runCoder runs a runtime.Core for the todo and emits coord.code.ready with
// the patch body (when supplied by the test harness) or an empty diff.
func (r *runtimeAgentRunner) runCoder(ctx context.Context, task event.TaskAssignEvent) error {
	deps, err := r.buildRuntimeDeps(ctx, task)
	if err != nil {
		return err
	}
	// Persist per todo rather than at the end of the plan: the runner has no
	// teardown hook of its own, and a plan abandoned halfway should still keep
	// what the completed todos learned.
	defer r.flushMemory(ctx)

	sid, err := deps.Session.OpenSession(ctx, task.PlanID, task.Brief)
	if err != nil {
		return err
	}

	core := runtime.New(deps)
	if _, err := core.Submit(ctx, sid, task.Brief); err != nil {
		return err
	}

	diff := ""
	if body, ok := r.patches[task.TodoID]; ok {
		diff = body
	}

	return r.bus.Publish(ctx, &event.CodeReadyEvent{
		PlanID:     task.PlanID,
		TodoID:     task.TodoID,
		Diff:       diff,
		SelfReport: "done via runtime.Core",
	})
}

// runReviewer reads the artifact file referenced by the task and approves if
// the file exists and contains non-trivial content.
func (r *runtimeAgentRunner) runReviewer(ctx context.Context, task event.TaskAssignEvent) error {
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		return err
	}
	path := firstArtifact(task.Artifacts, task.Brief)
	obs, err := execAd.Dispatch(ctx, runtime.ToolCall{Tool: "read_file", Args: []byte(`{"file":"` + path + `"}`)})
	// Approve if the artifact reads with content, or if there is no artifact
	// to audit (e.g. legacy tests with plain titles and no file extension).
	approved := err != nil || len(obs.Stdout) > 10
	return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
		PlanID: task.PlanID, TodoID: task.TodoID, Approved: approved,
	})
}

// runTester runs a low-risk bash command (go version) and passes if it exits 0.
func (r *runtimeAgentRunner) runTester(ctx context.Context, task event.TaskAssignEvent) error {
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		return err
	}
	obs, err := execAd.Dispatch(ctx, runtime.ToolCall{Tool: "bash", Args: []byte(`{"command":"go version"}`)})
	passed := err == nil && strings.Contains(obs.Stdout, "go") && !strings.Contains(obs.Stdout, "error")
	output := obs.Stdout
	if !passed && obs.Stdout == "" {
		output = fmt.Sprintf("error: %v", err)
	}
	return r.bus.Publish(ctx, &event.TestReportEvent{
		PlanID: task.PlanID, TodoID: task.TodoID, Passed: passed, Output: output,
	})
}

// buildRuntimeDeps wires the same real adapters the headless runner uses.
func (r *runtimeAgentRunner) buildRuntimeDeps(ctx context.Context, task event.TaskAssignEvent) (runtime.Deps, error) {
	// §4.18b: the session Store had the shape §4.18a removed from memory below —
	// an os.MkdirTemp per todo that nothing removed and nothing read again. It
	// leaked a directory per todo (1194 of them on one dev box) holding the very
	// sessions/ and tasks/ JSON that makes a run resumable. One Manager now
	// backs every todo, rooted under the durable sessionStateDir. Deleting the
	// dir instead would not have worked: runtime.Core.Submit calls
	// session.Manager.Resume, which re-reads the session off disk, so the store
	// has to outlive every Core built from these Deps — and buildRuntimeDeps
	// returns before the first one is even constructed.
	smgr, err := r.sessions()
	if err != nil {
		return runtime.Deps{}, err
	}

	execAd, verifyAd, patchAd, restorer, err := r.buildAdapters()
	if err != nil {
		return runtime.Deps{}, err
	}

	cogProv := r.provider
	if body, ok := r.patches[task.TodoID]; ok {
		path := firstArtifact(task.Artifacts, task.Brief)
		cogProv = &patchToolProvider{path: path, body: body}
	}

	// §4.18a: the Store used to be opened on an os.MkdirTemp per todo, so the
	// preferences and insights the coder wrote were persisted into a directory
	// nothing would ever read again — durable-looking, effectively /dev/null.
	// One Store now backs every todo, rooted at the same durable per-user root
	// the headless path uses (YOLO_MEMORY_DIR when set), so lessons survive the
	// process and carry across todos and roles. Sharing is safe because every
	// sub-store the Store fans out to holds its own mutex — not, as this comment
	// used to say, because runner.Run is called synchronously from one
	// goroutine. It no longer is: agent turns run on their own goroutines now.
	memStore, err := r.memory(ctx)
	if err != nil {
		return runtime.Deps{}, err
	}

	d := runtime.Deps{
		Bus:     r.bus,
		Session: smgr,
		// Tools: see the twin in headless.go — the offered tool set is injected
		// from here so the prompt's AVAILABLE TOOLS block cannot drift from what
		// the provider is really given.
		Context:   contextAdapter{eng: econtext.New(econtext.Deps{Bus: r.bus, Repo: r.repo, Memory: contextMemoryAdapter{store: memStore}, Tools: cognitive.DefaultTools()})},
		Prompt:    promptAdapter{comp: prompt.New(nil, r.bus)},
		Cognitive: newRealCognitiveCore(cogProv, r.bus),
		Exec:      execAd,
		Verify:    verifyAd,
		Patch:     patchAd,
		Restore:   restorer,
		// Scope Loop Engineering + Dynamic Workflow: each per-todo Core gets its
		// own scope controller + workflow engine on the shared coord bus so
		// scope./workflow. events are observable alongside coord.> events.
		Scope:    newScopeAdapter(r.bus),
		Workflow: newWorkflowAdapter(r.bus),
		// One ledger per todo Core. The caps are per-task inside the controller
		// either way, so a shared instance would behave identically; a fresh one
		// keeps each todo's deadline starting when that todo starts rather than
		// when the plan did.
		Cost: newCostLedger(),
	}
	return d, nil
}

// sessions returns the runner's shared session Manager, opening it on first
// use. Sharing one Manager rather than one root is the load-bearing part: every
// Manager numbers its sessions from s_1 (session.Manager holds monotonic
// counters, not UUIDs), so a fresh Manager per todo pointed at a common root
// would have each todo overwrite the previous todo's s_1.json. One Manager
// hands out s_1, s_2, … instead. Sharing is safe because the Manager guards its
// own maps and allocates its counters atomically — not, as this comment used to
// say, because the orchestrator calls runner.Run synchronously. It no longer
// does: agent turns run on their own goroutines now.
//
// The store is rooted on sessionStateDir itself — the same root runTUI's Manager
// uses. It sat in a "coord" subdirectory until session.FileStore started
// claiming each ID with O_CREATE|O_EXCL, and that separation was necessary right
// up to that point: an ID was a per-Manager counter, so two Managers on one root
// both minted s_1 and the second write landed on top of the first. With the
// atomic claim (and the Manager's retry on ErrIDTaken) the relationship inverts
// — a shared root is now the thing that makes the IDs disjoint, and two roots
// are what made them collide. Both Managers publish to one bus, so two
// different tasks were reaching the event log, the memory records and the
// transcripts wearing the same t_1: the TUI adopted a sub-agent's lifecycle as
// the user's own and rendered the plan DONE with two of three todos unstarted.
//
// Sub-agent sessions now land beside the user's own in one directory. That costs
// nothing a filter does not already cover: runCoder opens these with ProjectID
// set to the plan ID where the interactive session uses "tui", and ListSessions
// filters by project — so a resume picker asking for the user's sessions never
// sees them. (There is no picker today; ListSessions' only non-test caller is
// the Manager's own counter seed, which wants every record anyway.)
func (r *runtimeAgentRunner) sessions() (*session.Manager, error) {
	r.sessOnce.Do(func() {
		root, err := sessionStateDir()
		if err != nil {
			r.sessErr = err
			return
		}
		r.sessMgr = session.New(session.Deps{
			Store: session.NewFileStore(root),
			Bus:   r.bus,
			Git:   session.NewInMemCheckpointer(),
		})
	})
	return r.sessMgr, r.sessErr
}

// memory returns the runner's shared memory Store, opening it on first use.
// No Bus is wired: the coord runtime doesn't drive the listener-driven learning
// path, and a listening Store's Close blocks until the bus it subscribed to is
// closed — the bus here belongs to the caller, not to the runner.
func (r *runtimeAgentRunner) memory(ctx context.Context) (*memory.Store, error) {
	r.memOnce.Do(func() {
		root, err := memoryRoot()
		if err != nil {
			r.memErr = err
			return
		}
		st, err := memory.Open(memory.Deps{Root: root})
		if err != nil {
			r.memErr = err
			return
		}
		// memory.Open no longer aborts on a corrupt store file — it moves the
		// bytes to <name>.corrupt and starts that sub-store empty. Silently
		// swallowing that would present amnesia as a normal run, so say it.
		for _, w := range st.Warnings() {
			fmt.Fprintf(os.Stderr, "yolo: memory: %v\n", w)
		}
		// Cold-start index r.repo so the coord runtime's RAG seam has real
		// chunks (§11.7.5). Best-effort + bounded by a timeout (a huge repo
		// shouldn't stall task assignment). Once per run now, not once per todo.
		indexCtx, indexCancel := context.WithTimeout(ctx, 30*time.Second)
		_, _ = memory.IndexRepo(indexCtx, st.Semantic(), r.repo)
		indexCancel()
		r.memStore = st
	})
	return r.memStore, r.memErr
}

// flushMemory persists whatever the todo just learned. The Store only persists
// itself on task.completed via its listener, and no listener is wired here, so
// without this call an interrupted plan loses every insight it recorded.
func (r *runtimeAgentRunner) flushMemory(ctx context.Context) {
	if r.memStore == nil {
		return
	}
	if err := r.memStore.Flush(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "yolo: memory flush: %v\n", err)
	}
}

// buildAdapters builds the shared exec/verify/patch/restorer adapters for
// this repo. The returned adapters are safe to reuse across roles — and are now
// built exactly once (sync.Once) rather than per role per todo. Rebuilding them
// leaked a shadow temp dir per call, and the approval bridge below subscribes
// the bus, so a per-call build would also have leaked one subscriber goroutine
// per todo per role. Shadow checkpoints are keyed by task ID inside the snap
// dir, so one snap shared across todos keeps rollback semantics intact.
func (r *runtimeAgentRunner) buildAdapters() (*execAdapter, *verifyAdapter, *patchAdapter, runtime.Restorer, error) {
	r.adaptersOnce.Do(r.initAdapters)
	return r.execAd, r.verifyAd, r.patchAd, r.restorer, r.adaptersErr
}

// initAdapters is the body of buildAdapters, run once. Errors are recorded on
// the runner so every later caller sees the same failure instead of retrying a
// build that already failed halfway.
func (r *runtimeAgentRunner) initAdapters() {
	sandbox := exec.NewSandbox(r.repo, r.repo)
	reg := new(exec.Registry)
	reg.Register(exec.NewBash(sandbox))
	reg.Register(exec.NewRead(sandbox))
	reg.Register(exec.NewListFiles(sandbox))
	reg.Register(exec.NewEditFile(sandbox))
	reg.Register(exec.NewGrep(sandbox))
	// §4.2: the HITL gate used to be a hardcoded
	// AutoApprove{RiskMedium: true, RiskHigh: true} literal right here, so every
	// medium/high-risk sub-agent action ran with no human in the loop —
	// exec.Bash.Run refuses only RiskCritical on its own, so this gate is the
	// only thing between RiskHigh and execution. newExecEngine is the one place
	// that decides, reading the documented YOLO_AUTO_APPROVE_MEDIUM /
	// YOLO_AUTO_APPROVE_HIGH switches (both default false = gate on). Sharing it
	// with the headless path keeps coord from being a second, laxer policy.
	execEng := newExecEngine(reg, sandbox, r.bus)
	// The gate publishes approval.request and then blocks until
	// ResolveApproval(id) — bridge this engine to the bus so the human at the
	// TUI (the coordinator's only interactive host) can actually answer. Under
	// `--plan` there is no human; the plan runner's refuse-on-stall watcher
	// answers instead. Either way the call never hangs unanswered.
	watchApprovalDecisions(execEng, r.bus)

	snap, err := newShadowSnap(r.repo)
	if err != nil {
		r.adaptersErr = err
		return
	}
	cp := newShadowCheckpointer(snap)
	patchEng := newPatchEngine(sandbox, cp, r.bus)

	r.snap = snap
	// registry is the same one execEng dispatches from, and handing it over is
	// what arms execAdapter.unrouted: without it the adapter cannot tell "the
	// engine has never heard of this name" from "the engine says it is
	// harmless", so it reports false and the fail-closed gate is inert. Two
	// construction sites needed it; this is the sub-agent one, and sub-agents are
	// the callers no human is watching turn by turn.
	r.execAd = &execAdapter{engine: execEng, patcher: patchEng, registry: reg}
	r.verifyAd = &verifyAdapter{engine: newVerifyEngine(sandbox)}
	r.patchAd = &patchAdapter{engine: patchEng}
	r.restorer = newShadowRestorer(snap)
}

// close releases what the runner allocated for the plan. The shadow tree is the
// only thing that goes: sessions() and memory() are deliberately rooted
// somewhere durable and are meant to outlive the process, while the snap is
// per-run rollback state under os.MkdirTemp that nothing removed — one
// directory per `--plan` invocation and per multi-agent goal in a TUI session.
//
// The runner has no teardown hook of its own, so this is the caller's job, and
// it can only be called once the orchestrator's Run has returned: buildAdapters
// hands the same snap to every todo and role, and the patch engine reads the
// tree back mid-todo to roll a failed verification out. Run is safe to close
// after because its terminal path cancels every inflight agent turn and waits
// for the goroutine to return (coord.Orchestrator.finish → quiesceAgents) before
// handing control back — that join exists for exactly this reason, so nothing is
// reading the tree once Run has returned.
func (r *runtimeAgentRunner) close() error {
	return r.snap.close() // nil-safe: a runner that never built adapters has no tree
}

func artifactFromBrief(brief string) string {
	for _, w := range strings.Fields(brief) {
		w = strings.Trim(w, ".,;:!?")
		if strings.Contains(w, ".") {
			return w
		}
	}
	return "artifact.go"
}

func firstArtifact(artifacts []string, brief string) string {
	if len(artifacts) > 0 {
		return artifacts[0]
	}
	return artifactFromBrief(brief)
}

// patchToolProvider is a scripted cognitive provider that emits a single
// patch tool call. It is used by the test harness to drive deterministic coder
// agents without an external LLM. After emitting the patch once, subsequent
// Think calls return a final answer so the FSM terminates (otherwise HasMore
// keeps returning true and the drive loop spins forever).
type patchToolProvider struct {
	path    string
	body    string
	emitted bool // true after the first Think call emits the patch
}

func (p *patchToolProvider) Window() int { return 128_000 }

func (p *patchToolProvider) Stream(ctx context.Context, req cognitive.Request) (<-chan cognitive.Chunk, error) {
	joined := strings.Join(func() []string {
		out := make([]string, 0, len(req.Messages))
		for _, m := range req.Messages {
			out = append(out, m.Content)
		}
		return out
	}(), "\n")

	out := make(chan cognitive.Chunk, 1)
	go func() {
		defer close(out)

		// Reflection: abort on verify failure (takes priority).
		if strings.Contains(joined, "Reflect on the failed verification") {
			select {
			case out <- cognitive.Chunk{Delta: "DECISION: abort"}:
			case <-ctx.Done():
			}
			return
		}

		// Subsequent Think calls after the patch was already emitted: return a
		// final answer so the FSM reaches DONE instead of looping forever.
		if p.emitted {
			select {
			case out <- cognitive.Chunk{Delta: "The task is complete."}:
			case <-ctx.Done():
			}
			return
		}

		// First Think call: emit the patch tool block.
		p.emitted = true
		pa := patchToolArgs{Path: p.path, Body: p.body}
		raw, err := json.Marshal(pa)
		if err != nil {
			return
		}
		block := fmt.Sprintf("```tool\n{\"tool\":\"patch\",\"args\":%s,\"reason\":\"apply planned edit\"}\n```\n", raw)
		select {
		case out <- cognitive.Chunk{Delta: block}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}
