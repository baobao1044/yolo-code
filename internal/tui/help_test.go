// Anti-drift guard for the help overlay.
//
// The overlay used to be titled "key bindings" and listed only keys, so every
// slash command the TUI accepts was invisible: /theme, /model, /provider and
// /status had all shipped with no surface anywhere that names them. There is no
// completion, no man page and no `--help` for them — the overlay is the only
// place a user could find out they exist, and it did not mention them. A
// command nobody can discover is close to a command that does not exist, and
// nothing failed, because "the docs do not mention this" is not something a
// test of the command's behaviour can see.
//
// So the list is derived rather than written down twice. Asserting a hardcoded
// set here would just move the drift: the next command gets added to input.go,
// this test still passes against its own stale copy, and the overlay is wrong
// again. Instead the accepted commands are read out of handleSlashCommand's own
// switch, which is the definition of what the TUI accepts.

package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// acceptedCommands returns every command name handleSlashCommand switches on,
// read from the source. It reads input.go rather than exercising the function
// because there is no way to ask a Go switch what it would have matched.
func acceptedCommands(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "input.go", nil, 0)
	if err != nil {
		t.Fatalf("parse input.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if g, ok := d.(*ast.FuncDecl); ok && g.Name.Name == "handleSlashCommand" {
			fn = g
			break
		}
	}
	if fn == nil {
		t.Fatal("no handleSlashCommand in input.go — the derivation is broken, not the help")
	}

	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		// Only the switch on the parsed command word. Any other switch that
		// appears in this function later is not a routing table and its case
		// values are not command names.
		if !ok {
			return true
		}
		if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "cmd" {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List { // nil List is the default arm
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err == nil && v != "" {
					out = append(out, v)
				}
			}
		}
		return false
	})

	if len(out) == 0 {
		t.Fatal("parsed zero commands out of handleSlashCommand's switch — the derivation is " +
			"broken, not the help; a check that finds nothing to check passes for free")
	}
	return out
}

// TestHelpOverlayNamesEverySlashCommand is the guard. Every command the TUI
// accepts has to appear in the overlay, because the overlay is the only place
// it can be found.
func TestHelpOverlayNamesEverySlashCommand(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 100
	m.height = 200

	out := helpView(m)

	for _, cmd := range acceptedCommands(t) {
		if !strings.Contains(out, "/"+cmd) {
			t.Errorf("handleSlashCommand accepts /%s but the help overlay never names it — "+
				"there is no other surface that does, so the command is undiscoverable:\n%s",
				cmd, out)
		}
	}
}

// TestHelpOverlayDescribesPref is the content half. The listing above only
// checks that the string "/pref" occurs; it would be satisfied by a line that
// names the command and says nothing about it.
//
// Preferences need the extra sentence more than the other commands do, because
// what they do is invisible: nothing on screen changes when one is recorded,
// the effect is that a later prompt is built differently, and they persist
// across projects. A user who cannot tell whether /pref did anything will
// assume it did not.
func TestHelpOverlayDescribesPref(t *testing.T) {
	m := newModelForTest()
	m.ready = true
	m.width = 100
	m.height = 200

	out := helpView(m)

	for _, want := range []string{"preference", "cross-project"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help overlay never says %q, so /pref is listed without saying what "+
				"it affects or how long it lasts:\n%s", want, out)
		}
	}
}
