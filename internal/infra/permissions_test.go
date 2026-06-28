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
	"testing"
)

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
	p := newPermissions(PermissionsConfig{Mode: "auto"})
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
