// Port interfaces for the Context Engine's future-layer collaborators
// (File 06 §6.1). Each is a minimal surface; the owning layer (File 10 git,
// File 11 memory/graph, File 09 diagnostics) implements the real version in a
// later sprint. Sprint 2 injects no-op stubs so gather/rank/compress are
// testable now.

package context

import stdctx "context"

// Memory surfaces preferences, project memory, and RAG retrieval (File 11).
// Retrieve runs a semantic query against the vector store (Knowledge/Repository
// code chunks, §11.6.2) and returns the top-k chunks as Parts; the Context
// Engine's gather feeds them into the prompt's RAG group. Sprint 2's stub
// returns none; L10-006 wires the real memory.Store behind this seam.
type Memory interface {
	Preferences(ctx stdctx.Context, task string) []Part
	Project(ctx stdctx.Context, projectID string) []Part
	Retrieve(ctx stdctx.Context, query string, topK int) []Part
}

// GitDiff reports uncommitted working-tree changes (File 10). Sprint 2 stub
// returns none.
type GitDiff interface {
	Diff(ctx stdctx.Context, repo string) []Part
}

// Graph exposes the repo symbol graph (tree-sitter, File 11). Sprint 2 stub
// returns no symbols; centrality is 0.
type Graph interface {
	Symbols(ctx stdctx.Context, repo string, query string) []Part
}

// Diagnostics reports LSP/compile errors (File 09). Sprint 2 stub returns none.
type Diagnostics interface {
	Current(ctx stdctx.Context, repo string) []Part
}

// noopMemory/noopGraph/noopDiags/noopGitDiff are Sprint 2 no-ops.
type noopMemory struct{}

func (noopMemory) Preferences(stdctx.Context, string) []Part { return nil }
func (noopMemory) Project(stdctx.Context, string) []Part     { return nil }
func (noopMemory) Retrieve(stdctx.Context, string, int) []Part { return nil }

type noopGitDiff struct{}

func (noopGitDiff) Diff(stdctx.Context, string) []Part { return nil }

type noopGraph struct{}

func (noopGraph) Symbols(stdctx.Context, string, string) []Part { return nil }

type noopDiags struct{}

func (noopDiags) Current(stdctx.Context, string) []Part { return nil }
