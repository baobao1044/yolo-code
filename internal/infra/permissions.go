// Permissions model (File 13 §13.8). Permissions answer one question: may
// this action run? The answer is a policy lookup keyed by (action, resource),
// resolved before the action is dispatched. This is the layer the user
// pre-authorizes against when they choose a permission mode; the runtime's
// WAIT_TOOL/HITL flow (File 08 §8.5) is the runtime enforcement of the same
// policy.
//
// Modes (§13.8.2): Yolo (allow everything, P1 max), Auto (policy-driven,
// default), Ask (HITL on every action), Read-only (deny writes + network).
// The default auto-mode policy (§13.8.3) allows file.read, file.write in repo,
// denies file.write outside repo + net.request, asks on cmd.exec.
// Scoped elevation (§13.8.4): a denied action elevated by user approval
// appends an allow rule to the policy, so a subsequent identical request hits
// the fast path. Elevation never switches the global mode; it widens the
// policy.

package infra

// PermMode is the user's chosen permission mode (§13.8.2).
type PermMode string

const (
	PermYolo     PermMode = "yolo"      // allow everything (P1 max)
	PermAuto     PermMode = "auto"      // policy-driven (default)
	PermAsk      PermMode = "ask"       // HITL on every action
	PermReadOnly PermMode = "read-only" // deny all writes + network
)

// Action is the category of operation a permission decision covers (§13.8.2).
type Action string

const (
	ActFileRead   Action = "file.read"
	ActFileWrite  Action = "file.write"
	ActFileDelete Action = "file.delete"
	ActCmdExec    Action = "cmd.exec"
	ActNetRequest Action = "net.request"
	ActMCPTool    Action = "mcp.tool"
)

// Verdict is the outcome of a permission Check (§13.8.2): allow, deny, or ask.
type Verdict string

const (
	VerAllow Verdict = "allow"
	VerDeny  Verdict = "deny"
	VerAsk   Verdict = "ask"
)

// policyRule is one entry in the ordered policy table (first match wins,
// §13.8.2). actions is the set of Actions the rule covers; pattern is a glob on
// the resource (e.g. "/repo/**", "git status*"); verdict is the decision;
// reason is the human-readable justification (surfaced to HITL).
type policyRule struct {
	actions []Action
	pattern string
	verdict Verdict
	reason  string
}

// Permissions is the policy checker. mode is the user's chosen mode; policy is
// the ordered rule list (first match wins). The default auto-mode policy
// (§13.8.3) is loaded by newPermissions; Elevate appends to it.
type Permissions struct {
	mode   PermMode
	policy []policyRule
}

// newPermissions builds the checker from cfg. The default auto-mode policy
// (§13.8.3) is loaded when mode is auto (or unknown, which defaults to auto —
// conservative-but-not-paranoid). Yolo/Ask/Read-only ignore the policy.
func newPermissions(cfg PermissionsConfig) *Permissions {
	p := &Permissions{mode: PermMode(cfg.Mode)}
	if p.mode == "" || p.mode == PermAuto {
		p.policy = defaultAutoPolicy()
	}
	return p
}

// Check decides whether an action may proceed under the current mode (§13.8.2).
// Returns the verdict + a reason (for the HITL prompt). The mode switch short-
// circuits the policy; auto mode walks the ordered rules (first match wins),
// defaulting to ask when no rule matches (§13.8.2 conservative default).
func (p *Permissions) Check(a Action, resource string) (Verdict, string) {
	switch p.mode {
	case PermYolo:
		return VerAllow, "yolo mode"
	case PermReadOnly:
		if isWrite(a) || a == ActNetRequest {
			return VerDeny, "read-only mode"
		}
		return VerAllow, "read-only mode"
	case PermAsk:
		return VerAsk, "ask mode"
	default: // PermAuto (and unknown → auto)
		for _, r := range p.policy {
			if actionMatches(r.actions, a) && globMatch(r.pattern, resource) {
				return r.verdict, r.reason
			}
		}
		return VerAsk, "no explicit policy — default to ask"
	}
}

// Elevate appends an allow rule to the policy (§13.8.4 scoped elevation). The
// rule persists for the session (and, in a future config-backed form, to disk).
// Elevation never switches the global mode — it widens the policy, so an
// unrelated action still gets its normal verdict. The elevated rule is
// PREPENDED so it wins over the default policy (first-match-wins): a user who
// approved "https://api.example*" must not be re-prompted because a later
// default-deny rule for "" matched first.
func (p *Permissions) Elevate(r policyRule) error {
	p.policy = append([]policyRule{r}, p.policy...)
	return nil
}

// isWrite reports whether an action mutates state (§13.8.2 read-only gate).
func isWrite(a Action) bool {
	return a == ActFileWrite || a == ActFileDelete || a == ActCmdExec
}

// actionMatches reports whether the action is in the rule's action set.
func actionMatches(actions []Action, a Action) bool {
	for _, x := range actions {
		if x == a {
			return true
		}
	}
	return false
}

// globMatch reports whether resource matches a glob pattern. Supports "*" (any
// chars) and the literal prefix match. A "**" anywhere → prefix match up to the
// "**" (so "/repo**" matches "/repo", "/repo/a.go", "/repo/deep/n.go"). A
// trailing single "*" → prefix match (drop the star). A bare "*" or "" matches
// anything (the §13.8.3 "any resource" rows). A pattern with no "*" must equal
// the resource exactly (cmd.exec's literal command match).
func globMatch(pattern, resource string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// "**" anywhere → prefix match up to the "**" (checked before trailing "*"
	// so "/repo**" hits this branch, not the single-star branch).
	if i := indexOf(pattern, "**"); i >= 0 {
		prefix := pattern[:i]
		return hasPrefix(resource, prefix)
	}
	// Trailing single "*" → prefix match (drop the star).
	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return hasPrefix(resource, prefix)
	}
	return pattern == resource
}

// defaultAutoPolicy returns the §13.8.3 default auto-mode rules, ordered
// (first match wins). file.read any → allow; file.write in repo → allow;
// file.write outside repo → deny; cmd.exec read-only cmds → allow; cmd.exec
// mutating → ask; net.request any → deny. The order matters: the deny rules
// for file.write-outside + net.request must come AFTER the allow rules for
// the same actions but with different patterns, OR use glob precedence. The
// implementation below orders specific-allow before general-deny per action.
func defaultAutoPolicy() []policyRule {
	return []policyRule{
		{actions: []Action{ActFileRead}, pattern: "", verdict: VerAllow, reason: "reading is safe"},
		{actions: []Action{ActFileWrite, ActFileDelete}, pattern: "/repo**", verdict: VerAllow, reason: "inside workspace"},
		{actions: []Action{ActFileWrite, ActFileDelete}, pattern: "", verdict: VerDeny, reason: "path confinement — outside repo"},
		{actions: []Action{ActCmdExec}, pattern: "ls*", verdict: VerAllow, reason: "read-only allowlist"},
		{actions: []Action{ActCmdExec}, pattern: "cat*", verdict: VerAllow, reason: "read-only allowlist"},
		{actions: []Action{ActCmdExec}, pattern: "git status*", verdict: VerAllow, reason: "read-only allowlist"},
		{actions: []Action{ActCmdExec}, pattern: "git diff*", verdict: VerAllow, reason: "read-only allowlist"},
		{actions: []Action{ActCmdExec}, pattern: "git commit*", verdict: VerAsk, reason: "mutating — side effects"},
		{actions: []Action{ActCmdExec}, pattern: "rm*", verdict: VerAsk, reason: "mutating — side effects"},
		{actions: []Action{ActCmdExec}, pattern: "git push*", verdict: VerAsk, reason: "mutating — side effects"},
		{actions: []Action{ActNetRequest}, pattern: "", verdict: VerDeny, reason: "default-deny network"},
	}
}

// hasPrefix is a local strings.HasPrefix (kept local so this file doesn't add
// a strings import just for two call sites).
func hasPrefix(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// indexOf returns the index of substr in s, or -1 if absent (local
// strings.Index).
func indexOf(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
