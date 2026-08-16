// Seams that exist but are not connected to anything, pinned as dead.
//
// A dead seam is worse than a missing feature, because it reads as a present
// one. infra.Permissions is built by every run (infra.go:119), configured
// deliberately by the composition root — headless.go sets Permissions.Root with
// the comment "absolute; empty would deny every real write" — and its Check
// method is called from exactly one place in the module: its own test. The gate
// is armed, aimed, and consulted by nobody. Reading infra.go tells you the
// opposite.
//
// This file records the seams that are dead today, each with the evidence and
// the reason, and fails in BOTH directions:
//
//   - a seam listed here that is still dead: pass, with the reason logged.
//   - a seam listed here that has GAINED a consumer: fail. The entry's reason is
//     now false, and the next person to read it will trust it. Delete the entry.
//   - a seam listed here whose declaring code is gone: fail. Delete the entry.
//
// It is the twin of wiring_test.go. That file asks "do the three composition
// roots agree"; this one asks "is anything on the other end of the wire". The
// TUI bug wiring_test.go found was a port wired at two sites and not the third.
// These are ports wired at zero sites, which no parity check can see, because
// zero is consistent.
//
// RED proof for the mechanism (re-run it if you change the matcher): add
// `_, _ = i.Perms.Check(infra.ActFileWrite, "/tmp/x")` to any non-test file in a
// package that imports internal/infra. TestDeadSeamsAreStillDead fails naming
// Permissions.Check. Remove the scratch line.
//
// PRECISION. No go/types: that would need x/tools, and this module has three
// direct dependencies and should keep having three. The two matchers therefore
// differ in how exact they can be.
//
// Struct fields are exact. The qualifier at a call site (econtext.Deps) is
// resolved through that file's own import list, which is where the alias is
// declared, so context.Deps and session.Deps are told apart even though both
// have a Git field and headless.go constructs both.
//
// Method calls are not exact: a call is counted when the selector name matches
// AND the file's package imports the declaring package. Two same-named methods
// on different types in one importing file would both count. That imprecision
// runs in the safe direction only — it can report a dead seam as alive, sending
// a human to look and find nothing; it can never report a live seam as dead.
// The import test is what keeps it rare rather than constant: cognitive/core.go
// :265 calls policy.Allow on its own policy type, and without the import test
// that call would make the infra rate limiter look consumed.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/baobao1044/yolo-code"

// deadSeam is one piece of machinery with nothing on the far end.
type deadSeam struct {
	name string // human label, e.g. "infra.Permissions.Check"

	// method + declaredIn identify an "armed gate nobody consults": a method
	// whose only callers are its own tests. A consumer is a call to .method(
	// in a non-test file whose package imports declaredIn.
	method     string
	declaredIn string // import path, e.g. "internal/infra"

	// structName + field identify an "unimplemented port": a field on a Deps
	// struct that no construction site ever sets, so the constructor's nil
	// branch substitutes a stub on every single run. A consumer is any
	// composite literal of that struct with field as a key.
	//
	// structName is the BARE name ("Deps"); the package is resolved through the
	// calling FILE's own import list, which is where an alias is declared and so
	// is exact. Two earlier versions were not, and both were caught by running
	// the RED proof rather than assuming it:
	//
	// Comparing against the literal string "context.Deps" matched nothing at
	// all — both production sites import internal/context as econtext and write
	// econtext.Deps{…} — so all three ports were reported dead because the test
	// could not see, not because nothing was there. That is the one wrong answer
	// this file must never give.
	//
	// Falling back to the bare name plus "the file imports internal/context"
	// then over-matched: headless.go:215 builds a session.Deps that also has a
	// Git field, in a file that imports both packages. Nothing short of
	// resolving the qualifier separates those two, and the import list resolves
	// it without pulling in go/types.
	structName string
	field      string

	why string
}

// deadSeams is the register. Every entry was verified by grep before being
// added; none was assumed. Keep the evidence in `why` — an entry whose reason
// is "probably unused" is not worth having.
var deadSeams = []deadSeam{{
	name: "infra.Permissions.Check", method: "Check", declaredIn: "internal/infra",
	why: "The permission gate. Built on every run (infra.go:119) and configured " +
		"on purpose by the composition root — headless.go sets Permissions.Root " +
		"with the comment 'absolute; empty would deny every real write' — but " +
		"Check is called only from permissions_test.go. Nothing in the module " +
		"asks it whether a write is allowed. The actual gate in force is " +
		"exec.Engine.NeedsApproval plus the sandbox resolver, neither of which " +
		"consults Permissions. Wiring it or deleting it is a spec decision: " +
		"there are two overlapping permission models and only one is live.",
}, {
	name: "infra.RateLimiter.Allow", method: "Allow", declaredIn: "internal/infra",
	why: "Per-key token buckets with the §13.9.2 key scheme, exercised by 13 " +
		"assertions in ratelimit_test.go and called by zero production lines. " +
		"No tool call and no LLM request is rate limited. Note the near miss: " +
		"cognitive/core.go:265 calls policy.Allow, a different Allow on a " +
		"different type in a package that does not import infra.",
}, {
	name: "infra.Metrics.Histogram", method: "Histogram", declaredIn: "internal/infra",
	why: "The stronger case of the three: Histogram has no callers at all, not " +
		"even a test. Metrics.Record writes into m.histograms on every event " +
		"(metrics.go:65) and the map is read by nothing, so every latency " +
		"distribution the process measures is accumulated in memory and " +
		"discarded at exit. Counters are read; histograms are not.",
}, {
	name: "context.Deps.Git", declaredIn: "internal/context", structName: "Deps", field: "Git",
	why: "GitDiff (ports.go:24, 'File 10') has no implementation in the module " +
		"other than noopGitDiff. Neither construction site — headless.go:485 " +
		"nor coord_runner.go:204 — sets it, so engine.go:86 substitutes the " +
		"noop on every run and no context package has ever contained a working-" +
		"tree diff. The doc still says 'Sprint 2 stub'.",
}, {
	name: "context.Deps.Graph", declaredIn: "internal/context", structName: "Deps", field: "Graph",
	why: "Graph (ports.go:30, 'tree-sitter, File 11') likewise has only the " +
		"noop. pkg.Graph is therefore always empty, which makes six lines of " +
		"prompt/pipeline.go dead code that looks live: :112 and :154 count its " +
		"tokens, :140 and :144 count its members before and after trimming, " +
		":143's sibling trims it, :363 appends it to the retrieved set. The " +
		"retrieval budget arithmetic has never run against a non-empty Graph.",
}, {
	name: "context.Deps.Diags", declaredIn: "internal/context", structName: "Deps", field: "Diags",
	why: "Diagnostics (ports.go:35, 'LSP/compile errors, File 09') has only the " +
		"noop, and neither site sets it. Same dead arithmetic in " +
		"prompt/pipeline.go as Graph. Worth separating from the other two: the " +
		"model never sees a compile error as context, only as a verify verdict " +
		"after the fact.",
}, {
	// The four entries below are one subsystem, not four coincidences. Read
	// them together: File 03's history/undo machinery has no production caller
	// at either end, so Task.History is empty for the whole life of every real
	// task. Everything downstream that reads it is therefore dead code that
	// looks live — see RecordEntry's entry for the chain.
	name: "session.Manager.RecordEntry", method: "RecordEntry", declaredIn: "internal/session",
	why: "The only general-purpose writer of Task.History, documented as " +
		"'wiring used by the runtime, File 04 §3.7' — and the runtime does not " +
		"call it. Nor does anything else: the seven call sites are all tests. " +
		"With Checkpoint (below) also uncalled, NOTHING appends to Task.History " +
		"in production, so it is empty for the entire life of every task. Two " +
		"consequences follow and neither is visible locally. First, " +
		"context/engine.go:272 gatherConversation projects that empty slice " +
		"into the Conversation part group, so the group is always empty — while " +
		"budget.go's PctConversation reserves 45% of the window for it and " +
		"allocate() subtracts that share from the User remainder whether or not " +
		"anything fills it (compare System/Project, whose unused ceiling is " +
		"explicitly handed back). Second, session/cancel.go:106 walks the same " +
		"slice to build the Partial payload, so a cancelled task always reports " +
		"having done nothing. Note what is NOT broken by this: the model still " +
		"sees the conversation, because cognitive.Core keeps its own c.history " +
		"and merges each compiled prompt into it — which is the real point, " +
		"since that history is bounded by a message COUNT (maxHistoryMessages = " +
		"200, core.go:28) and never by tokens, so the accounted budget governs " +
		"a group that is always empty while the unaccounted one is what grows.",
}, {
	name: "session.Manager.Checkpoint", method: "Checkpoint", declaredIn: "internal/session",
	why: "The other writer of Task.History, and the one that looks wired. " +
		"patch/engine.go:134 does call Checkpoint — on patch.Checkpointer, its " +
		"own interface, and internal/patch does not import internal/session at " +
		"all. cmd/yolo satisfies that port with newShadowCheckpointer, so the " +
		"snapshots are real and the session manager never hears about them. " +
		"That is why Manager.Checkpoint has zero callers outside its tests " +
		"while checkpointing demonstrably works.",
}, {
	name: "session.Manager.Undo", method: "Undo", declaredIn: "internal/session",
	why: "The user-facing undo (File 03 §3.3.2). No caller anywhere outside " +
		"history_test.go — no slash command, no TUI key, no coord route. It " +
		"would have nothing to pop if there were one, since nothing writes the " +
		"history it reads. task.undone is a topic the bus can carry and no " +
		"component ever publishes.",
}, {
	name: "session.Manager.History", method: "History", declaredIn: "internal/session",
	why: "The read side, documented as the 'read-only view for the undo menu, " +
		"File 14'. There is no undo menu: the only call is a concurrency test. " +
		"Listed separately from Undo because they would be wired by different " +
		"people — History is what a UI needs, Undo is what an action needs — " +
		"and either could be animated without the other. Manager.Restore is the " +
		"fifth member of this set and is deliberately NOT listed: " +
		"runtime/core.go:547 calls c.restore.Restore, a different Restore on the " +
		"runtime's own Restorer port, in a file that does import internal/" +
		"session. The method matcher cannot tell those apart (see the header), " +
		"so listing it would produce a false 'this seam is alive'. Verified by " +
		"hand instead: session.Manager.Restore has no non-test caller either.",
}}

// TestDeadSeamsAreStillDead is the whole point. Each entry claims nothing
// consumes the seam; if that stopped being true, the written reason next to it
// became a lie.
func TestDeadSeamsAreStillDead(t *testing.T) {
	files := moduleGoFiles(t)
	for _, s := range deadSeams {
		consumers := s.consumersIn(files)
		if len(consumers) == 0 {
			t.Logf("%s: still dead — %s", s.name, s.why)
			continue
		}
		t.Errorf("%s now has %d consumer(s): %s\n"+
			"The seam is alive, so its entry in deadSeams is now false. Delete the "+
			"entry — a stale explanation is worse than none. If these are not real "+
			"consumers, the selector matcher over-counted (see the file header); "+
			"narrow the entry rather than deleting the check.",
			s.name, len(consumers), strings.Join(consumers, ", "))
	}
}

// TestDeadSeamRegisterIsNotStale catches the other rot: an entry describing
// code that no longer exists. A register full of ghosts stops being read.
func TestDeadSeamRegisterIsNotStale(t *testing.T) {
	files := moduleGoFiles(t)
	for _, s := range deadSeams {
		if s.method != "" {
			if !declaresMethod(files, s.declaredIn, s.method) {
				t.Errorf("deadSeams lists %s, but no method %q is declared in %s. "+
					"Drop the entry.", s.name, s.method, s.declaredIn)
			}
			continue
		}
		if !declaresDepsField(files, s.declaredIn, s.structName, s.field) {
			t.Errorf("deadSeams lists %s, but %s has no field %q in %s. Drop the entry.",
				s.name, s.structName, s.field, s.declaredIn)
		}
	}
}

// consumersIn returns "file:line" for every place that uses the seam.
func (s deadSeam) consumersIn(files []goFile) []string {
	var out []string
	for _, f := range files {
		if s.method != "" {
			if !f.imports[modulePath+"/"+s.declaredIn] {
				continue
			}
			ast.Inspect(f.ast, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == s.method {
					out = append(out, f.pos(call.Pos()))
				}
				return true
			})
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !f.resolves(lit.Type, s.declaredIn, s.structName) {
				return true
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == s.field {
					out = append(out, f.pos(kv.Pos()))
				}
			}
			return true
		})
	}
	return out
}

// declaresMethod reports whether pkgDir declares a method with this name. The
// declaring package is found by directory, not import path resolution, because
// the two coincide in a single-module tree.
func declaresMethod(files []goFile, pkgDir, method string) bool {
	for _, f := range files {
		if f.pkg != pkgDir {
			continue
		}
		for _, d := range f.ast.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Recv != nil && fn.Name.Name == method {
				return true
			}
		}
	}
	return false
}

// declaresDepsField reports whether the struct still has the field. The search
// is scoped to the declaring package's directory, so a same-named Deps struct
// in another layer cannot vouch for an entry that has gone stale.
func declaresDepsField(files []goFile, pkgDir, structName, field string) bool {
	found := false
	for _, f := range files {
		if f.pkg != pkgDir {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != structName {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fl := range st.Fields.List {
				for _, nm := range fl.Names {
					if nm.Name == field {
						found = true
					}
				}
			}
			return true
		})
	}
	return found
}

// goFile is one parsed non-test source file, with the two things the matchers
// need: its imports (to disambiguate same-named methods across packages) and a
// position formatter.
type goFile struct {
	rel  string // module-relative path, slash-separated
	pkg  string // module-relative directory of the declaring package
	fset *token.FileSet
	ast  *ast.File

	imports map[string]bool   // import path → present
	alias   map[string]string // qualifier as written in this file → import path
}

// resolves reports whether a type expression names structName from pkgDir, as
// seen from this file. Handles both the qualified form (econtext.Deps, resolved
// through this file's own import list) and the unqualified one, which is only
// that type when the file itself lives in the declaring package.
func (f goFile) resolves(e ast.Expr, pkgDir, structName string) bool {
	want := modulePath + "/" + pkgDir
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == structName && f.pkg == pkgDir
	case *ast.SelectorExpr:
		if t.Sel.Name != structName {
			return false
		}
		q, ok := t.X.(*ast.Ident)
		return ok && f.alias[q.Name] == want
	}
	return false
}

func (f goFile) pos(p token.Pos) string {
	return f.rel + ":" + itoa(f.fset.Position(p).Line)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// moduleGoFiles parses every non-test .go file in the module. The walk starts
// two levels up from cmd/yolo; a dead-seam claim is about the whole module, and
// checking one package could only ever prove the seam is dead in that package.
func moduleGoFiles(t *testing.T) []goFile {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s has no go.mod: %v — this test walks the whole "+
			"module and cannot make a module-wide claim from one package", root, err)
	}

	var out []goFile
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		imports := map[string]bool{}
		alias := map[string]string{}
		for _, im := range f.Imports {
			ip := strings.Trim(im.Path.Value, `"`)
			imports[ip] = true
			// The qualifier this file will use: the explicit alias when there is
			// one, otherwise the last path segment. That is an approximation only
			// for a package whose name differs from its directory; this module has
			// none, and a mismatch would over-count, not under-count.
			q := ip
			if i := strings.LastIndexByte(q, '/'); i >= 0 {
				q = q[i+1:]
			}
			if im.Name != nil {
				q = im.Name.Name
			}
			alias[q] = ip
		}
		rel = filepath.ToSlash(rel)
		out = append(out, goFile{
			rel: rel, pkg: filepath.ToSlash(filepath.Dir(rel)),
			fset: fset, ast: f, imports: imports, alias: alias,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(out) < 100 {
		t.Fatalf("parsed only %d non-test files under %s; this tree has ~280. "+
			"A short walk makes every seam look dead, which is the one wrong answer "+
			"this test must never give.", len(out), root)
	}
	return out
}
