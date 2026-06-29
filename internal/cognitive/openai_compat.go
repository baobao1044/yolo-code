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

package cognitive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yolo-code/yolo/internal/prompt"
)

// OpenAICompatProvider streams responses from an OpenAI-compatible
// /chat/completions endpoint using SSE.
type OpenAICompatProvider struct {
	baseURL    string
	apiKey     string
	model      string
	window     int
	httpClient *http.Client
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
// environment variables. Returns nil if YOLO_API_KEY is not set (so the
// caller can fall back to the stub provider).
func OpenAICompatProviderFromEnv() *OpenAICompatProvider {
	apiKey := os.Getenv("YOLO_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	baseURL := os.Getenv("YOLO_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
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
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("api returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse SSE stream in a goroutine.
	out := make(chan Chunk, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		p.parseSSE(ctx, resp.Body, out)
	}()

	return out, nil
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
		Name      strings.Builder
		Arguments strings.Builder
	}
	partials := make(map[int]*partialCall)

	// flushToolCalls emits all accumulated tool calls as Chunks.
	flushToolCalls := func() {
		for idx, pc := range partials {
			name := pc.Name.String()
			args := pc.Arguments.String()
			if name != "" {
				chunk := Chunk{
					ToolCall: &ToolCall{
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

		// Terminal signal — flush any remaining tool calls.
		if data == "[DONE]" {
			flushToolCalls()
			return
		}

		var ev chatStreamResponse
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			// Malformed chunk — skip it.
			continue
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
				if tc.Function.Name != "" {
					partials[idx].Name.WriteString(tc.Function.Name)
				}
				if tc.Function.Arguments != "" {
					partials[idx].Arguments.WriteString(tc.Function.Arguments)
				}
			}
		}

		// If the model signals it's done with tool calls, flush them.
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
	}
}

// --- Wire types for the OpenAI chat completions API ---

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
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
				Index    int `json:"index"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
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
