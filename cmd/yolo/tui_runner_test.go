// Sprint 13+ TUI integration: the driver turns user.submit events into real
// single-agent or multi-agent runs.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cog "github.com/baobao1044/yolo-code/internal/cognitive"
	"github.com/baobao1044/yolo-code/internal/event"
	execpkg "github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// TestTUIDriverSingleGoal wires a real runtime.Core and checks that a
// user.submit event drives the task through to task.completed.
func TestTUIDriverSingleGoal(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		t.Fatal(err)
	}
	// The caller that builds the deps owns the shadow tree they carry. Cleanup
	// rather than inline: the patch engine and the restorer read the tree back
	// throughout the run below, so it has to outlive the whole test body.
	t.Cleanup(func() { _ = deps.snap.close() })
	core := runtime.New(runtime.Deps{
		Bus: bus, Session: smgr,
		Context: deps.context, Prompt: deps.prompt, Cognitive: deps.cog,
		Exec: deps.exec, Verify: deps.verify, Patch: deps.patcher, Restore: deps.restorer,
	})

	ctx, cancel := context.WithCancel(context.Background())
	drv := &tuiDriver{
		ctx:    ctx,
		cancel: cancel,
		bus:    bus,
		smgr:   smgr,
		core:   core,
		sid:    sid,
		repo:   t.TempDir(),
	}
	drv.Start()
	defer drv.Stop()

	// Give the driver's user.> subscriber time to register before publishing.
	time.Sleep(50 * time.Millisecond)

	ch := bus.Subscribe(event.Topic(">"))
	done := make(chan struct{})
	var sawStarted, sawCompleted, sawAssistant bool
	go func() {
		for env := range ch {
			switch env.Evt.Type() {
			case "task.started":
				sawStarted = true
			case "task.completed":
				sawCompleted = true
			case "assistant.message":
				sawAssistant = true
			}
			if sawStarted && sawAssistant && sawCompleted {
				close(done)
				return
			}
		}
	}()

	_ = bus.Publish(ctx, &event.UserSubmitEvent{Text: "hello"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("single run not completed; started=%v assistant=%v completed=%v", sawStarted, sawAssistant, sawCompleted)
	}
}

// TestTUIDriverMultiGoal routes a multi-clause goal through the orchestrator.
func TestTUIDriverMultiGoal(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		t.Fatal(err)
	}
	// See TestTUIDriverSingleGoal: the builder owns the tree, and the goals below
	// read it back, so the close is cleanup-time.
	t.Cleanup(func() { _ = deps.snap.close() })
	core := runtime.New(runtime.Deps{
		Bus: bus, Session: smgr,
		Context: deps.context, Prompt: deps.prompt, Cognitive: deps.cog,
		Exec: deps.exec, Verify: deps.verify, Patch: deps.patcher, Restore: deps.restorer,
	})

	ctx, cancel := context.WithCancel(context.Background())
	drv := &tuiDriver{
		ctx:    ctx,
		cancel: cancel,
		bus:    bus,
		smgr:   smgr,
		core:   core,
		sid:    sid,
		repo:   t.TempDir(),
	}
	drv.Start()
	defer drv.Stop()

	// Give the driver's user.> subscriber time to register before publishing.
	time.Sleep(50 * time.Millisecond)

	ch := bus.Subscribe(event.Topic(">"))
	done := make(chan struct{})
	var sawPlan, sawDone bool
	go func() {
		for env := range ch {
			switch env.Evt.Type() {
			case "coord.plan.ready":
				sawPlan = true
			case "coord.plan.done":
				sawDone = true
			}
			if sawPlan && sawDone {
				close(done)
				return
			}
		}
	}()

	_ = bus.Publish(ctx, &event.UserSubmitEvent{Text: "add a.txt, add b.txt, add c.txt"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("multi run not completed; plan=%v done=%v", sawPlan, sawDone)
	}
}

// collect subscribes to topic and returns a stop func plus a pointer to the
// slice of envelopes seen. Tests here need "what actually reached the bus",
// which is a different question from "what did the component return".
func collect(t *testing.T, bus *event.Bus, topic event.Topic) (func() []event.Envelope, func()) {
	t.Helper()
	ch := bus.Subscribe(topic)
	got := make(chan []event.Envelope, 1)
	done := make(chan struct{})
	go func() {
		var seen []event.Envelope
		for {
			select {
			case env, ok := <-ch:
				if !ok {
					got <- seen
					return
				}
				seen = append(seen, env)
			case <-done:
				// Drain whatever is already buffered before reporting.
				for {
					select {
					case env := <-ch:
						seen = append(seen, env)
					default:
						got <- seen
						return
					}
				}
			}
		}
	}()
	var result []event.Envelope
	stop := func() {
		close(done)
		result = <-got
	}
	return func() []event.Envelope { return result }, stop
}

// TestExecToolOutputIsRedactedOnTheBus is the §13.7.3 boundary-1 proof: a
// secret a tool echoes must not reach the ToolResultEvent that every observer
// (TUI transcript, event durability log, Sentry) reads. The engine is built the
// way production builds it — through the coord runner's buildAdapters, which
// calls the shared newExecEngine — because a unit test on the normalizer passes
// even when the engine has passthroughNormalizer installed, which is precisely
// the gap that let this ship.
func TestExecToolOutputIsRedactedOnTheBus(t *testing.T) {
	const secret = "sk-abcdef0123456789abcdef0123456789"

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "leak.env"), []byte("OPENAI_API_KEY="+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := event.New()
	defer bus.Close()
	results, stop := collect(t, bus, event.Topic("tool.result"))

	r := newRuntimeAgentRunner(repo, nil, bus)
	// buildAdapters allocates the runner's shadow tree and the runner has no
	// teardown of its own; cleanup-time so the dispatch below can still read it.
	t.Cleanup(func() { _ = r.close() })
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		t.Fatal(err)
	}

	// read_file is RiskLow, so this exercises the output boundary without also
	// tripping the HITL gate (which the next test covers).
	obs, err := execAd.Dispatch(context.Background(), runtime.ToolCall{
		Task: "t1", Tool: "read_file", Args: []byte(`{"file":"leak.env"}`),
	})
	if err != nil {
		t.Fatalf("dispatch read_file: %v", err)
	}
	if strings.Contains(obs.Stdout, secret) {
		t.Errorf("secret survived normalization into the observation returned to the runtime")
	}

	stop()
	if len(results()) == 0 {
		t.Fatal("no tool.result reached the bus; the assertion below would be vacuous")
	}
	for _, env := range results() {
		raw, err := json.Marshal(env.Evt)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("secret reached the bus verbatim in %s: %s", env.Evt.Type(), raw)
		}
		if !strings.Contains(string(raw), "REDACTED") {
			t.Errorf("tool output was published with no redaction marker: %s", raw)
		}
	}
}

// TestCoordApprovalGateIsReachableAndRefusable pins §4.2. The coord runner used
// to build its engine with a hardcoded AutoApprove{Medium,High}, so
// exec.Engine.requestApproval had no reachable call site in any shipped path —
// no run in the product's history has ever asked a human. With the gate on by
// default a RiskHigh tool must publish approval.request and block, and a
// user.reject must come back as exec.ErrRejected with the write not performed.
func TestCoordApprovalGateIsReachableAndRefusable(t *testing.T) {
	t.Setenv("YOLO_AUTO_APPROVE_MEDIUM", "")
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "")

	repo := t.TempDir()
	bus := event.New()
	defer bus.Close()

	r := newRuntimeAgentRunner(repo, nil, bus)
	t.Cleanup(func() { _ = r.close() }) // the runner's shadow tree; see the test above
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		t.Fatal(err)
	}

	reqs := bus.Subscribe(event.Topic("approval.request"))

	type result struct {
		obs runtime.Observation
		err error
	}
	done := make(chan result, 1)
	go func() {
		obs, err := execAd.Dispatch(context.Background(), runtime.ToolCall{
			Task: "t1", Tool: "edit_file",
			Args:   []byte(`{"file":"owned.txt","content":"written"}`),
			Reason: "test",
		})
		done <- result{obs, err}
	}()

	var req *event.ApprovalRequestEvent
	select {
	case env := <-reqs:
		var ok bool
		req, ok = env.Evt.(*event.ApprovalRequestEvent)
		if !ok {
			t.Fatalf("unexpected event on approval.request: %T", env.Evt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no approval.request published: the HITL gate is still open for RiskHigh")
	}
	if req.Risk != execpkg.RiskHigh {
		t.Errorf("approval.request risk = %v, want high", req.Risk)
	}

	// watchApprovalDecisions (installed by buildAdapters) is the only thing that
	// can unblock the engine; without it this Dispatch would hang forever.
	if err := bus.Publish(context.Background(), &event.UserRejectEvent{
		Task: "t1", ApprovalID: req.ApprovalID, Reason: "no",
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("rejected call returned no error")
		}
		if !strings.Contains(res.err.Error(), "reject") {
			t.Errorf("dispatch error = %v, want a rejection", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch never returned after user.reject: the approval bridge is not wired")
	}

	if _, err := os.Stat(filepath.Join(repo, "owned.txt")); err == nil {
		t.Error("the rejected edit_file wrote the file anyway")
	}
}

// TestCoordAutoApproveHighOpensTheGate is the other half of §4.2: the gate is
// driven by the documented per-risk env vars, not hardcoded. With
// YOLO_AUTO_APPROVE_HIGH set, the same call runs with no prompt.
func TestCoordAutoApproveHighOpensTheGate(t *testing.T) {
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "1")

	repo := t.TempDir()
	bus := event.New()
	defer bus.Close()

	r := newRuntimeAgentRunner(repo, nil, bus)
	t.Cleanup(func() { _ = r.close() }) // the runner's shadow tree; see above
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		t.Fatal(err)
	}
	reqs, stop := collect(t, bus, event.Topic("approval.request"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := execAd.Dispatch(ctx, runtime.ToolCall{
		Task: "t1", Tool: "edit_file",
		Args: []byte(`{"file":"owned.txt","content":"written"}`),
	}); err != nil {
		t.Fatalf("dispatch with the gate opened: %v", err)
	}
	stop()
	if n := len(reqs()); n != 0 {
		t.Errorf("%d approval.request published despite YOLO_AUTO_APPROVE_HIGH=1", n)
	}
	if _, err := os.Stat(filepath.Join(repo, "owned.txt")); err != nil {
		t.Errorf("auto-approved edit_file did not write: %v", err)
	}
}

// TestCoordMemoryUsesDurableRoot pins §4.18a. The coord runner opened its
// memory Store on a fresh os.MkdirTemp per todo, so everything it "persisted"
// went to a directory nothing would ever read again. Proof that the durable
// root is the one in use, and that a corrupt file there is quarantined rather
// than silently overwritten: plant unparseable bytes in YOLO_MEMORY_DIR and
// watch them move to preference.json.corrupt.
func TestCoordMemoryUsesDurableRoot(t *testing.T) {
	memDir := t.TempDir()
	t.Setenv("YOLO_MEMORY_DIR", memDir)
	pref := filepath.Join(memDir, "preference.json")
	if err := os.WriteFile(pref, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := event.New()
	defer bus.Close()
	r := newRuntimeAgentRunner(t.TempDir(), nil, bus)
	if _, err := r.memory(context.Background()); err != nil {
		t.Fatalf("open memory: %v", err)
	}

	if _, err := os.Stat(pref + ".corrupt"); err != nil {
		t.Fatalf("the store did not open on YOLO_MEMORY_DIR (no quarantine there): %v", err)
	}
	if len(r.memStore.Warnings()) == 0 {
		t.Error("a quarantined store file produced no warning to surface")
	}

	// One Store, shared across todos and roles — not one per todo.
	second, err := r.memory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second != r.memStore {
		t.Error("memory() built a second Store; per-todo stores are what §4.18a removed")
	}
}

// TestBuildAdaptersAreBuiltOnce guards the other half of that change: the
// adapters are per-runner, not per-role-per-todo. A fresh build per call leaked
// a shadow temp dir and — since the engine now subscribes the bus for approval
// verdicts — a subscriber goroutine, both proportional to todos x roles.
func TestBuildAdaptersAreBuiltOnce(t *testing.T) {
	bus := event.New()
	defer bus.Close()
	r := newRuntimeAgentRunner(t.TempDir(), nil, bus)
	t.Cleanup(func() { _ = r.close() }) // the runner's shadow tree; see above

	a1, _, _, _, err := r.buildAdapters()
	if err != nil {
		t.Fatal(err)
	}
	a2, _, _, _, err := r.buildAdapters()
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Error("buildAdapters returned a fresh exec adapter on the second call")
	}
}

// TestProviderSwitchResetsModel pins the model-name bleed: `/model gpt-4o` then
// `/provider groq` used to leave YOLO_MODEL=gpt-4o, so the next request asked
// api.groq.com for a model name it has never heard of.
func TestProviderSwitchResetsModel(t *testing.T) {
	t.Setenv("YOLO_STUB", "1") // keep the swap itself from needing a real key
	t.Setenv("YOLO_MODEL", "")
	t.Setenv("YOLO_PROVIDER", "")
	t.Setenv("YOLO_BASE_URL", "")
	t.Setenv("GROQ_API_KEY", "gsk-test")

	bus := event.New()
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &tuiDriver{ctx: ctx, cancel: cancel, bus: bus, cog: cog.New(cog.NewStubProvider(128_000), bus)}

	d.cmdModel("gpt-4o")
	if got := os.Getenv("YOLO_MODEL"); got != "gpt-4o" {
		t.Fatalf("YOLO_MODEL after /model = %q, want gpt-4o", got)
	}

	d.cmdProvider("groq")
	preset, _ := cog.LookupProvider("groq")
	if got := os.Getenv("YOLO_MODEL"); got != preset.DefaultModel {
		t.Errorf("YOLO_MODEL after /provider groq = %q, want %q — the previous provider's model name bled across the switch", got, preset.DefaultModel)
	}
	if got := os.Getenv("YOLO_BASE_URL"); got != preset.BaseURL {
		t.Errorf("YOLO_BASE_URL = %q, want %q", got, preset.BaseURL)
	}
}

// TestProviderSwitchWithoutItsOwnKeyRefuses pins §4.6. The TUI carried its own
// key resolver that fell back KeyEnv -> YOLO_API_KEY -> OPENAI_API_KEY, so
// `/provider groq` with only an OpenAI key configured put that key in an
// Authorization header addressed to api.groq.com. The switch must refuse.
func TestProviderSwitchWithoutItsOwnKeyRefuses(t *testing.T) {
	t.Setenv("YOLO_STUB", "") // the stub short-circuits key policy; not what we test here
	t.Setenv("YOLO_PROVIDER", "")
	t.Setenv("YOLO_BASE_URL", "")
	t.Setenv("YOLO_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-should-never-be-sent-to-groq")
	t.Setenv("YOLO_API_KEY", "yolo-generic-key")
	t.Setenv("GROQ_API_KEY", "")

	bus := event.New()
	defer bus.Close()
	replies, stop := collect(t, bus, event.Topic("command.response"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := cog.NewStubProvider(128_000)
	core := cog.New(stub, bus)
	d := &tuiDriver{ctx: ctx, cancel: cancel, bus: bus, cog: core}

	d.cmdProvider("groq")
	stop()

	if len(replies()) == 0 {
		t.Fatal("no command.response for /provider groq")
	}
	last, ok := replies()[len(replies())-1].Evt.(*event.CommandResponseEvent)
	if !ok {
		t.Fatalf("unexpected event: %T", replies()[len(replies())-1].Evt)
	}
	if !strings.Contains(last.Text, "GROQ_API_KEY") {
		t.Errorf("/provider groq with no GROQ_API_KEY replied %q; want a refusal naming the key it needs", last.Text)
	}
}

// TestOrchestratorDoesNotDoubleCountCost pins a wiring bug the cost audit
// surfaced: runTUI starts a costPublisher for the session, and runOrchestrator
// used to start a second one on the same bus for every multi-agent goal. The
// publisher meters the bus (it subscribes tool.result itself), so the second
// one produced a duplicate cost.incurred for every tool call — the cost rail
// and the transcript both counted each call twice.
func TestOrchestratorDoesNotDoubleCountCost(t *testing.T) {
	repo := t.TempDir()
	// Give the reviewer real artifacts to read, so the run is guaranteed to
	// produce tool.result events and the comparison below isn't vacuous.
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("some real content here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bus := event.New()
	defer bus.Close()

	// The session-wide publisher runTUI installs.
	costPub := newCostPublisher(bus)
	costPub.Start(context.Background())
	defer costPub.Stop()

	counts, stop := collect(t, bus, event.Topic(">"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d := &tuiDriver{ctx: ctx, cancel: cancel, bus: bus, repo: repo}
	d.runOrchestrator("add a.txt, add b.txt, add c.txt")

	// The publisher accrues asynchronously off its own subscription.
	time.Sleep(200 * time.Millisecond)
	stop()

	var tools, costs int
	for _, env := range counts() {
		switch env.Evt.Type() {
		case "tool.result":
			tools++
		case "cost.incurred":
			costs++
		}
	}
	if tools == 0 {
		t.Fatal("no tool.result in the run; the comparison below would be vacuous")
	}
	if costs != tools {
		t.Errorf("cost.incurred = %d for %d tool.result: the orchestrator is metering the bus twice", costs, tools)
	}
}

// TestSessionStateDirHonoursItsOverride pins that the durable session root is
// redirectable. Without an override the only reachable path is the developer's
// real config directory, which makes the function untestable and makes any
// test that runs it pollute their sessions — the same reason YOLO_MEMORY_DIR
// exists. It must also still create the directory it returns: the caller
// writes transcripts into it immediately.
func TestSessionStateDirHonoursItsOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("YOLO_SESSION_DIR", want)

	got, err := sessionStateDir()
	if err != nil {
		t.Fatalf("sessionStateDir: %v", err)
	}
	if got != want {
		t.Errorf("sessionStateDir() = %q, want %q: YOLO_SESSION_DIR was ignored", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("sessionStateDir() did not create %q: %v", got, err)
	}
}

// TestTUISessionFlushesWhatItLearned is the interactive half of the memory
// lifecycle. runHeadlessDeps owns memStore.Close on the path where it builds
// the deps; runTUI built them the same way and dropped the Store, so the
// listener recorded everything the session learned into the in-memory
// sub-stores and nothing ever wrote them out — an insight or a transcript line
// that arrived without a following task.completed died with the process.
//
// The assertion is deliberately on the bytes under YOLO_MEMORY_DIR and not on
// "Close was called": Close is only interesting because Flush is what writes
// knowledge.json and conversations/, and a mock would have stayed green while
// the directory stayed empty.
func TestTUISessionFlushesWhatItLearned(t *testing.T) {
	const (
		lesson = "gofmt must run before the build stage"
		reply  = "reformatted the package and re-ran the build"
		task   = "t_1"
	)

	repo := t.TempDir()
	memDir := t.TempDir()
	t.Setenv("YOLO_REPO_ROOT", repo)
	t.Setenv("YOLO_MEMORY_DIR", memDir)
	t.Setenv("YOLO_SESSION_DIR", t.TempDir())

	// The front end stands in for tui.Run, which needs a TTY. Everything around
	// it — the composition, the driver, and the teardown chain under test — is
	// the production path.
	learn := func(ctx context.Context, bus *event.Bus) error {
		updates := bus.Subscribe(event.Topic("memory.update"))
		if err := bus.Publish(ctx, &event.VerificationFailedEvent{
			Task: task, Reason: lesson,
		}); err != nil {
			return err
		}
		if err := bus.Publish(ctx, &event.AssistantMessageEvent{
			Task: task, Text: reply, Final: true,
		}); err != nil {
			return err
		}
		// Return only once the listener has taken both in. Publishing and exiting
		// would race the drain, and the teardown would then be flushing a store
		// that had not learned anything yet.
		deadline := time.After(10 * time.Second)
		var sawKnowledge, sawConversation bool
		for !sawKnowledge || !sawConversation {
			select {
			case env, ok := <-updates:
				if !ok {
					return nil
				}
				u, ok := env.Evt.(*event.MemoryUpdateEvent)
				if !ok {
					continue
				}
				switch u.Store {
				case "knowledge":
					sawKnowledge = true
				case "conversation":
					sawConversation = true
				}
			case <-deadline:
				t.Errorf("memory listener never reported knowledge=%v conversation=%v", sawKnowledge, sawConversation)
				return nil
			}
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runTUIFrontend(ctx, learn); err != nil {
		t.Fatalf("runTUIFrontend: %v", err)
	}

	// The insight: recorded by the listener into the Knowledge store, written out
	// only by Flush (the listener's own Persist runs on task.completed, which an
	// interrupted session never reaches).
	knowledge, err := os.ReadFile(filepath.Join(memDir, "knowledge.json"))
	if err != nil {
		t.Fatalf("no knowledge.json under YOLO_MEMORY_DIR after the session exited: %v", err)
	}
	if !strings.Contains(string(knowledge), lesson) {
		t.Errorf("knowledge.json does not carry the lesson the session learned:\n%s", knowledge)
	}

	// The transcript line: same story, under conversations/<task>.json.
	conv, err := os.ReadFile(filepath.Join(memDir, "conversations", task+".json"))
	if err != nil {
		t.Fatalf("no conversations/%s.json under YOLO_MEMORY_DIR after the session exited: %v", task, err)
	}
	if !strings.Contains(string(conv), reply) {
		t.Errorf("conversations/%s.json does not carry the assistant turn:\n%s", task, conv)
	}
}

// TestPrefCommandReachesThePreferenceStore is the end-to-end proof that
// user.preference has both ends.
//
// It had a consumer and no producer: memory/listener.go has had the arm that
// writes the Preference store since L10-002, adapters.go feeds that store into
// every context build, context/compress.go has a KindPreferences case and the
// prompt pipeline renders the parts — a whole vertical slice, wired end to end,
// permanently empty, because nothing in the tree ever constructed the event.
// Every package reported PASS the entire time, because "nobody publishes this"
// is not a thing a unit test of either end can see.
//
// So the assertion is deliberately the far end: not "the driver published an
// event" (which would pass against a listener that dropped it) and not "the
// store has the value" (which would pass against a test that called Set), but
// the JSON file on disk after the real composition root has run and torn down.
// Everything between the slash command and that file is production code.
func TestPrefCommandReachesThePreferenceStore(t *testing.T) {
	const (
		key   = "tests"
		value = "table-driven, and name the case after the property"
	)

	memDir := t.TempDir()
	t.Setenv("YOLO_REPO_ROOT", t.TempDir())
	t.Setenv("YOLO_MEMORY_DIR", memDir)
	t.Setenv("YOLO_SESSION_DIR", t.TempDir())

	// Stands in for tui.Run, which needs a TTY. It publishes exactly what
	// input.go's runtime-command arm publishes for the line
	// "/pref tests table-driven, …" — same event, same field split — so the
	// only part of the chain this fake replaces is the keyboard.
	typePref := func(ctx context.Context, bus *event.Bus) error {
		updates := bus.Subscribe(event.Topic("memory.update"))
		if err := bus.Publish(ctx, &event.UserCommandEvent{
			Command: "pref", Args: key + " " + value,
		}); err != nil {
			return err
		}
		// Wait for the listener to report the write rather than sleeping. The
		// driver handles the command on its own goroutine and the listener
		// drains on a third, so returning here would race both and the test
		// would be flushing a store that had not been written yet.
		deadline := time.After(10 * time.Second)
		for {
			select {
			case env, ok := <-updates:
				if !ok {
					return nil
				}
				if u, ok := env.Evt.(*event.MemoryUpdateEvent); ok && u.Store == "preference" {
					return nil
				}
			case <-deadline:
				t.Error("the memory listener never reported a preference write: the command " +
					"reached the driver but no user.preference arrived, or the listener " +
					"dropped it")
				return nil
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runTUIFrontend(ctx, typePref); err != nil {
		t.Fatalf("runTUIFrontend: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(memDir, "preference.json"))
	if err != nil {
		t.Fatalf("no preference.json under YOLO_MEMORY_DIR after the session exited — the "+
			"slash command never became a durable preference: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("preference.json is not a key/value map: %v\n%s", err, raw)
	}
	if got[key] != value {
		t.Errorf("preference.json[%q] = %q, want %q\nfile:\n%s", key, got[key], value, raw)
	}
}

// TestDriverIsSubscribedWhenStartReturns pins the ordering inside Start.
//
// Subscribe was the first line of run, so Start returned before the driver was
// listening and anything published in that window was handed to nobody: the bus
// has no subscriber to give it to, so it is not queued, not counted in
// Stats().Dropped, and not logged. Nothing observes the loss.
//
// The structural half is the guard that actually holds. Asserting only that a
// command published right after Start gets answered would pass by luck against
// the old ordering whenever the scheduler happened to run the goroutine first —
// a test that is usually green against the bug is not a guard, it is a coin
// flip that files no report when it lands the other way. The channel being
// non-nil on return is the property, and it is either true or it is not.
//
// The behavioural half is still here because a non-nil field proves only that
// Subscribe was called, not that its channel is the one run reads.
func TestDriverIsSubscribedWhenStartReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := event.New()
	prefs := bus.Subscribe(event.Topic("user.preference"))
	d := planDriver(t, ctx, cancel, bus, t.TempDir())

	d.Start()
	defer d.Stop()

	if d.events == nil {
		t.Fatal("Start returned with no subscription: the driver is not listening yet, so " +
			"every user.* published before its goroutine happens to be scheduled is " +
			"discarded with no error and no counter")
	}
	if err := bus.Publish(ctx, &event.UserCommandEvent{Command: "pref", Args: "k v"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-prefs:
	case <-time.After(5 * time.Second):
		t.Error("the driver holds a subscription but run is not reading from it")
	}
}

// TestPrefIsAllowedWhileATaskIsRunning pins the exemption in handle's busy
// guard. The guard exists so a provider swap cannot race Think reading the
// provider out of the cognitive Core mid-drive; a preference write touches
// nothing the drive loop reads.
//
// Leaving /pref under the guard would not have been a crash, which is why it
// needs a test rather than trusting review: the user would get
// "task running — finish or cancel before switching model/provider" in answer
// to a line that mentions neither a model nor a provider, and would have to
// wait out the run to record the very observation the run just prompted.
func TestPrefIsAllowedWhileATaskIsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := event.New()
	prefs := bus.Subscribe(event.Topic("user.preference"))
	d := planDriver(t, ctx, cancel, bus, t.TempDir())
	d.busy.Store(true)

	d.handle(event.Envelope{Evt: &event.UserCommandEvent{Command: "pref", Args: "style tabs"}})

	select {
	case env := <-prefs:
		p, ok := env.Evt.(*event.UserPreferenceEvent)
		if !ok {
			t.Fatalf("got %T on user.preference, want *UserPreferenceEvent", env.Evt)
		}
		if p.Key != "style" || p.Value != "tabs" {
			t.Errorf("got Key=%q Value=%q, want style/tabs", p.Key, p.Value)
		}
	case <-time.After(5 * time.Second):
		t.Error("no user.preference while busy: the command was refused by the guard that " +
			"protects the provider swap, which /pref does not perform")
	}

	// The guard must still hold for what it was written for. Asserting only the
	// exemption would pass just as well against a driver that had lost the guard
	// entirely, and losing it is the actual hazard — a mid-drive SetProvider.
	responses := bus.Subscribe(event.Topic("command.response"))
	d.handle(event.Envelope{Evt: &event.UserCommandEvent{Command: "provider", Args: "groq"}})
	select {
	case env := <-responses:
		r, ok := env.Evt.(*event.CommandResponseEvent)
		if !ok || !strings.Contains(r.Text, "task running") {
			t.Errorf("busy /provider answered %v, want the task-running refusal", env.Evt)
		}
	case <-time.After(5 * time.Second):
		t.Error("busy /provider got no response at all")
	}
}

// planDriver builds the minimum tuiDriver runOrchestrator actually uses — a
// context, the bus and a repo root. The submit-to-orchestrator routing around
// it is TestTUIDriverMultiGoal's subject; these two tests are about what the
// user is told once the run is over.
func planDriver(t *testing.T, ctx context.Context, cancel context.CancelFunc, bus *event.Bus, repo string) *tuiDriver {
	t.Helper()
	return &tuiDriver{ctx: ctx, cancel: cancel, bus: bus, repo: repo}
}

// chatResponses drains command.response — the topic the TUI folds into the chat
// pane as a system message — and returns the texts seen once stop is called.
func chatResponses(t *testing.T, bus *event.Bus) (stop func() []string) {
	t.Helper()
	ch := bus.Subscribe(event.Topic("command.response"))
	out := make(chan []string, 1)
	go func() {
		var seen []string
		for env := range ch {
			if e, ok := env.Evt.(*event.CommandResponseEvent); ok {
				seen = append(seen, e.Text)
			}
		}
		out <- seen
	}()
	return func() []string {
		_ = bus.Close()
		select {
		case seen := <-out:
			return seen
		case <-time.After(5 * time.Second):
			t.Fatal("command.response drain did not finish")
			return nil
		}
	}
}

// TestTUIPlanFailureReachesTheChatPane pins the third of the three gaps that
// left a TUI user with a board that stopped moving and no explanation: the
// orchestrator's error was discarded at the call site. The assertion is on the
// bus, not on a return value, because reaching the user is the whole point.
func TestTUIPlanFailureReachesTheChatPane(t *testing.T) {
	bus := event.New()
	// A repo root that does not exist: the todos cannot make progress against
	// it, so each one reworks up to the cap and ends Failed and Run answers
	// ErrPlanFailed. The same goal against a real t.TempDir() runs clean and
	// publishes nothing here, which is the control for this test. Deliberately
	// not anchored on a verification verdict — internal/verify's rules for an
	// empty change are being reworked right now, and a test that leaned on them
	// would be pinning a peer's intermediate state rather than this call site.
	repo := filepath.Join(t.TempDir(), "no-such-repo")
	responses := chatResponses(t, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	planDriver(t, ctx, cancel, bus, repo).runOrchestrator("add a.txt, add b.txt")

	got := responses()
	if len(got) != 1 {
		t.Fatalf("want exactly one command.response for a finished plan, got %d: %q", len(got), got)
	}
	if !strings.HasPrefix(got[0], "plan failed: ") {
		t.Errorf("response does not report the failure: %q", got[0])
	}
}

// TestTUIPlanCancelIsNotReportedAsFailure is the other half. Run returns
// ctx.Err() (never ErrPlanFailed) for a run the operator stopped, and calling
// that "plan failed" is its own small lie — the user pressed the key, and the
// chat pane should not accuse the plan of collapsing on its own.
func TestTUIPlanCancelIsNotReportedAsFailure(t *testing.T) {
	bus := event.New()
	repo := t.TempDir()
	responses := chatResponses(t, bus)

	// Cancel on the first dispatched todo: that is the earliest point at which
	// the run is genuinely underway, so the cancel lands inside Run's event loop
	// rather than before it.
	assigns := bus.Subscribe(event.Topic("coord.task.assign"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for range assigns {
			cancel()
		}
	}()

	planDriver(t, ctx, cancel, bus, repo).runOrchestrator("add a.txt, add b.txt")

	got := responses()
	if len(got) != 1 {
		t.Fatalf("want exactly one command.response for a cancelled plan, got %d: %q", len(got), got)
	}
	if got[0] != "plan cancelled" {
		t.Errorf("a user-initiated cancel was reported as %q, want %q", got[0], "plan cancelled")
	}
}
