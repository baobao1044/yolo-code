// The Cognitive Core (File 07 §7.1/§7.7). The Core is a set of specialized,
// prompt-driven sub-roles invoked by the runtime at the right FSM state:
// the Planner (Think) at PLAN, Reflection at VERIFY failure, the Reasoner as
// the streamed chain-of-thought. Sprint 3 (L6-001…007) lands the Planner's
// streaming Think, the plan + tool-call parsing, reflection, the tool/verify
// policies, and the Cost Controller's degradation ladder — against mock and
// deterministic-stub providers (no real LLM call yet).

package cognitive

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/session"
)

// maxHistoryMessages bounds the accumulated conversation. A long agent loop
// appends an assistant message plus one tool result per tool call, so an
// unbounded history eventually overruns the provider's context window and
// re-bills every earlier turn on every request. The bound is on messages, not
// tokens, because the Prompt Compiler (Layer 5) owns the token budget — this is
// the cheap backstop that keeps the loop from growing without limit.
const maxHistoryMessages = 200

// toolResultPrefix marks a history message as a tool result. Results are
// recorded as role "user" (universally supported; some chat endpoints reject
// role "tool"), so the prefix is what identifies them when trimming.
const toolResultPrefix = "[Tool Result:"

// Core is the Cognitive Core (File 07 §7.7). The provider streams responses;
// the bus carries token/thinking/tool-call events; the parser turns the
// accumulated text into a Turn. Sprint 3 wires provider + bus; the parser,
// policies, and cost controller land in L6-002…006.
type Core struct {
	provider Provider
	bus      *event.Bus
	tools    []string         // tool names the Planner may emit (passed to the provider for native tool calling)
	policy   *ToolPolicy      // default-deny allowlist over those names; nil disables enforcement
	denials  int              // consecutive turns whose every tool call was denied
	lastTurn Turn             // most recent Think result; HasMore consults this
	history  []prompt.Message // accumulated conversation across turns (tool calls + results)
	pending  []pendingCall    // last turn's tool calls still awaiting a result
	callSeq  int              // monotonic counter behind the generated tool call ids
}

// pendingCall is one tool call from the last turn that has not been answered
// yet. The id is the provider's tool_call_id when the call came off the wire,
// and a Core-minted call_N for a fenced ```tool call (that format carries
// none). RecordToolResultID consumes these by id so a turn with N parallel
// calls produces N results, each naming the call it answers.
type pendingCall struct {
	id   string
	tool string
}

// New constructs a Core. The bus is optional (unit tests can pass nil and
// inspect the returned Turn directly); Think is a no-op for publishing when
// bus is nil. Tools is optional (nil → the provider won't include tool
// definitions; the parser's ```tool block path still works).
//
// The tool names double as the Tool Policy's allowlist (File 07 §7.5.1): the
// set the model is offered is exactly the set it may call, derived here so
// there is nothing to keep in sync. A Core given no tools gets no policy and
// enforces nothing — it was never told what exists, and an empty allowlist
// there would deny every call rather than the unoffered ones. The composition
// root always passes the tool set, so production always has a policy; callers
// that need a wider one than the advertisement call SetToolPolicy.
func New(provider Provider, bus *event.Bus, tools ...string) *Core {
	c := &Core{provider: provider, bus: bus, tools: tools}
	if len(tools) > 0 {
		c.policy = NewToolPolicy(tools)
	}
	return c
}

// SetToolPolicy replaces the allowlist enforced on the model's tool calls. New
// already derives one from the advertised names, so this exists for the
// composition root's one asymmetry: a tool with a dispatch route but no
// advertisement (cmd/yolo routes "patch" to patch.Engine, and it carries
// neither a schema nor a line in the system prompt). Passing nil disables
// enforcement entirely and is not something production should do.
func (c *Core) SetToolPolicy(p *ToolPolicy) {
	c.policy = p
}

// SetProvider swaps the LLM provider at runtime (slash command /model,
// /provider). It keeps the conversation history + tools so a mid-session model
// switch preserves context. The caller (driver) must ensure no task is running
// (d.busy guard) before calling — Think reads provider in the drive goroutine.
func (c *Core) SetProvider(p Provider) {
	c.provider = p
}

// Reset starts a fresh conversation: the accumulated history, the outstanding
// tool calls, and the last turn are dropped, while the provider, bus and tool
// list stay wired. The driver calls it between independent tasks so one task's
// transcript does not leak into the next (and so a repeated goal is genuinely
// re-asked rather than merged into what is already there). Like SetProvider it
// must not be called while a task is running — Think reads history in the drive
// goroutine.
func (c *Core) Reset() {
	c.history = nil
	c.pending = nil
	c.lastTurn = Turn{}
	c.callSeq = 0
	c.denials = 0
}

// Think runs one Planner turn (File 07 §7.2.2): stream the provider, publish
// token/thinking deltas as events, accumulate the text, and return a parsed
// Turn (final answer or tool calls). The task ID is threaded via context
// (session.WithTaskID) so TokenEvent carries the right Task without changing
// the spec's Think(ctx, msgs) signature.
//
// For multi-turn agent loops, Think accumulates the conversation history across
// turns: the compiled prompt is merged in (see mergePrompt), the assistant's
// reply — including every tool call it made — is recorded, and the tool results
// arrive via RecordToolResult.
//
// Contract on a non-nil error: the returned Turn is NOT a turn. It never
// carries Text, ToolCalls or Final=true, so a caller that reads it as a reply
// gets an empty non-final turn rather than a plausible-looking answer. The only
// fields it may populate are TokensIn/TokensOut/UsageKnown, because a stream
// that breaks after the provider reported its counts still cost that money and
// the ledger is the one reader that must see them. Callers that bill are
// expected to read usage on BOTH paths and gate on UsageKnown, exactly as they
// do on success; every other field must be ignored when err != nil.
func (c *Core) Think(ctx context.Context, msgs []prompt.Message) (Turn, error) {
	c.mergePrompt(msgs)

	req := Request{Messages: c.history}

	// When the Core has been told which tools are available, pass them to the
	// provider so it can include native tool definitions in the request (e.g.
	// OpenAI function calling). This lets models that support structured tool
	// calling emit delta.tool_calls instead of inline tool tokens.
	if len(c.tools) > 0 {
		req.Tools = c.tools
	}

	stream, err := c.provider.Stream(ctx, req)
	if err != nil {
		return Turn{}, err
	}

	var (
		buf      strings.Builder
		toolCall []ToolCall
		usage    *Usage
	)
	for chunk := range stream {
		if chunk.Usage != nil {
			// The provider sends usage once, on a chunk of its own at the end of
			// the stream. Last one wins so a server that repeats it is harmless.
			// Read before the Err check, not after: a provider is free to put the
			// counts on the same chunk that ends the stream, and a count read
			// only on the success path is a count thrown away.
			usage = chunk.Usage
		}
		if chunk.Err != nil {
			// The provider already charged for the prompt it processed, and it
			// told us so before the connection dropped. Returning a bare Turn{}
			// here discarded a measured count: a run that loses the stream late
			// (the expensive turns, with the whole context in the prompt) billed
			// at $0.00 and MaxDollars could never fire. Carry the measurement out
			// with the error — see Think's contract above for what else the
			// error-path Turn is allowed to say (nothing).
			return usageTurn(usage), chunk.Err
		}
		if chunk.Delta != "" {
			buf.WriteString(chunk.Delta)
			c.publishTokens(ctx, chunk.Delta)
		}
		if chunk.Thinking != "" {
			c.publishThinking(ctx, chunk.Thinking)
		}
		if chunk.ToolCall != nil {
			toolCall = append(toolCall, *chunk.ToolCall)
		}
	}

	// Parse the accumulated text for fenced ```tool blocks (File 07 §7.2.3,
	// the provider-agnostic portable path). The parsed Turn carries the
	// visible answer as Text and the parsed tool calls; Final iff no calls.
	// Providers with native tool calls surface them as Chunk.ToolCall — those
	// merge with the parsed ones so both paths produce a Turn.
	turn, parseErr := parseTurnChecked(buf.String())
	// parseTurn reads text only and leaves UsageKnown false — the counts arrive
	// out of band on the chunk stream. Attach them here, and only here: a turn
	// whose provider said nothing keeps UsageKnown false, which is what stops a
	// downstream reader rendering "0 tokens" as if it had been measured.
	if usage != nil {
		turn.TokensIn, turn.TokensOut, turn.UsageKnown = usage.TokensIn, usage.TokensOut, true
	}
	if len(toolCall) > 0 {
		turn.ToolCalls = append(turn.ToolCalls, toolCall...)
		turn.Final = false
	}
	// The model opened a ```tool block and nothing in the turn decoded into a
	// call. Report it: the runtime's only two branches are "final answer" and
	// "dispatch these calls", and this turn is neither. It used to take the
	// final-answer branch, which published the truncated block as the reply and
	// marked the task DONE having done nothing. A turn that produced calls
	// somewhere — a native one off the wire, or an earlier good block — has real
	// work to dispatch and stays a turn, malformed straggler and all.
	if parseErr != nil && len(turn.ToolCalls) == 0 {
		return usageTurn(usage), parseErr
	}
	// Record the assistant's response in the conversation history so the next
	// Think() call includes it in the prompt; the tool results are appended
	// separately via RecordToolResult. This records the turn the model actually
	// produced, a call the policy is about to refuse included, so the refusal
	// below reads as an answer to something the transcript shows.
	c.recordAssistantTurn(turn)

	// Default-deny the tool names the model was never offered (File 07 §7.5.1).
	// This is the last point at which a tool call is still cognitive-layer data
	// rather than dispatchable work, so it is where the allowlist belongs.
	turn, err = c.enforceToolPolicy(ctx, turn)
	if err != nil {
		return usageTurn(usage), err
	}
	c.lastTurn = turn

	return turn, nil
}

// maxDeniedTurns bounds how many turns in a row may consist entirely of tool
// calls the policy refused. One refusal is not a failure — the model is told
// what it did wrong and gets another turn, which is the right first response —
// but a model that keeps naming a tool that does not exist never converges, and
// PLAN's only other brake is the cost ledger's wall clock. Three is enough for
// a model that can read the denial and few enough to stop one that cannot.
const maxDeniedTurns = 3

// enforceToolPolicy applies the default-deny allowlist to what the model asked
// for, and returns the turn carrying only the calls that passed (File 07
// §7.5.1). A refused call is answered the way a failed tool is: a tool result in
// the transcript, keyed to the call it answers, plus a recoverable error on the
// bus so the run shows it. The next Think therefore sees both its own bad call
// and the reason it was refused, and can pick a tool that exists.
//
// Final is deliberately not recomputed. A turn whose every call was refused
// keeps Final=false, so the runtime loops PLAN→EXECUTE(nothing)→PLAN for
// another attempt; recomputing it would take the direct-answer branch and mark
// the task DONE with the model's text published as a finished answer and no
// work done — the same failure the parser's unparsed-block guard exists to
// prevent.
//
// A Core with no policy enforces nothing; see New for why that is the right
// reading of "no tools were configured" rather than a hole.
func (c *Core) enforceToolPolicy(ctx context.Context, turn Turn) (Turn, error) {
	if c.policy == nil || len(turn.ToolCalls) == 0 {
		c.denials = 0
		return turn, nil
	}
	admitted := make([]ToolCall, 0, len(turn.ToolCalls))
	var refused []string
	for _, call := range turn.ToolCalls {
		err := c.policy.Allow(call)
		if err == nil {
			admitted = append(admitted, call)
			continue
		}
		refused = append(refused, call.Tool)
		c.RecordToolResult(call.Tool, err.Error())
		c.publishToolDenied(ctx, err)
	}
	if len(refused) == 0 {
		c.denials = 0
		return turn, nil
	}
	turn.ToolCalls = admitted
	if len(admitted) > 0 {
		// The turn still has real work; the refused call was a stray, not a
		// stuck model. Only a turn that produced nothing counts toward the cap.
		c.denials = 0
		return turn, nil
	}
	c.denials++
	if c.denials >= maxDeniedTurns {
		return turn, fmt.Errorf("cognitive: %d turns in a row called only tools that do not exist (last: %s); the available tools are %s",
			c.denials, strings.Join(refused, ", "), strings.Join(c.policy.AllowedTools(), ", "))
	}
	return turn, nil
}

// publishToolDenied surfaces a refused tool call on the bus so the run's
// transcript shows it rather than the call simply vanishing. Retry is true: the
// model gets another turn, so this is a recoverable error, not a dead task. A
// nil bus makes it a no-op, as everywhere else in the Core.
func (c *Core) publishToolDenied(ctx context.Context, err error) {
	if c.bus == nil {
		return
	}
	_ = c.bus.Publish(ctx, &event.ErrorEvent{
		Task:  event.TaskID(taskID(ctx)),
		Layer: "cognitive",
		Code:  "tool_not_allowed",
		Msg:   err.Error(),
		Retry: true,
	})
}

// usageTurn builds the Turn that accompanies a Think error: no text, no calls,
// Final false — the only thing it asserts is what the provider measured before
// things went wrong. It exists so the cost ledger can still be told about a
// prompt that was charged for, without any error path ever handing a caller
// something that could be mistaken for an answer.
func usageTurn(u *Usage) Turn {
	if u == nil {
		return Turn{}
	}
	return Turn{TokensIn: u.TokensIn, TokensOut: u.TokensOut, UsageKnown: true}
}

// mergePrompt merges a freshly compiled prompt into the conversation history.
// The runtime re-compiles the whole prompt from the task's ContextPackage on
// every PLAN turn, so most of msgs is a verbatim repeat of what history already
// holds — but not all of it: a new task compiles a new goal against the same
// system prompt, and that goal MUST reach the model. So we append exactly the
// messages history does not already carry, in order. That keeps the system
// prompt single while never discarding new instructions; the old "seed only
// when history is empty" rule silently dropped every task after the first.
// A caller that wants the prior turns gone rather than merged calls Reset.
func (c *Core) mergePrompt(msgs []prompt.Message) {
	for _, m := range msgs {
		if c.historyHas(m) {
			continue
		}
		c.history = append(c.history, m)
	}
	c.trimHistory()
}

// historyHas reports whether the conversation already carries an identical
// message (same role, same content).
func (c *Core) historyHas(m prompt.Message) bool {
	for _, h := range c.history {
		if h.Role == m.Role && h.Content == m.Content {
			return true
		}
	}
	return false
}

// recordAssistantTurn records the assistant's reply, tool calls included. A
// turn that called tools records EVERY call — one ```tool block per call, each
// tagged with the id its result will quote — not just the visible text.
// Recording only the text left a parallel-tool-call turn as an empty assistant
// message followed by N unattributed results: the model could not tell which
// call produced which output, so it re-asked or invented one.
func (c *Core) recordAssistantTurn(turn Turn) {
	c.pending = nil
	if turn.Text == "" && len(turn.ToolCalls) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(turn.Text)
	for _, call := range turn.ToolCalls {
		// Prefer the provider's own tool_call_id: it is what the model will
		// look for, and it is the only id the runtime can quote back. Fenced
		// ```tool blocks carry no id, so those still get a minted one — as does
		// a call whose id is already outstanding this turn. Two pending calls
		// under one id are worse than a renamed one: takePending matches the
		// first by id, so the second tool's output is filed as the first tool's
		// answer. Minting until the id is free keeps the pairing one-to-one
		// whatever a provider (or a model echoing a rendered block) sends.
		id := call.ID
		for id == "" || c.pendingHasID(id) {
			c.callSeq++
			id = fmt.Sprintf("call_%d", c.callSeq)
		}
		c.pending = append(c.pending, pendingCall{id: id, tool: call.Tool})
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(toolBlockFence)
		b.WriteString("\n")
		b.WriteString(renderToolCall(id, call))
		b.WriteString("\n```")
	}
	c.history = append(c.history, prompt.Message{Role: "assistant", Content: b.String()})
	c.trimHistory()
}

// pendingHasID reports whether an outstanding call already answers to this id.
func (c *Core) pendingHasID(id string) bool {
	for _, p := range c.pending {
		if p.id == id {
			return true
		}
	}
	return false
}

// renderToolCall serializes one tool call back into the portable ```tool block
// shape the parser reads, carrying the id the matching result will quote. Args
// is raw provider JSON; if it is not valid JSON the call is still recorded,
// argument-less, rather than dropped from the transcript.
func renderToolCall(id string, call ToolCall) string {
	obj := struct {
		ID     string          `json:"id"`
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args,omitempty"`
		Reason string          `json:"reason,omitempty"`
	}{ID: id, Tool: call.Tool, Args: json.RawMessage(call.Args), Reason: call.Reason}
	b, err := json.Marshal(obj)
	if err != nil {
		obj.Args = nil
		b, _ = json.Marshal(obj)
	}
	return string(b)
}

// RecordToolResult records a tool execution result in the conversation history.
// After a tool runs, the runtime calls this so the next Think() includes the
// tool's output in the messages sent to the model. This implements the
// multi-turn agent loop: Think → tool call → execute → RecordToolResult → Think.
// We use role "user" for tool results because it's universally supported by all
// chat completion endpoints (some don't support role "tool"), and the header
// carries the id of the call being answered — a turn with N parallel calls
// produces N results and the model needs to know which is which.
//
// This is the id-less entry point: with no tool_call_id to go on it can only
// guess which outstanding call a result answers (see takePending). Callers that
// know the id — anything driving native tool calls — should use
// RecordToolResultID instead, which pairs exactly.
func (c *Core) RecordToolResult(toolName, result string) {
	c.RecordToolResultID("", toolName, result)
}

// RecordToolResultID records a tool result against the call it answers, keyed
// by the provider's tool_call_id. This is the correct pairing: a turn that
// calls read_file twice produces two results that differ only by id, and
// parallel calls do not complete in dispatch order, so matching by tool name or
// by position attributes the wrong output to the wrong call. A callID of "" (or
// one with no outstanding call) falls back to the name/position guess so the
// fenced-block path and older callers keep working.
func (c *Core) RecordToolResultID(callID, toolName, result string) {
	header := fmt.Sprintf("%s %s]", toolResultPrefix, toolName)
	if id := c.takePending(callID, toolName); id != "" {
		header = fmt.Sprintf("%s %s id=%s]", toolResultPrefix, toolName, id)
	}
	c.history = append(c.history, prompt.Message{
		Role:    "user",
		Content: header + "\n" + result,
	})
	c.trimHistory()
}

// takePending removes and returns the id of the call this result answers. An
// exact id match is the only reliable pairing and is tried first. Without an id
// we fall back to the oldest outstanding call for the tool, then to the oldest
// outstanding call of any name — a guess, but one that keeps the queue in step
// (a result the runtime reports under a different name, e.g. the corrective
// patch call reflection injects, still consumes a slot). No outstanding call →
// "" and the result is recorded un-keyed rather than dropped.
func (c *Core) takePending(callID, toolName string) string {
	if callID != "" {
		for i, p := range c.pending {
			if p.id == callID {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				return p.id
			}
		}
	}
	for i, p := range c.pending {
		if p.tool == toolName {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return p.id
		}
	}
	if len(c.pending) > 0 {
		id := c.pending[0].id
		c.pending = c.pending[1:]
		return id
	}
	return ""
}

// trimHistory bounds the conversation at maxHistoryMessages. The strategy is
// deliberately explicit: the leading system messages are pinned (dropping the
// system prompt would change the agent's identity mid-task), the oldest
// messages after them are dropped until the history fits, and the cut then
// advances past any tool result it would leave at the head — a result whose
// assistant tool-call message was just dropped is an orphan, and a model handed
// an answer to a call it cannot see misreads it.
func (c *Core) trimHistory() {
	if len(c.history) <= maxHistoryMessages {
		return
	}
	// Pin the leading system messages.
	pin := 0
	for pin < len(c.history) && c.history[pin].Role == "system" {
		pin++
	}
	cut := pin + len(c.history) - maxHistoryMessages
	if cut > len(c.history) {
		cut = len(c.history) // the system prefix alone already fills the bound
	}
	for cut < len(c.history) && isToolResult(c.history[cut]) {
		cut++
	}
	kept := make([]prompt.Message, 0, pin+len(c.history)-cut)
	kept = append(kept, c.history[:pin]...)
	kept = append(kept, c.history[cut:]...)
	c.history = kept
}

// isToolResult reports whether a history message is a recorded tool result (as
// opposed to a genuine user message that happens to share the "user" role).
func isToolResult(m prompt.Message) bool {
	return m.Role == "user" && strings.HasPrefix(m.Content, toolResultPrefix)
}

// publishTokens emits an llm.token event for a delta (File 07 §7.2.2). A nil
// bus makes this a no-op so unit tests can drive the Core without a bus.
func (c *Core) publishTokens(ctx context.Context, delta string) {
	if c.bus == nil || delta == "" {
		return
	}
	_ = c.bus.Publish(ctx, &event.TokenEvent{Task: event.TaskID(taskID(ctx)), Delta: delta})
}

// publishThinking emits an llm.thinking event for a chain-of-thought delta
// (File 07 §7.4).
func (c *Core) publishThinking(ctx context.Context, delta string) {
	if c.bus == nil || delta == "" {
		return
	}
	_ = c.bus.Publish(ctx, &event.ThinkingEvent{Task: event.TaskID(taskID(ctx)), Delta: delta})
}

// HasMore reports whether the task has remaining work (File 07 §7.5.3) —
// more todos or loop iterations. The runtime uses it to decide VERIFY→PLAN
// vs VERIFY→DONE. When the last Think produced tool calls, the agent still
// has work (it needs to loop back to PLAN to let the model reason about the
// tool results and decide next steps). When the model gave a direct answer
// (Final=true), there is nothing more to do.
func (c *Core) HasMore(*session.Task) bool { return !c.lastTurn.Final }
