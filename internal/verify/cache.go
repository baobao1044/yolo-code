// The unchanged-file verification cache (File 09 §9.5 + roadmap L8-005:
// "re-verify of an unchanged file is O(1)"). A per-file, content-keyed cache
// remembers a file's stage result keyed by (path, stage, content hash); a
// second Verify of the same unchanged file skips the work and returns the
// cached result. The cache is wired into the per-file stages (AST, Policy):
// the command stages (Lint/Build/Tests) run on dirs, not files, so they
// aren't keyed here — a package-level cache is a later refinement; L8-005
// ships the per-file cache the exit bar names.
//
// Determinism: the key uses FNV-1a over the file content (stdlib hash/fnv), so
// the key is byte-deterministic — the same content hits the cache across runs
// (S5 byte-identical transcripts). A cache hit records a SevSkip with detail
// "cached: unchanged content" so the trace shows *why* the stage didn't run
// (§9.5.3: "skips are recorded in the verdict so the trace shows why a stage
// didn't run, not just that it didn't").
//
// Concurrency: the cache is single-threaded by construction — only the runtime
// goroutine drives Verify (Invariant I1, File 04 §4.2.1). A future multi-task
// scheduler that drives concurrent verifications would add a mutex here; the
// map isn't safe for concurrent use today.

package verify

import (
	"hash/fnv"
	"sync"
)

// FileCache remembers the StageResult a stage produced for a file's content,
// keyed by (path, stage, content hash). A nil FileCache is a no-op: Lookup
// returns false, Record is a no-op, so a nil cache (unit tests, or the
// composition root opting out) means every stage runs its own logic — no skip.
type FileCache struct {
	mu  sync.Mutex // guards entries; single-writer today (I1), mutex for safety.
	ent map[cacheKey]StageResult
}

// cacheKey is the (path, stage, content-hash) triple a result is filed under.
// The hash is FNV-1a (32-bit) over the file content; collisions are acceptable
// here — a collision causes a wrong cache hit, but the worst case is a stale
// result masking a break, which the cache-miss-on-content-change test guards.
// 32 bits is enough for a project's file count.
type cacheKey struct {
	path  string
	stage Stage
	hash  uint32
}

// NewFileCache returns an empty, ready-to-use FileCache.
func NewFileCache() *FileCache {
	return &FileCache{ent: make(map[cacheKey]StageResult)}
}

// hashContent returns the FNV-1a 32-bit hash of the content (deterministic, stdlib).
func hashContent(content string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(content))
	return h.Sum32()
}

// Lookup returns the cached StageResult for (path, stage, content), and whether
// one was found. A nil cache returns (zero, false). The hash is derived from
// content so the caller doesn't have to compute it.
func (c *FileCache) Lookup(path string, stage Stage, content string) (StageResult, bool) {
	if c == nil {
		return StageResult{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.ent[cacheKey{path: path, stage: stage, hash: hashContent(content)}]
	return r, ok
}

// Record files a StageResult under (path, stage, content). A nil cache is a
// no-op. Only terminal results (pass/warn/fail) are recorded — a skip result
// (the cache-hit skip itself, or a not-required skip) isn't cached, so it
// doesn't poison a later real run.
func (c *FileCache) Record(path string, stage Stage, content string, r StageResult) {
	if c == nil {
		return
	}
	if r.Status == SevSkip {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ent[cacheKey{path: path, stage: stage, hash: hashContent(content)}] = r
}
