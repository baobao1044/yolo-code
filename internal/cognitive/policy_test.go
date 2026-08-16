package cognitive

import (
	"strings"
	"testing"
)

// TestToolPolicyDeniesUnsafeToolCall is the L6-005 exit criterion: a tool not
// on the allowlist is denied at the policy (before the executor sees it). The
// denial error names the tool so it surfaces to the model.
func TestToolPolicyDeniesUnsafeToolCall(t *testing.T) {
	p := NewToolPolicy([]string{"read_file", "list_files"})
	err := p.Allow(ToolCall{Tool: "rm"})
	if err == nil {
		t.Fatal("Allow(rm) err = nil, want a denial (rm is not allowlisted)")
	}
	if !strings.Contains(err.Error(), "rm") {
		t.Errorf("denial error = %q, want it to name the tool", err.Error())
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("denial error = %q, want 'not allowed'", err.Error())
	}
}

// TestToolPolicyDenialListsTheRealTools pins the actionable half of the denial:
// the message tells the model which tools do exist. Without it the model's next
// turn is another guess, and the run spends a turn per invented name.
func TestToolPolicyDenialListsTheRealTools(t *testing.T) {
	p := NewToolPolicy([]string{"read_file", "list_files"})
	err := p.Allow(ToolCall{Tool: "write_file"})
	if err == nil {
		t.Fatal("Allow(write_file) = nil, want a denial")
	}
	for _, want := range []string{"list_files", "read_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("denial %q does not name the available tool %q", err.Error(), want)
		}
	}
}

// TestToolPolicyAdmitsAllowedTool pins the positive case: a tool on the
// allowlist is admitted (nil error).
func TestToolPolicyAdmitsAllowedTool(t *testing.T) {
	p := NewToolPolicy([]string{"read_file", "list_files"})
	for _, tool := range []string{"read_file", "list_files"} {
		if err := p.Allow(ToolCall{Tool: tool}); err != nil {
			t.Errorf("Allow(%q) = %v, want nil", tool, err)
		}
	}
}

// TestToolPolicyNilDeniesAll pins the default-deny posture (File 02): a nil
// policy denies everything, including allowlisted-looking tools. This is the
// safe fallback when no policy is configured.
func TestToolPolicyNilDeniesAll(t *testing.T) {
	var p *ToolPolicy
	err := p.Allow(ToolCall{Tool: "read_file"})
	if err == nil {
		t.Fatal("nil policy Allow = nil, want a denial (default-deny posture)")
	}
	if !strings.Contains(err.Error(), "default-deny") {
		t.Errorf("nil policy denial = %q, want it to mention default-deny", err.Error())
	}
}

// TestDefaultPolicyRequiredStages pins §7.5.2's default: all stages except
// tests, in canonical order, lint at error level, 30s test timeout.
func TestDefaultPolicyRequiredStages(t *testing.T) {
	p := DefaultPolicy()
	got := p.RequiredStages()
	want := []string{"ast", "format", "lint", "typecheck", "build"}
	if len(got) != len(want) {
		t.Fatalf("DefaultPolicy stages = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("stage[%d] = %q, want %q", i, got[i], w)
		}
	}
	if p.RequireTests {
		t.Error("DefaultPolicy.RequireTests = true, want false (tests opt-in)")
	}
	if p.LintLevel != "error" {
		t.Errorf("DefaultPolicy.LintLevel = %q, want error", p.LintLevel)
	}
	if p.TestTimeout != 30*1e9 { // 30s in ns
		t.Errorf("DefaultPolicy.TestTimeout = %v, want 30s", p.TestTimeout)
	}
}

// TestLightPolicyRequiresOnlyAST pins the read-only/explain policy (§7.5.2):
// AST only, no build/lint/tests — "done" for an explain task is syntactic
// coherence, nothing more.
func TestLightPolicyRequiresOnlyAST(t *testing.T) {
	p := LightPolicy()
	stages := p.RequiredStages()
	if len(stages) != 1 || stages[0] != "ast" {
		t.Errorf("LightPolicy stages = %v, want [ast] only", stages)
	}
	if p.RequireBuild || p.RequireTests || p.RequireLint {
		t.Errorf("LightPolicy over-requires: build=%v tests=%v lint=%v", p.RequireBuild, p.RequireTests, p.RequireLint)
	}
}

// TestPolicyGatesBeforeEmit pins the wiring intent (§7.5.1): the policy gates
// tool calls BEFORE the executor sees them. A denied call does not reach
// EmitToolCalls — the runtime's drive loop filters through Allow first. This
// test models that filter: deny → skip emission; admit → emit.
func TestPolicyGatesBeforeEmit(t *testing.T) {
	p := NewToolPolicy([]string{"read_file"})
	core, bus := newTestCore(t, nil)
	ch := bus.Subscribe("tool.call")

	turn := Turn{ToolCalls: []ToolCall{
		{Tool: "read_file"},  // admitted
		{Tool: "rm"},         // denied
		{Tool: "list_files"}, // denied (not allowlisted)
	}}
	// Filter through the policy, emitting only admitted calls.
	var admitted []ToolCall
	for _, c := range turn.ToolCalls {
		if err := p.Allow(c); err != nil {
			continue
		}
		admitted = append(admitted, c)
	}
	core.EmitToolCalls(ctxWithTask("t_p"), Turn{ToolCalls: admitted})

	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != 1 {
		t.Errorf("emitted %d tool.call events, want 1 (only the admitted read_file)", count)
	}
}
