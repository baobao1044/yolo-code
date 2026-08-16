package cognitive

import (
	stdctx "context"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/session"
)

// ctxWithTask returns a context carrying the given task ID, mirroring what the
// runtime attaches to the task-scoped context (session.WithTaskID).
func ctxWithTask(id session.TaskID) stdctx.Context {
	return session.WithTaskID(stdctx.Background(), id)
}

// newTestCore wires a Core over a mock provider + a real bus, returning both so
// the test can inspect published events.
func newTestCore(t *testing.T, chunks []Chunk) (*Core, *event.Bus) {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	core := New(NewMockProvider(chunks, 128_000), bus)
	return core, bus
}

// drain reads the first event of a topic, or fails after a timeout.
func drain(t *testing.T, ch <-chan event.Envelope) event.Envelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(500 * time.Millisecond):
		t.Fatal("event not published within 500ms")
	}
	return event.Envelope{}
}

// TestThinkStreamsTokensAsEvents is the L6-001 exit criterion: the mock
// provider streams token deltas, and the Core publishes one llm.token event
// per non-empty Delta.
func TestThinkStreamsTokensAsEvents(t *testing.T) {
	chunks := []Chunk{
		{Delta: "Hello"},
		{Delta: ", "},
		{Delta: "world"},
	}
	core, bus := newTestCore(t, chunks)
	tokCh := bus.Subscribe("llm.token")

	turn, err := core.Think(ctxWithTask("t_1"), []prompt.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}

	// Collect every token event published.
	var got []string
	for {
		select {
		case env := <-tokCh:
			te, ok := env.Evt.(*event.TokenEvent)
			if !ok {
				t.Fatalf("event type = %T, want *TokenEvent", env.Evt)
			}
			got = append(got, te.Delta)
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:
	want := []string{"Hello", ", ", "world"}
	if len(got) != len(want) {
		t.Fatalf("published %d token events, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("token[%d] = %q, want %q", i, got[i], w)
		}
	}
	// The accumulated text must reconstruct the full answer.
	if turn.Text != "Hello, world" {
		t.Errorf("turn.Text = %q, want %q", turn.Text, "Hello, world")
	}
}

// TestThinkPublishesThinkingEvents pins that a chain-of-thought delta is
// published as an llm.thinking event, distinct from the visible answer's
// token events (File 07 §7.4).
func TestThinkPublishesThinkingEvents(t *testing.T) {
	chunks := []Chunk{
		{Thinking: "let me consider the options"},
		{Delta: "answer"},
	}
	core, bus := newTestCore(t, chunks)
	thinkCh := bus.Subscribe("llm.thinking")

	_, err := core.Think(ctxWithTask("t_2"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	env := drain(t, thinkCh)
	te, ok := env.Evt.(*event.ThinkingEvent)
	if !ok {
		t.Fatalf("event type = %T, want *ThinkingEvent", env.Evt)
	}
	if te.Delta != "let me consider the options" {
		t.Errorf("thinking delta = %q, want the chain-of-thought text", te.Delta)
	}
}

// TestThinkCarriesTaskIDFromContext pins that the task ID threaded via context
// (session.WithTaskID) lands on the published TokenEvent.Task — so the event
// trace attributes tokens to the right task.
func TestThinkCarriesTaskIDFromContext(t *testing.T) {
	chunks := []Chunk{{Delta: "x"}}
	core, bus := newTestCore(t, chunks)
	tokCh := bus.Subscribe("llm.token")

	_, _ = core.Think(ctxWithTask("t_99"), nil)
	env := drain(t, tokCh)
	te, _ := env.Evt.(*event.TokenEvent)
	if te.Task != event.TaskID("t_99") {
		t.Errorf("TokenEvent.Task = %q, want %q (threaded via context)", te.Task, "t_99")
	}
}

// TestThinkFinalWhenNoToolCalls pins that a turn with no tool-call chunks is
// Final (the visible answer is the whole turn, File 07 §7.2.3). The parser
// lands in L6-002; Sprint 3's streaming path marks Final when no tool calls
// arrived.
func TestThinkFinalWhenNoToolCalls(t *testing.T) {
	chunks := []Chunk{{Delta: "just an answer"}}
	core, _ := newTestCore(t, chunks)
	turn, err := core.Think(ctxWithTask("t_3"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !turn.Final {
		t.Error("turn.Final = false, want true (no tool calls → direct answer)")
	}
	if turn.Text != "just an answer" {
		t.Errorf("turn.Text = %q, want %q", turn.Text, "just an answer")
	}
}

// TestThinkPropagatesStreamError pins that an Err chunk terminates the stream
// and surfaces as Think's error.
func TestThinkPropagatesStreamError(t *testing.T) {
	chunks := []Chunk{
		{Delta: "partial"},
		{Err: errStream("provider: connection reset")},
	}
	core, _ := newTestCore(t, chunks)
	_, err := core.Think(ctxWithTask("t_4"), nil)
	if err == nil {
		t.Fatal("Think err = nil, want the stream error propagated")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("Think err = %v, want it to contain the stream error", err)
	}
}

// errStream is a tiny test error type for the stream-error case.
type errStream string

func (e errStream) Error() string { return string(e) }

// recordingProvider streams a fixed script like MockProvider, but keeps a copy
// of every Request it was handed so a test can assert what actually reached the
// model (not just what the Core returned).
type recordingProvider struct {
	chunks []Chunk
	reqs   []Request
}

func (p *recordingProvider) Stream(_ stdctx.Context, req Request) (<-chan Chunk, error) {
	p.reqs = append(p.reqs, Request{
		Messages: append([]prompt.Message(nil), req.Messages...),
		Tools:    req.Tools,
	})
	out := make(chan Chunk, len(p.chunks))
	for _, ch := range p.chunks {
		out <- ch
	}
	close(out)
	return out, nil
}

func (p *recordingProvider) Window() int { return 128_000 }

// hasMessage reports whether the messages carry one with the given role and
// exact content.
func hasMessage(msgs []prompt.Message, role, content string) bool {
	for _, m := range msgs {
		if m.Role == role && m.Content == content {
			return true
		}
	}
	return false
}

// countRole counts messages with the given role.
func countRole(msgs []prompt.Message, role string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n++
		}
	}
	return n
}

// TestThinkSendsNewPromptMessagesOnEveryTurn pins the 4.9 regression: once the
// first task seeded the history, every later task's compiled prompt was
// discarded wholesale and the new goal never reached the model. The second
// task's goal must appear in the request, without duplicating the system
// prompt and without losing the prior conversation.
func TestThinkSendsNewPromptMessagesOnEveryTurn(t *testing.T) {
	p := &recordingProvider{chunks: []Chunk{{Delta: "ok"}}}
	core := New(p, nil)

	sys := prompt.Message{Role: "system", Content: "you are yolo"}
	if _, err := core.Think(ctxWithTask("t_1"), []prompt.Message{sys, {Role: "user", Content: "task one"}}); err != nil {
		t.Fatalf("Think(first): %v", err)
	}
	if _, err := core.Think(ctxWithTask("t_2"), []prompt.Message{sys, {Role: "user", Content: "task two"}}); err != nil {
		t.Fatalf("Think(second): %v", err)
	}

	if len(p.reqs) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(p.reqs))
	}
	got := p.reqs[1].Messages
	if !hasMessage(got, "user", "task two") {
		t.Errorf("second request lost the new goal; messages = %+v", got)
	}
	if !hasMessage(got, "user", "task one") {
		t.Errorf("second request lost the prior conversation; messages = %+v", got)
	}
	if n := countRole(got, "system"); n != 1 {
		t.Errorf("second request carries %d system messages, want 1 (no duplication)", n)
	}
}

// TestThinkDoesNotDuplicateAnUnchangedPrompt pins the other half of 4.9: the
// runtime re-compiles the identical prompt on every PLAN turn of a task, so
// re-appending it verbatim would grow the request without adding information.
func TestThinkDoesNotDuplicateAnUnchangedPrompt(t *testing.T) {
	p := &recordingProvider{chunks: []Chunk{{Delta: "ok"}}}
	core := New(p, nil)

	msgs := []prompt.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "goal"}}
	for i := 0; i < 3; i++ {
		if _, err := core.Think(ctxWithTask("t_dup"), msgs); err != nil {
			t.Fatalf("Think(%d): %v", i, err)
		}
	}
	last := p.reqs[len(p.reqs)-1].Messages
	if n := countRole(last, "system"); n != 1 {
		t.Errorf("system message appears %d times, want 1", n)
	}
	if n := countRole(last, "user"); n != 1 {
		t.Errorf("user goal appears %d times, want 1", n)
	}
}

// TestThinkRecordsEveryToolCallInHistory pins the 4.14 history half: a turn
// carrying N parallel tool calls must record all N in the assistant message.
// Recording only turn.Text left the model looking at N tool results with no
// record of the calls that produced them.
func TestThinkRecordsEveryToolCallInHistory(t *testing.T) {
	chunks := []Chunk{
		{ToolCall: &ToolCall{Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}},
		{ToolCall: &ToolCall{Tool: "grep", Args: []byte(`{"pattern":"foo"}`)}},
		{ToolCall: &ToolCall{Tool: "list_dir", Args: []byte(`{"dir":"."}`)}},
	}
	core, _ := newTestCore(t, chunks)
	turn, err := core.Think(ctxWithTask("t_multi"), []prompt.Message{{Role: "user", Content: "go"}})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(turn.ToolCalls) != 3 {
		t.Fatalf("turn carries %d tool calls, want 3", len(turn.ToolCalls))
	}
	var assistant string
	for _, m := range core.history {
		if m.Role == "assistant" {
			assistant = m.Content
		}
	}
	for _, name := range []string{"read_file", "grep", "list_dir"} {
		if !strings.Contains(assistant, name) {
			t.Errorf("assistant history message lost call %q; got %q", name, assistant)
		}
	}
}

// TestRecordToolResultKeysEachResultToItsCall pins that each of a turn's N
// tool results is keyed to the call it answers. Without a key the model cannot
// tell which of three parallel reads produced which output.
func TestRecordToolResultKeysEachResultToItsCall(t *testing.T) {
	chunks := []Chunk{
		{ToolCall: &ToolCall{Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}},
		{ToolCall: &ToolCall{Tool: "read_file", Args: []byte(`{"file":"b.go"}`)}},
	}
	core, _ := newTestCore(t, chunks)
	if _, err := core.Think(ctxWithTask("t_keys"), nil); err != nil {
		t.Fatalf("Think: %v", err)
	}
	core.RecordToolResult("read_file", "contents of a")
	core.RecordToolResult("read_file", "contents of b")

	var results []string
	for _, m := range core.history {
		if strings.HasPrefix(m.Content, "[Tool Result:") {
			results = append(results, m.Content)
		}
	}
	if len(results) != 2 {
		t.Fatalf("recorded %d tool results, want 2", len(results))
	}
	// Each result must quote a distinct call id, and that id must be one the
	// assistant message announced.
	var assistant string
	for _, m := range core.history {
		if m.Role == "assistant" {
			assistant = m.Content
		}
	}
	seen := map[string]bool{}
	for i, r := range results {
		idx := strings.Index(r, "id=")
		if idx < 0 {
			t.Fatalf("result[%d] = %q, want it keyed with an id=<tool_call_id>", i, r)
		}
		id := r[idx+len("id="):]
		if end := strings.IndexAny(id, "]\n"); end >= 0 {
			id = id[:end]
		}
		if seen[id] {
			t.Errorf("result[%d] reuses call id %q; each call needs its own result", i, id)
		}
		seen[id] = true
		if !strings.Contains(assistant, id) {
			t.Errorf("result[%d] cites id %q which the assistant message never announced (%q)", i, id, assistant)
		}
	}
}

// TestHistoryIsBoundedAndKeepsSystemMessage pins the 4.9 unbounded-growth half.
// A long agent loop appends an assistant message + a tool result per tool call;
// the history must stay bounded, must never drop the system prompt, and must
// never leave a tool result at the head with its assistant call trimmed away
// (an orphaned result the model cannot attribute).
func TestHistoryIsBoundedAndKeepsSystemMessage(t *testing.T) {
	core := New(NewMockProvider([]Chunk{{Delta: "ok"}}, 0), nil)
	msgs := []prompt.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "goal"}}
	for i := 0; i < maxHistoryMessages*2; i++ {
		if _, err := core.Think(ctxWithTask("t_bound"), msgs); err != nil {
			t.Fatalf("Think(%d): %v", i, err)
		}
		core.RecordToolResult("read_file", "out")
	}
	if len(core.history) > maxHistoryMessages {
		t.Fatalf("history grew to %d messages, want ≤ %d (unbounded growth)", len(core.history), maxHistoryMessages)
	}
	if core.history[0].Role != "system" || core.history[0].Content != "sys" {
		t.Fatalf("history[0] = %+v, want the pinned system message", core.history[0])
	}
	if strings.HasPrefix(core.history[1].Content, "[Tool Result:") {
		t.Errorf("history[1] is an orphaned tool result (its assistant call was trimmed): %q", core.history[1].Content)
	}
}

// TestResetStartsAFreshConversation pins that Reset drops the accumulated
// transcript so an independent task does not inherit the previous one's turns,
// while keeping the provider and tools wired.
func TestResetStartsAFreshConversation(t *testing.T) {
	p := &recordingProvider{chunks: []Chunk{{ToolCall: &ToolCall{Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}}}}
	core := New(p, nil, "read_file")

	if _, err := core.Think(ctxWithTask("t_a"), []prompt.Message{{Role: "user", Content: "task one"}}); err != nil {
		t.Fatalf("Think(first): %v", err)
	}
	core.Reset()
	if len(core.history) != 0 {
		t.Errorf("Reset left %d history messages, want 0", len(core.history))
	}
	if len(core.pending) != 0 {
		t.Errorf("Reset left %d outstanding tool calls, want 0", len(core.pending))
	}
	// Reset restores the as-constructed state, so the last turn is the zero
	// Turn exactly as it is on a freshly built Core.
	if core.lastTurn.Text != "" || len(core.lastTurn.ToolCalls) != 0 {
		t.Errorf("Reset left lastTurn = %+v, want the zero Turn", core.lastTurn)
	}
	if _, err := core.Think(ctxWithTask("t_b"), []prompt.Message{{Role: "user", Content: "task two"}}); err != nil {
		t.Fatalf("Think(second): %v", err)
	}

	got := p.reqs[1].Messages
	if hasMessage(got, "user", "task one") {
		t.Errorf("Reset did not clear the prior conversation; messages = %+v", got)
	}
	if !hasMessage(got, "user", "task two") {
		t.Errorf("second request lost its goal; messages = %+v", got)
	}
	if len(p.reqs[1].Tools) != 1 || p.reqs[1].Tools[0] != "read_file" {
		t.Errorf("Reset dropped the tool list; tools = %v", p.reqs[1].Tools)
	}
}

// resultIDs returns the id= key of every tool result in the history, paired
// with the body it carries, in recorded order.
func resultIDs(core *Core) map[string]string {
	out := map[string]string{}
	for _, m := range core.history {
		if !isToolResult(m) {
			continue
		}
		head, body, _ := strings.Cut(m.Content, "\n")
		id := ""
		if i := strings.Index(head, "id="); i >= 0 {
			id = strings.TrimSuffix(head[i+len("id="):], "]")
		}
		out[id] = body
	}
	return out
}

// TestThinkPrefersProviderToolCallIDs pins that a native tool call is tracked
// under the provider's own tool_call_id, not a Core-minted one. The minted id
// is invisible to the model and unusable by the runtime, so a turn whose calls
// came off the wire must carry the wire ids into both the assistant message and
// the tool results.
func TestThinkPrefersProviderToolCallIDs(t *testing.T) {
	chunks := []Chunk{
		{ToolCall: &ToolCall{ID: "call_abc123", Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}},
		{ToolCall: &ToolCall{ID: "call_def456", Tool: "grep", Args: []byte(`{"pattern":"foo"}`)}},
	}
	core, _ := newTestCore(t, chunks)
	if _, err := core.Think(ctxWithTask("t_wireid"), nil); err != nil {
		t.Fatalf("Think: %v", err)
	}
	var assistant string
	for _, m := range core.history {
		if m.Role == "assistant" {
			assistant = m.Content
		}
	}
	for _, id := range []string{"call_abc123", "call_def456"} {
		if !strings.Contains(assistant, id) {
			t.Errorf("assistant message lost wire id %q; got %q", id, assistant)
		}
	}
	if strings.Contains(assistant, "call_1") {
		t.Errorf("assistant message minted an id over the wire id; got %q", assistant)
	}

	core.RecordToolResult("read_file", "contents of a")
	core.RecordToolResult("grep", "one match")
	got := resultIDs(core)
	if got["call_abc123"] != "contents of a" {
		t.Errorf("result for call_abc123 = %q, want %q (results = %v)", got["call_abc123"], "contents of a", got)
	}
	if got["call_def456"] != "one match" {
		t.Errorf("result for call_def456 = %q, want %q (results = %v)", got["call_def456"], "one match", got)
	}
}

// TestRecordToolResultIDPairsSameToolCalledTwice is the mispairing regression:
// a turn calling read_file twice produces two results that differ only by
// tool_call_id. Parallel calls do not complete in dispatch order, so pairing by
// tool name — oldest outstanding first — hands the second file's contents to
// the first call. Only an id match gets it right.
func TestRecordToolResultIDPairsSameToolCalledTwice(t *testing.T) {
	chunks := []Chunk{
		{ToolCall: &ToolCall{ID: "call_a", Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}},
		{ToolCall: &ToolCall{ID: "call_b", Tool: "read_file", Args: []byte(`{"file":"b.go"}`)}},
	}
	core, _ := newTestCore(t, chunks)
	if _, err := core.Think(ctxWithTask("t_twice"), nil); err != nil {
		t.Fatalf("Think: %v", err)
	}
	// The second call completes first — the ordering a name/position match
	// cannot survive.
	core.RecordToolResultID("call_b", "read_file", "contents of b")
	core.RecordToolResultID("call_a", "read_file", "contents of a")

	got := resultIDs(core)
	if got["call_a"] != "contents of a" {
		t.Errorf("call_a paired with %q, want %q (results = %v)", got["call_a"], "contents of a", got)
	}
	if got["call_b"] != "contents of b" {
		t.Errorf("call_b paired with %q, want %q (results = %v)", got["call_b"], "contents of b", got)
	}
}

// TestEchoedToolCallIDDoesNotCollapseTwoCalls is the F3 mis-attribution
// regression. The Core renders every call back into the history tagged with its
// id, so the model SEES the ids and can echo one back in its next ```tool
// block. When the parser honoured the echo, the echoed id was reused for the
// first call and callSeq never advanced past it, so a second call in the same
// turn could land under the same id. takePending then exact-matched the first
// entry and filed grep's output as read_file's answer — the two tools swapped
// results with no error anywhere.
func TestEchoedToolCallIDDoesNotCollapseTwoCalls(t *testing.T) {
	block := "```tool\n" +
		`{"id":"call_1","tool":"read_file","args":{}}` + "\n" +
		`{"tool":"grep","args":{}}` + "\n" +
		"```\n"
	core, _ := newTestCore(t, []Chunk{{Delta: block}})
	if _, err := core.Think(ctxWithTask("t_echoid"), nil); err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(core.pending) != 2 {
		t.Fatalf("pending = %+v, want 2 outstanding calls", core.pending)
	}
	if core.pending[0].id == core.pending[1].id {
		t.Fatalf("pending calls %s and %s share id %q; results cannot be paired back to their call",
			core.pending[0].tool, core.pending[1].tool, core.pending[0].id)
	}

	// Answer each by its own id, grep first — the ordering a collapsed id
	// cannot survive.
	grepID, readID := core.pending[1].id, core.pending[0].id
	core.RecordToolResultID(grepID, "grep", "one match")
	core.RecordToolResultID(readID, "read_file", "file contents")

	got := resultIDs(core)
	if got[readID] != "file contents" {
		t.Errorf("read_file (id %s) paired with %q, want %q (results = %v)", readID, got[readID], "file contents", got)
	}
	if got[grepID] != "one match" {
		t.Errorf("grep (id %s) paired with %q, want %q (results = %v)", grepID, got[grepID], "one match", got)
	}
}

// TestDuplicateWireToolCallIDsGetDistinctPendingSlots is the backstop for the
// same collapse arriving from the other direction: a provider (or a proxy
// replaying a retry) that puts the same tool_call_id on two native calls. The
// parser fix cannot reach that path, so recordAssistantTurn mints past any id
// already outstanding rather than queueing two calls the pairing cannot tell
// apart.
func TestDuplicateWireToolCallIDsGetDistinctPendingSlots(t *testing.T) {
	chunks := []Chunk{
		{ToolCall: &ToolCall{ID: "call_dup", Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}},
		{ToolCall: &ToolCall{ID: "call_dup", Tool: "grep", Args: []byte(`{"pattern":"x"}`)}},
	}
	core, _ := newTestCore(t, chunks)
	if _, err := core.Think(ctxWithTask("t_dupwire"), nil); err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(core.pending) != 2 {
		t.Fatalf("pending = %+v, want 2 outstanding calls", core.pending)
	}
	if core.pending[0].id == core.pending[1].id {
		t.Errorf("both pending calls kept id %q; the second tool's output would answer the first call", core.pending[0].id)
	}
	// The first one still keeps the wire id — that is the one the provider will
	// quote back, and only the collision needs renaming.
	if core.pending[0].id != "call_dup" {
		t.Errorf("pending[0].id = %q, want the wire id call_dup preserved", core.pending[0].id)
	}
}

// TestFencedToolCallsStillGetMintedIDs guards the other half: the ```tool fence
// format carries no id, so those calls must keep getting a minted one rather
// than collapsing to a single un-keyed result.
func TestFencedToolCallsStillGetMintedIDs(t *testing.T) {
	block := "```tool\n" +
		`[{"tool":"read_file","args":{"file":"a.go"}},{"tool":"read_file","args":{"file":"b.go"}}]` + "\n" +
		"```\n"
	core, _ := newTestCore(t, []Chunk{{Delta: block}})
	turn, err := core.Think(ctxWithTask("t_fenced"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("parsed %d fenced calls, want 2", len(turn.ToolCalls))
	}
	for i, call := range turn.ToolCalls {
		if call.ID != "" {
			t.Errorf("fenced call[%d].ID = %q, want \"\" (the fence format carries no id)", i, call.ID)
		}
	}
	core.RecordToolResult("read_file", "contents of a")
	core.RecordToolResult("read_file", "contents of b")

	got := resultIDs(core)
	if len(got) != 2 {
		t.Fatalf("recorded %d distinctly-keyed results, want 2 (results = %v)", len(got), got)
	}
	if got["call_1"] != "contents of a" || got["call_2"] != "contents of b" {
		t.Errorf("minted pairing = %v, want call_1→a, call_2→b", got)
	}
}
