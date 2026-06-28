// Tests for L8-004's per-language stage skip rules (File 09 §9.5.3 + §9.5.4
// matrix): a command stage skips when the changed language has no tool for it.
// Go has all five command stages (Format/Lint/TypeCheck/Build/Tests); Other
// (Markdown, plain text, …) skips all five. A mixed Go + Markdown patch keeps
// the Go stages running (the .md files don't gate them off); a Markdown-only
// patch skips every command stage. These tests run at the Engine.Verify level
// so they assert the skip advisories land on the bus (§9.5.3: "skips are
// recorded in the verdict so the trace shows why a stage didn't run").

package verify

import (
	"context"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// stageStatus builds a {stage: status} map from the collected advisories so a
// test can assert per-stage outcomes by name.
func stageStatus(stage []*event.VerificationStageEvent) map[string]string {
	out := make(map[string]string, len(stage))
	for _, s := range stage {
		out[s.Stage] = s.Status
	}
	return out
}

func TestMarkdownOnlyPatchSkipsCommandStages(t *testing.T) {
	// A patch touching only b.md → every command stage (Format/Lint/TypeCheck/
	// Build/Tests) skips (no tool for "Other", §9.5.4 matrix row). AST passes
	// (the validator skips unknown extensions — no grammar, not a fail); Policy
	// runs (the project gate always runs). The Verdict passes and no `go *`
	// command is ever shelled out.
	bus := newBusEnv()
	defer bus.bus.Close()
	r := &fakeRunner{fn: func(string, []string) (string, string, int, error) {
		t.Error("no command should run for a Markdown-only patch (all command stages skip)")
		return "", "", 0, nil
	}}
	fs := fakeFS{"b.md": "# title\n"}
	e := NewEngine(Deps{Runner: r, FS: fs, Bus: bus.bus})

	v := e.Verify(context.Background(), Change{Task: "t_md", Files: []string{"b.md"}}, fullPolicy())

	if !v.Pass {
		t.Fatalf("Verdict.Pass = false, want true (a Markdown-only patch doesn't fail): %+v", v)
	}
	if v.Severity != SevPass {
		t.Errorf("Severity = %s, want pass", v.Severity)
	}

	// 7 stage advisories: AST(pass) + 5 command stages(skip) + Policy(pass).
	stage, fail := bus.collect(7)
	if len(fail) != 0 {
		t.Errorf("verification.failed published on a clean Markdown patch: %+v", fail)
	}
	if len(stage) != 7 {
		t.Fatalf("stage events = %d, want 7 (one per stage, skips recorded)", len(stage))
	}
	status := stageStatus(stage)
	for _, s := range []string{"format", "lint", "typecheck", "build", "tests"} {
		if status[s] != "skip" {
			t.Errorf("stage %q status = %q, want skip (no tool for Other)", s, status[s])
		}
	}
	if status["ast"] == "skip" {
		t.Error("ast stage skipped; want pass (the validator skips unknown exts, not the stage)")
	}
	if status["policy"] == "skip" {
		t.Error("policy stage skipped; want pass (the project gate always runs)")
	}
}

func TestMixedGoAndMarkdownRunsGoStages(t *testing.T) {
	// A patch touching a.go AND b.md → Go has a tool for every command stage,
	// so they all run (the .md file doesn't gate them off). The runner is a
	// pass runner; the test asserts the go/gofmt commands were actually called
	// and no command stage skipped.
	bus := newBusEnv()
	defer bus.bus.Close()
	r := passRunner()
	fs := fakeFS{
		"a.go": "package main\n\nfunc a() {}\n",
		"b.md": "# title\n",
	}
	e := NewEngine(Deps{Runner: r, FS: fs, Bus: bus.bus})

	v := e.Verify(context.Background(), Change{Task: "t_mix", Files: []string{"a.go", "b.md"}}, fullPolicy())

	if !v.Pass {
		t.Fatalf("Verdict.Pass = false, want true: %+v", v)
	}

	stage, _ := bus.collect(7)
	status := stageStatus(stage)
	for _, s := range []string{"format", "lint", "typecheck", "build", "tests"} {
		if status[s] == "skip" {
			t.Errorf("stage %q skipped; want run (Go file present → Go tool applies)", s)
		}
	}

	// The Go commands actually ran (Format = gofmt, Lint = go vet, TypeCheck/
	// Build = go build, Tests = go test). The presence of a .md file must not
	// suppress them.
	if !containsCall(r.calls, "gofmt", "") {
		t.Error("gofmt never ran (Format skipped despite a .go file)")
	}
	if !containsCall(r.calls, "go", "vet") {
		t.Error("go vet never ran (Lint skipped despite a .go file)")
	}
	if !containsCall(r.calls, "go", "build") {
		t.Error("go build never ran (Build/TypeCheck skipped despite a .go file)")
	}
	if !containsCall(r.calls, "go", "test") {
		t.Error("go test never ran (Tests skipped despite a .go file)")
	}
}
