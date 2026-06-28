// The deterministic local Embedder (File 11 §11.7.4) — a hash-based term-
// frequency embedder so the vector store is offline-testable with zero deps.
// A real OpenAI/Ollama embedder plugs behind the same Embedder interface later;
// the MVP default keeps no code leaving the machine (air-gapped audience,
// File 01 §1.3.3).
//
// Strategy: each token in the text is hashed (FNV-1a) into one of `dim` buckets
// and accumulates into that dimension (term-frequency hashing). This gives:
// - determinism (S5): the same text → the same vector across runs.
// - discrimination: distinct term profiles → distinct vectors.
// - a fixed dimension (so cosine is comparable across chunks).
// It is NOT a quality embedding (no semantics, no context) — it's a stable
// shape the cosine/top-k machinery is tested against. The chunking + retrieval
// logic is the real test target; embedding quality isn't (the hosted embedder
// swaps in behind the interface).

package memory

import (
	"context"
	"hash/fnv"
	"strings"
)

// hashEmbedder is the deterministic local Embedder. dim is the fixed vector
// dimension (the same across all chunks so cosine is comparable).
type hashEmbedder struct {
	dim int
}

// NewHashEmbedder returns a deterministic hash-based embedder over a fixed
// `dim`-dimensional space. dim must be > 0.
func NewHashEmbedder(dim int) Embedder {
	if dim <= 0 {
		dim = 256
	}
	return &hashEmbedder{dim: dim}
}

// Embed returns one vector per input text, in order. Each text is tokenized on
// whitespace + punctuation, and each token's FNV-1a hash picks a bucket that
// accumulates +1 (term frequency). An empty/whitespace-only text yields the
// zero vector (a valid vector; cosine with it is 0).
func (e *hashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = e.embedOne(text)
	}
	return out, nil
}

// embedOne hashes the text's tokens into a fixed-dim term-frequency vector.
func (e *hashEmbedder) embedOne(text string) []float32 {
	vec := make([]float32, e.dim)
	for _, tok := range tokenize(text) {
		h := fnv.New32a()
		h.Write([]byte(tok))
		bucket := int(h.Sum32()) % e.dim
		if bucket < 0 {
			bucket = -bucket
		}
		vec[bucket]++
	}
	return vec
}

// tokenize splits text into lowercase word tokens on whitespace + punctuation.
// Kept simple — the embedder only needs stable tokenization for determinism.
func tokenize(text string) []string {
	var out []string
	var b strings.Builder
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			if r >= 'A' && r <= 'Z' {
				r = r + ('a' - 'A')
			}
			b.WriteRune(r)
		} else {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// cosine is the cosine similarity of two vectors (File 11 §11.6.2). Returns 0
// for zero vectors (no division by zero). Range [-1, 1] in general; the hash
// embedder only produces non-negative vectors so the practical range is [0, 1].
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

// sqrt is a stdlib-free square root for float64 (math.Sqrt would do, but
// keeping math out of the embedder keeps the import list minimal; the
// precision is fine for cosine ranking). Uses math.Sqrt via the math import in
// the calling file where needed — kept here as a thin wrapper for testability.
func sqrt(x float64) float64 {
	// Babylonian method; ~10 iterations is float64-precise for ranking.
	if x <= 0 {
		return 0
	}
	g := x
	for i := 0; i < 20; i++ {
		g = 0.5 * (g + x/g)
	}
	return g
}
