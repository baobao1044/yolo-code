// role.go — agent roles with scoped tools (File 12 §12.2.1, §12.5.2).
//
// Five specialist roles (Planner/Coder/Reviewer/Tester/Researcher) plus the
// synthetic "user" role for pre-emption priority. Each role's tool set is
// enforced at the registry level (File 12 §12.2.1), not just by prompting:
//
//   - Planner, Reviewer: read-only (Read, Grep, Glob) — no Write, no Patch.
//   - Coder: the only writer (Read, Write, Patch, Bash).
//   - Tester: confined to test/build (Bash, Read).
//   - Researcher: read-only investigation (Read, Grep, Glob, Browser) — the
//     only role with Browser/HTTP.
//
// Spec gap (Decision 2 + Sprint 10 design): the real per-role exec.Engine
// construction (File 12 §12.7.1: "NewAgentTurn(role, …) builds a per-agent
// exec.Engine with only the tools the role may use") is the integration
// sprint. This sprint ships the tool-set map + RoleAllowed gate that the
// integration sprint's engine builder will consume; the AgentRunner seam
// (seam.go) carries the role so the engine can be scoped at the composition
// root. The Researcher role is defined here; its QuestionEvent/FindingsEvent
// delegation (File 12 §12.5.3) is deferred (the events aren't in the catalog).

package coord

// Role names a specialist agent (File 12 §12.2.1). The synthetic "user" role
// carries the highest pre-emption priority (File 12 §12.5.2).
type Role string

const (
	// RoleUser is the synthetic user role (highest priority — pre-empts all).
	RoleUser Role = "user"
	// RolePlanner decomposes the goal into a Plan (read-only tools).
	RolePlanner Role = "planner"
	// RoleCoder implements one todo (the only role with Write/Patch).
	RoleCoder Role = "coder"
	// RoleReviewer audits the Coder's diff (read-only tools).
	RoleReviewer Role = "reviewer"
	// RoleTester runs tests and reports (Bash + Read).
	RoleTester Role = "tester"
	// RoleResearcher investigates the codebase/external docs (read-only + Browser).
	RoleResearcher Role = "researcher"
)

// roleTools maps each role to its allowed tool set (File 12 §12.2.1). An
// unknown role has no entry → ScopedTools returns nil, RoleAllowed returns
// false (deny by default — never silently grant all tools to an undefined
// role).
var roleTools = map[Role][]string{
	RolePlanner:    {"Read", "Grep", "Glob"},
	RoleCoder:      {"Read", "Write", "Patch", "Bash"},
	RoleReviewer:   {"Read", "Grep", "Glob"},
	RoleTester:     {"Bash", "Read"},
	RoleResearcher: {"Read", "Grep", "Glob", "Browser"},
}

// rolePriority is the §12.5.2 pre-emption order: user > planner > coder >
// reviewer > tester > researcher (lower number = higher priority).
var rolePriority = map[Role]int{
	RoleUser:       0,
	RolePlanner:    1,
	RoleCoder:      2,
	RoleReviewer:   3,
	RoleTester:     4,
	RoleResearcher: 5,
}

// ScopedTools returns the allowed tool set for a role, or nil if the role is
// unknown. The integration sprint's exec.Engine builder consumes this to
// construct a role-scoped registry (File 12 §12.7.1).
func ScopedTools(r Role) []string {
	return roleTools[r]
}

// RoleAllowed reports whether a role may use a tool. Unknown roles deny
// everything (defense — never grant tools to an undefined role).
func RoleAllowed(r Role, tool string) bool {
	for _, t := range roleTools[r] {
		if t == tool {
			return true
		}
	}
	return false
}

// RolePriority returns the §12.5.2 pre-emption priority of a role (lower =
// higher priority). An unknown role has the lowest priority (max int) so it
// never pre-empts a known role.
func RolePriority(r Role) int {
	if p, ok := rolePriority[r]; ok {
		return p
	}
	return 1 << 30
}
