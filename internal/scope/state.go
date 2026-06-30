package scope

// State is the controller's working state at the current Level: which files and
// functions are in scope, and the active hypotheses under test. Slice fields are
// append-only views owned by the controller; callers must treat them as
// read-only.
type State struct {
	Level        Level
	AllowedFiles []string
	AllowedFuncs []string
	Hypotheses   []string
}
