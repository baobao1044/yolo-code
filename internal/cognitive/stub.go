// The deterministic stub provider for golden-transcript tests (File 07 §7.7.1,
// File 15 §15.15.3). The golden harness replays a recorded event log and
// asserts a run reproduces it byte-for-byte (modulo model nondeterminism,
// which this stub removes). The stub generates its response as a pure function
// of the last user message — the same input always yields the same chunk
// sequence — so two runs of a golden fixture produce identical token deltas,
// thinking, and tool-call events (S5).
//
// The stub is NOT a real model. It is a fixed, input-driven responder: it
// scans the last user message for keywords ("list"/"files", "read", "edit")
// and emits a deterministic ```tool block or a direct answer accordingly. This
// makes the golden trace both stable and meaningful — the trace reflects the
// input, so a fixture pins a real input→output transition, not a constant.

package cognitive

import (
	"context"
	"strings"

	"github.com/baobao1044/yolo-code/internal/prompt"
)

// StubProvider is the golden-test provider: same Request → same []Chunk, every
// run (S5). It derives the response from the last user message's keywords, so
// a golden fixture's trace is a faithful function of its input.
type StubProvider struct {
	window int
}

// NewStubProvider builds a stub with the given context window (zero → a
// default; matches the Prompt Compiler's budget default in Sprint 2).
func NewStubProvider(window int) *StubProvider {
	if window <= 0 {
		window = 128_000
	}
	return &StubProvider{window: window}
}

// Window returns the provider's context window (File 06 §6.6.1 uses it for the
// budget; the stub's is fixed).
func (s *StubProvider) Window() int { return s.window }

// Stream generates a deterministic chunk sequence from the last user message
// (File 15 §15.15.3). The response is a pure function of the input: the same
// messages always produce the same chunks, so golden transcripts are stable.
func (s *StubProvider) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	chunks := s.respond(req)
	out := make(chan Chunk, len(chunks))
	go func() {
		defer close(out)
		for _, ch := range chunks {
			select {
			case out <- ch:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// respond is the pure input→chunks function. It scans the last user message
// for keywords and emits a deterministic response:
//   - "list" or "files" → a thinking delta + a ```list_files tool block;
//   - "read" → a ```read_file tool block on the mentioned file;
//   - "edit"/"fix"/"refactor" → an ```edit_file tool block;
//   - otherwise → a direct answer (Final).
//
// The response is split into fixed deltas so the token stream is a stable
// sequence the golden harness can record.
func (s *StubProvider) respond(req Request) []Chunk {
	last := lastUser(req.Messages)
	low := strings.ToLower(last)

	switch {
	case strings.Contains(low, "list") || strings.Contains(low, "files"):
		return []Chunk{
			{Thinking: "I'll list the files in the repo."},
			{Delta: "Listing files.\n```tool\n" + `{"tool":"list_files","args":{},"reason":"see the repo"}` + "\n"},
			{Delta: "```\n"},
		}
	case strings.Contains(low, "read"):
		file := extractFile(last)
		return []Chunk{
			{Delta: "Reading " + file + ".\n```tool\n" + `{"tool":"read_file","args":{"file":"` + file + `"},"reason":"inspect it"}` + "\n"},
			{Delta: "```\n"},
		}
	case strings.Contains(low, "edit") || strings.Contains(low, "fix") || strings.Contains(low, "refactor"):
		file := extractFile(last)
		if file == "" {
			file = "main.go"
		}
		return []Chunk{
			{Thinking: "I'll edit the target file."},
			{Delta: "Editing " + file + ".\n```tool\n" + `{"tool":"edit_file","args":{"file":"` + file + `"},"reason":"apply the change"}` + "\n"},
			{Delta: "```\n"},
		}
	default:
		return []Chunk{
			{Delta: "I'll answer directly: " + last},
		}
	}
}

// lastUser returns the content of the last user message, or "" if none. The
// stub's response is a function of this — the input that drives the golden
// trace.
func lastUser(msgs []prompt.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// extractFile pulls a @-referenced or bare path from the message, so the stub's
// tool call targets a real file mentioned in the input. Empty if none found.
func extractFile(s string) string {
	for _, tok := range strings.Fields(s) {
		if strings.HasPrefix(tok, "@") {
			return strings.TrimPrefix(tok, "@")
		}
	}
	// Fall back to a bare path token containing a slash + dot.
	for _, tok := range strings.Fields(s) {
		if strings.Contains(tok, "/") && strings.Contains(tok, ".") {
			return tok
		}
	}
	return ""
}
