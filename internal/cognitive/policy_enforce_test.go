// The Tool Policy as it is actually enforced: a tool name the model was never
// offered must not survive Think, because everything downstream of Think
// treats a ToolCall as work to run. cmd/yolo's cognitiveAdapter copies
// Turn.ToolCalls verbatim into runtime.CognitiveTurn, the runtime stashes them
// as h.pending, and EXECUTE dispatches them — so the Turn boundary is the last
// place the name is still cognitive-layer data.
//
// These tests drive that boundary the way the runtime does rather than calling
// Allow() directly: a scripted model emits an unadvertised tool, a dispatcher
// stands in for the Executor and actually writes to disk, and the assertion is
// that the file never appears. That is the shape the unprompted-write incident
// took, and a unit test on Allow() would have passed throughout it.

package cognitive

import (
	stdctx "context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/prompt"
)

// productionTools is the tool set the composition root advertises. Spelled out
// rather than taken from DefaultTools() so these tests state the offer they are
// testing against instead of inheriting whatever it becomes.
var productionTools = []string{"list_files", "read_file", "edit_file", "bash", "grep"}

// scriptedProvider answers turn N with script[N], repeating the last entry once
// the script runs out — a model that keeps making the same mistake.
type scriptedProvider struct {
	script []string
	turn   int
}

func (p *scriptedProvider) Window() int { return 128_000 }

func (p *scriptedProvider) Stream(ctx stdctx.Context, _ Request) (<-chan Chunk, error) {
	text := p.script[len(p.script)-1]
	if p.turn < len(p.script) {
		text = p.script[p.turn]
	}
	p.turn++
	out := make(chan Chunk, 1)
	go func() {
		defer close(out)
		select {
		case out <- Chunk{Delta: text}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// toolBlock renders one fenced ```tool block, the portable call format the
// model is told to use (File 07 §7.2.3).
func toolBlock(tool string, args map[string]string) string {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("```tool\n{\"tool\":%q,\"args\":%s,\"reason\":\"work\"}\n```\n", tool, raw)
}

// fakeExecutor stands in for the Execution Engine. It records every call it is
// handed and, for anything carrying file+content, performs the write — so a
// call that reaches it leaves evidence on disk, not just in a slice.
type fakeExecutor struct {
	root  string
	calls []string
}

func (e *fakeExecutor) dispatch(call ToolCall) string {
	e.calls = append(e.calls, call.Tool)
	var args struct {
		File    string `json:"file"`
		Path    string `json:"path"`
		Content string `json:"content"`
		Body    string `json:"body"`
	}
	_ = json.Unmarshal(call.Args, &args)
	target, body := args.File, args.Content
	if target == "" {
		target, body = args.Path, args.Body
	}
	if target == "" || body == "" {
		return "ok"
	}
	full := filepath.Join(e.root, target)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "error: " + err.Error()
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		return "error: " + err.Error()
	}
	return "wrote " + target
}

// drive runs the loop the runtime's FSM runs, reduced to the part that decides
// whether a tool call becomes work: Think, dispatch every call the Turn
// carries, record each result, repeat while HasMore. maxTurns stands in for the
// cost ledger's wall clock — the real loop's only other brake — so a model that
// never converges ends the test instead of hanging it.
func drive(t *testing.T, core *Core, exec *fakeExecutor, maxTurns int) error {
	t.Helper()
	ctx := ctxWithTask("t_policy")
	msgs := []prompt.Message{{Role: "user", Content: "do the thing"}}
	for i := 0; i < maxTurns; i++ {
		turn, err := core.Think(ctx, msgs)
		if err != nil {
			return err
		}
		for _, call := range turn.ToolCalls {
			core.RecordToolResultID(call.ID, call.Tool, exec.dispatch(call))
		}
		if turn.Final || !core.HasMore(nil) {
			return nil
		}
	}
	return nil
}

// historyText flattens the conversation so a test can assert what the model
// will see on its next turn.
func historyText(c *Core) string {
	var b strings.Builder
	for _, m := range c.history {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// TestUnofferedToolCallNeverReachesDispatch is the whole point. A scripted model
// asked to do something ordinary instead calls "write_file" — a name that
// appears nowhere in the prompt, in the tool schemas, or in the exec registry,
// and which internal/scope only ever BROADENS scope for. With the allowlist
// wired the call dies at the Turn boundary and the file stays absent; without
// it the call flows through to the Executor and the write lands.
func TestUnofferedToolCallNeverReachesDispatch(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "hooks", "install.go")

	provider := &scriptedProvider{script: []string{
		"Sure.\n" + toolBlock("write_file", map[string]string{
			"file":    "hooks/install.go",
			"content": "package hooks\n\n// the payload nobody asked for\nfunc Install() {}\n",
		}),
	}}
	core := New(provider, nil, productionTools...)
	exec := &fakeExecutor{root: root}

	// The run ends either way — the denial cap trips on a model this stubborn —
	// but the assertion is about what reached the executor, not how it ended.
	if err := drive(t, core, exec, 5); err != nil {
		t.Logf("run ended: %v", err)
	}

	for _, got := range exec.calls {
		if got == "write_file" {
			t.Errorf("write_file reached the executor; dispatched calls = %v", exec.calls)
		}
	}
	if _, err := os.Stat(target); err == nil {
		body, _ := os.ReadFile(target)
		t.Fatalf("an unoffered tool wrote %s:\n%s", target, body)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", target, err)
	}
}

// TestDeniedToolIsFedBackAndTheModelRecovers pins the other half of the
// contract: a denial is a turn the model can learn from, not a dropped call and
// not a dead task. The transcript must carry the refusal, name the tool that
// was refused, and list the ones that exist — and the model's next choice, when
// it is a real tool, must run.
func TestDeniedToolIsFedBackAndTheModelRecovers(t *testing.T) {
	root := t.TempDir()
	provider := &scriptedProvider{script: []string{
		toolBlock("write_file", map[string]string{"file": "a.go", "content": "package a\n"}),
		toolBlock("edit_file", map[string]string{"file": "a.go", "content": "package a\n"}),
		"Done.",
	}}
	core := New(provider, nil, productionTools...)
	exec := &fakeExecutor{root: root}

	if err := drive(t, core, exec, 5); err != nil {
		t.Fatalf("drive: %v (one bad call then a good one must not end the run)", err)
	}

	hist := historyText(core)
	if !strings.Contains(hist, toolResultPrefix+" write_file") {
		t.Errorf("no tool result answering the refused call:\n%s", hist)
	}
	if !strings.Contains(hist, "not allowed") {
		t.Errorf("the refusal does not say the tool was refused:\n%s", hist)
	}
	for _, want := range productionTools {
		if !strings.Contains(hist, want) {
			t.Errorf("the refusal does not offer %q as an alternative:\n%s", want, hist)
		}
	}
	// The recovery: the second turn's real tool ran, and only it.
	if len(exec.calls) != 1 || exec.calls[0] != "edit_file" {
		t.Errorf("dispatched calls = %v, want exactly [edit_file]", exec.calls)
	}
	if _, err := os.Stat(filepath.Join(root, "a.go")); err != nil {
		t.Errorf("the admitted edit_file did not run: %v", err)
	}
}

// TestRepeatedUnofferedToolEndsTheRun bounds the recovery path. Feeding the
// denial back gives the model another turn, which is right — but a model that
// keeps naming a tool that does not exist never converges, and the runtime's
// PLAN→EXECUTE(nothing)→PLAN cycle has no brake of its own. After
// maxDeniedTurns the Core reports a hard error naming the tool and the real
// alternatives, so the run stops instead of spinning against the wall clock.
func TestRepeatedUnofferedToolEndsTheRun(t *testing.T) {
	provider := &scriptedProvider{script: []string{
		toolBlock("run_shell", map[string]string{"command": "echo hi"}),
	}}
	core := New(provider, nil, productionTools...)
	exec := &fakeExecutor{root: t.TempDir()}

	err := drive(t, core, exec, 20)
	if err == nil {
		t.Fatalf("drive returned nil after 20 turns of a tool that does not exist; want a hard error by turn %d", maxDeniedTurns)
	}
	if !strings.Contains(err.Error(), "run_shell") {
		t.Errorf("error = %q, want it to name the tool", err)
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("error = %q, want it to list the tools that do exist", err)
	}
	if provider.turn > maxDeniedTurns {
		t.Errorf("model was asked %d times, want at most %d", provider.turn, maxDeniedTurns)
	}
	if len(exec.calls) != 0 {
		t.Errorf("dispatched %v, want nothing", exec.calls)
	}
}

// TestAdmittedToolStillDispatches guards against the fix being a blanket block:
// every advertised tool must still reach the executor untouched.
func TestAdmittedToolStillDispatches(t *testing.T) {
	for _, tool := range productionTools {
		provider := &scriptedProvider{script: []string{toolBlock(tool, map[string]string{}), "Done."}}
		core := New(provider, nil, productionTools...)
		exec := &fakeExecutor{root: t.TempDir()}
		if err := drive(t, core, exec, 4); err != nil {
			t.Fatalf("drive(%s): %v", tool, err)
		}
		if len(exec.calls) != 1 || exec.calls[0] != tool {
			t.Errorf("dispatched %v for %q, want exactly [%s]", exec.calls, tool, tool)
		}
	}
}

// TestPolicyDerivesFromTheAdvertisedToolSet is the anti-drift pin. The
// allowlist is not a second list to maintain: New builds it from the names it
// was given, which are the names that become the provider's tool schemas. Add a
// schema and the tool is allowed; do not, and it is not.
func TestPolicyDerivesFromTheAdvertisedToolSet(t *testing.T) {
	core := New(NewMockProvider(nil, 0), nil, DefaultTools()...)
	if core.policy == nil {
		t.Fatal("New with tools left policy nil; the advertised set must become the allowlist")
	}
	got := core.policy.AllowedTools()
	want := DefaultTools()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("allowlist = %v, want the advertised set %v", got, want)
	}
	// And the advertised set is the schema table, not a retyped copy of it.
	for _, name := range want {
		if _, ok := toolDefs[name]; !ok {
			t.Errorf("DefaultTools offers %q with no schema in toolDefs", name)
		}
	}
	if len(want) != len(toolDefs) {
		t.Errorf("DefaultTools returned %d names for %d schemas", len(want), len(toolDefs))
	}
}

// TestCoreWithoutToolsEnforcesNothing pins the documented reading of "no tools
// configured": a Core told nothing about tools cannot police them, and an empty
// allowlist there would deny every call rather than the unoffered ones. This is
// the unit-test construction; the composition root always passes the set.
func TestCoreWithoutToolsEnforcesNothing(t *testing.T) {
	provider := &scriptedProvider{script: []string{toolBlock("anything", map[string]string{}), "Done."}}
	core := New(provider, nil)
	exec := &fakeExecutor{root: t.TempDir()}
	if err := drive(t, core, exec, 4); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if len(exec.calls) != 1 || exec.calls[0] != "anything" {
		t.Errorf("dispatched %v, want [anything] (no policy configured)", exec.calls)
	}
}

// TestSetToolPolicyWidensBeyondTheAdvertisement covers the composition root's
// one asymmetry: cmd/yolo routes "patch" to patch.Engine but advertises no
// schema for it, so it widens the policy explicitly. Pinned here because the
// widening is the only way a non-advertised name is ever admitted, and it
// should stay a deliberate call rather than a default.
func TestSetToolPolicyWidensBeyondTheAdvertisement(t *testing.T) {
	root := t.TempDir()
	provider := &scriptedProvider{script: []string{
		toolBlock("patch", map[string]string{"path": "a.go", "body": "package a\n"}),
		"Done.",
	}}
	core := New(provider, nil, productionTools...)
	core.SetToolPolicy(NewToolPolicy(append(append([]string{}, productionTools...), "patch")))
	exec := &fakeExecutor{root: root}

	if err := drive(t, core, exec, 4); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if len(exec.calls) != 1 || exec.calls[0] != "patch" {
		t.Errorf("dispatched %v, want [patch] (the route the root wired)", exec.calls)
	}
}
