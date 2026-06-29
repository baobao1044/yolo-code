// Tests for the Verdict model + Engine.Verify entry point (File 09 §9.4/§9.6):
// Engine.Verify runs the pipeline (L8-001) for a Change under a Policy and
// returns a Verdict (pass / warn / fail with structured reasons). A fail
// publishes `verification.failed`; every stage publishes `verification.stage`
// (the per-stage advisory, §9.4.2). The Policy is verify's mirror of
// cognitive.VerificationPolicy (verify may not import cognitive, File 15
// §15.15.2; the composition root translates).

package verify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// busEnv subscribes to verification.* and lets a test drain the events
// synchronously with a timeout. A background collector goroutine would race the
// test: Publish blocks until the event lands in the buffered channel, but the
// collector may not have drained it yet when the test inspects. Draining inline
// (select on the channel) guarantees the test sees exactly what Publish sent.
type busEnv struct {
	bus *event.Bus
	ch  <-chan event.Envelope
}

func newBusEnv() *busEnv {
	bus := event.New()
	return &busEnv{bus: bus, ch: bus.Subscribe("verification.>")}
}

// collect drains up to n events within a short timeout and splits them into
// stage/failed slices. n==0 returns immediately (used to assert "no events").
func (e *busEnv) collect(n int) (stage []*event.VerificationStageEvent, fail []*event.VerificationFailedEvent) {
	if n == 0 {
		// Drain anything already buffered (a publish race) within a tiny window.
		select {
		case env, ok := <-e.ch:
			if !ok {
				return
			}
			stage, fail = appendEnv(env, stage, fail)
		case <-time.After(50 * time.Millisecond):
		}
		return
	}
	for len(stage)+len(fail) < n {
		select {
		case env, ok := <-e.ch:
			if !ok {
				return
			}
			stage, fail = appendEnv(env, stage, fail)
		case <-time.After(2 * time.Second):
			return
		}
	}
	return
}

// collectAll drains until the channel goes quiet for `quiet` with no new event,
// then returns everything seen. Used when the count isn't known up front.
func (e *busEnv) collectAll(quiet time.Duration) (stage []*event.VerificationStageEvent, fail []*event.VerificationFailedEvent) {
	for {
		select {
		case env, ok := <-e.ch:
			if !ok {
				return
			}
			stage, fail = appendEnv(env, stage, fail)
		case <-time.After(quiet):
			return
		}
	}
}

func appendEnv(env event.Envelope, stage []*event.VerificationStageEvent, fail []*event.VerificationFailedEvent) ([]*event.VerificationStageEvent, []*event.VerificationFailedEvent) {
	switch ev := env.Evt.(type) {
	case *event.VerificationStageEvent:
		stage = append(stage, ev)
	case *event.VerificationFailedEvent:
		fail = append(fail, ev)
	}
	return stage, fail
}

func fullPolicy() Policy {
	return Policy{
		RequireAST:       true,
		RequireFormat:    true,
		RequireLint:      true,
		RequireTypeCheck: true,
		RequireBuild:     true,
		RequireTests:     true,
		LintLevel:        "error",
		TestTimeout:      30 * time.Second,
	}
}

func TestVerifyPassReturnsPassVerdict(t *testing.T) {
	bus := newBusEnv()
	defer bus.bus.Close()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: passRunner(), FS: fs, Bus: bus.bus})

	v := e.Verify(context.Background(), Change{Task: "t_1", Files: []string{"a.go"}}, fullPolicy())

	if !v.Pass {
		t.Fatalf("Verdict.Pass = false, want true (all stages pass): %+v", v)
	}
	if v.Severity != SevPass {
		t.Errorf("Severity = %s, want pass", v.Severity)
	}
	if len(v.Errors) != 0 {
		t.Errorf("Errors = %v, want empty on pass", v.Errors)
	}
	// All 7 stages published an advisory.
	stage, fail := bus.collect(7)
	if len(fail) != 0 {
		t.Errorf("verification.failed published on pass: %+v", fail)
	}
	if len(stage) != 7 {
		t.Errorf("stage events = %d, want 7 (one per stage)", len(stage))
	}
}

func TestVerifyFailPublishesVerificationFailed(t *testing.T) {
	// go test fails → the Test stage fails → Verify returns a fail Verdict and
	// publishes verification.failed with a reason naming the stage.
	bus := newBusEnv()
	defer bus.bus.Close()
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "go" && len(args) > 0 && args[0] == "test" {
			return "", "FAIL a_test.go:10 broken\n", 1, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs, Bus: bus.bus})

	v := e.Verify(context.Background(), Change{Task: "t_7", Files: []string{"a.go"}}, fullPolicy())

	if v.Pass {
		t.Fatal("Verdict.Pass = true, want false (tests failed)")
	}
	if v.Severity != SevFail {
		t.Errorf("Severity = %s, want fail", v.Severity)
	}
	if v.Stage != StageTest {
		t.Errorf("Stage = %s, want tests (the failing stage)", v.Stage)
	}
	if len(v.Errors) == 0 {
		t.Error("Errors empty on fail, want the test diagnostic")
	}

	// 6 stages ran (AST..Build pass) + Test fail short-circuits before Policy
	// = 6 stage events + 1 verification.failed = 7 total. Collect both.
	_, fail := bus.collect(7)
	if len(fail) != 1 {
		t.Fatalf("verification.failed events = %d, want 1", len(fail))
	}
	fe := fail[0]
	if string(fe.Task) != "t_7" {
		t.Errorf("failed event Task = %q, want t_7", fe.Task)
	}
	if !strings.Contains(fe.Reason, "test") {
		t.Errorf("failed event Reason = %q, want it to name the tests stage", fe.Reason)
	}
}

func TestVerifyFailShortCircuitPublishesSkippedStagesAsFail(t *testing.T) {
	// An AST fail short-circuits — the later stages don't run, but the Verdict
	// names the failing stage (AST) and only that stage's advisory is a fail.
	bus := newBusEnv()
	defer bus.bus.Close()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {\n"} // missing close brace
	e := NewEngine(Deps{Runner: passRunner(), FS: fs, Bus: bus.bus})

	v := e.Verify(context.Background(), Change{Task: "t_2", Files: []string{"a.go"}}, fullPolicy())

	if v.Pass || v.Stage != StageAST {
		t.Fatalf("Verdict = %+v, want fail at AST stage", v)
	}
	// Only AST ran (short-circuit) → one stage advisory published.
	stage, _ := bus.collect(1)
	if len(stage) != 1 || stage[0].Status != "fail" {
		t.Errorf("stage events = %+v, want 1 fail (AST)", stage)
	}
}

func TestVerifyWarnPassesWithWarnings(t *testing.T) {
	// gofmt -l lists the file → Format warns; Verify passes but carries the
	// warning (§9.4.1: a warning is acceptable, recorded, surfaced).
	bus := newBusEnv()
	defer bus.bus.Close()
	r := &fakeRunner{fn: func(name string, args []string) (string, string, int, error) {
		if name == "gofmt" {
			return "a.go\n", "", 0, nil
		}
		return "", "", 0, nil
	}}
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: r, FS: fs, Bus: bus.bus})

	v := e.Verify(context.Background(), Change{Task: "t_3", Files: []string{"a.go"}}, fullPolicy())

	if !v.Pass {
		t.Fatalf("Verdict.Pass = false, want true (a warning shouldn't fail): %+v", v)
	}
	if len(v.Warnings) == 0 {
		t.Error("Warnings empty, want the format warning recorded")
	}
	// No fail event on a pass-with-warnings.
	_, fail := bus.collect(7) // 7 stages, all pass-or-warn, no fail event
	if len(fail) != 0 {
		t.Errorf("verification.failed published on a warn: %+v", fail)
	}
}

func TestPolicySelectsRequiredStages(t *testing.T) {
	// A light policy (AST only) → only AST runs, the rest skip. L8-002 wires
	// the policy→stage planning; the skipped stages appear as SevSkip in the
	// trace (the §9.5.3 "skips are recorded" rule, refined by L8-004's per-
	// language matrix).
	bus := newBusEnv()
	defer bus.bus.Close()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: passRunner(), FS: fs, Bus: bus.bus})

	pol := Policy{RequireAST: true} // light: AST only
	v := e.Verify(context.Background(), Change{Task: "t_4", Files: []string{"a.go"}}, pol)

	if !v.Pass {
		t.Fatalf("Verdict.Pass = false, want true (AST only, passes): %+v", v)
	}
	// AST + Policy run (Policy always runs — the project gate); the 5 command
	// stages (Format/Lint/TypeCheck/Build/Test) each publish a skip advisory.
	stage, _ := bus.collect(7)
	var passCount, skipCount int
	for _, s := range stage {
		switch s.Status {
		case "pass":
			passCount++
		case "skip":
			skipCount++
		}
	}
	if passCount != 2 || skipCount != 5 {
		t.Errorf("pass/skip stage events = %d/%d, want 2/5 (AST+Policy run, rest skipped)", passCount, skipCount)
	}
}

func TestVerifyNilBusStillReturnsVerdict(t *testing.T) {
	// A nil bus (unit tests, or the composition root skipping events) must
	// still produce a Verdict — publishing is best-effort.
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: passRunner(), FS: fs}) // no Bus

	v := e.Verify(context.Background(), Change{Task: "t_5", Files: []string{"a.go"}}, fullPolicy())
	if !v.Pass {
		t.Fatalf("Verdict.Pass = false with nil bus, want true: %+v", v)
	}
}
