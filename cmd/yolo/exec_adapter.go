// Adapter wiring exec.Engine into runtime.Executor (Sprint 12 INT-001).
// The composition root owns this bridge so internal/runtime never imports exec.

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/patch"
	"github.com/baobao1044/yolo-code/internal/runtime"
)

// execAdapter implements runtime.Executor using exec.Engine. The "patch" tool
// is routed straight to patch.Engine so the planner can emit edits without
// exec importing patch (kept out of internal/exec per the import matrix).
type execAdapter struct {
	engine  *exec.Engine
	patcher *patch.Engine
}

func (a *execAdapter) NeedsApproval(call runtime.ToolCall) bool {
	if call.Tool == "patch" {
		return false
	}
	return a.engine.NeedsApproval(runtimeToExecCall(call))
}

func (a *execAdapter) Dispatch(ctx context.Context, call runtime.ToolCall) (runtime.Observation, error) {
	if call.Tool == "patch" {
		return a.dispatchPatch(ctx, call)
	}
	obs, err := a.engine.Dispatch(ctx, runtimeToExecCall(call))
	if err != nil {
		return runtime.Observation{}, err
	}
	robs := execToRuntimeObs(obs)
	robs.Tool = call.Tool
	return robs, nil
}

// dispatchPatch parses the JSON tool args {"path":..., "body":...} and applies
// the patch through patch.Engine. The returned Observation carries the touched
// files and the checkpoint name so the runtime can verify/rollback.
func (a *execAdapter) dispatchPatch(ctx context.Context, call runtime.ToolCall) (runtime.Observation, error) {
	if a.patcher == nil {
		return runtime.Observation{}, fmt.Errorf("patch engine not wired")
	}
	var args patchToolArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return runtime.Observation{}, fmt.Errorf("patch args: %w", err)
	}
	body := args.Body
	if args.Path != "" && body == "" {
		// Allow raw blocks passed as the args string when Body is omitted.
		body = string(call.Args)
	}

	var blocks []patch.Block
	var fullContent string
	if parsed, err := patch.ParseBlocks(body); err == nil {
		blocks = parsed
	} else {
		fullContent = body
	}

	res, err := a.patcher.Apply(ctx, patch.Op{
		Task:        string(call.Task),
		Seq:         1,
		Path:        args.Path,
		Blocks:      blocks,
		FullContent: fullContent,
	})
	if err != nil {
		return runtime.Observation{}, err
	}
	if !res.Accepted {
		return runtime.Observation{}, fmt.Errorf("patch rejected: %s", res.Reason)
	}

	var files []string
	for _, f := range res.Summary.Files {
		files = append(files, f.Path)
	}
	return runtime.Observation{
		FromPatch:  true,
		Files:      files,
		Checkpoint: res.Checkpoint,
		Summary:    fmt.Sprintf("+%d/-%d", res.Summary.Insertions, res.Summary.Deletions),
	}, nil
}

type patchToolArgs struct {
	Path string `json:"path"`
	Body string `json:"body"`
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
