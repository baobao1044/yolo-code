// The Planner's tool-call / plan parser (File 07 §7.2.3). The model is
// instructed to emit tool calls as fenced ```tool blocks containing one JSON
// object {tool, args, reason} per call. The parser scans the accumulated
// stream text for these blocks; text outside a block is the visible answer. A
// turn is Final iff it contains zero tool-call blocks (§7.2.3). This is the
// provider-agnostic portable path — no reliance on native function-calling.
//
// A "plan" (todo list) is the set of tool calls a turn produced. Each todo is
// a tool call with a file target + an intent: the L6-002 exit bar requires a
// parsed plan to carry ≥1 todo with file+intent. The parser surfaces exactly
// the {tool, args, reason} object as a ToolCall, where `tool` is the intent
// and a `file` field in args is the target.

package cognitive

import (
	"encoding/json"
	"strings"
)

// toolBlockFence is the marker the model uses to delimit a tool-call block.
// Per §7.2.3 the block is fenced ```tool … ```.
const toolBlockFence = "```tool"

// parseTurn parses the accumulated stream text into a Turn (File 07 §7.2.3):
// every ```tool block becomes a ToolCall; the text outside blocks is the
// visible answer (Text). Final is true iff no tool-call blocks were found.
// The JSON object inside a block is {tool, args, reason}; args is preserved
// as a raw JSON byte slice (may be an object with a "file" field, etc.).
func parseTurn(text string) Turn {
	calls, body := parseToolBlocks(text)
	return Turn{
		Text:      body,
		Final:     len(calls) == 0,
		ToolCalls: calls,
	}
}

// parseToolBlocks extracts the ```tool blocks from text, returning the parsed
// tool calls and the visible answer (text with blocks removed). A malformed
// block (bad JSON or missing fields) is skipped — the model occasionally emits
// a partial block during streaming; the parser recovers by treating it as
// prose rather than failing the whole turn.
func parseToolBlocks(text string) ([]ToolCall, string) {
	var calls []ToolCall
	var body strings.Builder
	rest := text
	for {
		i := strings.Index(rest, toolBlockFence)
		if i < 0 {
			body.WriteString(rest)
			break
		}
		// Prose before the block.
		body.WriteString(rest[:i])
		// Skip the opening fence and the rest of its line.
		rest = rest[i+len(toolBlockFence):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		} else {
			rest = "" // opening fence at EOF: nothing to parse
		}
		// The block runs until the closing fence ``` ; collect it.
		end := strings.Index(rest, "```")
		var block, after string
		if end >= 0 {
			block = rest[:end]
			after = rest[end+len("```"):]
		} else {
			block = rest
			after = ""
		}
		if call, ok := parseToolJSON(block); ok {
			calls = append(calls, call)
		}
		rest = after
	}
	return calls, strings.TrimSpace(body.String())
}

// parseToolJSON unmarshals one ```tool block's JSON object into a ToolCall.
// The object is {tool, args, reason}; args may be any JSON value (commonly an
// object with a "file" field). A missing reason is allowed (defaults to "");
// a missing tool or a bad JSON value means the block is not a tool call.
func parseToolJSON(block string) (ToolCall, bool) {
	var obj struct {
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
		Reason string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(block)), &obj); err != nil {
		return ToolCall{}, false
	}
	if obj.Tool == "" {
		return ToolCall{}, false
	}
	return ToolCall{Tool: obj.Tool, Args: []byte(obj.Args), Reason: obj.Reason}, true
}
