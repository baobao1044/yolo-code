// Adapter wiring exec.Engine into runtime.Executor (Sprint 12 INT-001).
// The composition root owns this bridge so internal/runtime never imports exec.

package main

import (
	"context"

	"github.com/yolo-code/yolo/internal/exec"
	"github.com/yolo-code/yolo/internal/runtime"
)

// execAdapter implements runtime.Executor using exec.Engine.
type execAdapter struct {
	engine *exec.Engine
}

func (a *execAdapter) NeedsApproval(call runtime.ToolCall) bool {
	return a.engine.NeedsApproval(runtimeToExecCall(call))
}

func (a *execAdapter) Dispatch(ctx context.Context, call runtime.ToolCall) (runtime.Observation, error) {
	obs, err := a.engine.Dispatch(ctx, runtimeToExecCall(call))
	if err != nil {
		return runtime.Observation{}, err
	}
	return execToRuntimeObs(obs), nil
}

func runtimeToExecCall(call runtime.ToolCall) exec.ToolCall {
	return exec.ToolCall{
		Tool:   call.Tool,
		Args:   call.Args,
		Reason: call.Reason,
		Task:   call.Task,
	}
}

func execToRuntimeObs(obs exec.Observation) runtime.Observation {
	return runtime.Observation{
		Payload: []byte(obs.Stdout),
		Files:   obs.Files,
		Stdout:  obs.Stdout,
		Summary: obs.Summary,
	}
}
