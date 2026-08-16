// The provider-agnostic interface and the streaming shapes (File 07 §7.7.1). A
// Provider streams a response one Chunk at a time; the Core reads the channel,
// publishes token/thinking events as deltas arrive, and accumulates the text
// for parsing. This is the seam that makes S9 (add a provider without touching
// the runtime) true: one interface, one implementation per provider
// (OpenAI/Anthropic/Gemini/local llama.cpp).

package cognitive

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/session"
)

// Request is the input to a Provider: the compiled prompt's messages plus the
// tool schemas the model may emit (File 07 §7.2.2). Sprint 3 carries tools as
// a list of names; the schemas are wired when the Tool Registry (File 08)
// lands.
type Request struct {
	Messages []prompt.Message
	Tools    []string // tool names the model may call; schemas wired in L7
}

// Chunk is one element of a streamed response. A turn is accumulated from the
// Delta strings; Thinking carries the model's chain-of-thought (rendered
// separately, File 07 §7.4); ToolCall is non-nil when the provider surfaces a
// structured tool call; Usage is non-nil on the one chunk that carries the
// provider's token counts; Err terminates the stream with an error.
type Chunk struct {
	Delta    string    // a token/text delta of the visible answer
	Thinking string    // a chain-of-thought delta
	ToolCall *ToolCall // non-nil for a structured tool call
	Usage    *Usage    // non-nil when the provider reported token counts
	Err      error     // non-nil terminates the stream with an error
}

// Usage is a provider-reported token count for one request. It is a pointer on
// Chunk and a flag on Turn rather than two bare ints because "the server did
// not tell us" has to stay distinguishable from "the server told us zero": a
// confident 0 is exactly what let an empty cost ledger read as a cheap run.
// Only the provider fills this in — nothing here estimates.
type Usage struct {
	TokensIn  int
	TokensOut int
}

// ToolCall is a tool the Planner chose (File 07 §7.2.3). The portable default
// parses tool calls from fenced ```tool blocks; providers with native tool
// calls surface them as Chunk.ToolCall. Args is the raw JSON args object.
//
// ID is the provider's own tool_call_id when the call came off the wire, and
// "" for calls parsed from a fenced block (that format carries no id). It is
// the only reliable way to pair a tool result back to its call: a turn may
// call the same tool twice, so matching by tool name — or by position — is
// wrong. Callers that need an id for a fenced call must mint their own.
type ToolCall struct {
	ID     string // provider's tool_call_id; "" for fenced-block calls
	Tool   string
	Args   []byte // json.RawMessage
	Reason string
}

// Turn is the parsed result of one Planner call (File 07 §7.2.2): either a
// final answer (Final=true, Text set) or a set of tool calls (ToolCalls set).
// TokensIn/Out feed the Cost Controller's ledger.
//
// UsageKnown says whether TokensIn/Out are a measurement. Two ints cannot say
// "no idea" — 0 reads as free — so the flag carries that half of the truth: it
// is true only when the provider reported counts on the wire. A caller that
// bills, budgets, or displays these numbers must check it first; on a false
// UsageKnown the honest rendering is "unknown", never "0".
type Turn struct {
	Text       string
	Final      bool
	ToolCalls  []ToolCall
	TokensIn   int
	TokensOut  int
	UsageKnown bool
}

// Provider is the provider-agnostic seam (File 07 §7.7.1). Stream returns a
// channel the caller drains; closing the channel signals a clean end, an Err
// chunk signals failure. Window returns the provider's context window (used by
// the Prompt Compiler's budget, File 06 §6.6.1).
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
	Window() int
}

// taskID retrieves the session task ID from the context (File 07 publishes
// TokenEvent{Task}; the runtime attaches the ID via session.WithTaskID).
func taskID(ctx context.Context) session.TaskID {
	return session.TaskIDFromContext(ctx)
}
