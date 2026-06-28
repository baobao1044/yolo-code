// The verify engine entry point (File 09 §9.4/§9.6): Engine.Verify runs the
// pipeline (L8-001) for a Change under a Policy and returns a Verdict. A fail
// publishes `verification.failed`; every stage publishes `verification.stage`
// (the per-stage advisory, §9.4.2 — green check / red cross per stage). The
// aggregate Verdict (Pass / Stage / Severity / Reason / Errors / Warnings) is
// what the runtime acts on (File 04 §4.5 T11–T14) and Reflection cites (File
// 07 §7.3).
//
// Policy is verify's own mirror of cognitive.VerificationPolicy. The import
// matrix (File 15 §15.15.2) forbids verify importing cognitive (L6 < L8,
// bottom-up), so the policy's shape is duplicated here and the composition
// root (cmd/yolo) translates cognitive.VerificationPolicy → verify.Policy.
// This mirrors patch.FileStat ↔ event.PatchFile — a matrix-driven duplication
// the doc comments flag so it's not mistaken for accidental drift.

package verify

import (
	"context"
	"fmt"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// Policy defines what "done" means for a task (File 07 §7.5.2, mirrored here):
// which stages must pass and at what strictness. A light "explain this
// function" task uses RequireAST only; a "ship it" task uses the full policy.
// LintLevel is "error" (a warning doesn't fail) or "warning" (any lint output
// fails). TestTimeout caps the test stage (a timeout is a warning, §9.3.6).
type Policy struct {
	RequireAST       bool
	RequireFormat    bool
	RequireLint      bool
	RequireTypeCheck bool
	RequireBuild     bool
	RequireTests     bool
	LintLevel        string // "error" | "warning"
	TestTimeout      time.Duration
}

// Required returns the canonical stage names this policy requires, in pipeline
// order — mirrors cognitive.VerificationPolicy.RequiredStages so the two stay
// translatable field-for-field.
func (p Policy) Required() []Stage {
	var out []Stage
	if p.RequireAST {
		out = append(out, StageAST)
	}
	if p.RequireFormat {
		out = append(out, StageFormat)
	}
	if p.RequireLint {
		out = append(out, StageLint)
	}
	if p.RequireTypeCheck {
		out = append(out, StageTypeCheck)
	}
	if p.RequireBuild {
		out = append(out, StageBuild)
	}
	if p.RequireTests {
		out = append(out, StageTest)
	}
	out = append(out, StagePolicy) // Policy always runs (the project gate).
	return out
}

// Change is what Verify inspects: the task the verification belongs to (for
// the event's causal id) and the files the patch/tool touched. L8-002 carries
// just Task + Files; a fuller Observation shape (the patch summary, the tool's
// stdout) is threaded in L8-003.
type Change struct {
	Task  string   // event.TaskID; carried so the events name the right task
	Files []string // paths touched by the change
}

// Verdict is the aggregate result of a Verify run (File 09 §9.4): Pass is
// false iff a stage failed; Stage is the stage that failed (zero value on a
// clean pass); Severity is pass/warn/fail; Reason is the one-line summary the
// runtime/Reflection reads; Errors are the structured failures, Warnings the
// recorded-but-acceptable issues.
type Verdict struct {
	Pass     bool
	Stage    Stage
	Severity Severity
	Reason   string
	Errors   []Issue
	Warnings []Issue
}

// Deps wires the verify engine (extends the L8-001 Pipeline Deps with the
// event bus). Bus is *event.Bus (concrete, like exec/patch); a nil bus makes
// publishing a no-op (unit tests run Verify without one).
type Deps struct {
	Runner Runner
	FS     FS
	Bus    *event.Bus
}

// Engine runs the verification pipeline under a policy and emits the Verdict.
type Engine struct {
	pipeline *Pipeline
	bus      *event.Bus
}

// NewEngine wires an Engine from Deps.
func NewEngine(d Deps) *Engine {
	return &Engine{pipeline: NewPipeline(PipelineDeps{Runner: d.Runner, FS: d.FS}), bus: d.Bus}
}

// Verify runs the required stages for the Change under the Policy and returns
// the aggregate Verdict (File 09 §9.6). It plans the stages from the policy,
// runs each (skipping stages the policy doesn't require → SevSkip), publishes
// a per-stage `verification.stage` advisory, and on a fail publishes
// `verification.failed` naming the failing stage. A warning does NOT fail
// (§9.4.1) and does NOT publish verification.failed — the warning is recorded
// on the Verdict for the model to fix in a follow-up.
func (e *Engine) Verify(ctx context.Context, ch Change, pol Policy) Verdict {
	v := Verdict{Severity: SevPass}
	wanted := pol.Required()
	required := map[Stage]bool{}
	for _, s := range wanted {
		required[s] = true
	}

	// Walk the pipeline's 7 stages in canonical order; run the ones the
	// policy requires, skip the rest. A fail short-circuits.
	for _, st := range e.pipeline.stages {
		if !required[st.Name()] {
			skip := StageResult{Stage: st.Name(), Status: SevSkip, Detail: "not required by policy"}
			e.publishStage(ctx, ch.Task, skip)
			continue
		}
		r := st.Run(ctx, ch.Files)
		e.publishStage(ctx, ch.Task, r)
		switch r.Status {
		case SevFail:
			// First failure decides the Verdict and stops the chain.
			v.Pass = false
			v.Stage = r.Stage
			v.Severity = SevFail
			v.Reason = fmt.Sprintf("stage %s failed: %s", r.Stage, r.Detail)
			v.Errors = r.Issues
			e.publishFailed(ctx, ch.Task, v.Reason)
			return v
		case SevWarn:
			v.Warnings = append(v.Warnings, r.Issues...)
			if v.Severity == SevPass {
				v.Severity = SevWarn
			}
		}
	}
	v.Pass = true
	return v
}

// publishStage emits the per-stage advisory (File 09 §9.4.2). Best-effort: a
// nil bus (or a dropped event) is survivable — the Verdict is the source of
// truth, the event is the TUI's view of progress.
func (e *Engine) publishStage(ctx context.Context, task string, r StageResult) {
	if e.bus == nil {
		return
	}
	_ = e.bus.Publish(ctx, &event.VerificationStageEvent{
		Task:   event.TaskID(task),
		Stage:  r.Stage.String(),
		Status: r.Status.String(),
		Detail: r.Detail,
	})
}

// publishFailed emits the verification.failed event naming the failing stage.
func (e *Engine) publishFailed(ctx context.Context, task, reason string) {
	if e.bus == nil {
		return
	}
	_ = e.bus.Publish(ctx, &event.VerificationFailedEvent{
		Task:   event.TaskID(task),
		Reason: reason,
	})
}
