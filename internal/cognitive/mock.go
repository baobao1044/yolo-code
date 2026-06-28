// A mock provider for unit tests (File 07 §7.7.1). It returns a scripted
// sequence of Chunks over a channel, letting the Core's streaming loop be
// exercised without a real LLM. The deterministic-stub provider for golden
// transcripts (L6-007) is a richer variant; this is the minimal test double.

package cognitive

import "context"

// MockProvider streams a fixed script of Chunks. It is the test double for the
// Core's Think loop: construct one with the chunks you want the "model" to
// emit, and Think will see exactly those, in order.
type MockProvider struct {
	chunks []Chunk
	window int
}

// NewMockProvider builds a mock that streams the given chunks and reports the
// given window (zero → a default).
func NewMockProvider(chunks []Chunk, window int) *MockProvider {
	mp := &MockProvider{chunks: chunks, window: window}
	if mp.window <= 0 {
		mp.window = 128_000
	}
	return mp
}

// Stream pushes the scripted chunks onto a channel in order and closes it. It
// ignores the request (the mock is not a real model); the chunks are the
// scripted response.
func (m *MockProvider) Stream(ctx context.Context, _ Request) (<-chan Chunk, error) {
	out := make(chan Chunk, len(m.chunks))
	go func() {
		defer close(out)
		for _, ch := range m.chunks {
			select {
			case out <- ch:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (m *MockProvider) Window() int { return m.window }
