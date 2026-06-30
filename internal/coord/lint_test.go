// Tests for L11-001 — Import-allowlist lint (File 12, roadmap §15.13 import
// matrix). internal/coord MAY import:
//   - internal/{event, cognitive, exec, verify, patch, memory, infra}
//   - the standard library (a path with no dot in its first segment, e.g.
//     "context", "time", "sync")
// Sprint 10 uses the STRICTEST form of the matrix — event + stdlib only —
// with every other layer behind a coord-local seam (seam.go). The broader
// matrix is exercised at the integration sprint. The allowlist here enforces
// the documented ceiling so a forbidden import (runtime, session, context,
// prompt, tui, or any third party) fails the build.
//
// The lint is a self-proving Go test (no CI tooling): it parses every .go file
// in this package with go/parser, collects the imports, and asserts each is on
// the allowlist. The RED proof is the mutation below (add a forbidden import
// to a scratch file → the test fails → remove the scratch).

package coord

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coordAllowlist is the roadmap §15.13 import matrix for internal/coord, as
// documented in internal/coord/doc.go:
//
//	"Allowed imports: event, cognitive, exec, verify, patch, memory, infra."
//
// Sprint 10 uses event + stdlib; the broader set is the ceiling.
var coordAllowlist = []string{
	"github.com/baobao1044/yolo-code/internal/event",
	"github.com/baobao1044/yolo-code/internal/cognitive",
	"github.com/baobao1044/yolo-code/internal/exec",
	"github.com/baobao1044/yolo-code/internal/verify",
	"github.com/baobao1044/yolo-code/internal/patch",
	"github.com/baobao1044/yolo-code/internal/memory",
	"github.com/baobao1044/yolo-code/internal/infra",
}

// coordAllowedImport reports whether an import path is on the coord allowlist
// or is stdlib (first segment has no dot, e.g. "context", "go/parser").
func coordAllowedImport(p string) bool {
	for _, a := range coordAllowlist {
		if p == a {
			return true
		}
	}
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if !strings.Contains(first, ".") {
		return true // stdlib
	}
	return false
}

// TestCoordImportsAreAllowlisted is the §15.13 lint gate: every .go file in
// internal/coord (prod + test) imports only an allowlisted path. A forbidden
// import (e.g. internal/runtime, internal/tui, or a third party) fails the
// build. The allowlist is the architectural seam; violating it couples the
// orchestrator to a layer it must reach through an interface.
//
// RED proof: temporarily add a scratch file importing internal/runtime, run
// this test → it fails → remove the scratch. The mutation IS the RED.
func TestCoordImportsAreAllowlisted(t *testing.T) {
	pkgDir := "." // the test runs in internal/coord
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
			if !coordAllowedImport(p) {
				bad = append(bad, e.Name()+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("forbidden imports in internal/coord (§15.13 allowlist: event, cognitive, exec, verify, patch, memory, infra + stdlib only):\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// TestCoordImportsRejectForbiddenImport is the RED-proof mutation: the
// allowlist helper must REJECT a representative forbidden path from each
// forbidden category (runtime, session, context, prompt, tui) and a
// third-party path. This pins the negative case so a future allowlist tweak
// can't silently widen the seam.
func TestCoordImportsRejectForbiddenImport(t *testing.T) {
	for _, p := range []string{
		"github.com/baobao1044/yolo-code/internal/runtime",
		"github.com/baobao1044/yolo-code/internal/session",
		"github.com/baobao1044/yolo-code/internal/context",
		"github.com/baobao1044/yolo-code/internal/prompt",
		"github.com/baobao1044/yolo-code/internal/tui",
		"example.com/some/third/party",
		"golang.org/x/sys",
	} {
		if coordAllowedImport(p) {
			t.Errorf("coordAllowedImport(%q) = true, want false (forbidden by §15.13)", p)
		}
	}
}
