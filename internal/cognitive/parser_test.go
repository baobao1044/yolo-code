package cognitive

import (
	"encoding/json"
	"strings"
	"testing"
)

// todo is a parsed plan item: a tool call's intent (tool) + its file target.
// The L6-002 exit bar requires a plan to carry ≥1 todo with file+intent, so
// these tests assert the parser surfaces both.
type todo struct {
	tool string
	file string
}

// todos extracts the todo list from a Turn's tool calls. A todo's file comes
// from the call's args "file" field; the intent is the call's tool name.
func todos(turn Turn) []todo {
	out := make([]todo, 0, len(turn.ToolCalls))
	for _, c := range turn.ToolCalls {
		var args struct {
			File string `json:"file"`
		}
		if len(c.Args) > 0 {
			_ = json.Unmarshal(c.Args, &args)
		}
		out = append(out, todo{tool: c.Tool, file: args.File})
	}
	return out
}

// TestParsePlanHasTodoWithFileAndIntent is the L6-002 exit criterion: parsing
// a model response that contains a ```tool block yields a plan with ≥1 todo
// carrying both a file target and an intent (the tool name).
func TestParsePlanHasTodoWithFileAndIntent(t *testing.T) {
	text := "I'll inspect the login module first.\n" +
		"```tool\n" +
		`{"tool":"read_file","args":{"file":"auth/login.go"},"reason":"understand the current implementation"}` + "\n" +
		"```\n"
	turn := parseTurn(text)

	if turn.Final {
		t.Fatal("turn.Final = true, want false (a tool-call block → not a direct answer)")
	}
	ts := todos(turn)
	if len(ts) < 1 {
		t.Fatalf("parsed %d todos, want ≥1", len(ts))
	}
	first := ts[0]
	if first.tool == "" {
		t.Error("todo[0].intent (tool) empty; the plan must carry an intent")
	}
	if first.file == "" {
		t.Error("todo[0].file empty; the plan must carry a file target")
	}
	if first.tool != "read_file" {
		t.Errorf("todo[0].intent = %q, want %q", first.tool, "read_file")
	}
	if first.file != "auth/login.go" {
		t.Errorf("todo[0].file = %q, want %q", first.file, "auth/login.go")
	}
}

// TestParseMultipleToolBlocksMakesMultipleTodos pins that a turn with several
// ```tool blocks produces a plan with one todo per block, in order.
func TestParseMultipleToolBlocksMakesMultipleTodos(t *testing.T) {
	text := "```tool\n" +
		`{"tool":"read_file","args":{"file":"a.go"},"reason":""}` + "\n" +
		"```\n" +
		"now edit\n" +
		"```tool\n" +
		`{"tool":"edit_file","args":{"file":"a.go"},"reason":"fix the bug"}` + "\n" +
		"```\n"
	turn := parseTurn(text)

	ts := todos(turn)
	if len(ts) != 2 {
		t.Fatalf("parsed %d todos, want 2", len(ts))
	}
	if ts[0].tool != "read_file" || ts[1].tool != "edit_file" {
		t.Errorf("todo order = %q,%q; want read_file,edit_file", ts[0].tool, ts[1].tool)
	}
}

// TestParseFinalWhenNoToolBlocks pins §7.2.3: a turn with no ```tool blocks is
// Final (a direct answer), and the whole text is the visible answer.
func TestParseFinalWhenNoToolBlocks(t *testing.T) {
	text := "This is a direct answer with no tools."
	turn := parseTurn(text)
	if !turn.Final {
		t.Error("turn.Final = false, want true (no tool blocks)")
	}
	if turn.Text != text {
		t.Errorf("turn.Text = %q, want %q", turn.Text, text)
	}
	if len(turn.ToolCalls) != 0 {
		t.Errorf("parsed %d tool calls, want 0", len(turn.ToolCalls))
	}
}

// TestParseVisibleAnswerExcludesToolBlocks pins that the text outside ```tool
// blocks becomes the visible answer, with blocks removed and surrounding
// whitespace trimmed.
func TestParseVisibleAnswerExcludesToolBlocks(t *testing.T) {
	text := "Here's my plan.\n```tool\n" + `{"tool":"read_file","args":{"file":"x.go"}}` + "\n```\nLet me proceed."
	turn := parseTurn(text)
	if !strings.Contains(turn.Text, "Here's my plan.") {
		t.Errorf("visible answer lost leading prose; got %q", turn.Text)
	}
	if !strings.Contains(turn.Text, "Let me proceed.") {
		t.Errorf("visible answer lost trailing prose; got %q", turn.Text)
	}
	if strings.Contains(turn.Text, "```tool") {
		t.Errorf("visible answer contains a tool block; got %q", turn.Text)
	}
	if strings.Contains(turn.Text, "read_file") {
		t.Errorf("visible answer contains tool-call JSON; got %q", turn.Text)
	}
}

// TestParseMalformedBlockRecoversAsProse pins that a malformed ```tool block
// (bad JSON) is skipped rather than failing the turn — the model occasionally
// emits a partial block during streaming; the parser recovers.
func TestParseMalformedBlockRecoversAsProse(t *testing.T) {
	text := "```tool\n{not valid json\n```\nanswer follows"
	turn := parseTurn(text)
	// No tool call parsed from the malformed block → the turn is Final.
	if !turn.Final {
		t.Error("turn.Final = false, want true (malformed block should not count as a tool call)")
	}
	if len(turn.ToolCalls) != 0 {
		t.Errorf("parsed %d tool calls from a malformed block, want 0", len(turn.ToolCalls))
	}
}

// TestParseMissingToolFieldSkipped pins that a ```tool block whose JSON object
// lacks a "tool" field is not a tool call (the intent is the required field).
func TestParseMissingToolFieldSkipped(t *testing.T) {
	text := "```tool\n" + `{"args":{"file":"x.go"},"reason":"no tool"}` + "\n```\n"
	turn := parseTurn(text)
	if !turn.Final {
		t.Error("turn.Final = false, want true (a block missing 'tool' is not a tool call)")
	}
}

// TestParseReasonCarriesIntent pins that the reason field is preserved on the
// parsed ToolCall — the model's stated rationale feeds the event trace and the
// reflection loop.
func TestParseReasonCarriesIntent(t *testing.T) {
	text := "```tool\n" + `{"tool":"read_file","args":{"file":"x.go"},"reason":"understand the shape"}` + "\n```\n"
	turn := parseTurn(text)
	if len(turn.ToolCalls) != 1 {
		t.Fatalf("parsed %d tool calls, want 1", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Reason != "understand the shape" {
		t.Errorf("reason = %q, want the stated rationale", turn.ToolCalls[0].Reason)
	}
}

// TestThinkEndToEndParsesToolBlock pins the full streaming path: a mock
// provider streaming a ```tool block in deltas yields a parsed plan, not Final.
func TestThinkEndToEndParsesToolBlock(t *testing.T) {
	// The block arrives in two deltas, exercising the accumulator + parser.
	chunks := []Chunk{
		{Delta: "Planning...\n```tool\n" + `{"tool":"read_file","args":{"file":"y.go"},"reason":"r"}` + "\n"},
		{Delta: "```\ndone."},
	}
	core, _ := newTestCore(t, chunks)
	turn, err := core.Think(ctxWithTask("t_a"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if turn.Final {
		t.Fatal("turn.Final = true, want false (a streamed tool block → not final)")
	}
	ts := todos(turn)
	if len(ts) != 1 || ts[0].tool != "read_file" || ts[0].file != "y.go" {
		t.Errorf("parsed todos = %+v, want one read_file/y.go", ts)
	}
}
