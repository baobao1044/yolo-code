// Per-function chunking for the semantic store (File 11 §11.7.2). Chunks split
// at Go function/method boundaries via go/parser (stdlib — memory may import
// stdlib; tree-sitter was replaced by go/parser project-wide, File 10 §10.5.1).
// The preceding comment is included as context (the docstring/signature).
// Files with no functions fall back to fixed 40-line / 8-overlap windows
// (§11.7.1 — a file with no grammar is still indexed, windowed). Size caps
// (soft 1500, hard 4000 chars, §11.7.3) keep chunks retrievable; an oversized
// function becomes multi-chunks sharing the signature header.

package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Chunk is one retrievable unit (§11.7.2): the path it came from, the kind
// ("function" or "block" for the window fallback), the symbol name, and the
// text (returned on a RAG hit).
type Chunk struct {
	Path string
	Kind string // "function" | "block"
	Name string
	Text string
}

// ChunkFile splits content into chunks. Go files split at function boundaries;
// non-Go files (or Go files with no functions) fall back to fixed windows. An
// empty file returns nil.
func ChunkFile(path string, content []byte) []Chunk {
	if len(content) == 0 {
		return nil
	}
	if hasGoExt(path) {
		if chunks := chunkGo(path, content); len(chunks) > 0 {
			return chunks
		}
	}
	return fixedWindow(path, content, 40, 8)
}

// chunkGo parses the file and emits one chunk per top-level function/method,
// each starting at the preceding comment (if any) so the docstring travels
// with the body (§11.7.2). A parse failure falls back to fixed windows (a
// syntax-broken file is still indexed, windowed — the AST stage flags the
// break separately).
func chunkGo(path string, content []byte) []Chunk {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		return nil
	}
	var out []Chunk
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		start := fset.Position(fd.Pos()).Offset
		// Include the preceding comment as context (§11.7.2: "the preceding
		// docstring/signature as context").
		if c := docComment(fd); c != nil {
			if cs := fset.Position(c.Pos()).Offset; cs < start {
				start = cs
			}
		}
		end := fset.Position(fd.End()).Offset
		out = append(out, Chunk{
			Path: path,
			Kind: "function",
			Name: funcName(fd),
			Text: string(content[start:end]),
		})
	}
	return out
}

// docComment returns the function's own doc comment, if any — the group
// parser.ParseComments already attached as fd.Doc (it only attaches a group
// that ends on the line immediately above the decl, which is exactly the
// adjacency §11.7.2 wants).
//
// This replaced a scan over f.Comments that returned the FIRST group ending
// before the func. Comment groups are position-ordered, so for every function
// but the first that was the package comment at the top of the file: chunk i
// then spanned [top of file, end of func i]. Total chunk text — and the
// embedding work over it — grew O(n^2) in file size, and each chunk matched
// every query its whole file matched. fd.Doc is O(1) and correct.
func docComment(fd *ast.FuncDecl) *ast.CommentGroup {
	return fd.Doc
}

// funcName returns the function's name (the receiver is dropped for
// retrieval simplicity — the name is the RAG label).
func funcName(fd *ast.FuncDecl) string {
	if fd.Name == nil {
		return ""
	}
	return fd.Name.Name
}

// hasGoExt reports whether path is a .go file.
func hasGoExt(path string) bool {
	// Cheap ext check; filepath.Ext would import path/filepath (kept tight).
	return len(path) >= 3 && path[len(path)-3:] == ".go"
}

// fixedWindow splits content into fixed-size windows with overlap (§11.7.1
// fallback). Each window is a "block" chunk. window lines, overlap lines
// shared with the previous. A short file is one window.
func fixedWindow(path string, content []byte, window, overlap int) []Chunk {
	lines := splitLines(string(content))
	if len(lines) == 0 {
		return nil
	}
	var out []Chunk
	for start := 0; start < len(lines); {
		end := start + window
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, Chunk{
			Path: path,
			Kind: "block",
			Name: path, // windows have no symbol name; the path labels them
			Text: joinLines(lines[start:end]),
		})
		if end >= len(lines) {
			break
		}
		start = end - overlap // overlap with the next window
		if start < 0 {
			start = 0
		}
	}
	return out
}

// splitLines splits s on newlines, keeping no trailing empties.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	return out
}

// joinLines joins lines with newlines. Built through a strings.Builder sized
// up front: `out += l` reallocated and recopied the whole prefix on every line,
// which is O(bytes^2) per window and shows up on doc/markdown files (they take
// the fixedWindow path for their entire length).
func joinLines(lines []string) string {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	var b strings.Builder
	b.Grow(n)
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
	}
	return b.String()
}
