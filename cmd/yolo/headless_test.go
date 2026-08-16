package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	execpkg "github.com/baobao1044/yolo-code/internal/exec"
)

// TestMain pins the three pieces of ambient state the composition root now
// reads, for every test in package main (including the `golden` build).
//
//   - YOLO_STUB=1 opts into the deterministic stub provider. resolveProvider no
//     longer falls back to it silently (cognitive.ErrNoProvider is returned
//     instead), so without this every default-path test fails at startup with
//     "no LLM provider configured".
//   - YOLO_MEMORY_DIR redirects the durable memory root. Its default is now
//     os.UserConfigDir()/yolo-code/memory — a real user directory — and a test
//     suite must never write there.
//   - YOLO_SESSION_DIR does the same for the session root. The coord runner's
//     session store used to be a per-todo os.MkdirTemp; now that it is durable
//     (sessionStateDir()/coord), every test reaching buildRuntimeDeps writes to
//     os.UserConfigDir()/yolo-code/sessions for real. TestMultiAgentEndToEnd…
//     and the other orchestrator-driving tests were leaving files in the
//     developer's own config directory.
//
// Both temp roots are removed after the run, so the suite leaves nothing behind
// in either place.
func TestMain(m *testing.M) {
	memDir, err := os.MkdirTemp("", "yolo-test-memory-*")
	if err != nil {
		panic(err)
	}
	sessDir, err := os.MkdirTemp("", "yolo-test-sessions-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("YOLO_STUB", "1")
	_ = os.Setenv("YOLO_MEMORY_DIR", memDir)
	_ = os.Setenv("YOLO_SESSION_DIR", sessDir)
	code := m.Run()
	_ = os.RemoveAll(memDir)
	_ = os.RemoveAll(sessDir)
	os.Exit(code)
}

// TestHeadlessSingleTurnPrintsDeterministicTranscript is the L2-006 headline
// (and S5 for the spine): `echo "hi" | yolo --headless` prints one line per
// event, and two runs produce byte-identical output (modulo the timestamp,
// which the headless projection omits by design).
//
// L10-006 wires the memory listener, which publishes memory.update events.
// Those are excluded from the headless transcript projection (see headless.go:
// the listener goroutine's interleaving is non-deterministic); the transcript
// pins the agent's decision spine only.
func TestHeadlessSingleTurnPrintsDeterministicTranscript(t *testing.T) {
	first, err := runHeadless(bytes.NewBufferString("say hi\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless (1): %v", err)
	}
	second, err := runHeadless(bytes.NewBufferString("say hi\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless (2): %v", err)
	}
	if first != second {
		t.Errorf("transcript not byte-identical across runs (S5)\n first:\n%s\n second:\n%s", first, second)
	}

	// The spine events must appear in order.
	want := []string{
		"task.started",
		"state.change",
		"state.change",
		"context.built",
		"state.change",
		// The Prompt Compiler reports its budget decision between the transition
		// into PLAN and the first token. It belongs in the deterministic spine:
		// the numbers it carries are a pure function of the context package, and
		// TestGoldenHeadlessTranscript pins the exact bytes.
		"prompt.budget",
		"llm.token",
		"state.change",
		"assistant.message",
		"task.completed",
	}
	lines := strings.Split(strings.TrimRight(first, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("transcript has %d lines, want %d:\n%s", len(lines), len(want), first)
	}
	for i, w := range want {
		if !strings.Contains(lines[i], "\"type\":\""+w+"\"") {
			t.Errorf("line %d = %q, want type %q", i, lines[i], w)
		}
	}
	// memory.update must NOT appear (excluded from the projection as
	// non-deterministic telemetry).
	if strings.Contains(first, "memory.update") {
		t.Error("transcript contains memory.update; it should be excluded from the headless projection (non-deterministic telemetry)")
	}
}

// TestApprovalGateBlocksHighRiskUntilResolved is the defect-4.2 headline. The
// composition root used to hardcode AutoApprove{medium:true, high:true}, so no
// shipped path ever reached exec's requestApproval and no human was ever asked
// anything. This drives the whole gate through the composition root's own
// wiring: the engine newExecEngine builds must (1) report the call as needing
// approval, (2) actually publish approval.request — proving requestApproval is
// reachable — (3) unblock when the verdict arrives over the bus as the TUI
// sends it, which only works because watchApprovalDecisions bridges user.* to
// Engine.ResolveApproval, and (4) not run the tool after a rejection.
func TestApprovalGateBlocksHighRiskUntilResolved(t *testing.T) {
	repo := t.TempDir()
	bus := event.New()
	defer func() { _ = bus.Close() }()

	sandbox := execpkg.NewSandbox(repo, repo)
	reg := new(execpkg.Registry)
	reg.Register(execpkg.NewEditFile(sandbox))
	eng := newExecEngine(reg, sandbox, bus)
	watchApprovalDecisions(eng, bus)

	target := filepath.Join(repo, "victim.txt")
	call := execpkg.ToolCall{
		Tool: "edit_file",
		Args: []byte(`{"file":"victim.txt","content":"pwned"}`),
		Task: "t_1",
	}
	if !eng.NeedsApproval(call) {
		t.Fatal("edit_file (RiskHigh) does not need approval — the HITL gate is off by default")
	}

	// Subscribe before dispatching: the request is published from Dispatch.
	reqs := bus.Subscribe(event.Topic("approval.request"))
	done := make(chan error, 1)
	go func() {
		_, err := eng.Dispatch(context.Background(), call)
		done <- err
	}()

	select {
	case env := <-reqs:
		req, ok := env.Evt.(*event.ApprovalRequestEvent)
		if !ok {
			t.Fatalf("approval.request carried %T", env.Evt)
		}
		if req.Tool != "edit_file" {
			t.Errorf("approval.request tool = %q, want edit_file", req.Tool)
		}
		// The human says no — over the bus, exactly as the TUI's `n` key does.
		if err := bus.Publish(context.Background(), &event.UserRejectEvent{
			Task: string(req.Task), ApprovalID: req.ApprovalID,
		}); err != nil {
			t.Fatalf("publish user.reject: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no approval.request within 5s — exec.requestApproval is unreachable, the gate is theatre")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Dispatch succeeded after the user rejected it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch still parked in the gate 5s after user.reject — nothing calls Engine.ResolveApproval")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("edit_file wrote the file despite the rejection")
	}
}

// TestAutoApproveEnvOptsOutPerRisk pins the documented interface: the two
// per-risk variables are read (they were read nowhere before), default to
// false, and are independent — auto-approving medium must not auto-approve
// high.
func TestAutoApproveEnvOptsOutPerRisk(t *testing.T) {
	repo := t.TempDir()
	sandbox := execpkg.NewSandbox(repo, repo)
	reg := new(execpkg.Registry)
	reg.Register(execpkg.NewEditFile(sandbox))
	call := execpkg.ToolCall{Tool: "edit_file", Args: []byte(`{"file":"a.txt","content":"x"}`)}

	// Medium alone leaves the high-risk gate standing.
	t.Setenv("YOLO_AUTO_APPROVE_MEDIUM", "true")
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "")
	if !newExecEngine(reg, sandbox, nil).NeedsApproval(call) {
		t.Error("YOLO_AUTO_APPROVE_MEDIUM auto-approved a high-risk call")
	}

	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "true")
	if newExecEngine(reg, sandbox, nil).NeedsApproval(call) {
		t.Error("YOLO_AUTO_APPROVE_HIGH=true still gated a high-risk call")
	}
}

// TestExecEngineRedactsToolOutput: newExecEngine used to leave Deps.Normalizer
// nil, which installs exec's passthrough — raw stdout went onto the bus, into
// the transcript and into the durability log. The engine must run the redact
// pipeline backed by infra's process-wide registry, whose patterns cover the
// shapes exec's four local ones miss (sk-…, JWTs, bearer tokens).
//
// The sibling's tui_runner_test.go covers the same seam through execAdapter;
// this one pins the helper itself, which is the single construction point for
// all three run modes.
func TestExecEngineRedactsToolOutput(t *testing.T) {
	repo := t.TempDir()
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	if err := os.WriteFile(filepath.Join(repo, "creds.txt"), []byte("OPENAI="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	bus := event.New()
	defer func() { _ = bus.Close() }()
	results := bus.Subscribe(event.Topic("tool.result"))

	sandbox := execpkg.NewSandbox(repo, repo)
	reg := new(execpkg.Registry)
	reg.Register(execpkg.NewRead(sandbox)) // read_file is RiskLow: no gate in the way
	eng := newExecEngine(reg, sandbox, bus)

	obs, err := eng.Dispatch(context.Background(), execpkg.ToolCall{
		Tool: "read_file", Args: []byte(`{"file":"creds.txt"}`), Task: "t_1",
	})
	if err != nil {
		t.Fatalf("dispatch read_file: %v", err)
	}
	if strings.Contains(obs.Stdout, secret) {
		t.Errorf("the observation handed to the model still carries the key:\n%s", obs.Stdout)
	}

	select {
	case env := <-results:
		payload, _ := json.Marshal(env.Evt)
		if strings.Contains(string(payload), secret) {
			t.Errorf("tool.result published the key verbatim onto the bus:\n%s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tool.result event")
	}
}

// TestRunFlagsReachTheEnvironment covers the flag→env bridge in run(). The
// mutually-exclusive --plan/--headless error is the cheapest terminal path that
// still runs the whole flag block, so no agent work happens.
func TestRunFlagsReachTheEnvironment(t *testing.T) {
	for _, k := range []string{
		"YOLO_AUTO_APPROVE_MEDIUM", "YOLO_AUTO_APPROVE_HIGH",
		"YOLO_STUB", "YOLO_EVENT_LOG", "YOLO_REPO_ROOT",
	} {
		t.Setenv(k, "")
	}
	repo := t.TempDir()
	log := filepath.Join(t.TempDir(), "events.log")

	err := run([]string{
		"--auto-approve", "--stub", "--event-log", log, "--repo", repo,
		"--plan", "goal", "--headless",
	})
	if err == nil {
		t.Fatal("expected the --plan/--headless conflict error")
	}

	want := map[string]string{
		"YOLO_AUTO_APPROVE_MEDIUM": "true",
		"YOLO_AUTO_APPROVE_HIGH":   "true",
		"YOLO_STUB":                "1",
		"YOLO_EVENT_LOG":           log,
		"YOLO_REPO_ROOT":           repo, // --repo is read now; it used to be set and ignored
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// repoRoot is the single reader every runner shares.
	if got, err := repoRoot(); err != nil || got != repo {
		t.Errorf("repoRoot() = %q, %v; want %q, nil", got, err, repo)
	}
}

// TestHeadlessMemoryRootIsDurable: the default path used to open memory in
// os.MkdirTemp and os.RemoveAll it in the exit defer, deleting everything the
// run had just flushed. The root must be honoured and must survive the run.
func TestHeadlessMemoryRootIsDurable(t *testing.T) {
	memDir := filepath.Join(t.TempDir(), "mem") // deliberately not pre-created
	t.Setenv("YOLO_MEMORY_DIR", memDir)

	if _, err := runHeadless(bytes.NewBufferString("say hi\n"), 0); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}

	entries, err := os.ReadDir(memDir)
	if err != nil {
		t.Fatalf("memory root %s absent after the run: %v", memDir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("memory root %s is empty — the run flushed nothing durable", memDir)
	}
}

// TestHeadlessFlushesMemoryOnExit: the check above was intermittent, and the
// reason it was intermittent is the bug. Everything it can see —
// conversations/, exec/, knowledge.json — is written by the memory listener on
// task.completed, from its own goroutine. runHeadless closes the bus, which
// closes that goroutine's subscription channel, but only Store.Close joins it.
// The default `yolo --headless` path never handed its Store to the close chain
// (the handover was guarded on deps != nil, and this path is the one where deps
// is nil), so nothing joined the listener and nothing called Flush: what
// survived a run was whatever the listener happened to finish before the
// process exited. os.ReadDir then raced it, and lost on the coldest runs.
//
// preference.json is the assertion because it is the one sub-store this run
// never writes through the listener — no user.preference event is published, so
// Store.Flush, reachable here only via Store.Close, is its sole author. Its
// presence is therefore proof the close chain ran, not evidence that a
// goroutine won a race. Asserting on the racy files would just re-flake.
func TestHeadlessFlushesMemoryOnExit(t *testing.T) {
	memDir := filepath.Join(t.TempDir(), "mem")
	t.Setenv("YOLO_MEMORY_DIR", memDir)

	if _, err := runHeadless(bytes.NewBufferString("say hi\n"), 0); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}

	flushed := filepath.Join(memDir, "preference.json")
	if _, err := os.Stat(flushed); err != nil {
		t.Fatalf("%s absent after the run: %v\n"+
			"only Store.Flush writes it, and only Store.Close calls Flush — so the "+
			"default headless path never closed the memory Store it opened", flushed, err)
	}
}

// TestHeadlessEventLogIsWritten: event.Open had zero call sites, so no run was
// ever replayable. YOLO_EVENT_LOG (--event-log) must produce a log the event
// package can replay.
func TestHeadlessEventLogIsWritten(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	t.Setenv("YOLO_EVENT_LOG", logPath)

	if _, err := runHeadless(bytes.NewBufferString("say hi\n"), 0); err != nil {
		t.Fatalf("runHeadless: %v", err)
	}

	envs, err := event.Replay(logPath)
	if err != nil {
		t.Fatalf("replay %s: %v", logPath, err)
	}
	if len(envs) == 0 {
		t.Fatal("event log is empty — the bus was not the durable one")
	}
	var sawStart bool
	for _, e := range envs {
		if e.Evt.Type() == "task.started" {
			sawStart = true
		}
	}
	if !sawStart {
		t.Errorf("event log has %d records but no task.started", len(envs))
	}
}

// TestHeadlessStartsInfrastructure: infra.Start had exactly one caller in the
// whole tree — a test — so telemetry, metrics, permissions and secret
// redaction were dead in the shipped binary. The root subscriber's only
// externally visible effect is the DEBUG log line it writes per event to
// os.Stderr (§13.5.2), so capture that.
func TestHeadlessStartsInfrastructure(t *testing.T) {
	capture, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = capture
	_, runErr := runHeadless(bytes.NewBufferString("say hi\n"), 0)
	os.Stderr = orig
	_ = capture.Close()
	if runErr != nil {
		t.Fatalf("runHeadless: %v", runErr)
	}

	got, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "topic=task.started") {
		t.Fatalf("infra's root subscriber logged nothing for the run — infra.Start was never called on the startup path; stderr:\n%s", got)
	}
}

// TestHeadlessPipesStdinToTaskGoal verifies the prompt read from stdin becomes
// the task's goal (visible in task.started).
func TestHeadlessPipesStdinToTaskGoal(t *testing.T) {
	out, err := runHeadless(bytes.NewBufferString("refactor the auth module\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if !strings.Contains(out, "refactor the auth module") {
		t.Errorf("transcript missing the stdin goal; got:\n%s", out)
	}
}

// TestHeadlessCancelProducesCancelledTranscript verifies that canceling the
// headless context mid-run surfaces a CANCELLED task (the exit path the TUI's
// Ctrl+C will use). We drive a blocking cognitive core and cancel.
func TestHeadlessCancelProducesCancelledTranscript(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		cancel()
	}()
	out, err := runHeadlessCtx(ctx, bytes.NewBufferString("say hi\n"), 0)
	_ = err
	// Either the task was cancelled, or (if cancel raced ahead of StartTask)
	// no task ran. Both are valid terminal outcomes; assert we did not print a
	// DONE.
	if strings.Contains(out, "DONE") {
		t.Errorf("canceled run reached DONE; transcript:\n%s", out)
	}
}
