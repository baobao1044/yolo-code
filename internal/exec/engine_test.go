// Tests for the dispatcher (File 08 §8.3): one ToolCall → one Dispatch → one
// ToolResult. The dispatcher looks the tool up, validates args, runs the
// admitter gate, classifies risk, runs the tool under a per-call timeout in a
// worker goroutine, normalizes the output, and publishes a ToolResultEvent.
// HITL approval is wired in L7-006; L7-003 covers everything up to and
// including Run.

package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// newEngine builds a minimal Engine for dispatcher tests: a registry with
// the given tools, a live bus, and (optionally) an admitter. The normalizer
// is the engine's passthrough default (L7-007 swaps in the real one). Returns
// the engine and the bus so a test can subscribe.
func newEngine(t *testing.T, tools []Tool, adm ToolAdmitter) (*Engine, *event.Bus) {
	t.Helper()
	r := new(Registry)
	for _, tl := range tools {
		r.Register(tl)
	}
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	e := New(Deps{Registry: r, Bus: bus, Admitter: adm})
	return e, bus
}

// slowTool blocks until ctx is cancelled, then records that it saw the
// cancellation. Used to assert the per-call timeout actually fires.
type slowTool struct {
	sawCancel bool
}

func (s *slowTool) Name() string               { return "slow" }
func (s *slowTool) Metadata() Metadata         { return Metadata{Category: "demo"} }
func (s *slowTool) Schema() Schema             { return Schema{Type: "object"} }
func (s *slowTool) Risk(_ ToolCall) event.Risk { return RiskLow }
func (s *slowTool) Run(ctx context.Context, _ ToolInput) (ToolOutput, error) {
	<-ctx.Done()
	s.sawCancel = true
	return ToolOutput{}, ctx.Err()
}

func TestDispatchRunsToolAndPublishesResult(t *testing.T) {
	eng, bus := newEngine(t, []Tool{NewEcho()}, nil)

	ch := bus.Subscribe("tool.result")
	obs, err := eng.Dispatch(context.Background(), ToolCall{
		Tool: "echo",
		Args: []byte(`{"msg":"hi"}`),
		Task: "task-1",
	})
	if err != nil {
		t.Fatalf("Dispatch = %v, want nil", err)
	}
	if !strings.Contains(obs.Stdout, "hi") {
		t.Fatalf("Observation stdout = %q, want echoed msg", obs.Stdout)
	}

	env := drainOne(t, ch, "tool.result")
	res, ok := env.Evt.(*event.ToolResultEvent)
	if !ok {
		t.Fatalf("tool.result evt = %T, want *ToolResultEvent", env.Evt)
	}
	if res.Tool != "echo" {
		t.Fatalf("ToolResultEvent.Tool = %q, want echo", res.Tool)
	}
	if res.Task != "task-1" {
		t.Fatalf("ToolResultEvent.Task = %q, want task-1 (causal id threaded from the call)", res.Task)
	}
}

func TestDispatchTimeoutCancelsWorker(t *testing.T) {
	slow := &slowTool{}
	eng, _ := newEngine(t, []Tool{slow}, nil)
	eng.config.ToolTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "slow", Args: []byte(`{}`)})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Dispatch returned nil err for a tool that should time out")
	}
	// The timeout must fire promptly, not wait for the caller — give it a wide
	// but bounded window so a slow CI box doesn't flake while still catching a
	// missing-timeout regression (which would block ~forever).
	if elapsed > 2*time.Second {
		t.Fatalf("Dispatch took %v, want it bounded near the 50ms timeout (timeout not firing?)", elapsed)
	}
	if !slow.sawCancel {
		t.Fatal("slow tool did not see ctx.Done — the per-call timeout did not cancel the worker")
	}
}

func TestDispatchUnknownToolReturnsError(t *testing.T) {
	eng, _ := newEngine(t, []Tool{NewEcho()}, nil)

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "nope", Args: []byte(`{}`)})
	if err == nil {
		t.Fatal("Dispatch(unknown tool) = nil, want error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %q, want it to name the unknown tool", err.Error())
	}
}

func TestAdmitterDeniesToolBeforeRun(t *testing.T) {
	ran := false
	echo := &recordingTool{name: "echo", ran: &ran}
	adm := denyAdmitter("denied: echo")
	eng, _ := newEngine(t, []Tool{echo}, adm)

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "echo", Args: []byte(`{}`)})
	if err == nil {
		t.Fatal("Dispatch with denying admitter = nil, want the deny error")
	}
	if !strings.Contains(err.Error(), "denied: echo") {
		t.Fatalf("err = %q, want the admitter's deny reason surfaced", err.Error())
	}
	if ran {
		t.Fatal("tool.Run was called after the admitter denied — the gate must run before Run")
	}
}

func TestDispatchValidatesArgsBeforeRun(t *testing.T) {
	ran := false
	echo := &recordingTool{name: "echo", sch: Schema{Required: []string{"msg"}}, ran: &ran}
	eng, _ := newEngine(t, []Tool{echo}, nil)

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "echo", Args: []byte(`{}`)})
	if err == nil {
		t.Fatal("Dispatch with invalid args = nil, want validation error")
	}
	if ran {
		t.Fatal("tool.Run was called after args validation failed — validate before Run")
	}
}

// recordingTool wraps a name and schema and records whether Run was called;
// used to assert the gate/validation order relative to Run.
type recordingTool struct {
	name string
	sch  Schema
	ran  *bool
}

func (t *recordingTool) Name() string               { return t.name }
func (t *recordingTool) Metadata() Metadata         { return Metadata{Category: "demo"} }
func (t *recordingTool) Schema() Schema             { return t.sch }
func (t *recordingTool) Risk(_ ToolCall) event.Risk { return RiskLow }
func (t *recordingTool) Run(_ context.Context, _ ToolInput) (ToolOutput, error) {
	*t.ran = true
	return ToolOutput{Stdout: "ok"}, nil
}

// denyAdmitter returns an admitter that always denies with the given reason.
type denyAdmitter string

func (d denyAdmitter) Admit(_ ToolCall) error { return errors.New(string(d)) }

// drainOne reads exactly one envelope from ch within 1s, fataling on timeout.
func drainOne(t *testing.T, ch <-chan event.Envelope, topic string) event.Envelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(time.Second):
		t.Fatalf("event %q not published within 1s", topic)
	}
	return event.Envelope{}
}
