// Package memory implements Layer 10 — the memory system: six sub-stores
// updated ONLY via events, plus a pure-Go chunk retrieval index (File 11).
//
// The retrieval index is lexical, not semantic: the shipped Embedder is a
// hashing term-frequency vectorizer, so cosine over it ranks by shared literal
// tokens and nothing understands meaning. The Embedder interface is the seam
// for a real model — see semantic.go and embed.go.
//
// Allowed imports: event.
package memory
