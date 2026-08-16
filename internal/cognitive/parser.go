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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// toolBlockFence is the marker the model uses to delimit a tool-call block.
// Per §7.2.3 the block is fenced ```tool … ```.
const toolBlockFence = "```tool"

// closingFence ends a ```tool block. It is also, verbatim, what a model writes
// inside an edit_file payload whenever the file it is writing contains a code
// fence — see toolBlockEnd for why that stopped truncating the block.
const closingFence = "```"

// errToolBlockUnparsed reports that the model opened a ```tool block, the block
// held something that was trying to be JSON, and none of it decoded into a
// call. Think returns it rather than a Turn: a turn that lost its only tool
// call is not a finished answer, and the alternative — Final=true — made the
// runtime publish the mangled block as the assistant's reply and mark the task
// DONE with the work never done.
var errToolBlockUnparsed = errors.New("cognitive: a ```tool block held undecodable JSON and produced no tool call")

// parseTurn parses the accumulated stream text into a Turn (File 07 §7.2.3):
// every ```tool block becomes a ToolCall; the text outside blocks is the
// visible answer (Text). Final is true iff the text carried no tool-call block
// that meant anything — a block that opened and then failed to decode leaves
// Final false, because reporting a direct answer for work that was never
// parsed is the one failure mode with no downstream recovery.
// The JSON object inside a block is {tool, args, reason}; args is preserved
// as a raw JSON byte slice (may be an object with a "file" field, etc.).
//
// This is the error-discarding view, kept for callers that only want the Turn
// (tests, and anything with nowhere to put an error). Callers that can report a
// failure — Core.Think — use parseTurnChecked; Final is already false in the
// undecodable case so discarding the error still cannot produce a false DONE.
func parseTurn(text string) Turn {
	turn, _ := parseTurnChecked(text)
	return turn
}

// parseTurnChecked is parseTurn plus the reason the turn is not Final. The
// error is errToolBlockUnparsed and only fires when a block that was trying to
// be a tool call decoded to nothing; a turn that parsed at least one call
// anywhere returns nil, so a malformed tail after good calls stays recoverable.
func parseTurnChecked(text string) (Turn, error) {
	calls, body, unparsed := parseToolBlocks(text)
	turn := Turn{
		Text:      body,
		Final:     len(calls) == 0 && !unparsed,
		ToolCalls: calls,
	}
	if unparsed && len(calls) == 0 {
		return turn, errToolBlockUnparsed
	}
	return turn, nil
}

// parseToolBlocks extracts the ```tool blocks from text, returning the parsed
// tool calls, the visible answer (text with blocks removed), and whether any
// block was a tool call the parser could not read. A block holding prose rather
// than JSON is still skipped and recovered as prose — the model occasionally
// narrates inside a fence — but a block whose content opens with { or [ and
// then fails to decode sets unparsed: that was a call, and losing it silently
// is what let a turn report DONE without touching a file.
func parseToolBlocks(text string) ([]ToolCall, string, bool) {
	var calls []ToolCall
	var body strings.Builder
	unparsed := false
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
		// The block runs until its closing fence; find the one that actually
		// ends it rather than the first three backticks in sight.
		end := toolBlockEnd(rest)
		if end < 0 {
			// Nothing closes the block at JSON depth 0. That is a genuinely
			// truncated block (unbalanced braces or an unterminated string), so
			// fall back to the first fence: cutting there keeps a partial block
			// from swallowing every later block in the message.
			end = strings.Index(rest, closingFence)
		}
		var block, after string
		if end >= 0 {
			block = rest[:end]
			after = rest[end+len(closingFence):]
		} else {
			block = rest
			after = ""
		}
		blockCalls, err := parseToolCalls(block)
		if err != nil && len(blockCalls) == 0 && looksLikeJSON(block) {
			unparsed = true
		}
		calls = append(calls, blockCalls...)
		rest = after
	}
	return calls, strings.TrimSpace(body.String()), unparsed
}

// looksLikeJSON reports whether a block's content was trying to be a tool call
// at all: a JSON object or array, as opposed to the prose a model sometimes
// puts inside a fence. It is the guard that keeps a narrated block recoverable
// while a mangled call is escalated — both produce zero calls, and only the
// second one means work was lost.
func looksLikeJSON(block string) bool {
	t := strings.TrimSpace(block)
	return len(t) > 0 && (t[0] == '{' || t[0] == '[')
}

// toolBlockEnd returns the offset of the ``` that closes a tool block whose
// content starts at the beginning of s, or -1 when no fence sits outside the
// payload. The scan is JSON-aware — it tracks object/array depth and string
// quoting, honouring backslash escapes — so backticks inside a JSON string are
// content, not a delimiter.
//
// Taking the first ``` instead truncated any call whose args embedded a code
// fence, which is the common case and not an exotic one: the edit_file schema
// asks for the FULL file content, so writing any markdown file, or any source
// file quoting a fence, cut the JSON mid-string. The block then failed to
// decode, the turn parsed zero calls, and the runtime reported the task DONE
// with the file untouched.
//
// Depth-0 scanning is what makes two tool blocks in one message still work
// (preferring the LAST fence would merge them). Its residual blind spot is
// prose inside a fence carrying an unbalanced { or [ before the closing
// fence — depth never returns to 0, so the scan reports -1 and the caller falls
// back to the first-fence cut, i.e. exactly the old behaviour for that case.
func toolBlockEnd(s string) int {
	depth := 0
	inString := false
	for i := 0; i < len(s); i++ {
		if inString {
			switch s[i] {
			case '\\':
				i++ // the escaped byte is content, whatever it is
			case '"':
				inString = false
			}
			continue
		}
		switch s[i] {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case '`':
			if depth == 0 && strings.HasPrefix(s[i:], closingFence) {
				return i
			}
		}
	}
	return -1
}

// parseToolCalls extracts every tool call packed into one ```tool block. The
// block normally holds a single {tool, args, reason} object, but a model
// emitting parallel calls commonly packs them all into one block — either as a
// JSON array or as several objects back to back. Decoding the block as a stream
// of top-level JSON values handles all three shapes; before, the whole-block
// unmarshal rejected both packed forms outright, so a parallel-call turn lost
// every call, not just calls 2..N.
//
// The calls decoded so far are always returned, along with the syntax error
// that stopped the scan (nil at a clean end of input). A tail that fails after
// good calls is a recoverable fragment and the caller keeps the calls; a block
// that fails having produced none lost the whole call, and the caller escalates
// it rather than letting the turn pass for a finished answer.
func parseToolCalls(block string) ([]ToolCall, error) {
	dec := json.NewDecoder(strings.NewReader(block))
	var calls []ToolCall
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return calls, nil // clean end of the block
			}
			return calls, err
		}
		calls = append(calls, parseToolValue(raw)...)
	}
}

// parseToolValue turns one decoded JSON value into zero or more tool calls: an
// array yields one call per element, anything else yields at most one.
func parseToolValue(raw json.RawMessage) []ToolCall {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var elems []json.RawMessage
		if err := json.Unmarshal(trimmed, &elems); err != nil {
			return nil
		}
		calls := make([]ToolCall, 0, len(elems))
		for _, e := range elems {
			if call, ok := parseToolJSON(string(e)); ok {
				calls = append(calls, call)
			}
		}
		return calls
	}
	if call, ok := parseToolJSON(string(trimmed)); ok {
		return []ToolCall{call}
	}
	return nil
}

// parseToolJSON unmarshals one ```tool block's JSON object into a ToolCall.
// The object is {tool, args, reason}; args may be any JSON value (commonly an
// object with a "file" field). A missing reason is allowed (defaults to "");
// a missing tool or a bad JSON value means the block is not a tool call.
//
// An "id" in the block is deliberately ignored (json.Unmarshal drops unknown
// keys), leaving ID empty as ToolCall documents for fenced calls. The id the
// Core renders into the history is written for the MODEL to read, so the model
// can echo it straight back; honouring the echo let two different tools in one
// turn arrive under the same id, and takePending's exact-id match then handed
// the second tool's output to the first call. The Core mints ids it controls
// (recordAssistantTurn) precisely so no model-supplied string can collide.
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
