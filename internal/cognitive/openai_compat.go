// OpenAI-compatible chat completions provider (File 07 §7.7.1). Implements
// cognitive.Provider against any /chat/completions endpoint (OpenAI, WandB,
// Together, local llama.cpp, etc.). Streams SSE responses and parses them
// into Chunk values. Configured via environment variables:
//
//	YOLO_API_KEY  — Bearer token (required)
//	YOLO_BASE_URL — API base URL (default: https://api.openai.com/v1)
//	YOLO_MODEL    — model ID (default: gpt-4o)
//	YLOLO_WINDOW  — context window size (default: 128000)

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
func (p *OpenAICompatProvider) parseSSE(ctx context.Context, r io.Reader, out chan<- Chunk) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and non-data lines.
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// Terminal signal.
		if data == "[DONE]" {
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

		delta := ev.Choices[0].Delta
		var chunk Chunk

		// Reasoning/thinking content (some providers use reasoning_content).
		if delta.ReasoningContent != "" {
			chunk.Thinking = delta.ReasoningContent
		}

		// Main content delta.
		if delta.Content != "" {
			chunk.Delta = delta.Content
		}

		// Tool calls.
		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				if tc.Function.Name != "" {
					chunk.ToolCall = &ToolCall{
						Tool:   tc.Function.Name,
						Args:   []byte(tc.Function.Arguments),
						Reason: "",
					}
				}
			}
		}

		// Only emit non-empty chunks.
		if chunk.Delta != "" || chunk.Thinking != "" || chunk.ToolCall != nil || chunk.Err != nil {
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}

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
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

// Ensure OpenAICompatProvider satisfies the Provider interface at compile time.
var _ Provider = (*OpenAICompatProvider)(nil)

// Ensure prompt.Message is not unused (the wire conversion uses it).
var _ = func() prompt.Message { return prompt.Message{} }
