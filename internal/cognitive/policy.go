// Tool and Verification policies (File 07 §7.5). The Tool Policy gates the
// Planner's tool choices *before* the execution engine sees them — a denial
// returns to the model as a tool result explaining why, so it can choose a
// different tool. The Verification Policy defines what "done" means — which
// verification stages must pass and at what strictness.
//
// Sprint 3 (L6-005) implements the admit/deny gates. The policies are pure
// decisions (no side effects); Core.Think applies the Tool Policy between the
// Planner turn and the Turn it hands back, so a denied call never becomes work
// the runtime can dispatch. The denial surfaces to the model as a tool result
// with the reason (see enforceToolPolicy).

package cognitive

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ToolPolicy gates which tools a Planner may call (File 07 §7.5.1). Allowlist
// is the set of tool names the model may emit; every other name is denied.
//
// The set comes from what the model was actually offered: Core.New builds its
// policy from the tool names it is constructed with, and those same names are
// what become the provider's native tool definitions. So the allowlist cannot
// drift away from the advertisement — changing one changes the other, because
// they are one list (see DefaultTools).
//
// The struct used to carry a PerTaskAllow map and a MaxConcurrent bound. Both
// were dead, and PerTaskAllow could not do the job its doc described: it was
// keyed by tool name, not task, so "allow this tool for privileged tasks" was
// really "allow this tool for every task that passes a non-nil pointer". They
// are removed rather than wired — an override nobody configures is a way for
// the allowlist to be quietly wider than it reads.
type ToolPolicy struct {
	Allowlist map[string]bool
}

// NewToolPolicy builds a policy admitting exactly the given tool names.
func NewToolPolicy(allowed []string) *ToolPolicy {
	p := &ToolPolicy{Allowlist: map[string]bool{}}
	for _, a := range allowed {
		p.Allowlist[a] = true
	}
	return p
}

// Allow admits or denies a tool call (File 07 §7.5.1). A tool is allowed iff it
// is in Allowlist. A nil policy denies everything (default-deny posture, File
// 02).
//
// The denial is written for the model to read, not just for a log: it names the
// tool it refused AND lists the ones that exist. A model told only that its
// choice was refused guesses again — usually at another name it invented — and
// burns the turn; one handed the list picks from it.
func (p *ToolPolicy) Allow(call ToolCall) error {
	if p == nil {
		return fmt.Errorf("tool %q denied: no policy configured (default-deny)", call.Tool)
	}
	if p.Allowlist[call.Tool] {
		return nil
	}
	allowed := p.AllowedTools()
	if len(allowed) == 0 {
		return fmt.Errorf("tool %q is not allowed: no tools are available", call.Tool)
	}
	return fmt.Errorf("tool %q is not allowed: it is not one of the tools you were given. The available tools are: %s. Use one of those instead",
		call.Tool, strings.Join(allowed, ", "))
}

// AllowedTools returns the admitted tool names in sorted order — the list the
// denial quotes back to the model, and what a caller reports when it gives up
// on one that keeps guessing.
func (p *ToolPolicy) AllowedTools() []string {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.Allowlist))
	for n := range p.Allowlist {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
