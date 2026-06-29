// Adapter wiring workflow.Engine into runtime.WorkflowEngine (Dynamic Workflow).
// The runtime may not import internal/workflow, so this adapter lives in the
// composition root and translates the runtime-local mirror types
// (runtime.WFState / WFEvent / WFEventKind / WFAction / WFActionKind) to and
// from the real workflow.* types.

package main

import (
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/workflow"
)

// workflowAdapter implements runtime.WorkflowEngine using a workflow.Engine.
type workflowAdapter struct {
	engine *workflow.Engine
}

// newWorkflowAdapter builds a workflow engine backed by the shared event bus.
// The bus is nil-safe (workflow.Engine.Select never panics on a nil bus).
func newWorkflowAdapter(bus *event.Bus) *workflowAdapter {
	return &workflowAdapter{engine: workflow.New(bus)}
}

// runtimeEventToWorkflow maps a runtime.WFEventKind to a workflow.EventKind.
func runtimeEventToWorkflow(k runtime.WFEventKind) workflow.EventKind {
	switch k {
	case runtime.WFEventVerifyPass:
		return workflow.EventVerifyPass
	case runtime.WFEventContextNeeded:
		return workflow.EventContextNeeded
	case runtime.WFEventTimeout:
		return workflow.EventTimeout
	default:
		return workflow.EventVerifyFail
	}
}

func workflowActionToRuntime(a workflow.Action) runtime.WFAction {
	switch a.Kind {
	case workflow.ActionLocalize:
		return runtime.WFAction{Kind: runtime.WFActionLocalize, Note: a.Note}
	case workflow.ActionGenerate:
		return runtime.WFAction{Kind: runtime.WFActionGenerate, Note: a.Note}
	case workflow.ActionMultiHyp:
		return runtime.WFAction{Kind: runtime.WFActionMultiHyp, Note: a.Note}
	case workflow.ActionVerify:
		return runtime.WFAction{Kind: runtime.WFActionVerify, Note: a.Note}
	case workflow.ActionRepair:
		return runtime.WFAction{Kind: runtime.WFActionRepair, Note: a.Note}
	case workflow.ActionContract:
		return runtime.WFAction{Kind: runtime.WFActionContract, Note: a.Note}
	case workflow.ActionDegrade:
		return runtime.WFAction{Kind: runtime.WFActionDegrade, Note: a.Note}
	default:
		return runtime.WFAction{Kind: runtime.WFActionSubmit, Note: a.Note}
	}
}

func (a *workflowAdapter) Next(goal string, state *runtime.WFState, ev runtime.WFEvent) (runtime.WFAction, error) {
	wfState := &workflow.State{
		Phase:      string(state.Phase),
		Hypotheses: state.Hypotheses,
		Candidates: state.Candidates,
		Retries:    state.Retries,
	}
	wfEv := workflow.WFEvent{Kind: runtimeEventToWorkflow(ev.Kind), Payload: ev.Payload}
	act, err := a.engine.Next(goal, wfState, wfEv)
	if err != nil {
		return runtime.WFAction{}, err
	}
	return workflowActionToRuntime(act), nil
}
