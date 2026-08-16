// Adapter wiring exec.Engine into runtime.Executor (Sprint 12 INT-001).
// The composition root owns this bridge so internal/runtime never imports exec.

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/baobao1044/yolo-code/internal/event"
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
	// registry is the set engine.Dispatch can actually run. It is here so the
	// two gate methods below can tell "the engine has never heard of this name"
	// apart from "the engine says it is harmless" — exec.Engine reports RiskLow
	// and NeedsApproval=false for both. A nil registry keeps the old behaviour
	// so the existing test constructions are unaffected; production sets it.
	registry *exec.Registry
}

// unroutedRisk is the class an unrecognised tool name is gated at. exec.Engine
// fails OPEN on a registry miss (RiskLow, no approval) and is right to: its own
// Dispatch rejects a miss before anything runs, so there is no class to state.
// That reasoning stops holding here, because this adapter adds routes the
// registry does not know about — "patch" today, whatever comes next tomorrow.
// So a name this adapter cannot account for is treated as the most dangerous
// thing it could turn out to be, not the least.
const unroutedRisk = exec.RiskHigh

// unrouted reports whether a call names something neither the exec registry nor
// this adapter can run. With no registry wired it always reports false — the
// adapter cannot know, and guessing "dangerous" for every tool would gate the
// read-only ones too.
func (a *execAdapter) unrouted(tool string) bool {
	if a.registry == nil || tool == "patch" {
		return false
	}
	_, ok := a.registry.Get(tool)
	return !ok
}

// patchRisk is the class the "patch" tool is gated at. patch writes
// model-chosen content to a model-chosen path, which is exactly what edit_file
// does, and edit_file is RiskHigh — so patch is too. The patch engine's AST
// check and checkpoint do not lower it: they make a bad write undoable, not
// unasked-for, and neither one can tell a wanted file from an unwanted one.
//
// Not RiskMedium, tempting as the "it's validated and reversible" argument is:
// medium and high are separately switchable (YOLO_AUTO_APPROVE_MEDIUM /
// _HIGH), so classing patch below edit_file would make the milder switch grant
// arbitrary repo writes while the stricter one still guarded the tool that
// does the same thing.
const patchRisk = exec.RiskHigh

func (a *execAdapter) NeedsApproval(call runtime.ToolCall) bool {
	if call.Tool == "patch" {
		return !patchAutoApproved()
	}
	if a.unrouted(call.Tool) {
		return !autoApprovedAt(unroutedRisk)
	}
	return a.engine.NeedsApproval(runtimeToExecCall(call))
}

// RiskOf satisfies runtime.RiskClassifier so the runtime's approval prompt can
// state the risk class. "patch" is not in the exec registry — it is routed to
// patch.Engine below — so the class is named here rather than left to the
// registry miss, which would report RiskLow for a full-file write.
func (a *execAdapter) RiskOf(call runtime.ToolCall) event.Risk {
	if call.Tool == "patch" {
		return patchRisk
	}
	if a.unrouted(call.Tool) {
		return unroutedRisk
	}
	return a.engine.RiskOf(runtimeToExecCall(call))
}

// patchAutoApproved reports whether the operator has opted patch's risk class
// out of the gate. It reads the same documented switch newExecEngine reads
// (--auto-approve sets it), because that env var — not the engine — is the
// source of truth both sides derive from: patch is not a registered tool, so
// the engine's own AutoApprove map cannot be consulted for it.
func patchAutoApproved() bool {
	return autoApprovedAt(patchRisk)
}

// autoApprovedAt reports whether the operator has opted a risk class out of the
// gate, for the tools this file classifies itself rather than the engine.
func autoApprovedAt(risk event.Risk) bool {
	auto := map[event.Risk]bool{
		exec.RiskMedium: envBool("YOLO_AUTO_APPROVE_MEDIUM"),
		exec.RiskHigh:   envBool("YOLO_AUTO_APPROVE_HIGH"),
	}
	return auto[risk]
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
		Tool:        call.Tool,
		Args:        call.Args,
		Reason:      call.Reason,
		Task:        call.Task,
		PreApproved: call.PreApproved,
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
