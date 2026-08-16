// Tests for L12-006 — Permissions model (File 13 §13.8). Permissions answer one
// question: may this action run? The answer is a policy lookup keyed by
// (action, resource, scope), resolved before the action is dispatched. This is
// the layer the user pre-authorizes against; the runtime's WAIT_TOOL/HITL flow
// (File 08 §8.5) is the runtime enforcement of the same policy.
//
// Modes (§13.8.2): Yolo (allow all), Auto (policy-driven, default), Ask
// (HITL on every action), Read-only (deny writes/network). The default auto
// policy (§13.8.3) allows file.read, file.write in repo, denies file.write
// outside repo + net.request, asks on cmd.exec. Scoped elevation (§13.8.4)
// persists an allow rule so a subsequent identical request hits the fast path.

package infra

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPermissionsGlobBypasses is the table for the two globMatch bypasses that
// compounded into a policy that was simultaneously too permissive on crafted
// paths and useless on real ones. Prefix matching with no separator boundary let
// a sibling directory pose as the workspace; no ".." normalisation let a
// traversal escape it while still matching the prefix.
func TestPermissionsGlobBypasses(t *testing.T) {
	const root = "/repo"
	p := newPermissions(PermissionsConfig{Mode: "auto", Root: root})
	cases := []struct {
		name string
		res  string
		want Verdict
	}{
		// The workspace itself and everything genuinely beneath it.
		{"workspace root", "/repo", VerAllow},
		{"file in workspace", "/repo/a.go", VerAllow},
		{"nested file in workspace", "/repo/deep/nested/n.go", VerAllow},
		{"harmless .. inside workspace", "/repo/pkg/../a.go", VerAllow},
		// Bypass (a1): no separator boundary — a sibling whose name merely
		// starts with the workspace's name prefix-matched "/repo".
		{"sibling with shared prefix", "/repo-evil/x", VerDeny},
		{"sibling suffixed", "/repository/secrets", VerDeny},
		{"adjacent file, not a dir", "/repo.bak", VerDeny},
		// Bypass (a2): no ".." normalisation — a traversal that resolves far
		// outside the workspace still prefix-matched it.
		{"traversal out of workspace", "/repo/../../etc/passwd", VerDeny},
		{"traversal one level out", "/repo/../etc/passwd", VerDeny},
		{"traversal back in is fine", "/repo/sub/../../repo/a.go", VerAllow},
		// Plain outside paths (these always worked).
		{"unrelated absolute path", "/etc/passwd", VerDeny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if v, _ := p.Check(ActFileWrite, c.res); v != c.want {
				t.Errorf("Check(file.write, %q) = %q, want %q", c.res, v, c.want)
			}
			// file.delete shares the rule; the bypass must be closed for both.
			if v, _ := p.Check(ActFileDelete, c.res); v != c.want {
				t.Errorf("Check(file.delete, %q) = %q, want %q", c.res, v, c.want)
			}
		})
	}
}

// TestPermissionsRootIsConfigurable pins the second half of the defect: the
// workspace root comes from config, so a real repository path is allowed.
// The old hardcoded "/repo" matched nothing on a real machine, which meant
// every genuine absolute write fell through to the catch-all deny.
func TestPermissionsRootIsConfigurable(t *testing.T) {
	root := t.TempDir()
	p := newPermissions(PermissionsConfig{Mode: "auto", Root: root})
	if v, _ := p.Check(ActFileWrite, filepath.Join(root, "main.go")); v != VerAllow {
		t.Errorf("write inside the configured root = %q, want allow", v)
	}
	if v, _ := p.Check(ActFileWrite, root+"-evil/main.go"); v != VerDeny {
		t.Errorf("write to a sibling of the configured root = %q, want deny", v)
	}
	if v, _ := p.Check(ActFileWrite, "/etc/passwd"); v != VerDeny {
		t.Errorf("write outside the configured root = %q, want deny", v)
	}
}

// TestPermissionsEmptyRootFallsBackToWorkingDir pins the default: an unset root
// resolves to the process working directory rather than a literal that matches
// nothing, so a caller that hasn't plumbed a repo root through yet still gets a
// usable policy instead of a blanket deny.
func TestPermissionsEmptyRootFallsBackToWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("no working directory: %v", err)
	}
	p := newPermissions(PermissionsConfig{Mode: "auto"})
	if v, _ := p.Check(ActFileWrite, filepath.Join(wd, "a.go")); v != VerAllow {
		t.Errorf("write inside the working directory = %q, want allow", v)
	}
	if v, _ := p.Check(ActFileWrite, "/etc/passwd"); v != VerDeny {
		t.Errorf("write to /etc/passwd = %q, want deny", v)
	}
}

// TestPermissionsCommandGlobsUnaffected pins that the path-aware matching did
// not leak into cmd.exec patterns: those are command strings, not paths, and
// must keep plain prefix semantics ("git status*" matches "git status --short").
func TestPermissionsCommandGlobsUnaffected(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "auto", Root: "/repo"})
	for _, c := range []struct {
		cmd  string
		want Verdict
	}{
		{"git status --short", VerAllow},
		{"ls -la /tmp", VerAllow},
		{"git push --force", VerAsk},
		{"weird-cmd", VerAsk},
	} {
		if v, _ := p.Check(ActCmdExec, c.cmd); v != c.want {
			t.Errorf("Check(cmd.exec, %q) = %q, want %q", c.cmd, v, c.want)
		}
	}
}

// TestPermissionsYoloModeAllowsAll pins §13.8.2: yolo mode allows every action
// on every resource. This is the P1-max "I accept the risk" mode.
func TestPermissionsYoloModeAllowsAll(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "yolo"})
	for _, c := range []struct {
		act Action
		res string
	}{
		{ActFileWrite, "/repo/a.go"},
		{ActCmdExec, "rm -rf /"},
		{ActNetRequest, "https://evil.example"},
		{ActFileDelete, "/repo/b.go"},
	} {
		if v, _ := p.Check(c.act, c.res); v != VerAllow {
			t.Errorf("yolo Check(%s,%s) = %q, want allow", c.act, c.res, v)
		}
	}
}

// TestPermissionsReadOnlyDeniesWritesAndNet pins §13.8.2: read-only mode allows
// reads but denies writes (file.write/delete, cmd.exec) and network requests.
func TestPermissionsReadOnlyDeniesWritesAndNet(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "read-only"})
	if v, _ := p.Check(ActFileRead, "/repo/a.go"); v != VerAllow {
		t.Errorf("read-only file.read = %q, want allow", v)
	}
	for _, c := range []struct {
		act Action
		res string
	}{
		{ActFileWrite, "/repo/a.go"},
		{ActFileDelete, "/repo/a.go"},
		{ActCmdExec, "ls"},
		{ActNetRequest, "https://x"},
	} {
		if v, _ := p.Check(c.act, c.res); v != VerDeny {
			t.Errorf("read-only Check(%s,%s) = %q, want deny", c.act, c.res, v)
		}
	}
}

// TestPermissionsReadOnlyDeniesUnknownActions pins the fail-closed direction of
// read-only mode. The gate used to deny isWrite()+net.request and allow
// everything else, so mcp.tool — which writes files and reaches the network
// through a server this policy never inspects — was allowed in the one mode
// whose purpose is to forbid exactly that, and so was any Action added later.
// Auto mode already defaults unknown actions to ask; read-only defaulting them
// to allow made the stricter mode the permissive one.
func TestPermissionsReadOnlyDeniesUnknownActions(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "read-only"})
	for _, a := range []Action{ActMCPTool, Action("db.drop"), Action("")} {
		if v, _ := p.Check(a, "anything"); v != VerDeny {
			t.Errorf("read-only Check(%q) = %q, want deny (an unclassified action is unaudited)", a, v)
		}
	}
	// The allowlist must still admit the genuine read.
	if v, _ := p.Check(ActFileRead, "/repo/a.go"); v != VerAllow {
		t.Errorf("read-only file.read = %q, want allow", v)
	}
}

// TestPermissionsAskModeDefersAll pins §13.8.2: ask mode returns "ask" for
// every action — every action goes to HITL.
func TestPermissionsAskModeDefersAll(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "ask"})
	if v, _ := p.Check(ActFileRead, "/repo/a.go"); v != VerAsk {
		t.Errorf("ask file.read = %q, want ask", v)
	}
	if v, _ := p.Check(ActCmdExec, "rm -rf /"); v != VerAsk {
		t.Errorf("ask cmd.exec(rm) = %q, want ask", v)
	}
}

// TestPermissionsAutoDefaultPolicy pins §13.8.3 default auto-mode policy:
// file.read any → allow; file.write in repo → allow; file.write outside repo →
// deny; cmd.exec read-only cmds (ls) → allow; cmd.exec mutating (git commit) →
// ask; net.request → deny.
func TestPermissionsAutoDefaultPolicy(t *testing.T) {
	// Root is explicit now: the policy used to hardcode "/repo", which is not
	// the workspace on any real machine.
	p := newPermissions(PermissionsConfig{Mode: "auto", Root: "/repo"})
	cases := []struct {
		name string
		act  Action
		res  string
		want Verdict
	}{
		{"file.read any", ActFileRead, "/anywhere/a.go", VerAllow},
		{"file.write in repo", ActFileWrite, "/repo/a.go", VerAllow},
		{"file.write outside repo", ActFileWrite, "/etc/passwd", VerDeny},
		{"cmd.exec read-only (ls)", ActCmdExec, "ls -la", VerAllow},
		{"cmd.exec read-only (cat)", ActCmdExec, "cat file.go", VerAllow},
		{"cmd.exec read-only (git status)", ActCmdExec, "git status", VerAllow},
		{"cmd.exec mutating (git commit)", ActCmdExec, "git commit -m x", VerAsk},
		{"cmd.exec mutating (rm)", ActCmdExec, "rm file.go", VerAsk},
		{"cmd.exec unknown", ActCmdExec, "weird-cmd --flag", VerAsk},
		{"net.request any", ActNetRequest, "https://x.example", VerDeny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if v, _ := p.Check(c.act, c.res); v != c.want {
				t.Errorf("auto Check(%s,%s) = %q, want %q", c.act, c.res, v, c.want)
			}
		})
	}
}

// TestPermissionsAutoUnknownDefaultsToAsk pins §13.8.2: in auto mode, an action
// with no explicit policy rule defaults to "ask" (conservative).
func TestPermissionsAutoUnknownDefaultsToAsk(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "auto"})
	// mcp.tool has no default rule → ask.
	if v, _ := p.Check(ActMCPTool, "mcp:server:tool"); v != VerAsk {
		t.Errorf("auto mcp.tool (no rule) = %q, want ask", v)
	}
}

// TestPermissionsElevationPersistsAllowRule pins §13.8.4: a denied action
// elevated by user approval (via Elevate) appends an allow rule, so a
// subsequent identical request hits the fast path (allow, no prompt).
func TestPermissionsElevationPersistsAllowRule(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "auto"})

	// Before elevation: net.request is denied (§13.8.3).
	if v, _ := p.Check(ActNetRequest, "https://api.example"); v != VerDeny {
		t.Fatalf("pre-elevate net.request = %q, want deny", v)
	}
	// Elevate: persist an allow rule for this exact resource.
	if err := p.Elevate(policyRule{
		actions: []Action{ActNetRequest},
		pattern: "https://api.example*",
		verdict: VerAllow,
		reason:  "user approved this host",
	}); err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	// After elevation: the same request is now allowed (fast path).
	if v, _ := p.Check(ActNetRequest, "https://api.example/path"); v != VerAllow {
		t.Errorf("post-elevate net.request = %q, want allow (elevation persisted)", v)
	}
}

// TestPermissionsElevationDoesNotSwitchGlobalMode pins §13.8.4: elevation
// widens the policy, it does NOT switch the global mode to yolo. An unrelated
// action still gets its normal verdict (net.request to a DIFFERENT host is
// still denied, because the elevated rule's pattern doesn't match it).
func TestPermissionsElevationDoesNotSwitchGlobalMode(t *testing.T) {
	p := newPermissions(PermissionsConfig{Mode: "auto"})
	_ = p.Elevate(policyRule{
		actions: []Action{ActNetRequest},
		pattern: "https://api.example*",
		verdict: VerAllow,
		reason:  "approved one host",
	})
	// A different host is still denied — elevation was scoped, not global.
	if v, _ := p.Check(ActNetRequest, "https://evil.example"); v != VerDeny {
		t.Errorf("unrelated net.request = %q, want deny (elevation must not switch mode)", v)
	}
}
