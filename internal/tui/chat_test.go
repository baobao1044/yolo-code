// Tests for TUI-002 — Chat pane (File 14 §14.5, §14.7.2). The chat pane is an
// append-only list of messageViews derived from the streaming events:
//   llm.thinking  → accumulate into the live thinking bubble, streaming on
//   llm.token      → accumulate into the live assistant bubble
//   assistant.message → flush the live bubble (+Text) to messages, clear thinking
//   tool.call      → "calling <tool>" line, activeTool set
//   tool.result    → clear activeTool, summarize the observation (no outcome badge — spec gap)
//   observation.received → truncated obs preview line
//   reflection.note → dimmed inline note
// All are pure folds; View renders them. The thinking/liveAssistant accumulate
// (TUI-007 coalesces the repaint, but the accumulation is TUI-002's correctness).

package tui

import (
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// TestFoldThinkingAccumulates pins §14.5: llm.thinking deltas append to the
// live thinking bubble and turn streaming on. Two deltas → concatenation.
func TestFoldThinkingAccumulates(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.ThinkingEvent{Task: "t_1", Delta: "I'll "}))
	m, _ = fold(m, env(&event.ThinkingEvent{Task: "t_1", Delta: "check auth."}))

	if m.thinking != "I'll check auth." {
		t.Errorf("thinking = %q, want %q (deltas accumulate)", m.thinking, "I'll check auth.")
	}
	if !m.streaming {
		t.Error("streaming = false, want true (a thinking stream is in flight)")
	}
}

// TestFoldTokenAccumulatesLiveAssistant pins §14.5: llm.token deltas append
// to the live assistant bubble (separate from thinking). The assistant bubble
// is what assistant.message flushes to messages.
func TestFoldTokenAccumulatesLiveAssistant(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "Hello"}))
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: " world"}))

	if m.liveAssistant != "Hello world" {
		t.Errorf("liveAssistant = %q, want %q (token deltas accumulate into the live bubble)", m.liveAssistant, "Hello world")
	}
}

// TestFoldAssistantMessageFlushesAndClears is the §14.5 invariant: an
// assistant.message event finalizes the assistant bubble — it appends a
// messageView to messages (the streamed text + the final Text), clears the
// thinking + live bubbles, and turns streaming off. This is the mutation
// guard: if thinking isn't cleared, a stale thinking bubble lingers into the
// next turn (a real visual bug the user would see).
func TestFoldAssistantMessageFlushesAndClears(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.ThinkingEvent{Task: "t_1", Delta: "planning…"}))
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "partial"}))
	m, _ = fold(m, env(&event.AssistantMessageEvent{Task: "t_1", Text: "Final answer", Final: true}))

	if len(m.messages) == 0 {
		t.Fatal("messages is empty — assistant.message must append a finalized bubble")
	}
	last := m.messages[len(m.messages)-1]
	if last.role != "assistant" {
		t.Errorf("last message role = %q, want %q", last.role, "assistant")
	}
	// The finalized text is the event's Text (the live accumulation is folded in
	// by a later hardening pass; TUI-002 appends the final Text).
	if last.text != "Final answer" {
		t.Errorf("last message text = %q, want %q", last.text, "Final answer")
	}
	if m.thinking != "" {
		t.Errorf("thinking = %q after flush, want \"\" (cleared — mutation guard)", m.thinking)
	}
	if m.liveAssistant != "" {
		t.Errorf("liveAssistant = %q after flush, want \"\" (cleared)", m.liveAssistant)
	}
	if m.streaming {
		t.Error("streaming = true after flush, want false (stream ended)")
	}
}

// TestFoldToolCallSetsActiveTool pins §14.5: tool.call sets activeTool and
// appends a "calling <tool>" line. The spinner (TUI-007) reads activeTool.
func TestFoldToolCallSetsActiveTool(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.ToolCallEvent{Task: "t_1", Tool: "edit_file", Reason: "fix nil"}))

	if m.activeTool != "edit_file" {
		t.Errorf("activeTool = %q, want %q", m.activeTool, "edit_file")
	}
	if len(m.messages) == 0 || m.messages[len(m.messages)-1].role != "tool" {
		t.Errorf("tool.call must append a tool-role message line, got %v", m.messages)
	}
}

// TestFoldToolResultClearsActiveTool pins §14.5: tool.result clears activeTool
// (the tool finished) and appends a summarized result line. ToolResultEvent
// has no outcome field (spec gap — no ✓/✗ badge); the line shows the tool.
func TestFoldToolResultClearsActiveTool(t *testing.T) {
	m := newModelForTest()
	m.activeTool = "edit_file"
	m, _ = fold(m, env(&event.ToolResultEvent{Task: "t_1", Tool: "edit_file", Obs: []byte(`{"ok":true}`)}))

	if m.activeTool != "" {
		t.Errorf("activeTool = %q after tool.result, want \"\" (tool finished)", m.activeTool)
	}
	last := m.messages[len(m.messages)-1]
	if last.role != "tool" {
		t.Errorf("tool.result line role = %q, want %q", last.role, "tool")
	}
}

// TestFoldObservationAddsPreviewLine pins §14.5: observation.received appends a
// truncated observation preview line (display-only; the full obs lives in the
// event log). The line's role is observation.
func TestFoldObservationAddsPreviewLine(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.ObservationEvent{Task: "t_1", Tool: "read_file", Obs: []byte(`{"content":"…long…"}`)}))

	if len(m.messages) == 0 {
		t.Fatal("observation.received must append a preview line")
	}
	last := m.messages[len(m.messages)-1]
	if last.role != "observation" {
		t.Errorf("observation line role = %q, want %q", last.role, "observation")
	}
}

// TestFoldReflectionAddsNote pins §14.5: reflection.note appends a dimmed inline
// note. The line's role is reflection.
func TestFoldReflectionAddsNote(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.ReflectionEvent{Task: "t_1", Note: "considered error path"}))

	if len(m.messages) == 0 {
		t.Fatal("reflection.note must append a note line")
	}
	last := m.messages[len(m.messages)-1]
	if last.role != "reflection" {
		t.Errorf("reflection line role = %q, want %q", last.role, "reflection")
	}
	if last.text != "considered error path" {
		t.Errorf("reflection line text = %q, want the note verbatim", last.text)
	}
}
