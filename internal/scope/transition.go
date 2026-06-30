package scope

// Action is the kind of scope move SuggestTransition recommends (File 15 §15.x,
// W3 rules).
type Action int

const (
	ActionNoOp     Action = iota // stay
	ActionExpand                 // widen search (e.g. File → Repo)
	ActionContract               // narrow (e.g. Edit → Function)
	ActionStay                   // stay in current scope, repair
)

// String returns the uppercase name of the action.
func (a Action) String() string {
	switch a {
	case ActionNoOp:
		return "NOOP"
	case ActionExpand:
		return "EXPAND"
	case ActionContract:
		return "CONTRACT"
	case ActionStay:
		return "STAY"
	default:
		return "UNKNOWN"
	}
}

// Transition is a recommended scope move: the target Level, the Action, and a
// human-readable reason.
type Transition struct {
	TargetLevel Level
	Action      Action
	Reason      string
}

// Verdict is the scope-local shape of a verification verdict. Scope cannot
// import the verify layer (File 15 §15.15.2), so the runtime translates a
// verify verdict into this local struct before consulting the scope loop.
type Verdict struct {
	Pass   bool
	Stage  string
	Hint   string
	Reason string
}

// SuggestTransition applies the W3 expansion/contraction rules (File 15 §15.x)
// to recommend the next scope move given the current Level and a Verdict:
//
//   - Pass                    → NoOp at the current level (verification succeeded).
//   - test + missing_import   → Expand to LevelRepo (need broader context).
//   - compile failure         → Stay at the current level (syntax fix in place).
//   - otherwise               → Contract one level (narrow to re-examine).
//
// LevelTask is the floor: it cannot contract further, so a contract from
// LevelTask stays at LevelTask.
func SuggestTransition(current Level, v Verdict) Transition {
	if v.Pass {
		return Transition{TargetLevel: current, Action: ActionNoOp, Reason: "verify passed"}
	}
	if v.Stage == "test" && v.Hint == "missing_import" {
		return Transition{TargetLevel: LevelRepo, Action: ActionExpand, Reason: "need broader context"}
	}
	if v.Stage == "compile" {
		return Transition{TargetLevel: current, Action: ActionStay, Reason: "syntax fix"}
	}
	target := current - 1
	if target < LevelTask {
		target = LevelTask
	}
	return Transition{TargetLevel: target, Action: ActionContract, Reason: "narrow to re-examine"}
}
