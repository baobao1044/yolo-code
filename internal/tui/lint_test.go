// Tests for TUI-008 — Import-allowlist lint (File 14 §14.1.1, §15.12 TUI-008).
// The TUI may import ONLY:
//   - internal/event (the bus seam)
//   - github.com/charmbracelet/{bubbletea,lipgloss,bubbles} (the renderer)
//   - the standard library (a path with no dot before the first slash, e.g.
//     "context", "strconv", "testing")
// Any other internal layer (cognitive, runtime, exec, infra, …) or any other
// third-party module is FORBIDDEN — the architectural seam that lets the same
// agent run headless or behind a future web UI without changing runtime code.
//
// The lint is a self-proving Go test (no CI tooling): it parses every .go file
// in this package with go/parser, collects the imports, and asserts each is on
// the allowlist. If a future change adds a forbidden import, this test fails
// the build. The RED proof is the mutation below (add a forbidden import to a
// scratch file → the test fails → remove the scratch).

package tui

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedImport reports whether an import path is on the TUI allowlist.
//   - stdlib: a path with no dot in its first segment (e.g. "context",
//     "strconv"). Module-rooted stdlib like "golang.org/x/sys" is NOT stdlib
//     under this rule — but it's a transitive of bubbletea, never a direct
//     import the TUI writes, so it would only appear if a file explicitly
//     imported it, which the allowlist rejects.
//   - the event seam: github.com/yolo-code/yolo/internal/event
//   - the renderer: github.com/charmbracelet/bubbletea, lipgloss, bubbles
func allowedImport(p string) bool {
	switch p {
	case "github.com/yolo-code/yolo/internal/event",
		"github.com/charmbracelet/bubbletea",
		"github.com/charmbracelet/lipgloss",
		"github.com/charmbracelet/bubbles",
		"github.com/charmbracelet/bubbles/textinput",
		"github.com/charmbracelet/bubbles/viewport":
		return true
	}
	// stdlib: first segment has no dot (e.g. "context", "go/parser").
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if !strings.Contains(first, ".") {
		return true
	}
	return false
}

// TestTUIImportsAreAllowlisted is the §14.1.1 lint gate: every .go file in
// internal/tui (prod + test) imports only the allowlisted paths. A forbidden
// import (e.g. internal/cognitive) fails the build. The allowlist is the
// architectural seam; violating it makes the TUI a second source of truth.
//
// RED proof: temporarily add a scratch file importing internal/cognitive,
// run this test → it fails → remove the scratch. The mutation IS the RED.
func TestTUIImportsAreAllowlisted(t *testing.T) {
	pkgDir := "." // the test runs in internal/tui
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	fset := token.NewFileSet()
	var bad []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(pkgDir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !allowedImport(p) {
				bad = append(bad, e.Name()+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("forbidden imports in internal/tui (§14.1.1 allowlist: only event + charmbracelet/{bubbletea,lipgloss,bubbles} + stdlib):\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// TestTUIImportsRejectForbiddenImport is the RED-proof mutation: the allowlist
// helper must REJECT a representative forbidden path (internal/cognitive) and a
// third-party path. This pins the negative case so a future allowlist tweak
// can't silently widen the seam. (The scratch-file mutation is the live RED;
// this unit assertion is the regression guard for the helper itself.)
func TestTUIImportsRejectForbiddenImport(t *testing.T) {
	for _, p := range []string{
		"github.com/yolo-code/yolo/internal/cognitive",
		"github.com/yolo-code/yolo/internal/runtime",
		"github.com/yolo-code/yolo/internal/infra",
		"example.com/some/third/party",
		"golang.org/x/sys", // a transitive of bubbletea, but not an allowed DIRECT import
	} {
		if allowedImport(p) {
			t.Errorf("allowedImport(%q) = true, want false (forbidden by §14.1.1)", p)
		}
	}
}
