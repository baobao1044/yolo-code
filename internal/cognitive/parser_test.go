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

// TestParseNarratedBlockRecoversAsProse pins the half of the old
// "malformed block recovers as prose" contract that is still true: a ```tool
// fence the model filled with prose rather than JSON was never a call, so the
// turn keeps reading as a direct answer.
func TestParseNarratedBlockRecoversAsProse(t *testing.T) {
	text := "```tool\nI don't need a tool for this one.\n```\nanswer follows"
	turn, err := parseTurnChecked(text)
	if err != nil {
		t.Errorf("parseTurnChecked = %v, want nil (a narrated fence is not a lost tool call)", err)
	}
	if !turn.Final {
		t.Error("turn.Final = false, want true (prose in a fence should not count as a tool call)")
	}
	if len(turn.ToolCalls) != 0 {
		t.Errorf("parsed %d tool calls from a narrated block, want 0", len(turn.ToolCalls))
	}
}

// TestParseUndecodableBlockIsNotFinal pins the half that was wrong, and it is
// the whole reason a broken tool call could report success. A block whose
// content opens as JSON and then fails to decode was a tool call — the work was
// requested and lost. Final=true there tells the runtime "direct answer", so it
// published the mangled block as the reply and marked the task DONE without
// running anything. Not-final plus an error is the only honest shape.
func TestParseUndecodableBlockIsNotFinal(t *testing.T) {
	text := "```tool\n{not valid json\n```\nanswer follows"
	turn, err := parseTurnChecked(text)
	if err == nil {
		t.Error("parseTurnChecked = nil error for a block that lost its only tool call")
	}
	if turn.Final {
		t.Error("turn.Final = true for an undecodable tool block; the runtime would report the task DONE with the work never done")
	}
	if len(turn.ToolCalls) != 0 {
		t.Errorf("parsed %d tool calls from an undecodable block, want 0", len(turn.ToolCalls))
	}
	// The error-discarding view must be safe too: parseTurn is what stub_test
	// and any future caller reach for, and Final is the field that does damage.
	if parseTurn(text).Final {
		t.Error("parseTurn(text).Final = true; discarding the error must not restore the false DONE")
	}
}

// TestParseToolCallArgsContainingCodeFence is the F1 reproduction, verbatim in
// shape: an edit_file call whose content writes a markdown file that itself
// contains a ```bash fence. Cutting the block at the FIRST ``` truncated the
// JSON mid-string, so the call failed to decode, the turn parsed zero calls and
// reported Final=true — the runtime then published the wreckage as the answer
// and marked the task DONE with README.md untouched. This is not exotic: the
// edit_file schema asks for the FULL file content, so every markdown file and
// every source file quoting a fence hits it.
func TestParseToolCallArgsContainingCodeFence(t *testing.T) {
	// The args content is a JSON string holding "## Usage\n```bash\nyolo run\n```\n".
	args := `{"file":"README.md","content":"## Usage\n` + "```" + `bash\nyolo run\n` + "```" + `\n"}`
	text := "Updating the README.\n```tool\n" +
		`{"tool":"edit_file","args":` + args + `}` + "\n" +
		"```\n"

	turn, err := parseTurnChecked(text)
	if err != nil {
		t.Fatalf("parseTurnChecked = %v, want nil (the call is well-formed; only the delimiter was wrong)", err)
	}
	if turn.Final {
		t.Fatal("turn.Final = true; a turn whose only tool call was truncated must never read as a finished answer")
	}
	if len(turn.ToolCalls) != 1 {
		t.Fatalf("parsed %d tool calls, want 1 (the fence inside args must not end the block)", len(turn.ToolCalls))
	}
	call := turn.ToolCalls[0]
	if call.Tool != "edit_file" {
		t.Errorf("tool = %q, want edit_file", call.Tool)
	}
	// The args must survive intact — a call whose content was cut short would
	// write a truncated README, which is worse than not writing one.
	var got struct {
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(call.Args, &got); err != nil {
		t.Fatalf("args did not survive as valid JSON: %v (args = %s)", err, call.Args)
	}
	if got.File != "README.md" {
		t.Errorf("args.file = %q, want README.md", got.File)
	}
	want := "## Usage\n```bash\nyolo run\n```\n"
	if got.Content != want {
		t.Errorf("args.content = %q, want %q (the embedded fence was truncated)", got.Content, want)
	}
	if strings.Contains(turn.Text, "yolo run") {
		t.Errorf("the block leaked into the visible answer; got %q", turn.Text)
	}
}

// TestParseTwoToolBlocksWithFencedArgs is the case that rules out the two easy
// delimiter fixes. Preferring the LAST ``` in the message merges these two
// blocks into one; a "fence at the start of a line" rule cuts block 1 at the
// ```go inside its own JSON string, which begins a line. Only a scan that
// tracks JSON string quoting and brace depth ends each block where it ends.
func TestParseTwoToolBlocksWithFencedArgs(t *testing.T) {
	text := "```tool\n" +
		`{"tool":"edit_file","args":{"file":"a.md","content":"` + "```" + `go\npackage a\n` + "```" + `"}}` + "\n" +
		"```\n" +
		"then\n" +
		"```tool\n" +
		`{"tool":"read_file","args":{"file":"b.go"}}` + "\n" +
		"```\n"

	turn := parseTurn(text)
	ts := todos(turn)
	if len(ts) != 2 {
		t.Fatalf("parsed %d todos, want 2 (blocks = %+v)", len(ts), ts)
	}
	if ts[0].tool != "edit_file" || ts[0].file != "a.md" {
		t.Errorf("todo[0] = %+v, want edit_file/a.md", ts[0])
	}
	if ts[1].tool != "read_file" || ts[1].file != "b.go" {
		t.Errorf("todo[1] = %+v, want read_file/b.go", ts[1])
	}
}

// TestParseIgnoresModelSuppliedID is the F3 parser half. The Core renders each
// call back into the history tagged with its id so the MODEL can see which
// result answers which call — which means the model can echo an id straight
// back. Honouring the echo put two different tools in one turn under one id and
// the result pairing then handed grep's output to read_file. The fence format
// carries no tool_call_id (see ToolCall.ID), so the parser leaves it empty and
// the Core mints ids it alone controls.
func TestParseIgnoresModelSuppliedID(t *testing.T) {
	text := "```tool\n" +
		`{"id":"call_1","tool":"read_file","args":{}}` + "\n" +
		`{"tool":"grep","args":{}}` + "\n" +
		"```\n"
	turn := parseTurn(text)
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("parsed %d tool calls, want 2", len(turn.ToolCalls))
	}
	for i, c := range turn.ToolCalls {
		if c.ID != "" {
			t.Errorf("call[%d] (%s).ID = %q, want \"\"; a model-supplied id must not reach the pairing", i, c.Tool, c.ID)
		}
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

// TestThinkOnFencedArgsDoesNotReportAFinishedAnswer walks the F1 reproduction
// through the seam that did the damage. The runtime branches on exactly two
// things — turn.Final and turn.ToolCalls — so this asserts the pair a caller
// sees: the edit_file call is there to dispatch, and Final is false, meaning
// the runtime cannot take the direct_answer branch that published the mangled
// block and called CompleteTask on an untouched file.
func TestThinkOnFencedArgsDoesNotReportAFinishedAnswer(t *testing.T) {
	args := `{"file":"README.md","content":"## Usage\n` + "```" + `bash\nyolo run\n` + "```" + `\n"}`
	text := "Updating the README.\n```tool\n" +
		`{"tool":"edit_file","args":` + args + `}` + "\n" +
		"```\n"
	// Split mid-payload so the block is reassembled by the accumulator, the way
	// it arrives on the wire.
	half := len(text) / 2
	core, _ := newTestCore(t, []Chunk{{Delta: text[:half]}, {Delta: text[half:]}})
	turn, err := core.Think(ctxWithTask("t_fencedargs"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if turn.Final {
		t.Fatalf("turn.Final = true; the runtime would publish %q as the answer and mark the task DONE", turn.Text)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Tool != "edit_file" {
		t.Fatalf("turn.ToolCalls = %+v, want one edit_file call to dispatch", turn.ToolCalls)
	}
}

// TestThinkErrorsRatherThanClaimingDoneOnABrokenBlock pins F1(b) at the Think
// seam. A block that opened as a tool call and decoded to nothing is neither of
// the runtime's two branches; an error puts the task in ERROR where a human can
// see it. Silently returning Final=true reported success for work never done,
// which is the one outcome with no downstream recovery.
func TestThinkErrorsRatherThanClaimingDoneOnABrokenBlock(t *testing.T) {
	core, _ := newTestCore(t, []Chunk{{Delta: "```tool\n{\"tool\":\"edit_file\",\"args\":{\"file\":\n```\ndone!"}})
	turn, err := core.Think(ctxWithTask("t_broken"), nil)
	if err == nil {
		t.Fatalf("Think = nil error on an undecodable tool block; turn = %+v", turn)
	}
	if turn.Final {
		t.Error("the error-path Turn claims Final; a caller ignoring the error would report DONE")
	}
	if turn.Text != "" || len(turn.ToolCalls) != 0 {
		t.Errorf("error-path Turn carries content (Text=%q, %d calls); it must assert nothing but usage", turn.Text, len(turn.ToolCalls))
	}
}

// TestParseToolBlockWithJSONArrayOfCalls pins the 4.14 parser half: a model
// emitting parallel calls commonly packs them into one ```tool block as a JSON
// array. Decoding that block as a single object failed outright, so every call
// in the turn was dropped, not just calls 2..N.
func TestParseToolBlockWithJSONArrayOfCalls(t *testing.T) {
	text := "```tool\n" +
		`[{"tool":"read_file","args":{"file":"a.go"}},` +
		`{"tool":"read_file","args":{"file":"b.go"}},` +
		`{"tool":"grep","args":{"file":"c.go"},"reason":"find it"}]` + "\n" +
		"```\n"
	turn := parseTurn(text)

	ts := todos(turn)
	if len(ts) != 3 {
		t.Fatalf("parsed %d todos from a 3-element array, want 3", len(ts))
	}
	if ts[0].file != "a.go" || ts[1].file != "b.go" || ts[2].tool != "grep" {
		t.Errorf("parsed todos = %+v, want a.go, b.go, grep/c.go in order", ts)
	}
	if turn.Final {
		t.Error("turn.Final = true, want false (the block carries tool calls)")
	}
}

// TestParseToolBlockWithConcatenatedObjects pins the other packing models use:
// several {tool,...} objects back to back inside one ```tool block. Only the
// whole-block unmarshal saw them, and it rejected the trailing data.
func TestParseToolBlockWithConcatenatedObjects(t *testing.T) {
	text := "```tool\n" +
		`{"tool":"read_file","args":{"file":"a.go"}}` + "\n" +
		`{"tool":"edit_file","args":{"file":"b.go"},"reason":"fix"}` + "\n" +
		"```\n"
	turn := parseTurn(text)

	ts := todos(turn)
	if len(ts) != 2 {
		t.Fatalf("parsed %d todos from two concatenated objects, want 2", len(ts))
	}
	if ts[0].tool != "read_file" || ts[1].tool != "edit_file" {
		t.Errorf("todo order = %q,%q; want read_file,edit_file", ts[0].tool, ts[1].tool)
	}
}
