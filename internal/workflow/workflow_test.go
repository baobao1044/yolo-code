package workflow

import (
	"errors"
	"testing"
)

// wfCase is one row of a per-workflow (phase, event) → (action, next phase) table.
type wfCase struct {
	name       string
	phase      string
	ev         WFEvent
	wantAction ActionKind
	wantPhase  string
	wantErr    bool
}

// runWFCases drives a table of wfCases through wf, asserting the returned
// action, the post-call phase, and the error sentinel.
func runWFCases(t *testing.T, wf Workflow, cases []wfCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &State{Phase: tc.phase}
			act, err := wf.Next(state, tc.ev)
			if tc.wantErr {
				if !errors.Is(err, ErrNoAction) {
					t.Fatalf("err = %v, want ErrNoAction", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if act.Kind != tc.wantAction {
				t.Errorf("Action.Kind = %q, want %q", act.Kind, tc.wantAction)
			}
			if state.Phase != tc.wantPhase {
				t.Errorf("Phase = %q, want %q", state.Phase, tc.wantPhase)
			}
		})
	}
}

// TestBugFixWorkflow_Next walks the bugfix machine, including the key branches:
// verify_pass→submit, verify_fail logic→multi_hypothesis, compile fail→repair
// loop, timeout→degrade_model, and the LOCALIZE verify_fail→repair dispatch.
func TestBugFixWorkflow_Next(t *testing.T) {
	wf := BugFixWorkflow{}
	runWFCases(t, wf, []wfCase{
		// Entry: any event kicks off localization.
		{name: "entry localizes", phase: "", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionLocalize, wantPhase: "LOCALIZE"},
		{name: "entry localize on fail", phase: "", ev: WFEvent{Kind: EventVerifyFail}, wantAction: ActionLocalize, wantPhase: "LOCALIZE"},

		// LOCALIZE branches.
		{name: "localize verify_fail repairs", phase: "LOCALIZE", ev: WFEvent{Kind: EventVerifyFail}, wantAction: ActionRepair, wantPhase: "REPAIR"},
		{name: "localize context_needed stays", phase: "LOCALIZE", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "LOCALIZE"},
		{name: "localize timeout degrades", phase: "LOCALIZE", ev: WFEvent{Kind: EventTimeout}, wantAction: ActionDegrade, wantPhase: "LOCALIZE"},
		{name: "localize verify_pass generates", phase: "LOCALIZE", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionGenerate, wantPhase: "REPAIR"},

		// REPAIR always advances to verify.
		{name: "repair advances to verify", phase: "REPAIR", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionVerify, wantPhase: "VALIDATE"},
		{name: "repair advances on fail", phase: "REPAIR", ev: WFEvent{Kind: EventVerifyFail}, wantAction: ActionVerify, wantPhase: "VALIDATE"},

		// VALIDATE key branches.
		{name: "validate pass submits", phase: "VALIDATE", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionSubmit, wantPhase: "VALIDATE"},
		{name: "validate compile fail repairs", phase: "VALIDATE", ev: WFEvent{Kind: EventVerifyFail, Payload: "compile error: undefined: foo"}, wantAction: ActionRepair, wantPhase: "REPAIR"},
		{name: "validate logic fail multi_hyp", phase: "VALIDATE", ev: WFEvent{Kind: EventVerifyFail, Payload: "logic error: off by one"}, wantAction: ActionMultiHyp, wantPhase: "REPAIR"},
		{name: "validate timeout degrades", phase: "VALIDATE", ev: WFEvent{Kind: EventTimeout}, wantAction: ActionDegrade, wantPhase: "VALIDATE"},
		{name: "validate context_needed re-localizes", phase: "VALIDATE", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "LOCALIZE"},

		// Unknown phase.
		{name: "unknown phase errors", phase: "BOGUS", ev: WFEvent{Kind: EventVerifyPass}, wantErr: true},
	})
}

// TestFeatureWorkflow_Next walks the feature machine: design → decompose →
// implement → verify, with context_needed→localize, verify_pass→submit, and
// verify_fail→scope_contract then re-implement.
func TestFeatureWorkflow_Next(t *testing.T) {
	wf := FeatureWorkflow{}
	runWFCases(t, wf, []wfCase{
		// DESIGN (entry).
		{name: "design generates to decompose", phase: "", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionGenerate, wantPhase: "DECOMPOSE"},
		{name: "design context_needed localizes", phase: "DESIGN", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "DESIGN"},

		// DECOMPOSE.
		{name: "decompose generates to implement", phase: "DECOMPOSE", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionGenerate, wantPhase: "IMPLEMENT"},
		{name: "decompose context_needed localizes", phase: "DECOMPOSE", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "DECOMPOSE"},

		// IMPLEMENT.
		{name: "implement generates to verify", phase: "IMPLEMENT", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionGenerate, wantPhase: "VERIFY"},
		{name: "implement context_needed localizes", phase: "IMPLEMENT", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "IMPLEMENT"},

		// VERIFY key branches.
		{name: "verify pass submits", phase: "VERIFY", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionSubmit, wantPhase: "VERIFY"},
		{name: "verify fail contracts to implement", phase: "VERIFY", ev: WFEvent{Kind: EventVerifyFail}, wantAction: ActionContract, wantPhase: "IMPLEMENT"},
		{name: "verify context_needed localizes", phase: "VERIFY", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "VERIFY"},
		{name: "verify timeout degrades", phase: "VERIFY", ev: WFEvent{Kind: EventTimeout}, wantAction: ActionDegrade, wantPhase: "VERIFY"},

		// Unknown phase.
		{name: "unknown phase errors", phase: "BOGUS", ev: WFEvent{Kind: EventVerifyPass}, wantErr: true},
	})
}

// TestRefactorWorkflow_Next walks the refactor machine: analyze → transform →
// behavior-preserving verify, with verify_pass→submit and verify_fail
// (non-behavior-preserving) → scope_contract then re-transform.
func TestRefactorWorkflow_Next(t *testing.T) {
	wf := RefactorWorkflow{}
	runWFCases(t, wf, []wfCase{
		// ANALYZE (entry).
		{name: "analyze localizes to transform", phase: "", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionLocalize, wantPhase: "TRANSFORM"},
		{name: "analyze context_needed localizes", phase: "ANALYZE", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "ANALYZE"},

		// TRANSFORM.
		{name: "transform generates to verify", phase: "TRANSFORM", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionGenerate, wantPhase: "VERIFY"},
		{name: "transform context_needed localizes", phase: "TRANSFORM", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "TRANSFORM"},

		// VERIFY key branches.
		{name: "verify pass submits", phase: "VERIFY", ev: WFEvent{Kind: EventVerifyPass}, wantAction: ActionSubmit, wantPhase: "VERIFY"},
		{name: "verify fail contracts to transform", phase: "VERIFY", ev: WFEvent{Kind: EventVerifyFail}, wantAction: ActionContract, wantPhase: "TRANSFORM"},
		{name: "verify context_needed localizes", phase: "VERIFY", ev: WFEvent{Kind: EventContextNeeded}, wantAction: ActionLocalize, wantPhase: "VERIFY"},
		{name: "verify timeout degrades", phase: "VERIFY", ev: WFEvent{Kind: EventTimeout}, wantAction: ActionDegrade, wantPhase: "VERIFY"},

		// Unknown phase.
		{name: "unknown phase errors", phase: "BOGUS", ev: WFEvent{Kind: EventVerifyPass}, wantErr: true},
	})
}
