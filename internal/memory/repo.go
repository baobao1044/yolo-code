// Repository memory — the agent's understanding of the codebase, surviving
// across sessions (§11.4.2). L10-001 ships the surfaces the Context Engine
// consumes: AGENTS.md read (Project()), a directory tree cache (in-memory), and
// Invalidate(paths) to mark paths stale on file writes. The symbol graph
// (§11.4.2, tree-sitter-derived) is deferred — L10-003/004 build the semantic
// store instead; a PageRank-style graph is a later refinement.
//
// The tree cache is in-memory for L10-001 (the spec's full walk + .yoloignore
// is a later ticket); Invalidate marks paths stale so the next read re-walks.

package memory

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ProjectStore holds the project's AGENTS.md content + a tree cache invalidated
// on file writes (via patch.applied, L10-002). AGENTS.md is read on first
// Project() call and re-read after Invalidate touches it.
type ProjectStore struct {
	root string
	mu   sync.Mutex
	// stale is the set of paths Invalidate marked stale; the next Project()
	// /ReadFiles call re-reads them from disk. Kept as a set, listed sorted
	// (S5 determinism — Stale() returns a stable order).
	stale map[string]bool
	// agents caches the AGENTS.md content; empty until first read.
	agents string
}

// NewProjectStore returns a project store rooted at dir (the project root).
func NewProjectStore(dir string) *ProjectStore {
	return &ProjectStore{root: dir, stale: make(map[string]bool)}
}

// Invalidate marks the given paths stale (§11.4.2 — on patch.applied, the
// touched paths drop out of cache so the next read re-walks them).
func (p *ProjectStore) Invalidate(paths []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, path := range paths {
		p.stale[path] = true
	}
}

// Stale returns the paths currently marked stale, sorted (S5). The test asserts
// the invalidate landed.
func (p *ProjectStore) Stale() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.stale))
	for path := range p.stale {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// Project returns the project memory as Parts: AGENTS.md content (if present)
// as a KindProject part (File 06 §6.4). The Context Engine's project group
// (engine.go gather) reads this. Read-on-first-call; a re-read after the file
// is invalidated refreshes the cache.
func (p *ProjectStore) Project(_ context.Context) []Part {
	p.mu.Lock()
	if p.stale["AGENTS.md"] || p.agents == "" {
		p.mu.Unlock()
		content := p.readAgents()
		p.mu.Lock()
		p.agents = content
		delete(p.stale, "AGENTS.md")
	}
	content := p.agents
	p.mu.Unlock()
	if content == "" {
		return nil
	}
	return []Part{{Kind: KindProject, Source: "AGENTS.md", Text: content}}
}

// readAgents reads AGENTS.md from the project root (best-effort: a missing or
// unreadable file is "no project memory", not an error).
func (p *ProjectStore) readAgents() string {
	data, err := os.ReadFile(filepath.Join(p.root, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return string(data)
}
