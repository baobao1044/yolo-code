// Types for the verification pipeline (File 09 §9.4). A Stage is one step of
// the AST→Format→Lint→TypeCheck→Build→Tests→Policy chain (§9.2); a StageResult
// is what one stage emits ("a verdict"); Severity is pass/warn/fail/skip. An
// Issue is one structured problem a stage found (a linter rule id, a line, a
// message) — the shape Reflection cites (File 07 §7.3).
//
// The Severity set carries a "skip" value used by L8-004's stage skip rules:
// a stage that didn't run (no Go file → skip Build) still appears in the trace
// as a skipped result, so the transcript shows *why* it didn't run, not just
// that it didn't.

package verify

// Stage is one pipeline step (File 09 §9.2/§9.4). The order of the constants
// is the canonical pipeline order — AST first (cheapest), Policy last (the
// project-specific gate).
type Stage int

const (
	StageAST Stage = iota
	StageFormat
	StageLint
	StageTypeCheck
	StageBuild
	StageTest
	StagePolicy
)

// stageNames maps each Stage to its canonical name, in constant order. Used by
// String so the transcript and events name stages consistently.
var stageNames = [...]string{
	"ast", "format", "lint", "typecheck", "build", "tests", "policy",
}

// String returns the canonical stage name ("ast", "lint", …).
func (s Stage) String() string {
	if s < 0 || int(s) >= len(stageNames) {
		return "unknown"
	}
	return stageNames[s]
}

// Severity is a stage's verdict level (File 09 §9.4). Pass = the stage is
// clean; Warn = acceptable but imperfect (a slow build, an unformatted file);
// Fail = the change broke something → rollback path; Skip = the stage did not
// run (no tool for the language, or the policy didn't require it).
type Severity int

const (
	SevPass Severity = iota
	SevWarn
	SevFail
	SevSkip
)

var sevNames = [...]string{"pass", "warn", "fail", "skip"}

// String returns "pass"/"warn"/"fail"/"skip" — the strings the verification
// events carry (File 09 §9.4.2) and the transcript prints.
func (s Severity) String() string {
	if s < 0 || int(s) >= len(sevNames) {
		return "unknown"
	}
	return sevNames[s]
}

// Issue is one structured problem a stage found (File 09 §9.4): which file,
// which line, the linter/rule code, and a human message. Reflection (File 07
// §7.3) cites these to diagnose root cause.
type Issue struct {
	Path    string
	Line    int
	Code    string // linter/rule id, e.g. "vet" or "no-vendor"
	Message string
}

// StageResult is one stage's verdict (File 09 §9.4/§9.6): the stage, its
// status, a one-line detail, and the structured issues it found. L8-001's
// pipeline returns a slice of these — one per stage that ran, in order.
//
// Cached separates the two kinds of SevSkip. A plain skip measured nothing (no
// tool for the language, not required by the policy, no files); a cached skip
// (L8-005) is a *re-used* measurement — the file's content hash matched a
// previous pass. The engine's no-signal rule counts the second as verification
// signal and the first as none, so a re-verify of unchanged files still
// certifies while an empty run doesn't.
type StageResult struct {
	Stage  Stage
	Status Severity
	Detail string
	Issues []Issue
	Cached bool
}
