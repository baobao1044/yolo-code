package cognitive

import (
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// TestEmitToolCallsPublishesOneEventPerCall is the L6-004 exit criterion: a
// Planner turn that produced tool calls turns into one tool.call event per
// call, so the Executor consumes them. Each event carries the tool, args, and
// reason from the parsed call, plus the task ID threaded via context.
func TestEmitToolCallsPublishesOneEventPerCall(t *testing.T) {
	turn := Turn{Final: false, ToolCalls: []ToolCall{
		{Tool: "read_file", Args: []byte(`{"file":"a.go"}`), Reason: "inspect"},
		{Tool: "edit_file", Args: []byte(`{"file":"a.go"}`), Reason: "fix the bug"},
	}}
	core, bus := newTestCore(t, nil)
	ch := bus.Subscribe("tool.call")

	core.EmitToolCalls(ctxWithTask("t_e"), turn)

	var got []*event.ToolCallEvent
	for {
		select {
		case env := <-ch:
			te, ok := env.Evt.(*event.ToolCallEvent)
			if !ok {
				t.Fatalf("event type = %T, want *ToolCallEvent", env.Evt)
			}
			got = append(got, te)
		default:
			goto done
		}
	}
done:
	if len(got) != 2 {
		t.Fatalf("published %d tool.call events, want 2 (one per call)", len(got))
	}
	if got[0].Tool != "read_file" || got[1].Tool != "edit_file" {
		t.Errorf("tool order = %q,%q, want read_file,edit_file", got[0].Tool, got[1].Tool)
	}
	if got[0].Task != event.TaskID("t_e") {
		t.Errorf("event[0].Task = %q, want %q (threaded via context)", got[0].Task, "t_e")
	}
	if string(got[0].Args) != `{"file":"a.go"}` {
		t.Errorf("event[0].Args = %s, want the call's args verbatim", got[0].Args)
	}
	if got[0].Reason != "inspect" {
		t.Errorf("event[0].Reason = %q, want %q", got[0].Reason, "inspect")
	}
}

// TestEmitToolCallsNoOpForFinalTurn pins that a Final turn (a direct answer,
// no tool calls) emits nothing — the plan is the answer, not dispatchable work.
func TestEmitToolCallsNoOpForFinalTurn(t *testing.T) {
	turn := Turn{Final: true, Text: "just an answer"}
	core, bus := newTestCore(t, nil)
	ch := bus.Subscribe("tool.call")

	core.EmitToolCalls(ctxWithTask("t_f"), turn)

	select {
	case env := <-ch:
		t.Errorf("Final turn published a tool.call: %+v", env)
	default:
	}
}

// TestEmitToolCallsNoOpOnNilBus pins that a nil bus is safe (unit tests can
// drive the Core without an event trace).
func TestEmitToolCallsNoOpOnNilBus(t *testing.T) {
	core := New(NewMockProvider(nil, 0), nil)
	core.EmitToolCalls(ctxWithTask("t_g"), Turn{ToolCalls: []ToolCall{{Tool: "x"}}})
	// No panic; nothing to assert beyond reaching here.
}

// TestReasonTurnsPlanAndReflectionIntoThinking pins §7.4: the Reasoner frames a
// turn as a Plan, folding the reflection's root-cause note into the thinking
// so the next PLAN iteration carries it. With no reflection, the thinking is
// just the visible answer.
func TestReasonTurnsPlanAndReflectionIntoThinking(t *testing.T) {
	core, _ := newTestCore(t, nil)
	turn := Turn{Text: "I'll read the file.", ToolCalls: []ToolCall{{Tool: "read_file"}}}

	plan := core.Reason(turn, "")
	if plan.Thinking != "I'll read the file." {
		t.Errorf("plan.Thinking = %q, want the visible answer when no reflection", plan.Thinking)
	}
	if len(plan.ToolCalls) != 1 || plan.ToolCalls[0].Tool != "read_file" {
		t.Errorf("plan.ToolCalls = %+v, want the turn's calls", plan.ToolCalls)
	}

	planRefl := core.Reason(turn, "root cause: wrong signature")
	if planRefl.Thinking != "root cause: wrong signature\nI'll read the file." {
		t.Errorf("plan.Thinking with reflection = %q, want note + answer", planRefl.Thinking)
	}
}

// TestThinkThenEmitEndToEnd is the L6-004 end-to-end path: the mock streams a
// ```tool block, Think parses it into a non-Final turn, and EmitToolCalls
// publishes the tool.call the Executor would consume.
func TestThinkThenEmitEndToEnd(t *testing.T) {
	chunks := []Chunk{
		{Delta: "Planning.\n```tool\n" + `{"tool":"list_files","args":{},"reason":"see the repo"}` + "\n"},
		{Delta: "```\n"},
	}
	core, bus := newTestCore(t, chunks)
	ch := bus.Subscribe("tool.call")

	turn, err := core.Think(ctxWithTask("t_h"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if turn.Final {
		t.Fatal("turn.Final = true, want false (a tool block → not final)")
	}
	core.EmitToolCalls(ctxWithTask("t_h"), turn)

	env := drain(t, ch)
	te, ok := env.Evt.(*event.ToolCallEvent)
	if !ok {
		t.Fatalf("event type = %T, want *ToolCallEvent", env.Evt)
	}
	if te.Tool != "list_files" {
		t.Errorf("tool.call.Tool = %q, want list_files", te.Tool)
	}
	if te.Reason != "see the repo" {
		t.Errorf("tool.call.Reason = %q, want the stated rationale", te.Reason)
	}
}
