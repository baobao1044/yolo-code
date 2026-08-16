// Tests for the verification pipeline (File 09 §9.2/§9.6): the 7-stage chain
// AST→Format→Lint→TypeCheck→Build→Tests→PolicyCheck. L8-001 is the stage
// infrastructure — each stage runs and emits a StageResult; a fail
// short-circuits. The command stages shell out via a Runner seam (the real
// os/exec adapter is wired in cmd/yolo; tests inject a fake) and the AST
// stage reuses patch.Validator (verify may import patch, File 15 §15.15.2).

package verify

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeRunner is an in-memory Runner: it dispatches on (name, args) via a
// closure so a test can return canned stdout/stderr/exit per command, and
// records every call so a test can assert which stages ran.
type fakeRunner struct {
	fn    func(name string, args []string) (stdout, stderr string, exit int, err error)
	calls []fakeCall
}

// fakeCall records one Runner invocation. ctx is kept as well as the command:
// a stage that manufactures its own context.Background() instead of forwarding
// the caller's is invisible in the verdict but obvious here, so the cancellation
// and per-stage-timeout wiring have something to assert against.
type fakeCall struct {
	ctx  context.Context
	name string
	args []string
}

func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	r.calls = append(r.calls, fakeCall{ctx: ctx, name: name, args: append([]string(nil), args...)})
	return r.fn(name, args)
}

// callFor returns the recorded invocation of `name sub` (e.g. "go build"), or
// nil if the stage never ran it.
func callFor(calls []fakeCall, name, sub string) *fakeCall {
	for i, c := range calls {
		if c.name != name {
			continue
		}
		if sub == "" || (len(c.args) > 0 && c.args[0] == sub) {
			return &calls[i]
		}
	}
	return nil
}

// fakeFS is an in-memory FS: Read returns the stored content or an error.
type fakeFS map[string]string

func (f fakeFS) Read(_ context.Context, path string) (string, error) {
	s, ok := f[path]
	if !ok {
		return "", fmt.Errorf("missing: %s", path)
	}
	return s, nil
}

// passRunner returns a clean (empty stdout, exit 0) result for every command.
func passRunner() *fakeRunner {
	return &fakeRunner{fn: func(string, []string) (string, string, int, error) {
		return "", "", 0, nil
	}}
}

// containsCall reports whether a stage's command was run, matched by the
// runner's command name and (for `go`) the subcommand in args[0].
func containsCall(calls []fakeCall, name, sub string) bool {
	for _, c := range calls {
		if c.name != name {
			continue
		}
		if sub == "" {
			return true
		}
		if len(c.args) > 0 && c.args[0] == sub {
			return true
		}
	}
	return false
}

func TestPipelineRunsAllStagesInOrder(t *testing.T) {
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	wantOrder := []Stage{StageAST, StageFormat, StageLint, StageTypeCheck, StageBuild, StageTest, StagePolicy}
	if len(res) != len(wantOrder) {
		t.Fatalf("got %d results, want %d (one per stage)", len(res), len(wantOrder))
	}
	for i, want := range wantOrder {
		if res[i].Stage != want {
			t.Errorf("res[%d].Stage = %s, want %s (order)", i, res[i].Stage, want)
		}
		if res[i].Status != SevPass {
			t.Errorf("res[%d] (%s) Status = %s, want pass", i, res[i].Stage, res[i].Status)
		}
	}
}

func TestPipelineShortCircuitsOnFail(t *testing.T) {
	// go vet fails (Lint) → TypeCheck/Build/Test/Policy must NOT run.
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "vet" {
			return "", "a.go:1: expected declaration\n", 1, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	// Expect AST(pass), Format(pass), Lint(fail) — then short-circuit.
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3 (short-circuit after Lint fail)", len(res))
	}
	if res[2].Stage != StageLint || res[2].Status != SevFail {
		t.Errorf("res[2] = %s/%s, want Lint/fail", res[2].Stage, res[2].Status)
	}
	if containsCall(r.calls, "go", "build") {
		t.Error("go build ran after Lint failed (short-circuit broken)")
	}
	if containsCall(r.calls, "go", "test") {
		t.Error("go test ran after Lint failed (short-circuit broken)")
	}
}

func TestASTStageFailsOnBrokenSyntax(t *testing.T) {
	fs := fakeFS{"a.go": "package main\n\nfunc a() {\n"} // missing close brace
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	if res[0].Stage != StageAST || res[0].Status != SevFail {
		t.Fatalf("AST = %s/%s, want fail", res[0].Stage, res[0].Status)
	}
	if len(res[0].Issues) == 0 {
		t.Error("AST fail has no Issues, want the parse error")
	}
	// Short-circuit: nothing after AST ran.
	if len(res) != 1 {
		t.Errorf("got %d results, want 1 (short-circuit after AST fail)", len(res))
	}
}

func TestASTStageAcceptsUnknownExtension(t *testing.T) {
	// A Markdown file: the patch Validator skips unknown extensions (§10.4),
	// so AST passes — verify forwards that guarantee.
	fs := fakeFS{"README.md": "# broken markdown (((\n"}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"README.md"}, Policy{})

	if res[0].Stage != StageAST || res[0].Status != SevPass {
		t.Errorf("AST(.md) = %s/%s, want pass (unknown extension skipped)", res[0].Stage, res[0].Status)
	}
}

func TestFormatStageWarnsOnMismatch(t *testing.T) {
	// gofmt -l lists an unformatted file on stdout → a warning, not a fail
	// (§9.3.2: a format mismatch with AutoFormat off is a warning).
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "gofmt" {
			return "a.go\n", "", 0, nil // lists the unformatted file
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	if res[1].Stage != StageFormat || res[1].Status != SevWarn {
		t.Errorf("Format = %s/%s, want warn (unformatted)", res[1].Stage, res[1].Status)
	}
	// A warning does NOT short-circuit: the next stage (Lint) runs.
	if len(res) <= 2 {
		t.Error("pipeline stopped after a warning; warnings should not short-circuit")
	}
}

func TestLintStageFailsOnVetError(t *testing.T) {
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "vet" {
			return "", "a.go:3: unused variable x\n", 1, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	if res[2].Stage != StageLint || res[2].Status != SevFail {
		t.Fatalf("Lint = %s/%s, want fail", res[2].Stage, res[2].Status)
	}
	if len(res[2].Issues) == 0 {
		t.Error("Lint fail has no parsed Issues, want the vet diagnostic")
	}
}

func TestBuildStageFailsOnCompileError(t *testing.T) {
	// go build fails → TypeCheck (which runs `go build`) fails, short-circuit
	// before Build/Test/Policy.
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "build" {
			return "", "a.go:5: undefined: foo\n", 1, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	// AST(pass) Format(pass) Lint(pass) TypeCheck(fail) — short-circuit.
	if len(res) != 4 {
		t.Fatalf("got %d results, want 4 (short-circuit after TypeCheck fail)", len(res))
	}
	if res[3].Stage != StageTypeCheck || res[3].Status != SevFail {
		t.Errorf("TypeCheck = %s/%s, want fail", res[3].Stage, res[3].Status)
	}
	if containsCall(r.calls, "go", "test") {
		t.Error("go test ran after TypeCheck failed (short-circuit broken)")
	}
}

func TestTestStageFailsOnTestFailure(t *testing.T) {
	// go test fails → Test stage fails, but only after the prior stages pass.
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "test" {
			return "", "FAIL a_test.go:10 bad\n", 1, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	// All 7 stages run; the last-but-one (Test) fails — Policy still runs?
	// No: a fail short-circuits, so Policy does NOT run after a Test fail.
	if len(res) != 6 {
		t.Fatalf("got %d results, want 6 (short-circuit after Test fail, Policy skipped)", len(res))
	}
	if res[5].Stage != StageTest || res[5].Status != SevFail {
		t.Errorf("Test = %s/%s, want fail", res[5].Stage, res[5].Status)
	}
}

func TestPolicyStageBlocksVendorEdits(t *testing.T) {
	fs := fakeFS{"vendor/pkg/x.go": "package pkg\n\nfunc X() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"vendor/pkg/x.go"}, Policy{})

	// Policy is the last stage; a vendor edit is a hard fail.
	if len(res) != 7 {
		t.Fatalf("got %d results, want 7", len(res))
	}
	pol := res[6]
	if pol.Stage != StagePolicy || pol.Status != SevFail {
		t.Errorf("Policy = %s/%s, want fail (vendor edit)", pol.Stage, pol.Status)
	}
	found := false
	for _, is := range pol.Issues {
		if is.Code == "no-vendor" {
			found = true
		}
	}
	if !found {
		t.Errorf("Policy Issues = %+v, want a no-vendor issue", pol.Issues)
	}
}

func TestPolicyStageWarnsOnTodoWithoutOwner(t *testing.T) {
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n// TODO fix this\n"}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	pol := res[6]
	if pol.Stage != StagePolicy {
		t.Fatalf("last stage = %s, want Policy", pol.Stage)
	}
	if pol.Status != SevWarn {
		t.Errorf("Policy = %s, want warn (TODO without owner is a warning)", pol.Status)
	}
	found := false
	for _, is := range pol.Issues {
		if is.Code == "todo-owner" {
			found = true
		}
	}
	if !found {
		t.Errorf("Policy Issues = %+v, want a todo-owner issue", pol.Issues)
	}
}

func TestPolicyStagePassesCleanFiles(t *testing.T) {
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"}, Policy{})

	if res[6].Stage != StagePolicy || res[6].Status != SevPass {
		t.Errorf("Policy = %s/%s, want pass (clean file)", res[6].Stage, res[6].Status)
	}
}

// --- context + policy threading ---------------------------------------------

// ctxProbeKey tags a caller's context so a test can prove a stage forwarded it
// rather than manufacturing a fresh context.Background().
type ctxProbeKey struct{}

func TestBuildStagesForwardCallerContext(t *testing.T) {
	// The TypeCheck and Build stages both shell out via buildCmd. buildCmd used
	// to call r.Run(context.Background(), ...), so a cancelled task context
	// never reached the compiler: the real runner uses osexec.CommandContext,
	// so those `go build` processes kept compiling after the runtime had
	// already reported the task cancelled. Every command a stage runs must
	// carry the caller's ctx.
	r := passRunner()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs})

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxProbeKey{}, "caller"))
	cancel() // already cancelled: the runner should see a dead context

	e.Verify(ctx, Change{Task: "t_ctx", Files: []string{"a.go"}}, fullPolicy())

	build := callFor(r.calls, "go", "build")
	if build == nil {
		t.Fatal("go build never ran; the test can't observe the context it was given")
	}
	if build.ctx.Value(ctxProbeKey{}) != "caller" {
		t.Errorf("go build ctx value = %v, want %q (stage manufactured its own context)", build.ctx.Value(ctxProbeKey{}), "caller")
	}
	if build.ctx.Err() == nil {
		t.Error("go build ctx is live after the caller cancelled; Ctrl-C can't kill the compiler")
	}
	// And the rest of the chain, which already forwarded ctx, still does.
	for _, c := range []struct{ name, sub string }{{"gofmt", ""}, {"go", "vet"}, {"go", "test"}} {
		got := callFor(r.calls, c.name, c.sub)
		if got == nil {
			t.Errorf("%s %s never ran", c.name, c.sub)
			continue
		}
		if got.ctx.Err() == nil {
			t.Errorf("%s %s ctx is live after the caller cancelled", c.name, c.sub)
		}
	}
}

func TestTestTimeoutCapsTheTestStageOnly(t *testing.T) {
	// Policy.TestTimeout was plumbed through four layers and read by nobody, so
	// the documented 30s cap did not exist. A non-zero timeout must put a
	// deadline on the `go test` context — and only that one; the other stages
	// keep the caller's context unchanged.
	r := passRunner()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs})

	pol := fullPolicy()
	pol.TestTimeout = 30 * time.Second
	v := e.Verify(context.Background(), Change{Task: "t_to", Files: []string{"a.go"}}, pol)

	if !v.Pass {
		t.Fatalf("Verdict = %+v, want pass (a generous cap changes nothing)", v)
	}
	test := callFor(r.calls, "go", "test")
	if test == nil {
		t.Fatal("go test never ran")
	}
	dl, ok := test.ctx.Deadline()
	if !ok {
		t.Fatal("go test ctx has no deadline; Policy.TestTimeout is not wired (a hung `go test` hangs verification forever)")
	}
	if until := time.Until(dl); until <= 0 || until > 30*time.Second {
		t.Errorf("go test deadline is %v away, want (0, 30s]", until)
	}
	if build := callFor(r.calls, "go", "build"); build != nil {
		if _, ok := build.ctx.Deadline(); ok {
			t.Error("go build inherited the test timeout; the cap is the test stage's")
		}
	}
}

func TestZeroTestTimeoutMeansNoCap(t *testing.T) {
	// The zero value must mean "no cap", never "a deadline that already
	// expired". A policy that leaves TestTimeout unset behaves exactly as it
	// did before the field was wired.
	r := passRunner()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs})

	pol := fullPolicy()
	pol.TestTimeout = 0
	v := e.Verify(context.Background(), Change{Task: "t_zero", Files: []string{"a.go"}}, pol)

	if !v.Pass || v.Severity != SevPass {
		t.Fatalf("Verdict = %+v, want a clean pass (zero timeout must not expire anything)", v)
	}
	test := callFor(r.calls, "go", "test")
	if test == nil {
		t.Fatal("go test never ran with TestTimeout=0")
	}
	if dl, ok := test.ctx.Deadline(); ok {
		t.Errorf("go test ctx has deadline %v with TestTimeout=0, want none", dl)
	}
}

func TestTestTimeoutExceededIsAWarningNotAPass(t *testing.T) {
	// A cap that fires is recorded as a warning (§9.3.6: a slow machine
	// shouldn't veto a correct patch) — but it must be *recorded*. Before the
	// fix the stage reported a clean pass no matter how long the tests took.
	r := passRunner()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs})

	pol := fullPolicy()
	pol.TestTimeout = time.Nanosecond // expires before the runner returns
	v := e.Verify(context.Background(), Change{Task: "t_slow", Files: []string{"a.go"}}, pol)

	if v.Severity != SevWarn {
		t.Fatalf("Severity = %s, want warn (the test cap fired): %+v", v.Severity, v)
	}
	if len(v.Warnings) == 0 {
		t.Fatal("Warnings empty; the timeout left no trace on the Verdict")
	}
	if !v.Pass {
		t.Errorf("Pass = false; §9.3.6 makes a test timeout a warning, not a veto: %+v", v)
	}
}

func TestLintLevelWarningFailsOnDiagnostics(t *testing.T) {
	// LintLevel "warning" is the strict level: any lint output fails, not just
	// a non-zero exit. The field was never read, so a strict policy silently
	// got lenient lint.
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "vet" {
			return "", "a.go:3: composite literal uses unkeyed fields\n", 0, nil // exit 0!
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs})

	pol := fullPolicy()
	pol.LintLevel = "warning"
	v := e.Verify(context.Background(), Change{Task: "t_strict", Files: []string{"a.go"}}, pol)

	if v.Pass {
		t.Fatalf("Verdict passed with vet diagnostics at LintLevel=warning: %+v", v)
	}
	if v.Stage != StageLint {
		t.Errorf("Stage = %s, want lint", v.Stage)
	}
	if len(v.Errors) == 0 {
		t.Error("Errors empty, want the vet diagnostic")
	}
}

func TestLintLevelErrorRecordsDiagnosticsAsWarnings(t *testing.T) {
	// The lenient level ("error", the default) does not fail on diagnostics the
	// tool itself let through — but it records them rather than dropping them.
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "vet" {
			return "", "a.go:3: composite literal uses unkeyed fields\n", 0, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs})

	pol := fullPolicy() // LintLevel: "error"
	v := e.Verify(context.Background(), Change{Task: "t_lenient", Files: []string{"a.go"}}, pol)

	if !v.Pass {
		t.Fatalf("Verdict failed at LintLevel=error on a non-fatal diagnostic: %+v", v)
	}
	if len(v.Warnings) == 0 {
		t.Error("Warnings empty; the vet diagnostic was dropped on the floor")
	}
}
