// Cold-start repo indexing (File 11 §11.7.5). IndexRepo walks the repo root,
// skips vendored/generated/cache trees and oversized files, chunks each source
// file (per-function for Go, fixed-window otherwise), and bulk-inserts the
// chunks into the SemanticStore so the first turn already has RAG signal. The
// walk is deterministic (filepath.WalkDir visits entries in lexical order) so
// two runs over the same tree produce byte-identical indexes (S5 — the
// headless transcript stays reproducible).
//
// stdlib-only: memory may import only event + stdlib (File 15 §15.15.2); the
// walk uses os + io/fs + path/filepath, all stdlib. The sandbox-confined FS is
// NOT used here — the cold-start walk reads the repo directly (it only reads,
// never writes, and the files are the agent's own repo). The FS seam is for
// the listener's Reindex on patch.applied (§11.7.5), where the store reads the
// mutated path through the sandbox so path-confinement holds.

package memory

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// skipDirs are the directory names IndexRepo never descends into (§11.7.5):
// generated/vendored/cache trees add noise without signal and dominate the
// index. Matched by base name, so a nested vendor/ is skipped too.
var skipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	"__pycache__": true, "dist": true, ".cache": true, "build": true,
	".idea": true, ".vscode": true,
}

// maxFileBytes is the per-file size cap; larger files are skipped (a multi-MB
// generated file would dominate the index and the soft budget). 1 MiB is the
// same floor the Context Engine's soft budget uses.
const maxFileBytes = 1 << 20

// IndexRepo walks root and indexes every source file into the store (§11.7.5
// cold-start). It skips vendored/generated/cache dirs, dotfiles, and files over
// the size cap; chunks each file via ChunkFile; and bulk-inserts the chunks in
// one locked pass. Chunk paths are made relative to root so the prompt
// displays clean repo-relative paths. Returns the number of chunks indexed.
// Best-effort: a read error on one file is skipped (the walk continues); a
// nil store or empty root returns (0, nil).
func IndexRepo(ctx context.Context, store *SemanticStore, root string) (int, error) {
	if store == nil || root == "" {
		return 0, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	var all []Chunk
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries (permissions, etc.)
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !indexableFile(d) {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			rel = path // fall back to absolute if Rel fails (shouldn't happen)
		}
		for _, c := range ChunkFile(path, content) {
			c.Path = rel // display repo-relative
			all = append(all, c)
		}
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}
	store.BulkInsert(ctx, all)
	return len(all), nil
}

// indexableFile reports whether the entry should be indexed: a non-dotfile
// under the size cap with a text/source extension.
func indexableFile(d fs.DirEntry) bool {
	name := d.Name()
	if len(name) > 0 && name[0] == '.' {
		return false
	}
	info, err := d.Info()
	if err != nil {
		return false
	}
	if info.Size() > maxFileBytes {
		return false
	}
	return isTextExt(name)
}

// isTextExt reports whether name has a text/source extension worth indexing.
// The set is deliberately broad (common source + config + doc); binaries are
// skipped to keep the index signal-clean.
func isTextExt(name string) bool {
	switch filepath.Ext(name) {
	case ".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx",
		".java", ".c", ".h", ".cpp", ".cc", ".hpp", ".rs", ".rb", ".php",
		".swift", ".kt", ".kts", ".scala", ".sh", ".bash", ".zsh",
		".yml", ".yaml", ".json", ".toml", ".ini", ".cfg", ".conf",
		".md", ".txt", ".sql", ".html", ".htm", ".css", ".scss", ".less",
		".proto", ".zig", ".nim", ".lua", ".vim", ".el", ".clj", ".cljs",
		".edn", ".ex", ".exs", ".erl", ".hs", ".ml", ".mli", ".dart",
		".gradle", ".cs", ".ps1", ".psm1", ".dockerfile", ".makefile",
		".mod", ".sum", ".work":
		return true
	}
	// Extensionless Makefiles/Dockerfiles are text.
	base := filepath.Base(name)
	switch base {
	case "Makefile", "makefile", "Dockerfile", "dockerfile", "LICENSE",
		"AUTHORS", "NOTICE", "README", "CHANGELOG":
		return true
	}
	return false
}

// BulkInsert embeds and inserts many chunks in one locked pass (the cold-start
// path indexes a whole repo; per-chunk locking via addChunk would be O(n) lock
// acquisitions). Embedding happens outside the lock (I/O + CPU work; no shared
// state touched), then a single locked pass assigns ids and appends. A nil
// embedder falls back to the default hash embedder (dim 384).
func (s *SemanticStore) BulkInsert(ctx context.Context, chunks []Chunk) {
	if len(chunks) == 0 {
		return
	}
	if s.embed == nil {
		s.embed = NewHashEmbedder(384)
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vecs, _ := s.embed.Embed(ctx, texts)
	newVecs := make([]chunkVec, len(chunks))
	for i, c := range chunks {
		cv := chunkVec{path: c.Path, kind: c.Kind, name: c.Name, text: c.Text}
		if i < len(vecs) {
			cv.vector = vecs[i]
		}
		newVecs[i] = cv
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range newVecs {
		s.nextID++
		newVecs[i].id = s.nextID
		s.chunks = append(s.chunks, newVecs[i])
	}
}
