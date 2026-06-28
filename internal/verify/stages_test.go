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
)

// fakeRunner is an in-memory Runner: it dispatches on (name, args) via a
// closure so a test can return canned stdout/stderr/exit per command, and
// records every call so a test can assert which stages ran.
type fakeRunner struct {
	fn    func(name string, args []string) (stdout, stderr string, exit int, err error)
	calls []fakeCall
}

type fakeCall struct {
	name string
	args []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	r.calls = append(r.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	return r.fn(name, args)
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
	p := NewPipeline(Deps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"README.md"})

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
	p := NewPipeline(Deps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: r, FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"vendor/pkg/x.go"})

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
	p := NewPipeline(Deps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

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
	p := NewPipeline(Deps{Runner: passRunner(), FS: fs})

	res := p.Run(context.Background(), []string{"a.go"})

	if res[6].Stage != StagePolicy || res[6].Status != SevPass {
		t.Errorf("Policy = %s/%s, want pass (clean file)", res[6].Stage, res[6].Status)
	}
}
