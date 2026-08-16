// Tests for cold-start repo indexing (§11.7.5) — IndexRepo walks the repo,
// skips vendored/cache/oversized files, chunks each source file, and
// bulk-inserts into the LexicalStore so the first turn has RAG signal.

package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	store := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
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

// indexedSources returns the set of paths the store actually holds chunks for.
// The skip tests below assert on THIS, not on Retrieve output: retrieval only
// shows a chunk whose hashed-token cosine against the query is positive, so a
// banned file that was indexed anyway stays invisible to any query that doesn't
// literally share its tokens — which is how both skip tests used to pass with
// the skip logic deleted. Tests live in package memory, so reading the chunk
// slice directly is the honest assertion.
func indexedSources(s *LexicalStore) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.chunks))
	for _, c := range s.chunks {
		out[c.path] = true
	}
	return out
}

// TestIndexRepoSkipsVendoredAndCacheDirs: .git/, vendor/, node_modules/ are
// skipped entirely (§11.7.5). Nothing under them may reach the index.
func TestIndexRepoSkipsVendoredAndCacheDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real.go", "package main\n\nfunc Real() int { return 1 }\n")
	writeFile(t, dir, "vendor/skip.go", "package vendor\n\nfunc Skip() int { return 2 }\n")
	writeFile(t, dir, ".git/config.go", "package git\n\nfunc Hidden() int { return 3 }\n")
	writeFile(t, dir, "node_modules/x.go", "package nm\n\nfunc Nm() int { return 4 }\n")

	store := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
	n, err := IndexRepo(context.Background(), store, dir)
	if err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}
	// Only real.go should be indexed; the vendored/cache dirs are skipped.
	got := indexedSources(store)
	for _, banned := range []string{
		filepath.Join("vendor", "skip.go"),
		filepath.Join(".git", "config.go"),
		filepath.Join("node_modules", "x.go"),
	} {
		if got[banned] {
			t.Errorf("indexed a file under a skipped dir: %s (indexed set: %v)", banned, got)
		}
	}
	if !got["real.go"] {
		t.Errorf("real.go was not indexed (indexed set: %v) — skip dirs over-skipped", got)
	}
	if len(got) != 1 {
		t.Errorf("indexed %d distinct sources, want exactly 1 (real.go): %v", len(got), got)
	}
	// One function in one file ⇒ one chunk. A larger count means the walk
	// descended somewhere it shouldn't have.
	if n != 1 || store.Size() != 1 {
		t.Errorf("IndexRepo = %d chunks, Size() = %d, want 1 each (only real.go's func)", n, store.Size())
	}
}

// TestIndexRepoSkipsOversizedFiles: a file over the size cap never reaches the
// index (§11.7.5). Asserted on the indexed source set + chunk count, because a
// 1 MiB run of one token retrieves for no realistic query whether it was
// indexed or not.
func TestIndexRepoSkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "small.go", "package main\n\nfunc Small() int { return 1 }\n")
	// Write a file just over the 1 MiB cap.
	big := make([]byte, maxFileBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	big[0] = 'p'
	big[1] = 'a'
	big[2] = 'c'
	big[3] = 'k' // plausible-ish header
	if err := os.WriteFile(filepath.Join(dir, "big.go"), big, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
	n, err := IndexRepo(context.Background(), store, dir)
	if err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}
	got := indexedSources(store)
	if got["big.go"] {
		t.Errorf("oversized big.go was indexed; want skipped (size cap). indexed set: %v", got)
	}
	if !got["small.go"] {
		t.Errorf("small.go was not indexed (indexed set: %v) — the cap over-skipped", got)
	}
	if n != 1 || store.Size() != 1 {
		t.Errorf("IndexRepo = %d chunks, Size() = %d, want 1 each (only small.go's func)", n, store.Size())
	}
}

// TestIndexRepoNilStoreOrEmptyRootIsNoop: defensive — nil store or empty root
// returns 0, nil (no panic).
func TestIndexRepoNilStoreOrEmptyRootIsNoop(t *testing.T) {
	if n, err := IndexRepo(context.Background(), nil, t.TempDir()); err != nil || n != 0 {
		t.Errorf("IndexRepo(nil store) = (%d, %v), want (0, nil)", n, err)
	}
	store := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
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

	a := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
	b := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
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

// synthGoCorpus builds one synthetic Go source file with nFuncs documented
// functions, each carrying a token unique to it ("marker<i>") so a retrieval
// can be checked against the one chunk that should own it. Used by the
// linearity test + the IndexRepo benchmarks.
func synthGoCorpus(nFuncs int) string {
	var b strings.Builder
	b.WriteString("// Package synth is a generated corpus for the index benchmarks.\npackage synth\n\n")
	for i := 0; i < nFuncs; i++ {
		fmt.Fprintf(&b,
			"// Fn%d handles the marker%d case of the synthetic corpus.\nfunc Fn%d(a int) int {\n\tmarker%d := a + %d\n\treturn marker%d * 2\n}\n\n",
			i, i, i, i, i, i)
	}
	return b.String()
}

// TestChunkTextStaysLinearInSourceSize: the total bytes of chunk text a file
// produces must stay proportional to the file's own size. The old
// precedingComment scan attached the FIRST comment group preceding a function
// (the package comment) instead of the function's own doc, so chunk i spanned
// the whole file up to function i — total chunk text grew O(n^2) and the
// embedder paid for every byte. Doubling the corpus must roughly double (not
// quadruple) the chunk bytes.
func TestChunkTextStaysLinearInSourceSize(t *testing.T) {
	measure := func(nFuncs int) (src, chunkBytes int) {
		s := synthGoCorpus(nFuncs)
		for _, c := range ChunkFile("synth.go", []byte(s)) {
			chunkBytes += len(c.Text)
		}
		return len(s), chunkBytes
	}
	srcSmall, chunkSmall := measure(100)
	srcBig, chunkBig := measure(400)

	// Each chunk is its own doc + body, so the sum is within a small constant
	// factor of the source (never a multiple of the function count).
	if chunkSmall > 2*srcSmall {
		t.Errorf("100 funcs: chunk bytes %d > 2x source %d — chunks carry the whole file prefix", chunkSmall, srcSmall)
	}
	if chunkBig > 2*srcBig {
		t.Errorf("400 funcs: chunk bytes %d > 2x source %d — chunks carry the whole file prefix", chunkBig, srcBig)
	}
	// Growth check: 4x the source must cost ~4x the chunk bytes, not ~16x.
	grewBy := float64(chunkBig) / float64(chunkSmall)
	if grewBy > 8 {
		t.Errorf("chunk bytes grew %.1fx for a 4x corpus (want ~4x) — the build is superlinear", grewBy)
	}
}

// TestChunkedFunctionsOwnOnlyTheirOwnText (correctness, the other half of the
// same defect): a chunk must carry its function plus its own doc comment, not
// the text of every function above it. Equivalent search results depend on it —
// a chunk that swallows the file prefix matches every query the file matches.
func TestChunkedFunctionsOwnOnlyTheirOwnText(t *testing.T) {
	src := synthGoCorpus(5)
	chunks := ChunkFile("synth.go", []byte(src))
	if len(chunks) != 5 {
		t.Fatalf("ChunkFile = %d chunks, want 5", len(chunks))
	}
	for i, c := range chunks {
		if !contains(c.Text, "marker"+strconv.Itoa(i)) {
			t.Errorf("chunk %d (%s) is missing its own marker%d: %q", i, c.Name, i, c.Text)
		}
		if i > 0 && contains(c.Text, "marker"+strconv.Itoa(i-1)) {
			t.Errorf("chunk %d (%s) swallowed the previous function's text (marker%d)", i, c.Name, i-1)
		}
	}
}

// TestRetrieveRanksTheOwningChunkFirst: the search-result equivalence guard for
// the linearity fix. A query for a token that lives in exactly one function must
// rank that function's chunk first. This is the behaviour the index exists for,
// and it must survive the chunking change.
func TestRetrieveRanksTheOwningChunkFirst(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "synth.go", synthGoCorpus(40))

	store := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
	if _, err := IndexRepo(context.Background(), store, dir); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}
	for _, want := range []int{0, 17, 39} {
		q := "marker" + strconv.Itoa(want)
		parts := store.Retrieve(context.Background(), q, 3)
		if len(parts) == 0 {
			t.Fatalf("Retrieve(%q) = 0 hits", q)
		}
		if got := parts[0].Attr["name"]; got != "Fn"+strconv.Itoa(want) {
			t.Errorf("Retrieve(%q) top hit = %q, want Fn%d", q, got, want)
		}
	}
}

// BenchmarkIndexRepo measures the cold-start index build over a synthetic Go
// corpus at two sizes (200 and 800 functions in one file). A linear build's
// ns/op should scale ~4x between them; the quadratic chunker scaled ~16x.
func BenchmarkIndexRepo(b *testing.B) {
	for _, nFuncs := range []int{200, 800} {
		b.Run("funcs="+strconv.Itoa(nFuncs), func(b *testing.B) {
			dir := b.TempDir()
			src := synthGoCorpus(nFuncs)
			if err := os.WriteFile(filepath.Join(dir, "synth.go"), []byte(src), 0o644); err != nil {
				b.Fatalf("WriteFile: %v", err)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				store := NewLexicalStoreWithFS(NewHashEmbedder(256), nil)
				if _, err := IndexRepo(context.Background(), store, dir); err != nil {
					b.Fatalf("IndexRepo: %v", err)
				}
			}
		})
	}
}
