// Tests for the Human-in-the-Loop flow (File 08 §8.5): a medium/high-risk tool
// blocks in Dispatch until the user resolves the approval. The dispatcher
// publishes an ApprovalRequestEvent (carrying the ApprovalID the TUI echoes
// back), then waits on a pending-decision channel. ResolveApproval(id,true)
// unblocks and runs the tool; ResolveApproval(id,false) returns ErrRejected
// without running. A critical-risk tool is denied outright (never prompts).

package exec

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// riskyTool is a tool whose Risk returns the given class, so a test can drive
// each rung of the HITL ladder. ran records whether Run was reached.
type riskyTool struct {
	name string
	risk event.Risk
	ran  bool
}

func (t *riskyTool) Name() string               { return t.name }
func (t *riskyTool) Metadata() Metadata         { return Metadata{Category: "demo"} }
func (t *riskyTool) Schema() Schema             { return Schema{Type: "object"} }
func (t *riskyTool) Risk(_ ToolCall) event.Risk { return t.risk }
func (t *riskyTool) Run(_ context.Context, _ ToolInput) (ToolOutput, error) {
	t.ran = true
	return ToolOutput{Stdout: "ran " + t.name}, nil
}

func newApprovalEngine(t *testing.T, tools ...Tool) (*Engine, *event.Bus) {
	t.Helper()
	r := new(Registry)
	for _, tl := range tools {
		r.Register(tl)
	}
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	return New(Deps{Registry: r, Bus: bus}), bus
}

func TestLowRiskToolRunsSilently(t *testing.T) {
	tool := &riskyTool{name: "ls", risk: RiskLow}
	eng, bus := newApprovalEngine(t, tool)

	// Low risk must not prompt — no ApprovalRequestEvent published.
	ch := bus.Subscribe("approval.request")
	obs, err := eng.Dispatch(context.Background(), ToolCall{Tool: "ls", Args: []byte(`{}`)})
	if err != nil {
		t.Fatalf("Dispatch(low) = %v, want nil", err)
	}
	if !tool.ran {
		t.Fatal("low-risk tool did not run (low risk runs silently, no approval)")
	}
	if !strings.Contains(obs.Stdout, "ran") {
		t.Fatalf("obs = %q, want the tool's output", obs.Stdout)
	}
	select {
	case env := <-ch:
		t.Fatalf("low-risk tool published approval.request (%v); low risk must run silently", env.Evt)
	case <-time.After(100 * time.Millisecond):
		// good — no approval event
	}
}

func TestMediumRiskToolBlocksUntilApproved(t *testing.T) {
	tool := &riskyTool{name: "git", risk: RiskMedium}
	eng, bus := newApprovalEngine(t, tool)
	ch := bus.Subscribe("approval.request")

	// Dispatch blocks until approval resolves; drive it in a goroutine.
	var gotID string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case env := <-ch:
			req, ok := env.Evt.(*event.ApprovalRequestEvent)
			if !ok {
				t.Errorf("approval.request evt = %T, want *ApprovalRequestEvent", env.Evt)
				return
			}
			gotID = req.ApprovalID
			eng.ResolveApproval(req.ApprovalID, true)
		case <-time.After(time.Second):
			t.Errorf("approval.request not published within 1s")
		}
	}()

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "git", Args: []byte(`{}`)})
	wg.Wait()
	if err != nil {
		t.Fatalf("Dispatch(medium, approved) = %v, want nil", err)
	}
	if !tool.ran {
		t.Fatal("medium-risk tool did not run after approval")
	}
	if gotID == "" {
		t.Fatal("ApprovalRequestEvent.ApprovalID was empty; the TUI needs it to echo back")
	}
}

func TestRejectReturnsError(t *testing.T) {
	tool := &riskyTool{name: "git", risk: RiskMedium}
	eng, bus := newApprovalEngine(t, tool)
	ch := bus.Subscribe("approval.request")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case env := <-ch:
			req := env.Evt.(*event.ApprovalRequestEvent)
			eng.ResolveApproval(req.ApprovalID, false)
		case <-time.After(time.Second):
			t.Errorf("approval.request not published within 1s")
		}
	}()

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "git", Args: []byte(`{}`)})
	wg.Wait()
	if err == nil {
		t.Fatal("Dispatch(rejected) = nil, want ErrRejected")
	}
	if err != ErrRejected && !strings.Contains(err.Error(), "reject") {
		t.Fatalf("err = %q, want ErrRejected", err.Error())
	}
	if tool.ran {
		t.Fatal("medium-risk tool ran after rejection — reject must skip Run")
	}
}

func TestCriticalRiskDeniedWithoutPrompt(t *testing.T) {
	tool := &riskyTool{name: "rm", risk: RiskCritical}
	eng, bus := newApprovalEngine(t, tool)
	ch := bus.Subscribe("approval.request")

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "rm", Args: []byte(`{}`)})
	if err == nil {
		t.Fatal("Dispatch(critical) = nil, want deny error (critical is explicitly denied)")
	}
	if !strings.Contains(err.Error(), "critical") && !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %q, want a critical/denied message", err.Error())
	}
	if tool.ran {
		t.Fatal("critical-risk tool ran — it must be blocked without running")
	}
	// And no approval.request should have been published for a denied tool.
	select {
	case env := <-ch:
		t.Fatalf("critical tool published approval.request (%v); it must be denied outright", env.Evt)
	case <-time.After(100 * time.Millisecond):
		// good — no prompt
	}
}

func TestCancelAbortsApprovalWait(t *testing.T) {
	tool := &riskyTool{name: "git", risk: RiskMedium}
	eng, _ := newApprovalEngine(t, tool)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.Dispatch(ctx, ToolCall{Tool: "git", Args: []byte(`{}`)})
		done <- err
	}()

	// Cancel after a short delay; Dispatch must return a context error promptly.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Dispatch on cancelled ctx returned nil, want ctx error")
		}
		if err != context.Canceled && !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("err = %q, want context.Canceled", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch did not return after ctx cancel — approval wait ignores ctx")
	}
	if tool.ran {
		t.Fatal("tool ran after ctx was cancelled — cancel must abort before Run")
	}
}
