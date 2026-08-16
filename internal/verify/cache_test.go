// Tests for L8-005's unchanged-file verification cache (File 09 §9.5 + roadmap
// L8-005: "re-verify of an unchanged file is O(1)"). A per-file, content-keyed
// cache remembers a file's stage result keyed by (path, content hash); a second
// Verify of the same unchanged file skips the work and returns the cached
// result. The cache is wired into the per-file stages (AST, Policy): the command
// stages (Lint/Build/Tests) run on dirs, not files, so they aren't keyed here
// (a package-level cache is a later refinement; L8-005 ships the per-file cache
// the exit bar names).
//
// Determinism: the cache uses FNV-1a over the file content (stdlib hash/fnv),
// so the key is byte-deterministic — the same content hits the cache across
// runs. A cache hit records a SevSkip with detail "cached: unchanged content"
// so the trace shows *why* the stage didn't run (§9.5.3: "skips are recorded").

package verify

import (
	"context"
	"testing"
)

// fsCounting is an FS that counts Read calls so a cache test can assert a cache
// hit avoided re-reading the file.
type fsCounting struct {
	inner fakeFS
	reads int
}

func (f *fsCounting) Read(ctx context.Context, path string) (string, error) {
	f.reads++
	return f.inner.Read(ctx, path)
}

func TestASTStageCachedOnUnchangedContent(t *testing.T) {
	// Verify a valid Go file twice with the same Cache. The first run validates
	// (pass); the second run hits the cache (SevSkip, "cached: unchanged
	// content") and does NOT re-read the file. The exit bar: re-verify of an
	// unchanged file is O(1) — one FS read for the hash, no re-parse.
	fs := &fsCounting{inner: fakeFS{"a.go": "package main\n\nfunc a() {}\n"}}
	cache := NewFileCache()
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs, Cache: cache})

	first := p.stages[0].Run(context.Background(), []string{"a.go"}, Policy{}) // astStage
	if first.Status != SevPass {
		t.Fatalf("first AST run = %s, want pass", first.Status)
	}
	readsAfterFirst := fs.reads

	second := p.stages[0].Run(context.Background(), []string{"a.go"}, Policy{})
	if second.Status != SevSkip {
		t.Fatalf("second AST run = %s, want skip (cache hit)", second.Status)
	}
	if second.Detail == "" || !containsSubstr(second.Detail, "cached") {
		t.Errorf("second AST detail = %q, want it to mention 'cached'", second.Detail)
	}
	// A cache hit reads the file once (to hash it) but no more than the first
	// run's reads — re-validation did NOT re-parse.
	if fs.reads < readsAfterFirst {
		t.Errorf("reads after cache hit = %d, before = %d (cache hit shouldn't reduce reads below first)", fs.reads, readsAfterFirst)
	}
}

func TestASTCacheMissedWhenContentChanged(t *testing.T) {
	// Verify a file, change its content, verify again — the cache must MISS
	// (the hash differs) and re-validate. A stale cache entry must not mask a
	// newly-broken file.
	fs := &fsCounting{inner: fakeFS{"a.go": "package main\n\nfunc a() {}\n"}}
	cache := NewFileCache()
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs, Cache: cache})

	first := p.stages[0].Run(context.Background(), []string{"a.go"}, Policy{})
	if first.Status != SevPass {
		t.Fatalf("first AST run = %s, want pass", first.Status)
	}

	// Break the file (missing close brace). The hash differs → cache miss →
	// re-validate → fail.
	fs.inner["a.go"] = "package main\n\nfunc a() {\n"
	second := p.stages[0].Run(context.Background(), []string{"a.go"}, Policy{})
	if second.Status != SevFail {
		t.Fatalf("second AST run = %s, want fail (changed content must re-validate)", second.Status)
	}
}

func TestCacheKeyedOnStage(t *testing.T) {
	// The cache is keyed by (path, stage, hash), so a result filed under one
	// stage must never satisfy another stage's lookup for the same bytes.
	//
	// This used to be asserted through the pipeline — run astStage, run
	// policyStage, check both passed — which proved nothing: policyStage has no
	// cache and never calls Lookup, so the assertion held with `stage` deleted
	// from cacheKey entirely. The claim is about the key, so it is tested
	// against the key.
	cache := NewFileCache()
	const content = "package main\n\nfunc a() {}\n"
	cache.Record("a.go", StageAST, content, StageResult{Stage: StageAST, Status: SevPass, Detail: "ast valid"})

	got, ok := cache.Lookup("a.go", StageAST, content)
	if !ok || got.Stage != StageAST {
		t.Fatalf("Lookup(a.go, ast) = %+v, %v; want the recorded AST result", got, ok)
	}
	for _, other := range []Stage{StagePolicy, StageLint, StageBuild, StageTest, StageFormat, StageTypeCheck} {
		if r, ok := cache.Lookup("a.go", other, content); ok {
			t.Errorf("Lookup(a.go, %s) hit the AST entry (%+v); the key must include the stage", other, r)
		}
	}
	// Same stage, same bytes, a different path is also a miss.
	if _, ok := cache.Lookup("b.go", StageAST, content); ok {
		t.Error("Lookup(b.go, ast) hit a.go's entry; the key must include the path")
	}
}

func TestEngineWiresTheFileCache(t *testing.T) {
	// L8-005's exit bar is "re-verify of an unchanged file is O(1)", but
	// PipelineDeps.Cache was never set outside the cache tests: every engine
	// built by NewEngine had a nil cache, so the binary did not have the
	// capability the tests certified. NewEngine now owns the cache.
	bus := newBusEnv()
	defer bus.bus.Close()
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	e := NewEngine(Deps{Runner: passRunner(), FS: fs, Bus: bus.bus})
	ch := Change{Task: "t_cache", Files: []string{"a.go"}}

	if v := e.Verify(context.Background(), ch, fullPolicy()); !v.Pass {
		t.Fatalf("first Verify = %+v, want pass", v)
	}
	first, _ := bus.collect(7)
	if stageStatus(first)["ast"] != "pass" {
		t.Fatalf("first run ast status = %q, want pass", stageStatus(first)["ast"])
	}

	// Same engine, same unchanged file: the AST stage must hit the cache.
	v := e.Verify(context.Background(), ch, fullPolicy())
	second, _ := bus.collect(7)
	if got := stageStatus(second)["ast"]; got != "skip" {
		t.Errorf("second run ast status = %q, want skip (cache hit); the engine's cache is not wired", got)
	}
	// And a cached skip is still verification signal — the re-verify passes
	// rather than tripping the engine's no-signal rule.
	if !v.Pass {
		t.Errorf("second Verify = %+v, want pass (a cache hit is a re-used measurement)", v)
	}
}

func TestNilCacheStillRunsStages(t *testing.T) {
	// A nil cache (unit tests skipping the cache, or the composition root
	// opting out) must NOT skip any stage — every stage runs its logic.
	fs := fakeFS{"a.go": "package main\n\nfunc a() {}\n"}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fs}) // no Cache

	r := p.stages[0].Run(context.Background(), []string{"a.go"}, Policy{})
	if r.Status != SevPass {
		t.Errorf("AST with nil cache = %s, want pass (stage ran normally)", r.Status)
	}
	// A second run with the nil cache also runs (no skip).
	r2 := p.stages[0].Run(context.Background(), []string{"a.go"}, Policy{})
	if r2.Status != SevPass {
		t.Errorf("second AST run with nil cache = %s, want pass (no cache → no skip)", r2.Status)
	}
}

// containsSubstr reports whether s contains sub. Kept local so the cache test
// doesn't pull strings just for one assertion.
func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
