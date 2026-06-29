package workflow

import (
	"errors"
	"strings"
)

// Workflow is one per-task-type state machine. Next resolves the next Action
// for the given State and event, mutating state.Phase to advance the machine
// (or leaving it on ActionSubmit). A workflow holds no state of its own — all
// progression lives in the State the drive loop threads through it, so a single
// zero-value Workflow serves every task of its type.
type Workflow interface {
	// Name is the workflow's identifier (also the registry key).
	Name() string
	// Next returns the action to take and advances state.Phase. It returns
	// ErrNoAction when (state.Phase, ev.Kind) resolves to no edge.
	Next(state *State, ev WFEvent) (Action, error)
}

// ErrNoAction means no edge resolves from the current phase on the given event
// — a recoverable dispatch miss (an unknown phase, or an event the phase does
// not handle). The drive loop logs and drops these rather than treating them as
// hard failures.
var ErrNoAction = errors.New("workflow: no action for phase and event")

// Phase names. Each workflow interprets Phase against its own ladder; the
// strings are the documented contract the drive loop and tests key on.
const (
	// Bugfix phases.
	phaseBugLocalize = "LOCALIZE"
	phaseBugRepair   = "REPAIR"
	phaseBugValidate = "VALIDATE"

	// Feature phases.
	phaseFeatDesign    = "DESIGN"
	phaseFeatDecompose = "DECOMPOSE"
	phaseFeatImplement = "IMPLEMENT"
	phaseFeatVerify    = "VERIFY"

	// Refactor phases.
	phaseRefAnalyze   = "ANALYZE"
	phaseRefTransform = "TRANSFORM"
	phaseRefVerify    = "VERIFY"
)

// isCompileFailure reports whether a verify_fail payload describes a compile
// (build) failure rather than a logic/behavior failure. Bugfix branches on this:
// a compile failure enters a repair loop; a logic failure explores a new
// hypothesis. The check is a case-insensitive substring match on "compile".
func isCompileFailure(payload string) bool {
	return strings.Contains(strings.ToLower(payload), "compile")
}

// BugFixWorkflow drives the localize → repair → validate pipeline. On a
// verify_pass it submits; on a verify_fail it branches by payload — a compile
// failure enters a repair loop, a logic failure explores a new hypothesis; on a
// timeout it degrades the model; on context_needed it re-localizes.
type BugFixWorkflow struct{}

// Name returns "bugfix".
func (BugFixWorkflow) Name() string { return "bugfix" }

// Next advances the bugfix machine. The empty phase kicks off localization;
// LOCALIZE branches on the event (verify_fail → repair loop, context_needed →
// stay localizing, else → generate a candidate patch); REPAIR always moves to
// verify; VALIDATE holds the verify/submit/repair/multi-hypothesis/degrade
// branches.
func (BugFixWorkflow) Next(state *State, ev WFEvent) (Action, error) {
	switch state.Phase {
	case "": // not started — kick off localization.
		state.Phase = phaseBugLocalize
		return Action{Kind: ActionLocalize, Note: "begin localization"}, nil

	case phaseBugLocalize:
		switch ev.Kind {
		case EventVerifyFail:
			// The localization hypothesis failed — enter a repair loop.
			state.Phase = phaseBugRepair
			return Action{Kind: ActionRepair, Note: "localization failed; repair loop"}, nil
		case EventContextNeeded:
			// Stay localizing until the context is gathered.
			return Action{Kind: ActionLocalize, Note: "need more context to localize"}, nil
		case EventTimeout:
			return Action{Kind: ActionDegrade, Note: "localize timeout; degrade model"}, nil
		default:
			// Localized successfully — generate a candidate patch and move to repair.
			state.Phase = phaseBugRepair
			return Action{Kind: ActionGenerate, Note: "localized; generate candidate patch"}, nil
		}

	case phaseBugRepair:
		// Patch generated — move to validation.
		state.Phase = phaseBugValidate
		return Action{Kind: ActionVerify, Note: "patch ready; verify"}, nil

	case phaseBugValidate:
		switch ev.Kind {
		case EventVerifyPass:
			return Action{Kind: ActionSubmit, Note: "verify passed; submit"}, nil
		case EventVerifyFail:
			if isCompileFailure(ev.Payload) {
				state.Phase = phaseBugRepair
				return Action{Kind: ActionRepair, Note: "compile failure; repair loop"}, nil
			}
			state.Phase = phaseBugRepair
			return Action{Kind: ActionMultiHyp, Note: "logic error; multi-hypothesis"}, nil
		case EventTimeout:
			return Action{Kind: ActionDegrade, Note: "verify timeout; degrade model"}, nil
		case EventContextNeeded:
			state.Phase = phaseBugLocalize
			return Action{Kind: ActionLocalize, Note: "verify needs context; re-localize"}, nil
		default:
			return Action{Kind: ActionVerify, Note: "verify"}, nil
		}

	default:
		return Action{}, ErrNoAction
	}
}

// FeatureWorkflow drives the design → decompose → implement → verify pipeline.
// On context_needed it localizes (staying in the current phase); on verify_pass
// it submits; on verify_fail it contracts the scope and returns to implement.
type FeatureWorkflow struct{}

// Name returns "feature".
func (FeatureWorkflow) Name() string { return "feature" }

// Next advances the feature machine. The empty phase and DESIGN are the entry:
// design produces the artifact and moves to decompose. DECOMPOSE and IMPLEMENT
// each produce and advance; on context_needed any phase localizes without
// advancing. VERIFY holds the submit/scope-contract branches.
func (FeatureWorkflow) Next(state *State, ev WFEvent) (Action, error) {
	switch state.Phase {
	case "", phaseFeatDesign:
		if ev.Kind == EventContextNeeded {
			state.Phase = phaseFeatDesign
			return Action{Kind: ActionLocalize, Note: "design: need more context"}, nil
		}
		state.Phase = phaseFeatDecompose
		return Action{Kind: ActionGenerate, Note: "design ready; decompose"}, nil

	case phaseFeatDecompose:
		if ev.Kind == EventContextNeeded {
			return Action{Kind: ActionLocalize, Note: "decompose: need more context"}, nil
		}
		state.Phase = phaseFeatImplement
		return Action{Kind: ActionGenerate, Note: "decomposed; implement"}, nil

	case phaseFeatImplement:
		if ev.Kind == EventContextNeeded {
			return Action{Kind: ActionLocalize, Note: "implement: need more context"}, nil
		}
		state.Phase = phaseFeatVerify
		return Action{Kind: ActionGenerate, Note: "implemented; verify"}, nil

	case phaseFeatVerify:
		switch ev.Kind {
		case EventVerifyPass:
			return Action{Kind: ActionSubmit, Note: "verify passed; submit"}, nil
		case EventVerifyFail:
			state.Phase = phaseFeatImplement
			return Action{Kind: ActionContract, Note: "verify failed; scope contract then re-implement"}, nil
		case EventContextNeeded:
			return Action{Kind: ActionLocalize, Note: "verify needs context"}, nil
		case EventTimeout:
			return Action{Kind: ActionDegrade, Note: "verify timeout; degrade model"}, nil
		default:
			return Action{Kind: ActionVerify, Note: "verify"}, nil
		}

	default:
		return Action{}, ErrNoAction
	}
}

// RefactorWorkflow drives the analyze → transform → behavior-preserving verify
// pipeline. On verify_pass it submits; on verify_fail (non-behavior-preserving)
// it contracts the scope and returns to transform.
type RefactorWorkflow struct{}

// Name returns "refactor".
func (RefactorWorkflow) Name() string { return "refactor" }

// Next advances the refactor machine. The empty phase and ANALYZE localize the
// code to refactor and move to transform; TRANSFORM produces the rewrite and
// moves to verify; VERIFY holds the submit/scope-contract branches.
func (RefactorWorkflow) Next(state *State, ev WFEvent) (Action, error) {
	switch state.Phase {
	case "", phaseRefAnalyze:
		if ev.Kind == EventContextNeeded {
			state.Phase = phaseRefAnalyze
			return Action{Kind: ActionLocalize, Note: "analyze: need more context"}, nil
		}
		state.Phase = phaseRefTransform
		return Action{Kind: ActionLocalize, Note: "analyze the code to refactor"}, nil

	case phaseRefTransform:
		if ev.Kind == EventContextNeeded {
			return Action{Kind: ActionLocalize, Note: "transform: need more context"}, nil
		}
		state.Phase = phaseRefVerify
		return Action{Kind: ActionGenerate, Note: "transformed; behavior-preserving verify"}, nil

	case phaseRefVerify:
		switch ev.Kind {
		case EventVerifyPass:
			return Action{Kind: ActionSubmit, Note: "behavior preserved; submit"}, nil
		case EventVerifyFail:
			state.Phase = phaseRefTransform
			return Action{Kind: ActionContract, Note: "non-behavior-preserving; scope contract then re-transform"}, nil
		case EventContextNeeded:
			return Action{Kind: ActionLocalize, Note: "verify needs context"}, nil
		case EventTimeout:
			return Action{Kind: ActionDegrade, Note: "verify timeout; degrade model"}, nil
		default:
			return Action{Kind: ActionVerify, Note: "verify behavior preservation"}, nil
		}

	default:
		return Action{}, ErrNoAction
	}
}
