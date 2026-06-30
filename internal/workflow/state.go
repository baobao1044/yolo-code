package workflow

// State is a workflow's working state: the phase it is in, the hypotheses under
// consideration, the number of candidate patches, and the repair retry count.
// The Phase string drives the per-type state machines in workflow.go (it is the
// FSM's "current state"); the other fields are bookkeeping the drive loop reads
// and the workflows may consult. Slice fields are append-only views owned by the
// caller; workflows treat them as read-only.
type State struct {
	Phase      string   // current phase name, e.g. "LOCALIZE", "REPAIR", "VALIDATE"
	Hypotheses []string // active hypotheses under test
	Candidates int      // number of candidate patches under consideration
	Retries    int      // repair-loop retries so far
}

// EventKind is the trigger a workflow reacts to. It is distinct from a bus event
// topic: the drive loop translates bus events and layer results into these
// kinds before asking a workflow for the next action.
type EventKind string

const (
	// EventVerifyPass is a verify stage passing — the workflow may advance or
	// submit.
	EventVerifyPass EventKind = "verify_pass"
	// EventVerifyFail is a verify stage failing — the workflow branches on the
	// payload to decide between repair, multi-hypothesis, or scope contraction.
	EventVerifyFail EventKind = "verify_fail"
	// EventContextNeeded signals the workflow needs more context before it can
	// proceed — the usual response is to localize.
	EventContextNeeded EventKind = "context_needed"
	// EventTimeout signals a stage exceeded its budget — the usual response is
	// to degrade the model.
	EventTimeout EventKind = "timeout"
)

// WFEvent is the event the drive loop hands to a workflow's Next. The Kind
// drives branching; the Payload carries a short diagnostic (e.g. "compile
// error: undefined: foo") that disambiguates sub-cases such as compile vs.
// logic failures.
type WFEvent struct {
	Kind    EventKind
	Payload string
}

// ActionKind is the kind of action a workflow returns from Next.
type ActionKind string

const (
	// ActionLocalize narrows the search to where the work must happen.
	ActionLocalize ActionKind = "localize"
	// ActionGenerate produces a candidate patch (or, for feature/refactor, the
	// design/decomposition/transform artifact).
	ActionGenerate ActionKind = "generate_patch"
	// ActionMultiHyp explores an alternative hypothesis after a logic failure.
	ActionMultiHyp ActionKind = "multi_hypothesis"
	// ActionVerify runs the verification stage.
	ActionVerify ActionKind = "verify"
	// ActionRepair enters a repair loop after a compile failure.
	ActionRepair ActionKind = "repair_loop"
	// ActionContract contracts the scope before retrying.
	ActionContract ActionKind = "scope_contract"
	// ActionSubmit finalizes the work — verify passed.
	ActionSubmit ActionKind = "submit"
	// ActionDegrade steps the model down a rung after a timeout.
	ActionDegrade ActionKind = "degrade_model"
)

// Action is what a workflow tells the drive loop to do next. The Kind selects
// the action; the Note is a short human-readable reason for the transcript.
type Action struct {
	Kind ActionKind
	Note string
}
