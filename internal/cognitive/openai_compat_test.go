package cognitive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestBuildToolDefsIncludesAllAdvertisedTools guards against the regression
// where a tool is registered in the exec registry and advertised in the docs
// but missing from the LLM tool schema (toolDefs) — a tool-calling model can
// never invoke a tool it was never told about. The README advertises five
// built-in tools: list_files, read_file, edit_file, bash, grep.
func TestBuildToolDefsIncludesAllAdvertisedTools(t *testing.T) {
	want := []string{"list_files", "read_file", "edit_file", "bash", "grep"}
	defs := buildToolDefs(want)
	if len(defs) != len(want) {
		t.Fatalf("buildToolDefs returned %d defs, want %d (a tool is missing its schema)", len(defs), len(want))
	}
	got := make(map[string]bool, len(defs))
	for _, d := range defs {
		got[d.Function.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q missing from buildToolDefs output — the model cannot call it", name)
		}
	}
}

// TestBuildToolDefsSkipsUnknownNames documents the silent-skip contract: an
// unknown tool name must not break request building, it is simply omitted.
func TestBuildToolDefsSkipsUnknownNames(t *testing.T) {
	defs := buildToolDefs([]string{"list_files", "no_such_tool", "read_file"})
	if len(defs) != 2 {
		t.Fatalf("buildToolDefs returned %d defs, want 2 (unknown should be skipped)", len(defs))
	}
}

// TestGrepToolSchemaHasRequiredParameters ensures the grep schema (the tool that
// was previously missing) carries the `pattern` parameter as required so the
// model is forced to supply it.
func TestGrepToolSchemaHasRequiredParameters(t *testing.T) {
	def, ok := toolDefs["grep"]
	if !ok {
		t.Fatal(`toolDefs["grep"] missing — grep was re-removed from the schema`)
	}
	if def.Function.Name != "grep" {
		t.Errorf("grep Function.Name = %q, want %q", def.Function.Name, "grep")
	}
	body := string(def.Function.Parameters)
	if !strings.Contains(body, `"pattern"`) {
		t.Errorf("grep schema missing `pattern` property: %s", body)
	}
	if !strings.Contains(body, `"required":["pattern"]`) {
		t.Errorf("grep schema missing `pattern` in required: %s", body)
	}
}

// TestFromEnvDoesNotSendOpenAIKeyToForeignHost is the credential-leak guard on
// the env-only path: OPENAI_API_KEY may only back a request to OpenAI's host.
// With YOLO_BASE_URL pointed elsewhere and no YOLO_API_KEY, nothing resolves.
func TestFromEnvDoesNotSendOpenAIKeyToForeignHost(t *testing.T) {
	t.Setenv("YOLO_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	for _, base := range []string{
		"https://api.groq.com/openai/v1",
		"http://localhost:11434/v1",
		"https://api.openai.com.evil.example/v1",
	} {
		t.Setenv("YOLO_BASE_URL", base)
		if p := OpenAICompatProviderFromEnv(); p != nil {
			t.Errorf("YOLO_BASE_URL=%s → provider with key %q, want nil (OpenAI key must not leave OpenAI)", base, p.apiKey)
		}
	}
}

// TestFromEnvKeepsOpenAIKeyForOpenAI pins the other half: the default base URL
// (and an explicit OpenAI one) still accept OPENAI_API_KEY.
func TestFromEnvKeepsOpenAIKeyForOpenAI(t *testing.T) {
	t.Setenv("YOLO_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	for _, base := range []string{"", "https://api.openai.com/v1"} {
		t.Setenv("YOLO_BASE_URL", base)
		p := OpenAICompatProviderFromEnv()
		if p == nil {
			t.Fatalf("YOLO_BASE_URL=%q → nil, want a provider (OPENAI_API_KEY is OpenAI's own key)", base)
		}
		if p.apiKey != "sk-openai-secret" {
			t.Errorf("YOLO_BASE_URL=%q → apiKey %q, want sk-openai-secret", base, p.apiKey)
		}
	}
}

// TestFromEnvYoloKeyWorksAnywhere pins that YOLO_API_KEY — yolo-code's own var,
// set by the user alongside the endpoint they chose — is not host-restricted.
func TestFromEnvYoloKeyWorksAnywhere(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("YOLO_API_KEY", "yolo-key")
	t.Setenv("YOLO_BASE_URL", "https://api.groq.com/openai/v1")
	p := OpenAICompatProviderFromEnv()
	if p == nil || p.apiKey != "yolo-key" {
		t.Fatalf("OpenAICompatProviderFromEnv() = %+v, want a provider with YOLO_API_KEY", p)
	}
}

// parallelToolCallSSE is a stream carrying three parallel tool calls whose
// fragments interleave across events — name first, arguments split over later
// events, exactly as OpenAI-compatible providers emit them.
const parallelToolCallSSE = `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_file"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"grep"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_c","function":{"name":"read_file"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file\":"}},{"index":1,"function":{"arguments":"{\"pattern\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}},{"index":1,"function":{"arguments":"\"x\"}"}},{"index":2,"function":{"arguments":"{\"file\":\"b.go\"}"}}]}}]}

data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}

data: [DONE]

`

// drainSSE parses an SSE body to completion and returns every chunk emitted.
func drainSSE(body string) []Chunk {
	p := NewOpenAICompatProvider("https://api.example.com/v1", "k", "m", 1000)
	out := make(chan Chunk, 32)
	go func() {
		defer close(out)
		p.parseSSE(context.Background(), strings.NewReader(body), out)
	}()
	var got []Chunk
	for c := range out {
		got = append(got, c)
	}
	return got
}

// TestParseSSEEmitsParallelToolCallsInIndexOrder is the determinism guard (S5)
// for parallel tool calls: they must come out in ascending wire index on every
// run. The accumulator is a map, so ranging it emitted them in randomised order
// — 50 iterations reliably catches a regression back to map order.
func TestParseSSEEmitsParallelToolCallsInIndexOrder(t *testing.T) {
	want := "read_file:call_a,grep:call_b,read_file:call_c"
	for i := 0; i < 50; i++ {
		var got []string
		for _, c := range drainSSE(parallelToolCallSSE) {
			if c.ToolCall != nil {
				got = append(got, c.ToolCall.Tool+":"+c.ToolCall.ID)
			}
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("iteration %d: tool calls = %v, want %s (ascending wire index)", i, got, want)
		}
	}
}

// TestParseSSECapturesToolCallID pins that the provider's tool_call_id reaches
// the ToolCall. Without it the runtime has to mint synthetic ids and pair
// results to calls by tool name — wrong the moment a turn calls the same tool
// twice, which this fixture does (read_file at index 0 and 2).
func TestParseSSECapturesToolCallID(t *testing.T) {
	var calls []*ToolCall
	for _, c := range drainSSE(parallelToolCallSSE) {
		if c.ToolCall != nil {
			calls = append(calls, c.ToolCall)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("got %d tool calls, want 3", len(calls))
	}
	wantIDs := []string{"call_a", "call_b", "call_c"}
	wantArgs := []string{`{"file":"a.go"}`, `{"pattern":"x"}`, `{"file":"b.go"}`}
	for i, c := range calls {
		if c.ID != wantIDs[i] {
			t.Errorf("call[%d].ID = %q, want %q", i, c.ID, wantIDs[i])
		}
		if string(c.Args) != wantArgs[i] {
			t.Errorf("call[%d].Args = %q, want %q", i, c.Args, wantArgs[i])
		}
	}
	// The two read_file calls are distinguishable only by ID — the point of
	// carrying the provider's id instead of matching on tool name.
	if calls[0].Tool != calls[2].Tool {
		t.Fatal("fixture must call the same tool twice")
	}
	if calls[0].ID == calls[2].ID {
		t.Error("same-tool calls share an ID; results cannot be paired back to their call")
	}
}

// usageSSE is a full turn from a server that honoured
// stream_options.include_usage: content deltas, the finish chunk carrying an
// explicit `"usage":null` (what OpenAI sends on every non-final chunk), then
// the usage chunk — whose choices array is EMPTY — and the [DONE] sentinel.
const usageSSE = `data: {"choices":[{"index":0,"delta":{"content":"hello "}}]}

data: {"choices":[{"index":0,"delta":{"content":"world"}}],"usage":null}

data: {"choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":null}

data: {"choices":[],"usage":{"prompt_tokens":1234,"completion_tokens":56,"total_tokens":1290}}

data: [DONE]

`

// noUsageSSE is the same turn from a server that never reports usage — an old
// proxy that ignored stream_options, or a local runner that does not implement
// it. This is the degraded case: the counts are unknown, not zero.
const noUsageSSE = `data: {"choices":[{"index":0,"delta":{"content":"hello world"}}]}

data: {"choices":[{"index":0,"finish_reason":"stop","delta":{}}]}

data: [DONE]

`

// streamUsage returns the usage a drained stream reported, or nil when it
// reported none — the distinction this whole path exists to preserve.
func streamUsage(chunks []Chunk) *Usage {
	var last *Usage
	for _, c := range chunks {
		if c.Usage != nil {
			last = c.Usage
		}
	}
	return last
}

// TestParseSSESurfacesUsageChunk is the core guard: the provider must read the
// final usage chunk. That chunk has an empty choices array, and the parse loop
// skips choice-less chunks, so usage has to be read before that skip or it is
// dropped on the floor — which is how TokensIn/TokensOut came to be declared
// and never assigned.
func TestParseSSESurfacesUsageChunk(t *testing.T) {
	u := streamUsage(drainSSE(usageSSE))
	if u == nil {
		t.Fatal("stream reported no usage; the usage chunk (empty choices) was dropped")
	}
	if u.TokensIn != 1234 || u.TokensOut != 56 {
		t.Errorf("usage = %+v, want {TokensIn:1234 TokensOut:56}", *u)
	}
}

// TestParseSSEUnknownUsageIsNotZeroUsage pins the degraded case. A server that
// never sends usage must leave the count absent (nil), never 0 — and the test
// pairs it with a server that genuinely reports zero output tokens to show the
// two are told apart: {7,0} is a measurement, nil is an admission.
func TestParseSSEUnknownUsageIsNotZeroUsage(t *testing.T) {
	if u := streamUsage(drainSSE(noUsageSSE)); u != nil {
		t.Errorf("silent server reported usage %+v, want nil (unknown, not zero)", *u)
	}
	const zeroOutSSE = `data: {"choices":[{"index":0,"delta":{"content":""}}]}

data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7}}

data: [DONE]

`
	u := streamUsage(drainSSE(zeroOutSSE))
	if u == nil {
		t.Fatal("a measured zero completion was reported as unknown")
	}
	if u.TokensIn != 7 || u.TokensOut != 0 {
		t.Errorf("usage = %+v, want {TokensIn:7 TokensOut:0}", *u)
	}
}

// TestParseSSEIgnoresAllZeroUsage documents the one usage object we refuse: an
// all-zero one. Some local runners attach `"usage":{"prompt_tokens":0,...}` to
// every chunk as a placeholder. No real request costs zero prompt tokens, so
// treating that as a measurement would re-manufacture the confident 0.
func TestParseSSEIgnoresAllZeroUsage(t *testing.T) {
	const placeholderSSE = `data: {"choices":[{"index":0,"delta":{"content":"hi"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}

data: [DONE]

`
	if u := streamUsage(drainSSE(placeholderSSE)); u != nil {
		t.Errorf("all-zero placeholder reported as usage %+v, want nil", *u)
	}
}

// TestParseSSEReadsAnthropicStyleUsageAliases covers the compatibility shims
// that speak the OpenAI wire shape but name the counts input_tokens /
// output_tokens. Same numbers, different keys; reading only one spelling means
// a whole class of endpoint silently reports nothing.
func TestParseSSEReadsAnthropicStyleUsageAliases(t *testing.T) {
	const aliasSSE = `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}

data: {"choices":[],"usage":{"input_tokens":80,"output_tokens":9}}

data: [DONE]

`
	u := streamUsage(drainSSE(aliasSSE))
	if u == nil {
		t.Fatal("input_tokens/output_tokens usage not read")
	}
	if u.TokensIn != 80 || u.TokensOut != 9 {
		t.Errorf("usage = %+v, want {TokensIn:80 TokensOut:9}", *u)
	}
}

// TestParseTurnLeavesUsageUnknown pins the parser's half of the contract: text
// alone carries no counts, so a turn parsed from it must claim none. Usage
// arrives out of band on the Chunk stream; the parser must not invent it.
func TestParseTurnLeavesUsageUnknown(t *testing.T) {
	turn := parseTurn("just an answer")
	if turn.UsageKnown {
		t.Error("parseTurn claimed UsageKnown from text alone")
	}
	if turn.TokensIn != 0 || turn.TokensOut != 0 {
		t.Errorf("parseTurn set tokens %d/%d, want 0/0 alongside UsageKnown=false", turn.TokensIn, turn.TokensOut)
	}
}

// streamErr returns the first Err a drained stream reported, or nil.
func streamErr(chunks []Chunk) error {
	for _, c := range chunks {
		if c.Err != nil {
			return c.Err
		}
	}
	return nil
}

// TestParseSSETruncatedStreamIsAnError pins the cut-off case. The body ends
// after a content delta and a usage chunk with no [DONE] and no finish_reason —
// nothing in it ever said the generation finished. bufio.Scanner sees a clean
// EOF, so scanner.Err() is nil, and without this guard the stream closed with
// no Err at all: Think returned Final=true, UsageKnown=true and the truncated
// turn was billed as a complete, measured one whose short completion count read
// as a fact.
func TestParseSSETruncatedStreamIsAnError(t *testing.T) {
	const truncatedSSE = `data: {"choices":[{"index":0,"delta":{"content":"partial"}}],"usage":{"prompt_tokens":9000,"completion_tokens":3}}

`
	chunks := drainSSE(truncatedSSE)
	err := streamErr(chunks)
	if err == nil {
		t.Fatalf("truncated stream reported no error; chunks = %+v", chunks)
	}
	if !strings.Contains(err.Error(), "cut off") {
		t.Errorf("error = %v, want it to name the truncation", err)
	}
	// The text and the usage still arrive before the Err — Core returns on the
	// Err chunk, so the truncated counts never reach a Turn. What matters is
	// that the error is in the stream at all.
	if u := streamUsage(chunks); u == nil || u.TokensIn != 9000 {
		t.Errorf("usage = %v, want the wire counts still emitted before the Err", u)
	}
}

// TestParseSSEFinishReasonWithoutDoneIsNotAnError is the other half, and the
// reason the guard is not "no [DONE] means error". Some OpenAI-compatible
// endpoints just close the connection instead of sending the [DONE] sentinel;
// the finish_reason they did send is the model's own statement that it stopped,
// so the turn is complete and must not be failed. Only a stream carrying
// neither signal is a truncation.
func TestParseSSEFinishReasonWithoutDoneIsNotAnError(t *testing.T) {
	const noDoneSSE = `data: {"choices":[{"index":0,"delta":{"content":"hello world"}}]}

data: {"choices":[{"index":0,"finish_reason":"stop","delta":{}}]}

`
	chunks := drainSSE(noDoneSSE)
	if err := streamErr(chunks); err != nil {
		t.Fatalf("stream that finished without [DONE] errored: %v — a missing sentinel is a framing quirk, not a truncation", err)
	}
	var text strings.Builder
	for _, c := range chunks {
		text.WriteString(c.Delta)
	}
	if text.String() != "hello world" {
		t.Errorf("text = %q, want %q", text.String(), "hello world")
	}
}

// TestParseSSECompleteStreamsHaveNoError guards the truncation check against
// over-firing on the fixtures that model real, complete turns.
func TestParseSSECompleteStreamsHaveNoError(t *testing.T) {
	for name, body := range map[string]string{
		"usage":            usageSSE,
		"noUsage":          noUsageSSE,
		"parallelToolCall": parallelToolCallSSE,
	} {
		if err := streamErr(drainSSE(body)); err != nil {
			t.Errorf("%s: complete stream reported %v, want no error", name, err)
		}
	}
}

// recordingSSEServer serves an SSE body over real HTTP and records every
// request body the provider sent, so a test can assert what was actually asked
// for. handler picks the status + body per request.
func recordingSSEServer(t *testing.T, handler func(reqBody string) (int, string)) (*OpenAICompatProvider, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		code, body := handler(string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	sent := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
	return NewOpenAICompatProvider(srv.URL, "k", "m", 1000), sent
}

// drainHTTPStream runs one Stream call to completion and returns every chunk.
func drainHTTPStream(t *testing.T, p *OpenAICompatProvider) []Chunk {
	t.Helper()
	ch, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []Chunk
	for c := range ch {
		got = append(got, c)
	}
	return got
}

// TestStreamAsksForUsage is the other half of the fix: a server only sends the
// usage chunk if the request asked for it. Before this, the request body
// contained no stream_options at all, so even OpenAI itself reported nothing.
func TestStreamAsksForUsage(t *testing.T) {
	p, sent := recordingSSEServer(t, func(string) (int, string) { return http.StatusOK, usageSSE })

	u := streamUsage(drainHTTPStream(t, p))
	if u == nil || u.TokensIn != 1234 || u.TokensOut != 56 {
		t.Errorf("usage over HTTP = %v, want {1234 56}", u)
	}

	bodies := sent()
	if len(bodies) != 1 {
		t.Fatalf("sent %d requests, want 1", len(bodies))
	}
	var req struct {
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &req); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Errorf("request omitted stream_options.include_usage: %s", bodies[0])
	}
}

// TestStreamRetriesWithoutStreamOptionsWhenRejected is the compatibility
// guard. Some endpoints reject an unknown top-level field outright, and asking
// for usage must never be the reason yolo cannot talk to a provider at all. The
// provider retries once without the field, latches the answer so it costs one
// round trip per process rather than per turn, and reports no usage — unknown,
// not zero.
func TestStreamRetriesWithoutStreamOptionsWhenRejected(t *testing.T) {
	p, sent := recordingSSEServer(t, func(body string) (int, string) {
		if strings.Contains(body, "stream_options") {
			return http.StatusBadRequest, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`
		}
		return http.StatusOK, noUsageSSE
	})

	var text strings.Builder
	for _, c := range drainHTTPStream(t, p) {
		text.WriteString(c.Delta)
	}
	if text.String() != "hello world" {
		t.Errorf("text = %q, want %q — the retry did not recover the stream", text.String(), "hello world")
	}
	if u := streamUsage(drainHTTPStream(t, p)); u != nil {
		t.Errorf("server that cannot report usage yielded %+v, want nil", *u)
	}

	bodies := sent()
	if len(bodies) != 3 {
		t.Fatalf("sent %d requests, want 3 (rejected + retry, then one latched call)", len(bodies))
	}
	if !strings.Contains(bodies[0], "stream_options") {
		t.Errorf("first request omitted stream_options: %s", bodies[0])
	}
	for i, b := range bodies[1:] {
		if strings.Contains(b, "stream_options") {
			t.Errorf("request %d re-sent stream_options after the endpoint refused it: %s", i+1, b)
		}
	}
}

// TestStreamSurfacesNonRequestErrors keeps the retry narrow: a 401 is not the
// endpoint failing to parse our body, so it must surface as-is rather than
// being retried into a confusing second failure.
func TestStreamSurfacesNonRequestErrors(t *testing.T) {
	p, sent := recordingSSEServer(t, func(string) (int, string) {
		return http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`
	})
	_, err := p.Stream(context.Background(), Request{})
	if err == nil {
		t.Fatal("Stream succeeded against a 401")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error = %v, want the endpoint's own message", err)
	}
	if n := len(sent()); n != 1 {
		t.Errorf("sent %d requests, want 1 (a 401 must not be retried)", n)
	}
}
