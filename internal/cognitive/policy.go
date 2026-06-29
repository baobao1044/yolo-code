// Tool and Verification policies (File 07 §7.5). The Tool Policy gates the
// Planner's tool choices *before* the execution engine sees them — a denial
// returns to the model as a tool result explaining why, so it can choose a
// different tool. The Verification Policy defines what "done" means — which
// verification stages must pass and at what strictness.
//
// Sprint 3 (L6-005) implements the admit/deny gates. The policies are pure
// decisions (no side effects); the runtime applies them between the Planner
// turn and tool dispatch. A denied tool call surfaces to the model as a tool
// result with the denial reason; the runtime wires that path in a later
// sprint.

package cognitive

import (
	"fmt"
	"time"

	"github.com/baobao1044/yolo-code/internal/session"
)

// ToolPolicy gates which tools a Planner may call (File 07 §7.5.1). Allowlist
// is the global set; PerTaskAllow overrides per task (e.g. a privileged task
// may call a tool the default policy denies). MaxConcurrent bounds parallel
// tools per turn (default 1 — Sprint 3's drive loop is single-tool-per-turn).
type ToolPolicy struct {
	Allowlist     map[string]bool
	PerTaskAllow  map[string]bool
	MaxConcurrent int
}

// NewToolPolicy builds a policy with the given allowed tool names and a
// MaxConcurrent of 1 (the Sprint 3 default — one tool per turn).
func NewToolPolicy(allowed []string) *ToolPolicy {
	p := &ToolPolicy{Allowlist: map[string]bool{}, MaxConcurrent: 1}
	for _, a := range allowed {
		p.Allowlist[a] = true
	}
	return p
}

// Allow admits or denies a tool call (File 07 §7.5.1). A tool is allowed iff
// it is in Allowlist OR in PerTaskAllow for the given task's id. A denial
// returns an error naming the tool and why; the runtime surfaces it to the
// model. A nil policy denies everything (default-deny posture, File 02).
func (p *ToolPolicy) Allow(call ToolCall, task *session.Task) error {
	if p == nil {
		return fmt.Errorf("tool %q denied: no policy configured (default-deny)", call.Tool)
	}
	if p.Allowlist[call.Tool] {
		return nil
	}
	if task != nil && p.PerTaskAllow[call.Tool] {
		return nil
	}
	return fmt.Errorf("tool %q not allowed", call.Tool)
}

// VerificationPolicy defines what "done" means for a task (File 07 §7.5.2):
// which verification stages must pass and at what strictness. A quick
// "explain this function" task uses a lighter policy (AST only); a "refactor
// and ship" task uses the full policy including tests.
type VerificationPolicy struct {
	RequireAST       bool
	RequireFormat    bool
	RequireLint      bool
	RequireTypeCheck bool
	RequireBuild     bool
	RequireTests     bool
	LintLevel        string // "error" | "warning"
	TestTimeout      time.Duration
}

// DefaultPolicy returns the §7.5.2 default: all stages required except tests,
// lint at error level, 30s test timeout.
func DefaultPolicy() VerificationPolicy {
	return VerificationPolicy{
		RequireAST:       true,
		RequireFormat:    true,
		RequireLint:      true,
		RequireTypeCheck: true,
		RequireBuild:     true,
		RequireTests:     false,
		LintLevel:        "error",
		TestTimeout:      30 * time.Second,
	}
}

// LightPolicy returns a minimal policy for read-only/explain tasks (File 07
// §7.5.2): AST only, no build/lint/tests. Used when the task doesn't modify
// code, so "done" just means the answer is syntactically coherent.
func LightPolicy() VerificationPolicy {
	return VerificationPolicy{
		RequireAST:       true,
		RequireFormat:    false,
		RequireLint:      false,
		RequireTypeCheck: false,
		RequireBuild:     false,
		RequireTests:     false,
		LintLevel:        "warning",
		TestTimeout:      0,
	}
}

// RequiredStages returns the names of the stages this policy requires pass, in
// canonical order (File 09's stage order). Used by the runtime to decide which
// stages to run and by tests to assert the policy shape.
func (v VerificationPolicy) RequiredStages() []string {
	var stages []string
	if v.RequireAST {
		stages = append(stages, "ast")
	}
	if v.RequireFormat {
		stages = append(stages, "format")
	}
	if v.RequireLint {
		stages = append(stages, "lint")
	}
	if v.RequireTypeCheck {
		stages = append(stages, "typecheck")
	}
	if v.RequireBuild {
		stages = append(stages, "build")
	}
	if v.RequireTests {
		stages = append(stages, "tests")
	}
	return stages
}
