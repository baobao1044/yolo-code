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

import (
	"os"
	"path/filepath"
)

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
// conservative-but-not-paranoid). Yolo/Ask/Read-only ignore the policy. The
// workspace root the write-allow rule is scoped to comes from cfg.Root
// (resolveRoot); it used to be the hardcoded literal "/repo", which is not the
// repository root on any real machine.
func newPermissions(cfg PermissionsConfig) *Permissions {
	p := &Permissions{mode: PermMode(cfg.Mode)}
	if p.mode == "" || p.mode == PermAuto {
		p.policy = defaultAutoPolicy(resolveRoot(cfg.Root))
	}
	return p
}

// resolveRoot returns the workspace root the default policy confines writes to.
// A configured root is cleaned and used as-is; an empty one falls back to the
// process working directory, which is what every cmd/yolo entry point already
// treats as the repo. If even that is unavailable the root stays empty and
// defaultAutoPolicy omits the in-workspace allow rule entirely, so writes fall
// through to the catch-all deny — fail closed, never fail open.
func resolveRoot(root string) string {
	if root != "" {
		return filepath.Clean(root)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Clean(wd)
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
		// Fail closed: allow only the actions known to be non-mutating, deny
		// the rest. The test used to be "deny isWrite() or net.request, allow
		// everything else", which allowed mcp.tool — an MCP server writes files
		// and reaches the network through a surface this policy never sees — in
		// the one mode whose entire purpose is to forbid exactly that. It also
		// meant every Action added to the list later defaulted to allow here
		// while defaulting to ask in auto mode, so the *stricter* mode was the
		// permissive one. An unrecognized action is an unaudited action.
		if isRead(a) {
			return VerAllow, "read-only mode"
		}
		return VerDeny, "read-only mode"
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

// isRead reports whether an action is one of the known non-mutating ones — the
// allowlist read-only mode gates on. Deliberately an allowlist rather than the
// negation of a mutates-state predicate: a new Action must be classified here
// to be permitted in read-only mode, so forgetting to classify it denies rather
// than allows. (The negated form was written first and is gone for that reason.)
func isRead(a Action) bool {
	return a == ActFileRead
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

// globMatch reports whether resource matches a glob pattern. A "**" anywhere →
// the resource must be the path before the "**" or lie beneath it (pathUnder).
// A trailing single "*" → literal prefix match (drop the star); this branch is
// for command patterns like "git status*", not paths. A bare "*" or "" matches
// anything (the §13.8.3 "any resource" rows). A pattern with no "*" must equal
// the resource exactly (cmd.exec's literal command match).
func globMatch(pattern, resource string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// "**" anywhere → path containment (checked before trailing "*" so
	// "<root>**" hits this branch, not the single-star branch).
	if i := indexOf(pattern, "**"); i >= 0 {
		return pathUnder(pattern[:i], resource)
	}
	// Trailing single "*" → prefix match (drop the star).
	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return hasPrefix(resource, prefix)
	}
	return pattern == resource
}

// pathUnder reports whether resource is prefix itself or lies beneath it. Two
// things a raw strings.HasPrefix gets wrong, both of them policy bypasses:
//
//   - No separator boundary: "/repo" prefix-matches "/repo-evil/x", so a rule
//     scoping writes to the workspace also allows writes to any sibling
//     directory whose name starts with the workspace's. The match here requires
//     the resource to continue with a separator after the prefix.
//   - No ".." normalisation: "/repo/../../etc/passwd" prefix-matches "/repo"
//     while actually resolving outside it. Both sides are Cleaned first, so
//     that resource is compared as "/etc/passwd" and no longer matches.
//
// Clean does not resolve symlinks (it is purely lexical, and the resource may
// not exist yet), so a symlink planted inside the workspace still escapes it.
// Confining that is the sandbox's job, not the policy table's.
func pathUnder(prefix, resource string) bool {
	if prefix == "" {
		return true
	}
	p := filepath.Clean(prefix)
	r := filepath.Clean(resource)
	if r == p {
		return true
	}
	// Clean strips any trailing separator, so append exactly one to force the
	// boundary. The root "/" already ends in one.
	sep := string(filepath.Separator)
	if !hasSuffix(p, sep) {
		p += sep
	}
	return hasPrefix(r, p)
}

// defaultAutoPolicy returns the §13.8.3 default auto-mode rules, ordered
// (first match wins). file.read any → allow; file.write in repo → allow;
// file.write outside repo → deny; cmd.exec read-only cmds → allow; cmd.exec
// mutating → ask; net.request any → deny. The order matters: the deny rules
// for file.write-outside + net.request must come AFTER the allow rules for
// the same actions but with different patterns, OR use glob precedence. The
// implementation below orders specific-allow before general-deny per action.
//
// root is the workspace the write-allow rule is scoped to. An empty root omits
// that rule, leaving every write to hit the catch-all deny below — the
// fail-closed outcome when the caller could not determine a workspace.
func defaultAutoPolicy(root string) []policyRule {
	rules := []policyRule{
		{actions: []Action{ActFileRead}, pattern: "", verdict: VerAllow, reason: "reading is safe"},
	}
	if root != "" {
		rules = append(rules, policyRule{
			actions: []Action{ActFileWrite, ActFileDelete},
			pattern: root + "**",
			verdict: VerAllow,
			reason:  "inside workspace",
		})
	}
	rules = append(rules,
		policyRule{actions: []Action{ActFileWrite, ActFileDelete}, pattern: "", verdict: VerDeny, reason: "path confinement — outside repo"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "ls*", verdict: VerAllow, reason: "read-only allowlist"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "cat*", verdict: VerAllow, reason: "read-only allowlist"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "git status*", verdict: VerAllow, reason: "read-only allowlist"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "git diff*", verdict: VerAllow, reason: "read-only allowlist"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "git commit*", verdict: VerAsk, reason: "mutating — side effects"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "rm*", verdict: VerAsk, reason: "mutating — side effects"},
		policyRule{actions: []Action{ActCmdExec}, pattern: "git push*", verdict: VerAsk, reason: "mutating — side effects"},
		policyRule{actions: []Action{ActNetRequest}, pattern: "", verdict: VerDeny, reason: "default-deny network"},
	)
	return rules
}

// hasPrefix is a local strings.HasPrefix (kept local so this file doesn't add
// a strings import just for two call sites).
func hasPrefix(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// hasSuffix is a local strings.HasSuffix (same reason as hasPrefix).
func hasSuffix(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
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
