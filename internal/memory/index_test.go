// Tests for cold-start repo indexing (§11.7.5) — IndexRepo walks the repo,
// skips vendored/cache/oversized files, chunks each source file, and
// bulk-inserts into the SemanticStore so the first turn has RAG signal.

package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes a file under dir, creating parent dirs lazily.
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestIndexRepoIndexesSourceFiles: a Go file is chunked per-function and the
// chunks are retrievable. Paths are repo-relative (§11.7.5 display).
func TestIndexRepoIndexesSourceFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc Fibonacci(n int) int {\n  if n <= 1 { return n }\n  return Fibonacci(n-1) + Fibonacci(n-2)\n}\n")

	store := NewSemanticStoreWithFS(NewHashEmbedder(256), nil)
	n, err := IndexRepo(context.Background(), store, dir)
	if err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}
	if n == 0 {
		t.Fatal("IndexRepo indexed 0 chunks, want >=1 (main.go has a function)")
	}
	if store.Size() != n {
		t.Errorf("Size() = %d, want %d", store.Size(), n)
	}

	// A query sharing terms with the function must retrieve it.
	parts := store.Retrieve(context.Background(), "Fibonacci", 5)
	if len(parts) == 0 {
		t.Fatal("Retrieve returned nothing after IndexRepo")
	}
	if parts[0].Source != "main.go" {
		t.Errorf("first hit Source = %q, want main.go (repo-relative)", parts[0].Source)
	}
}

// TestIndexRepoSkipsVendoredAndCacheDirs: .git/, vendor/, node_modules/ are
// skipped entirely (§11.7.5). A chunk placed there must not surface.
func TestIndexRepoSkipsVendoredAndCacheDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real.go", "package main\n\nfunc Real() int { return 1 }\n")
	writeFile(t, dir, "vendor/skip.go", "package vendor\n\nfunc Skip() int { return 2 }\n")
	writeFile(t, dir, ".git/config.go", "package git\n\nfunc Hidden() int { return 3 }\n")
	writeFile(t, dir, "node_modules/x.go", "package nm\n\nfunc Nm() int { return 4 }\n")

	store := NewSemanticStoreWithFS(NewHashEmbedder(256), nil)
	n, err := IndexRepo(context.Background(), store, dir)
	if err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}
	// Only real.go should be indexed; the vendored/cache dirs are skipped.
	parts := store.Retrieve(context.Background(), "Real", 10)
	for _, p := range parts {
		if p.Source == "vendor/skip.go" || p.Source == ".git/config.go" || p.Source == "node_modules/x.go" {
			t.Errorf("skipped dir file surfaced in retrieval: %s", p.Source)
		}
	}
	if n == 0 {
		t.Error("IndexRepo indexed 0 chunks despite real.go — skip dirs over-skipped")
	}
}

// TestIndexRepoSkipsOversizedFiles: a file over the size cap is skipped.
func TestIndexRepoSkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "small.go", "package main\n\nfunc Small() int { return 1 }\n")
	// Write a file just over the 1 MiB cap.
	big := make([]byte, maxFileBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	big[0] = 'p'; big[1] = 'a'; big[2] = 'c'; big[3] = 'k' // plausible-ish header
	if err := os.WriteFile(filepath.Join(dir, "big.go"), big, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := NewSemanticStoreWithFS(NewHashEmbedder(256), nil)
	if _, err := IndexRepo(context.Background(), store, dir); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}
	for _, p := range store.Retrieve(context.Background(), "pack", 10) {
		if p.Source == "big.go" {
			t.Error("oversized big.go was indexed; want skipped (size cap)")
		}
	}
}

// TestIndexRepoNilStoreOrEmptyRootIsNoop: defensive — nil store or empty root
// returns 0, nil (no panic).
func TestIndexRepoNilStoreOrEmptyRootIsNoop(t *testing.T) {
	if n, err := IndexRepo(context.Background(), nil, t.TempDir()); err != nil || n != 0 {
		t.Errorf("IndexRepo(nil store) = (%d, %v), want (0, nil)", n, err)
	}
	store := NewSemanticStoreWithFS(NewHashEmbedder(256), nil)
	if n, err := IndexRepo(context.Background(), store, ""); err != nil || n != 0 {
		t.Errorf("IndexRepo(empty root) = (%d, %v), want (0, nil)", n, err)
	}
}

// TestIndexRepoDeterministic: two walks of the same tree produce the same
// chunk set in the same order (S5 — the headless transcript stays reproducible).
func TestIndexRepoDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\nfunc A() int { return 1 }\n")
	writeFile(t, dir, "b.go", "package main\nfunc B() int { return 2 }\n")

	a := NewSemanticStoreWithFS(NewHashEmbedder(256), nil)
	b := NewSemanticStoreWithFS(NewHashEmbedder(256), nil)
	na, _ := IndexRepo(context.Background(), a, dir)
	nb, _ := IndexRepo(context.Background(), b, dir)
	if na != nb {
		t.Fatalf("chunk counts diverge: %d vs %d (not deterministic)", na, nb)
	}
	pa := a.Retrieve(context.Background(), "A B", 10)
	pb := b.Retrieve(context.Background(), "A B", 10)
	if len(pa) != len(pb) {
		t.Fatalf("Retrieve len diverges: %d vs %d (not deterministic)", len(pa), len(pb))
	}
	for i := range pa {
		if pa[i].Source != pb[i].Source || pa[i].Score != pb[i].Score {
			t.Errorf("hit %d diverges: %+v vs %+v (not deterministic)", i, pa[i], pb[i])
		}
	}
}
