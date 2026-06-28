// Adapter wiring verify.Engine into runtime.Verifier (Sprint 12 INT-002).

package main

import (
	"context"

	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
	"github.com/yolo-code/yolo/internal/verify"
)

// verifyAdapter implements runtime.Verifier using verify.Engine.
type verifyAdapter struct {
	engine *verify.Engine
}

func (a *verifyAdapter) Verify(ctx context.Context, obs runtime.Observation, task *session.Task, pol runtime.VerifyPolicy) (runtime.Verdict, error) {
	v := a.engine.Verify(ctx, verify.Change{
		Task:  string(task.ID),
		Files: obs.Files,
	}, runtimeToVerifyPolicy(pol))
	return verifyToRuntimeVerdict(v), nil
}

func runtimeToVerifyPolicy(pol runtime.VerifyPolicy) verify.Policy {
	return verify.Policy{
		RequireAST:       pol.RequireAST,
		RequireFormat:    pol.RequireFormat,
		RequireLint:      pol.RequireLint,
		RequireTypeCheck: pol.RequireTypeCheck,
		RequireBuild:     pol.RequireBuild,
		RequireTests:     pol.RequireTests,
		LintLevel:        pol.LintLevel,
		TestTimeout:      pol.TestTimeout,
	}
}

func verifyToRuntimeVerdict(v verify.Verdict) runtime.Verdict {
	return runtime.Verdict{
		Pass:     v.Pass,
		Stage:    v.Stage.String(),
		Severity: v.Severity.String(),
		Reason:   v.Reason,
	}
}
