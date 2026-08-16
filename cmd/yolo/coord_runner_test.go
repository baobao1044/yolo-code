// Tests for the runtime.Core-backed coord AgentRunner (Sprint 12 INT-006).

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	"github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// TestRuntimeAgentRunnerSpawnsCoder drives the orchestrator with a real
// runtime.Core coder runner for one todo. The coord.* stream still carries the
// canonical sequence, while the shared bus also sees the real task events
// (context.built, assistant.message, task.completed) produced by the agent.
func TestRuntimeAgentRunnerSpawnsCoder(t *testing.T) {
	// The coder's session store is now durable (§4.18b), and its default root is
	// the developer's real os.UserConfigDir — which TestMain redirects for
	// memory but not yet for sessions. Redirect it here so the suite does not
	// write there.
	t.Setenv("YOLO_SESSION_DIR", t.TempDir())

	repo := t.TempDir()
	bus := event.New()
	defer func() { _ = bus.Close() }()

	rec := newRecordingSub(bus)

	// Capture the runtime-level events emitted by the coder agent too.
	allCh := bus.Subscribe(event.Topic(">"))
	var sawContextBuilt, sawAssistantMsg bool
	allDone := make(chan struct{})
	go func() {
		defer close(allDone)
		for env := range allCh {
			if env.Evt.Type() == "context.built" {
				sawContextBuilt = true
			}
			if env.Evt.Type() == "assistant.message" {
				sawAssistantMsg = true
			}
		}
	}()

	runner := newRuntimeAgentRunner(repo, cognitive.NewStubProvider(128_000), bus)
	// The runner allocates a shadow tree on its first buildAdapters and has no
	// teardown of its own. Cleanup-time, not inline: the orchestrator run below
	// reads the tree back (the patch engine rolls a failed verification out).
	t.Cleanup(func() { _ = runner.close() })
	plan := coord.Plan{ID: "p-1", Goal: "explain repo"}
	plan.Todos = append(plan.Todos, coord.Todo{
		ID: "t1", Title: "summarize", Assignee: string(coord.RoleCoder), Status: coord.Pending,
	})

	o := coord.NewOrchestrator(
		coord.Config{MaxReworkCycles: 1, Concurrency: 1},
		cannedPlanner{plan: plan},
		bus, bus, runner,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := o.Run(ctx, plan.Goal); err != nil && !strings.Contains(err.Error(), "context") {
		t.Fatalf("orchestrator Run: %v", err)
	}
	_ = bus.Close()
	rec.close() // wait for drain to finish before reading rec.types
	<-allDone   // wait for the allCh drain goroutine to finish before reading its vars

	if len(rec.types) == 0 || rec.types[0] != "coord.plan.ready" {
		t.Fatalf("first event = %v, want coord.plan.ready", rec.types)
	}
	seq := rec.byTodo["t1"]
	if len(seq) < 3 {
		t.Fatalf("todo t1 events = %v, want >=3", seq)
	}
	got := joinTypes(seq)
	for _, want := range []string{"coord.task.assign", "coord.code.ready", "coord.review.verdict", "coord.test.report"} {
		if !strings.Contains(got, want) {
			t.Fatalf("todo t1 missing %s; got %s", want, got)
		}
	}

	if !sawContextBuilt {
		t.Error("coder agent did not emit context.built")
	}
	if !sawAssistantMsg {
		t.Error("coder agent did not emit assistant.message")
	}
}

// TestSessionStoreIsRunScopedNotPerTodo pins §4.18b, the session half of the
// change §4.18a made to memory. buildRuntimeDeps opened the session Store on a
// fresh os.MkdirTemp("", "yolo-coord-*") for every todo and nothing ever
// removed it: one dev box had 1194 of them, each holding the sessions/ and
// tasks/ JSON of a run nobody could resume. Three assertions, one per property
// the fix has to hold at once — no leftover temp dir, one Manager for the whole
// run, and the records landing somewhere durable.
func TestSessionStoreIsRunScopedNotPerTodo(t *testing.T) {
	repo := t.TempDir()
	memDir := t.TempDir()
	sessDir := t.TempDir()
	tmpRoot := t.TempDir()

	t.Setenv("YOLO_MEMORY_DIR", memDir)
	t.Setenv("YOLO_SESSION_DIR", sessDir)
	// os.MkdirTemp("", …) re-reads the temp dir on every call, so redirecting it
	// here scopes the leak count to this test — a peer test's temp dirs can
	// neither fail it nor fake it green. Set last: the t.TempDir calls above
	// allocate under the temp dir too, and they must not land inside the
	// directory being counted. scopeTempDir, not a bare TMPDIR Setenv: Windows
	// ignores TMPDIR entirely (see the note on the helper).
	scopeTempDir(t, tmpRoot)

	bus := event.New()
	defer func() { _ = bus.Close() }()

	ctx := context.Background()
	r := newRuntimeAgentRunner(repo, cognitive.NewStubProvider(128_000), bus)
	t.Cleanup(func() { _ = r.close() }) // the runner's shadow tree

	var mgrs []*session.Manager
	for _, id := range []string{"t1", "t2", "t3"} {
		d, err := r.buildRuntimeDeps(ctx, event.TaskAssignEvent{
			PlanID: "p-1", TodoID: id, Brief: "implement " + id,
		})
		if err != nil {
			t.Fatalf("buildRuntimeDeps(%s): %v", id, err)
		}
		mgrs = append(mgrs, d.Session)
	}

	leaked, err := filepath.Glob(filepath.Join(tmpRoot, "yolo-coord-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Errorf("buildRuntimeDeps left %d yolo-coord-* dir(s) behind (%v); nothing in the runner ever removes them, so a plan leaks one per todo", len(leaked), leaked)
	}

	for i, m := range mgrs[1:] {
		if m != mgrs[0] {
			t.Errorf("todo %d got a fresh session.Manager; the store is run-scoped, and a per-todo Manager restarts its ID counters at s_1", i+1)
		}
	}

	// Durability: the records the Manager writes must be readable after the run,
	// which means under YOLO_SESSION_DIR and not under a temp root.
	if _, err := mgrs[0].OpenSession(ctx, "p-1", "first"); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if n := countJSON(t, sessDir); n == 0 {
		t.Errorf("no session JSON under YOLO_SESSION_DIR %s; the store is still rooted somewhere disposable", sessDir)
	}
}

// TestCoordSessionStoreDoesNotClobberTheTUISession is the guarantee that once
// justified the coord store's own subdirectory and now outlives it. runTUI opens
// its own Manager on the same root in the same process, and the bare monotonic
// counter that made that unsafe — every Manager numbering from s_1 — is now
// backed by an O_CREATE|O_EXCL claim per record, so the coder todo takes a fresh
// number instead of writing over the interactive session's s_1.json.
//
// The assertion is deliberately unchanged: "the TUI's session survives a coder
// todo" is the property worth pinning, and it should hold however the store
// lays itself out. See TestCoordSessionsShareTheTUIRootSoIDsAreDisjoint for why
// the layout became one root.
func TestCoordSessionStoreDoesNotClobberTheTUISession(t *testing.T) {
	repo := t.TempDir()
	memDir := t.TempDir()
	sessDir := t.TempDir()

	t.Setenv("YOLO_MEMORY_DIR", memDir)
	t.Setenv("YOLO_SESSION_DIR", sessDir)

	bus := event.New()
	defer func() { _ = bus.Close() }()
	ctx := context.Background()

	// The TUI's own Manager, wired exactly as runTUI wires it.
	tuiMgr := session.New(session.Deps{
		Store: session.NewFileStore(sessDir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	tuiSID, err := tuiMgr.OpenSession(ctx, "tui", "interactive")
	if err != nil {
		t.Fatalf("tui OpenSession: %v", err)
	}

	r := newRuntimeAgentRunner(repo, cognitive.NewStubProvider(128_000), bus)
	t.Cleanup(func() { _ = r.close() }) // the runner's shadow tree
	d, err := r.buildRuntimeDeps(ctx, event.TaskAssignEvent{PlanID: "p-1", TodoID: "t1", Brief: "implement t1"})
	if err != nil {
		t.Fatalf("buildRuntimeDeps: %v", err)
	}
	if _, err := d.Session.OpenSession(ctx, "p-1", "coder todo"); err != nil {
		t.Fatalf("coord OpenSession: %v", err)
	}

	sess, _, err := tuiMgr.Resume(ctx, tuiSID)
	if err != nil {
		t.Fatalf("resume the TUI session after a coord todo ran: %v", err)
	}
	if sess.Title != "interactive" || sess.ProjectID != "tui" {
		t.Errorf("the coord store overwrote the TUI's %s.json: title=%q project=%q", tuiSID, sess.Title, sess.ProjectID)
	}
}

// countJSON counts .json files anywhere under root. Used instead of a hardcoded
// sessions/ path so the assertion pins where the store lives, not how it lays
// its subdirectories out.
func countJSON(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCoordSessionsShareTheTUIRootSoIDsAreDisjoint pins the reason the coord
// store no longer sits in its own subdirectory. Two Managers on one bus were
// both minting t_1, and every consumer of those IDs — the event log, the memory
// records, the transcripts — saw two different tasks wearing the same name. The
// separate roots were the cause, not the mitigation: session.FileStore claims
// each ID with O_CREATE|O_EXCL, and a claim only excludes what shares its
// directory.
func TestCoordSessionsShareTheTUIRootSoIDsAreDisjoint(t *testing.T) {
	t.Setenv("YOLO_SESSION_DIR", t.TempDir())
	ctx := context.Background()
	bus := event.New()
	defer func() { _ = bus.Close() }()

	// The interactive Manager, rooted exactly as runTUI roots it.
	root, err := sessionStateDir()
	if err != nil {
		t.Fatal(err)
	}
	tuiMgr := session.New(session.Deps{
		Store: session.NewFileStore(root),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	tuiSid, err := tuiMgr.OpenSession(ctx, "tui", "interactive")
	if err != nil {
		t.Fatal(err)
	}
	tuiTask, err := tuiMgr.StartTask(ctx, tuiSid, "the user's own goal")
	if err != nil {
		t.Fatal(err)
	}

	// The sub-agent Manager, in the same process and on the same bus.
	runner := newRuntimeAgentRunner(t.TempDir(), cognitive.NewStubProvider(128_000), bus)
	t.Cleanup(func() { _ = runner.close() })
	coordMgr, err := runner.sessions()
	if err != nil {
		t.Fatal(err)
	}
	coordSid, err := coordMgr.OpenSession(ctx, "plan-1", "todo brief")
	if err != nil {
		t.Fatal(err)
	}
	coordTask, err := coordMgr.StartTask(ctx, coordSid, "todo brief")
	if err != nil {
		t.Fatal(err)
	}

	if coordSid == tuiSid {
		t.Errorf("both Managers minted session %s", coordSid)
	}
	if coordTask == tuiTask {
		t.Errorf("both Managers minted task %s: every consumer of these IDs sees one name for two tasks", coordTask)
	}

	// The cost of one root is that sub-agent sessions sit beside the user's own.
	// ProjectID is what keeps them apart for anything that lists sessions, so
	// pin that rather than leave it as an assertion in a comment.
	own, err := session.NewFileStore(root).ListSessions(ctx, "tui")
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].ID != tuiSid {
		t.Errorf("listing the user's own project returned %d sessions, want just %s", len(own), tuiSid)
	}
}

// TestCoordExecAdapterGatesAnUnroutedTool pins the registry wiring. exec.Engine
// answers a registry miss with RiskLow and no approval, which is right inside
// the engine (its Dispatch rejects the miss anyway) and wrong in an adapter that
// adds routes of its own. The adapter's fail-closed check is inert until a
// construction site hands it the registry, and this is the site whose callers —
// sub-agents — have no human watching them turn by turn.
func TestCoordExecAdapterGatesAnUnroutedTool(t *testing.T) {
	t.Setenv("YOLO_SESSION_DIR", t.TempDir())
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "")
	bus := event.New()
	defer func() { _ = bus.Close() }()

	runner := newRuntimeAgentRunner(t.TempDir(), cognitive.NewStubProvider(128_000), bus)
	t.Cleanup(func() { _ = runner.close() })
	execAd, _, _, _, err := runner.buildAdapters()
	if err != nil {
		t.Fatal(err)
	}

	unknown := runtime.ToolCall{Tool: "write_anywhere"}
	if got := execAd.RiskOf(unknown); got != exec.RiskHigh {
		t.Errorf("RiskOf(%s) = %v, want %v — a name nothing routes is gated as the most dangerous thing it could be", unknown.Tool, got, exec.RiskHigh)
	}
	if !execAd.NeedsApproval(unknown) {
		t.Errorf("NeedsApproval(%s) = false: a sub-agent could name a tool nobody recognises and pass the gate", unknown.Tool)
	}

	// Control: the gate must not sweep up the registered read-only tools.
	if execAd.NeedsApproval(runtime.ToolCall{Tool: "read_file"}) {
		t.Error("NeedsApproval(read_file) = true; the unrouted check is over-reaching")
	}
}
