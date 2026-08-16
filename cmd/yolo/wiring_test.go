// What the composition root wires, pinned.
//
// Every package in this tree has its own tests and they all pass. The bugs
// were never in the packages — they were in this directory, where three
// separate call sites each assemble a runtime.Deps by hand and any one of them
// can leave a port nil. A nil port is not an error: runtime.New substitutes a
// noop for Scope and Workflow (core.go:137-144) and skips the call entirely for
// Memory and Cost. So a run with the scope controller unwired looks exactly
// like a run where the controller had nothing to say, and no package-level test
// can tell the difference, because no package-level test can see this file.
//
// That is how the interactive path ended up as the only one of the three whose
// drive loop never consulted the scope controller or the workflow engine: the
// adapters existed, headless and coord both wired them, and runTUI simply did
// not — silently, with a passing test suite.
//
// This test reads the production source with go/ast rather than calling the
// builders, for two reasons. runTUI's Deps is an argument to runtime.New inside
// a function that also opens a terminal, so there is nothing to call; and a
// source-level assertion cannot be satisfied by a test-only shim. What it pins
// is the thing that actually broke: which field names appear at each site.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// alwaysWired are the ports whose absence is silently absorbed by runtime.New
// and therefore cannot be caught downstream. Each one degrades to a stub that
// is indistinguishable from the real thing having nothing to report:
//
//	Scope    → noopScopeController (core.go:137) — CanUseTool always true, so
//	           the tool gate at core.go:442 never narrows anything.
//	Workflow → noopWorkflowEngine (core.go:141) — Next is never consulted, so
//	           the task runs the legacy fixed phase order.
//	Cost     → the runtime's noop ledger, so YOLO_MAX_COST / YOLO_MAX_TIME stop
//	           nothing and the drive loop is uncapped.
//
// Bus and Session are here too, but only for completeness: those two fail loudly.
var alwaysWired = []string{"Bus", "Session", "Context", "Prompt", "Cognitive",
	"Exec", "Verify", "Patch", "Restore", "Scope", "Workflow", "Cost"}

// knownUnwired records the ports a site deliberately leaves nil, with the
// reason. This is not a suppression list — the test fails just as loudly when a
// site listed here *starts* wiring the port, because that means the reason
// below has gone stale and nobody updated it.
var knownUnwired = map[string]map[string]string{
	"buildRuntimeDeps": {
		"Memory": "the coord bus carries the real task.completed and the Store " +
			"listens to it directly, so routing the same learning through the " +
			"MemoryStore port would fabricate a second lifecycle event rather " +
			"than learn anything. Compare runHeadlessDeps, which wires Memory on " +
			"its injected branch only, for the same reason.",
	},
	"runTUIFrontend": {
		"Memory": "same reason — deps.memory is opened, listens on the bus, and " +
			"is Closed in runTUIFrontend's defer; it just is not reached through " +
			"the runtime's MemoryStore port.",
	},
}

// depsSite is one production assembly of a runtime.Deps.
type depsSite struct {
	fn     string // enclosing function name
	file   string
	line   int
	fields map[string]bool
}

// TestEveryCompositionRootWiresTheSamePorts is the headline. Three call sites
// build a runtime.Deps for a real run; all three must reach the drive loop with
// the same capabilities, because "which entrypoint did you use" is not a
// sensible reason for the scope controller to be off.
func TestEveryCompositionRootWiresTheSamePorts(t *testing.T) {
	sites := productionDepsSites(t)
	if len(sites) < 3 {
		t.Fatalf("found %d runtime.Deps assemblies in package main, want at least 3 "+
			"(runHeadlessDeps, buildRuntimeDeps, runTUIFrontend). Either a real entrypoint was "+
			"removed or this test's parser stopped recognising one — check before "+
			"relaxing the bound, an unparsed site is an unpinned site.\nfound: %s",
			len(sites), siteNames(sites))
	}

	for _, s := range sites {
		for _, port := range alwaysWired {
			if s.fields[port] {
				continue
			}
			if why, ok := knownUnwired[s.fn][port]; ok {
				t.Logf("%s: %s deliberately nil — %s", s.fn, port, why)
				continue
			}
			t.Errorf("%s (%s:%d) does not wire runtime.Deps.%s.\n"+
				"runtime.New substitutes a silent stub for it, so this run is degraded "+
				"in a way no downstream test can observe. Wire it, or add it to "+
				"knownUnwired with the reason it is safe here.",
				s.fn, s.file, s.line, port)
		}
	}
}

// TestKnownUnwiredPortsAreStillUnwired is the other direction. knownUnwired
// carries a written justification for each nil port; if the port gets wired the
// justification is now wrong and the next reader will trust it.
func TestKnownUnwiredPortsAreStillUnwired(t *testing.T) {
	sites := map[string]depsSite{}
	for _, s := range productionDepsSites(t) {
		sites[s.fn] = s
	}
	for fn, ports := range knownUnwired {
		s, ok := sites[fn]
		if !ok {
			t.Errorf("knownUnwired names %q, which no longer assembles a runtime.Deps. "+
				"Drop the entry.", fn)
			continue
		}
		for port := range ports {
			if s.fields[port] {
				t.Errorf("%s now wires %s, but knownUnwired still explains why it does not. "+
					"Delete the entry — a stale reason is worse than none.", fn, port)
			}
		}
	}
}

// TestNoPortIsWiredAtOnlyOneSite catches the shape of the bug this file exists
// for, without needing alwaysWired to have been updated first. A port that two
// sites wire and a third does not is either a divergence or an undocumented
// decision; both want a human.
func TestNoPortIsWiredAtOnlyOneSite(t *testing.T) {
	sites := productionDepsSites(t)
	count := map[string]int{}
	for _, s := range sites {
		for f := range s.fields {
			count[f]++
		}
	}
	for _, s := range sites {
		for f, c := range count {
			// f is wired everywhere except exactly one site, and s is missing
			// it — so s is that one.
			if s.fields[f] || c != len(sites)-1 {
				continue
			}
			if _, documented := knownUnwired[s.fn][f]; documented {
				continue
			}
			t.Errorf("every composition root wires runtime.Deps.%s except %s (%s:%d).\n"+
				"An odd one out is how the scope controller ended up disabled in the "+
				"interactive path. Wire it, or record why it differs in knownUnwired.",
				f, s.fn, s.file, s.line)
		}
	}
}

// productionDepsSites parses the non-test sources of package main and returns
// every function that assembles a non-empty runtime.Deps, together with the
// field names it sets. Fields count whether they are set in the composite
// literal or assigned afterwards onto the same variable — headless.go does
// both, and treating the second form as "not wired" would report a false
// divergence.
func productionDepsSites(t *testing.T) []depsSite {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var sites []depsSite

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if s, found := depsSiteIn(fset, fn, name); found {
				sites = append(sites, s)
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].fn < sites[j].fn })
	return sites
}

// depsSiteIn scans one function for a runtime.Deps assembly. Empty literals
// (`return runtime.Deps{}, err`) are error returns, not assemblies, and are
// skipped — counting them would report every port as missing at three phantom
// sites.
func depsSiteIn(fset *token.FileSet, fn *ast.FuncDecl, file string) (depsSite, bool) {
	site := depsSite{fn: fn.Name.Name, file: file, fields: map[string]bool{}}
	var varNames []string
	found := false

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isRuntimeDeps(lit.Type) || len(lit.Elts) == 0 {
			return true
		}
		found = true
		if site.line == 0 {
			site.line = fset.Position(lit.Pos()).Line
		}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok {
				site.fields[id.Name] = true
			}
		}
		return true
	})
	if !found {
		return depsSite{}, false
	}

	// Which local variables hold a runtime.Deps, so `d.Scope = …` counts.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lit, ok := as.Rhs[0].(*ast.CompositeLit)
		if !ok || !isRuntimeDeps(lit.Type) {
			return true
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			varNames = append(varNames, id.Name)
		}
		return true
	})
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			base, ok := sel.X.(*ast.Ident)
			if !ok {
				continue
			}
			for _, v := range varNames {
				if base.Name == v {
					site.fields[sel.Sel.Name] = true
				}
			}
		}
		return true
	})
	return site, true
}

func isRuntimeDeps(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Deps" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "runtime"
}

func siteNames(sites []depsSite) string {
	var n []string
	for _, s := range sites {
		n = append(n, s.fn)
	}
	return strings.Join(n, ", ")
}
