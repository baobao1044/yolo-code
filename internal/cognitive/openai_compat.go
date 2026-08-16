// OpenAI-compatible chat completions provider (File 07 §7.7.1). Implements
// cognitive.Provider against any /chat/completions endpoint (OpenAI,
// Together, local llama.cpp, etc.). Streams SSE responses and parses them
// into Chunk values. Configured via environment variables:
//
//	YOLO_API_KEY  — Bearer token (required)
//	YOLO_BASE_URL — API base URL (default: https://api.openai.com/v1)
//	YOLO_MODEL    — model ID (default: gpt-4o)
//	YLOLO_WINDOW  — context window size (default: 128000)
//
// When the request carries tool names (Request.Tools), the provider includes
// OpenAI-native function/tool definitions in the chat completions request so
// models that support structured tool calling emit
// delta.tool_calls instead of inline tool tokens.
//
// The request also asks for token usage (stream_options.include_usage). A
// server that honours it appends a final chunk carrying prompt/completion
// counts; that is the only place a real token count exists, and without it
// Turn.TokensIn/Out are guesses. Endpoints that refuse the field are handled by
// a one-shot retry (see Stream) — asking for usage must never be the reason
// yolo cannot talk to a provider.

package cognitive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/baobao1044/yolo-code/internal/prompt"
)

// OpenAICompatProvider streams responses from an OpenAI-compatible
// /chat/completions endpoint using SSE.
type OpenAICompatProvider struct {
	baseURL    string
	apiKey     string
	model      string
	window     int
	httpClient *http.Client

	// noStreamOptions latches once an endpoint has refused stream_options, so
	// the compatibility retry costs one wasted round trip per process instead
	// of one per turn. Stream may be called from more than one goroutine
	// (the TUI driver swaps providers, the coordinator runs agents), hence
	// atomic rather than a plain bool.
	noStreamOptions atomic.Bool
}

// NewOpenAICompatProvider builds a provider from explicit parameters. The
// caller reads env vars or a config file and passes them in — the provider
// does no config I/O itself.
func NewOpenAICompatProvider(baseURL, apiKey, model string, window int) *OpenAICompatProvider {
	if window <= 0 {
		window = 128_000
	}
	// Strip trailing slash for clean Join.
	baseURL = strings.TrimRight(baseURL, "/")
	return &OpenAICompatProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		window:  window,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // long-running streaming calls
		},
	}
}

// OpenAICompatProviderFromEnv builds a provider from the standard
// environment variables. Returns nil if no key resolves, so the caller can
// report an unconfigured setup.
//
// OPENAI_API_KEY is OpenAI's credential, so it is only honoured when the base
// URL is OpenAI's own host. Otherwise a user with OPENAI_API_KEY exported (very
// common) who points YOLO_BASE_URL at a third-party endpoint would ship that
// key to a host it was never issued for. YOLO_API_KEY is yolo-code's own var —
// the user sets it alongside the base URL they chose — so it applies anywhere.
func OpenAICompatProviderFromEnv() *OpenAICompatProvider {
	baseURL := os.Getenv("YOLO_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	apiKey := os.Getenv("YOLO_API_KEY")
	if apiKey == "" && isOpenAIEndpoint(baseURL) {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	model := os.Getenv("YOLO_MODEL")
	if model == "" {
		model = "gpt-4o"
	}
	window := 128_000
	if w := os.Getenv("YOLO_WINDOW"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			window = n
		}
	}
	return NewOpenAICompatProvider(baseURL, apiKey, model, window)
}

// isOpenAIEndpoint reports whether baseURL addresses OpenAI's own API. Host
// match only — a path or scheme says nothing about who receives the bearer
// token. An unparseable URL is treated as foreign (fail closed).
func isOpenAIEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".api.openai.com")
}

// Window returns the provider's context window size.
func (p *OpenAICompatProvider) Window() int { return p.window }

// Stream sends a chat completions request with streaming enabled and returns
// a channel of Chunk values parsed from the SSE response.
func (p *OpenAICompatProvider) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	// Build the request body.
	messages := make([]chatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = chatMessage{Role: m.Role, Content: m.Content}
	}

	body := chatRequest{
		Model:    p.model,
		Messages: messages,
		Stream:   true,
	}

	// When the request carries tool names, include OpenAI-native tool
	// definitions so models with structured tool calling emit delta.tool_calls
	// instead of inline tool tokens.
	if len(req.Tools) > 0 {
		body.Tools = buildToolDefs(req.Tools)
	}

	// Ask for the usage chunk unless this endpoint has already refused to
	// accept the field. Most servers that don't implement stream_options simply
	// ignore it and send no usage, which is handled downstream as "unknown".
	includeUsage := !p.noStreamOptions.Load()
	resp, err := p.post(ctx, body, includeUsage)
	if err != nil && includeUsage && rejectsRequestBody(err) {
		// The endpoint refused the request body itself. stream_options is the
		// newest thing in it and the only field an older proxy is likely not to
		// know, so drop it and try once more: a provider we can talk to without
		// usage beats a provider we cannot talk to at all. If the second attempt
		// also fails the field was not the problem, so report the original
		// error — it describes the request we actually meant to send.
		if retryResp, retryErr := p.post(ctx, body, false); retryErr == nil {
			p.noStreamOptions.Store(true)
			resp, err = retryResp, nil
		}
	}
	if err != nil {
		return nil, err
	}

	// Parse SSE stream in a goroutine.
	out := make(chan Chunk, 64)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		p.parseSSE(ctx, resp.Body, out)
	}()

	return out, nil
}

// post sends one chat completions request and returns the streaming response
// body, already checked for a non-200 status. includeUsage decides whether the
// body carries stream_options; body is taken by value so setting it here does
// not leak into the caller's retry.
func (p *OpenAICompatProvider) post(ctx context.Context, body chatRequest, includeUsage bool) (*http.Response, error) {
	if includeUsage {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, &apiStatusError{code: resp.StatusCode, body: string(errBody)}
	}
	return resp, nil
}

// apiStatusError is a non-200 response from the endpoint. It exists so the
// compatibility retry can tell "you sent me a body I can't parse" apart from
// "your key is wrong" without matching on message text.
type apiStatusError struct {
	code int
	body string
}

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("api returned %d: %s", e.code, e.body)
}

// rejectsRequestBody reports whether err is the endpoint saying it could not
// make sense of what we sent, as opposed to an auth failure, a rate limit, or a
// server fault. Only the former is worth retrying with a smaller body — a 401
// retried is just two 401s and a confusing error message.
func rejectsRequestBody(err error) bool {
	var se *apiStatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.code == http.StatusBadRequest || se.code == http.StatusUnprocessableEntity
}

// parseSSE reads the SSE stream and emits Chunks. The OpenAI streaming
// format sends `data: {json}\n\n` lines, with `data: [DONE]` as the
// terminal signal.
//
// Tool calls arrive as indexed delta fragments: each chunk carries
// delta.tool_calls[i].function.name and/or .arguments, and the same index
// may appear across multiple SSE events (name first, then argument deltas).
// We accumulate them in a map keyed by index and emit a Chunk.ToolCall
// each time a complete call is available (when the finish_reason is
// "tool_calls" or [DONE] arrives).
func (p *OpenAICompatProvider) parseSSE(ctx context.Context, r io.Reader, out chan<- Chunk) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	// Accumulate partial tool calls by index across SSE chunks.
	type partialCall struct {
		ID        string
		Name      strings.Builder
		Arguments strings.Builder
	}
	partials := make(map[int]*partialCall)

	// finish_reason is the model's own statement that it stopped generating.
	// The end-of-body handler needs it to tell a stream that ended on purpose
	// from one that was cut off.
	//
	// [DONE] — the SSE framing sentinel — used to be tracked here in a second
	// flag, but it never could be read: the [DONE] branch below returns from
	// parseSSE outright, so reaching the tail already proves [DONE] never
	// arrived and the flag was false at the only place it was consulted. The
	// check there read `!sawDone && !sawFinish`, whose first half was a
	// tautology dressed up as a condition. Keeping the flag would mean the next
	// reader has to re-derive that to know whether the check is sound; the
	// invariant is written into the comment on the check instead.
	sawFinish := false

	// flushToolCalls emits all accumulated tool calls as Chunks, in ascending
	// wire index. Ranging the map directly would emit them in Go's randomised
	// map order, so a turn with parallel tool calls came out in a different
	// order on every run — nondeterministic against the golden transcripts
	// (S5), and it scrambles call↔result attribution for anything pairing by
	// position. The wire index is the model's own ordering, so sort by it.
	flushToolCalls := func() {
		idxs := make([]int, 0, len(partials))
		for idx := range partials {
			idxs = append(idxs, idx)
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			pc := partials[idx]
			name := pc.Name.String()
			args := pc.Arguments.String()
			if name != "" {
				chunk := Chunk{
					ToolCall: &ToolCall{
						ID:     pc.ID,
						Tool:   name,
						Args:   []byte(args),
						Reason: "",
					},
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			}
			delete(partials, idx)
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and non-data lines.
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// Terminal signal — flush any remaining tool calls. Returning here is
		// what makes the truncation check at the end of the function correct
		// without a [DONE] flag: the stream said it was finished, so there is
		// nothing left to diagnose.
		if data == "[DONE]" {
			flushToolCalls()
			return
		}

		var ev chatStreamResponse
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			// Malformed chunk — skip it.
			continue
		}

		// Usage rides its own chunk, emitted after the last content delta and
		// before [DONE], and that chunk's choices array is EMPTY — the skip
		// below would drop it on the floor, which is why Turn.TokensIn/Out were
		// never assigned. Read it first. It is not conditional on choices being
		// empty because some servers attach the counts to the finish chunk
		// instead.
		if u, ok := ev.Usage.usage(); ok {
			select {
			case out <- Chunk{Usage: &u}:
			case <-ctx.Done():
				return
			}
		}

		if len(ev.Choices) == 0 {
			continue
		}

		choice := ev.Choices[0]
		delta := choice.Delta
		var chunk Chunk

		// Reasoning/thinking content (some providers use reasoning_content).
		if delta.ReasoningContent != "" {
			chunk.Thinking = delta.ReasoningContent
		}

		// Main content delta.
		if delta.Content != "" {
			chunk.Delta = delta.Content
		}

		// Tool calls: accumulate by index, flush on finish.
		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if _, ok := partials[idx]; !ok {
					partials[idx] = &partialCall{}
				}
				// The provider's call id arrives on the first fragment of each
				// call; later fragments omit it. Keep the first one seen.
				if tc.ID != "" && partials[idx].ID == "" {
					partials[idx].ID = tc.ID
				}
				if tc.Function.Name != "" {
					partials[idx].Name.WriteString(tc.Function.Name)
				}
				if tc.Function.Arguments != "" {
					partials[idx].Arguments.WriteString(tc.Function.Arguments)
				}
			}
		}

		// Any finish_reason — "stop", "tool_calls", "length", "content_filter" —
		// is the model saying it stopped, which is what the EOF check below needs
		// to know. Only the two that end a turn with something to hand over
		// trigger a flush.
		if choice.FinishReason != "" {
			sawFinish = true
		}
		if choice.FinishReason == "tool_calls" || choice.FinishReason == "stop" {
			flushToolCalls()
		}

		// Only emit non-empty text/thinking chunks (tool calls are emitted by flush).
		if chunk.Delta != "" || chunk.Thinking != "" || chunk.Err != nil {
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}

	// Flush any remaining tool calls if we exit the loop without [DONE].
	flushToolCalls()

	if err := scanner.Err(); err != nil {
		select {
		case out <- Chunk{Err: fmt.Errorf("sse read: %w", err)}:
		case <-ctx.Done():
		}
		return
	}

	// The body ended. If nothing in the stream ever said the generation had
	// finished — no [DONE], no finish_reason — it was cut off, and no reader
	// downstream can tell: Think returns Final=true with whatever the usage
	// chunk carried, so a truncated turn is recorded as a complete, measured one
	// and its short completion count is billed as a fact. This is the last place
	// that is still knowable, so say it here.
	//
	// Only the absence of BOTH is fatal. A missing [DONE] on its own is a
	// framing quirk of endpoints that simply close the connection, and the
	// finish_reason they did send is the model's own statement that it stopped;
	// erroring on that would break providers that work today. A truncation that
	// cuts an HTTP chunk in half never reaches this branch either — the body
	// read fails and scanner.Err() above reports it.
	//
	// Reaching this line at all already means no [DONE] arrived, because that
	// branch returns; the only open question left is finish_reason.
	if !sawFinish {
		select {
		case out <- Chunk{Err: errors.New("sse stream ended without [DONE] or a finish_reason: the response was cut off mid-generation")}:
		case <-ctx.Done():
		}
	}
}

// --- Wire types for the OpenAI chat completions API ---

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Tools         []chatTool     `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions carries the include_usage opt-in. It is a pointer on
// chatRequest with omitempty so the retry path can send a body that has no
// stream_options key at all, not one set to null — an endpoint strict enough to
// reject the field is strict enough to reject a null too.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatStreamResponse struct {
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"` // the provider's tool_call_id
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"` // nil on every chunk but the usage one
}

// chatUsage is the token count a server sends when the request asked for it via
// stream_options.include_usage. The input_tokens/output_tokens spelling is what
// Anthropic-shaped compatibility shims emit for the same two numbers; reading
// only one spelling makes a whole class of endpoint look silent.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

// usage converts the wire object into a Usage, reporting false when there is
// nothing to report. Two cases produce false: no usage object at all (the
// server ignored stream_options), and an all-zero one — some local runners
// attach a zeroed placeholder to every chunk, and no real request costs zero
// prompt tokens, so believing it would manufacture exactly the confident 0 this
// path exists to remove. A measured zero on ONE side (an empty completion) is
// still a measurement and is reported.
func (u *chatUsage) usage() (Usage, bool) {
	if u == nil {
		return Usage{}, false
	}
	in, out := u.PromptTokens, u.CompletionTokens
	if in == 0 {
		in = u.InputTokens
	}
	if out == 0 {
		out = u.OutputTokens
	}
	if in == 0 && out == 0 {
		return Usage{}, false
	}
	return Usage{TokensIn: in, TokensOut: out}, true
}

// toolDefs is the registry of tool schemas the provider can send to the model.
// Keys are the tool names used in Request.Tools; values are OpenAI-format
// function definitions. This is the single source of truth for the tool schema
// — the system prompt in engine.go describes the same tools in prose; this
// table gives the model the structured schema it needs for native tool calling.
var toolDefs = map[string]chatTool{
	"list_files": {
		Type: "function",
		Function: chatFunction{
			Name:        "list_files",
			Description: "List files in the repository. Returns a list of relative file paths.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
	},
	"read_file": {
		Type: "function",
		Function: chatFunction{
			Name:        "read_file",
			Description: "Read a file's contents from the repository.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string","description":"relative path to the file"}},"required":["file"]}`),
		},
	},
	"edit_file": {
		Type: "function",
		Function: chatFunction{
			Name:        "edit_file",
			Description: "Edit a file by replacing its entire contents. Always output the FULL file content, never partial diffs.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string","description":"relative path to the file"},"content":{"type":"string","description":"the full new file content"}},"required":["file","content"]}`),
		},
	},
	"bash": {
		Type: "function",
		Function: chatFunction{
			Name:        "bash",
			Description: "Run a shell command in the repository directory. Use for building, testing, running scripts, etc.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"the shell command to run"}},"required":["command"]}`),
		},
	},
	"grep": {
		Type: "function",
		Function: chatFunction{
			Name:        "grep",
			Description: "Search file contents in the repository for a regex pattern. Returns matching lines with file paths and line numbers.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"regex pattern to search for"},"path":{"type":"string","description":"optional directory or file to search in (default: repo root)"}},"required":["pattern"]}`),
		},
	},
}

// DefaultTools returns the tool names the model is offered, sorted: exactly the
// keys of toolDefs. The composition root passes it to New, and New derives the
// Tool Policy's allowlist from what it is passed — so the schemas the provider
// sends, the names the request advertises, and the names the Core will admit
// are all one list read from one place. Adding a tool means adding a schema
// above and nothing else; there is no second declaration to forget.
//
// The system prompt's prose list (internal/context) is the one copy this does
// not reach. It is advisory — the model can only successfully call what is
// admitted here — but it can still fall out of step, and there is no test that
// would notice.
func DefaultTools() []string {
	names := make([]string, 0, len(toolDefs))
	for n := range toolDefs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// buildToolDefs converts a list of tool names to OpenAI-format tool definitions.
// Unknown names are skipped silently so the request doesn't break if a tool
// name has no schema yet.
func buildToolDefs(names []string) []chatTool {
	var tools []chatTool
	for _, name := range names {
		if def, ok := toolDefs[name]; ok {
			tools = append(tools, def)
		}
	}
	return tools
}

// Ensure OpenAICompatProvider satisfies the Provider interface at compile time.
var _ Provider = (*OpenAICompatProvider)(nil)

// Ensure prompt.Message is not unused (the wire conversion uses it).
var _ = func() prompt.Message { return prompt.Message{} }
