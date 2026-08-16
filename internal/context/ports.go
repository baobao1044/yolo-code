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

// GitDiff reports uncommitted working-tree changes (File 10).
//
// DEAD SEAM. noopGitDiff below is the only implementation in the module, and
// neither composition root sets Deps.Git, so engine.go:86 substitutes the noop
// on every run: no context package has ever contained a working-tree diff. The
// "Sprint 2 stub" note this comment used to carry was written when that was a
// schedule; it has since become a description. Pinned in
// cmd/yolo/deadseam_test.go, which fails if this gains an implementation and
// the note is not removed.
type GitDiff interface {
	Diff(ctx stdctx.Context, repo string) []Part
}

// Graph exposes the repo symbol graph (tree-sitter, File 11).
//
// DEAD SEAM, same shape as GitDiff: noopGraph is the only implementation and
// neither root sets Deps.Graph. The consequence reaches further than an empty
// section of the context package — because pkg.Graph is always empty, six lines
// of prompt/pipeline.go are dead code that reads as live (:112 and :154 count
// its tokens, :140 and :144 count members across the trim, :143 trims it, :363
// appends it to the retrieved set). The retrieval budget arithmetic has never
// run against a non-empty Graph. Pinned in cmd/yolo/deadseam_test.go.
type Graph interface {
	Symbols(ctx stdctx.Context, repo string, query string) []Part
}

// Diagnostics reports LSP/compile errors (File 09).
//
// DEAD SEAM, same shape again, with a distinct consequence worth naming: the
// model never sees a compile error as context, only as a verify verdict after
// it has already written the patch. Pinned in cmd/yolo/deadseam_test.go.
type Diagnostics interface {
	Current(ctx stdctx.Context, repo string) []Part
}

// noopMemory/noopGraph/noopDiags/noopGitDiff are the nil substitutes applied by
// New (engine.go:83-92). Only noopMemory is a genuine fallback — both roots do
// wire Memory. The other three are the whole implementation of their port; see
// the DEAD SEAM notes above.
type noopMemory struct{}

func (noopMemory) Preferences(stdctx.Context, string) []Part   { return nil }
func (noopMemory) Project(stdctx.Context, string) []Part       { return nil }
func (noopMemory) Retrieve(stdctx.Context, string, int) []Part { return nil }

type noopGitDiff struct{}

func (noopGitDiff) Diff(stdctx.Context, string) []Part { return nil }

type noopGraph struct{}

func (noopGraph) Symbols(stdctx.Context, string, string) []Part { return nil }

type noopDiags struct{}

func (noopDiags) Current(stdctx.Context, string) []Part { return nil }
