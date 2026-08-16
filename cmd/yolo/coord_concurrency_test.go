// Is runtimeAgentRunner actually safe above Concurrency: 1?
//
// coord.Config.Concurrency defaults to 1 and every composition root passes 1.
// seam.go states the contract plainly: "ABOVE 1 they overlap, and the runner
// must be goroutine-safe: shared session managers, memory stores, and
// patch/shadow trees are the usual casualties." coord_runner.go answers two of
// those three in its own comments — the session Manager guards its maps, every
// memory sub-store holds its own mutex — and both claims check out.
//
// The third is not answered anywhere, and it is the one that fails. These
// tests take the question apart along the line that matters: what the Go race
// detector can see, and what it cannot.
//
// The detector sees memory. The shadow tree is a filesystem, so a green -race
// run says nothing about it — which is exactly why this hazard can sit behind
// a config field whose documentation asks only for "goroutine safety". Losing
// another agent's work is not a data race, and no amount of mutex auditing
// finds it.
//
// The answer, for the record: raising Concurrency above 1 today silently
// corrupts multi-todo plans that touch overlapping files. The bound is not
// merely unaudited, it is unsafe, and the reason is structural rather than a
// missing lock.

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	"github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
)

// TestConcurrentTodosDoNotRaceTheSharedRunner is the memory half. Four
// independent todos dispatch at once against one runner, so the sync.Once
// guards, the shared session Manager and the shared memory Store are all
// entered from four goroutines simultaneously.
//
// What it covers, precisely: runner startup and the concurrent portion of four
// agent drive loops. The turns do NOT run to completion — the stub provider
// keeps the FSM going until the context expires and all four todos then fail
// as cancelled, which is a fine outcome for this test and a misleading one to
// read as "the plan worked". The assertion here is the race detector, nothing
// else. Meaningful only under -race; harmless without it.
//
// It passes. That is a real result and a narrow one: the mutexes hold. Read it
// with the test below, which is the half that does not.
func TestConcurrentTodosDoNotRaceTheSharedRunner(t *testing.T) {
	t.Setenv("YOLO_SESSION_DIR", t.TempDir())
	t.Setenv("YOLO_MEMORY_DIR", t.TempDir())

	repo := t.TempDir()
	bus := event.New()
	defer func() { _ = bus.Close() }()

	runner := newRuntimeAgentRunner(repo, cognitive.NewStubProvider(128_000), bus)
	t.Cleanup(func() { _ = runner.close() })

	plan := coord.Plan{ID: "p-conc", Goal: "four independent todos"}
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		plan.Todos = append(plan.Todos, coord.Todo{
			ID:       id,
			Title:    "edit " + id + ".go",
			Assignee: string(coord.RoleCoder),
			Status:   coord.Pending,
			// No DependsOn: all four are dispatchable on the first pass, which
			// is what puts four turns in flight at once.
		})
	}

	o := coord.NewOrchestrator(
		coord.Config{MaxReworkCycles: 1, Concurrency: 4},
		cannedPlanner{plan: plan},
		bus, bus, runner,
	)

	// Long enough that four turns genuinely overlap, short enough not to dominate
	// the suite. The window is the point; the outcome is not.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := o.Run(ctx, plan.Goal); err != nil &&
		!strings.Contains(err.Error(), "context") &&
		!strings.Contains(err.Error(), "failed todos") {
		t.Fatalf("orchestrator Run: %v", err)
	}
}

// TestOneShadowTreeCannotRollBackTwoTodosIndependently is the filesystem half,
// and it is deterministic — no goroutines, no timing, just the interleaving
// that Concurrency > 1 makes reachable and Concurrency == 1 forbids.
//
// buildAdapters hands every todo and every role the same *shadowSnap, and
// coord_runner.go justifies that: "Shadow checkpoints are keyed by task ID
// inside the snap dir, so one snap shared across todos keeps rollback semantics
// intact." The keying is real — snapshots never collide. But restore does not
// write into the keyed subtree, it writes into the repository, and there is
// exactly one of those. Two todos touching one file is not exotic; it is the
// normal case for a plan that works on a package.
//
// This test PINS the broken behaviour rather than demanding the fix, in the
// same spirit as deadseam_test.go: the repair is a design decision (a working
// copy per inflight todo, or a scheduler that refuses to co-dispatch todos with
// intersecting file sets), and neither belongs in a drive-by. If this test ever
// fails, the hazard was addressed and the pin should be replaced with the real
// assertion — that B's work survives.
func TestOneShadowTreeCannotRollBackTwoTodosIndependently(t *testing.T) {
	repo := t.TempDir()
	shared := filepath.Join(repo, "shared.go")
	const v0 = "package p // original\n"
	if err := os.WriteFile(shared, []byte(v0), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := newShadowSnap(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.close() }()
	ctx := context.Background()

	// Todo A checkpoints before its patch, then writes.
	if _, err := snap.checkpoint(ctx, "t_A", "pre", []string{"shared.go"}); err != nil {
		t.Fatalf("checkpoint A: %v", err)
	}
	if err := os.WriteFile(shared, []byte("package p // A's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Todo B, running concurrently, checkpoints and writes. Its snapshot is
	// keyed separately and holds A's content, which is correct as far as it goes.
	if _, err := snap.checkpoint(ctx, "t_B", "pre", []string{"shared.go"}); err != nil {
		t.Fatalf("checkpoint B: %v", err)
	}
	const bWork = "package p // B's work\n"
	if err := os.WriteFile(shared, []byte(bWork), 0o644); err != nil {
		t.Fatal(err)
	}

	// A's verification fails, so the patch engine rolls A back. A is entitled to
	// undo A. Nothing here is entitled to undo B.
	if err := snap.restore(ctx, "t_A", "pre"); err != nil {
		t.Fatalf("restore A: %v", err)
	}

	got, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == bWork {
		t.Fatalf("shared.go survived as B's work — the hazard this test pins is " +
			"gone. Replace this pin with the real assertion (B's edit survives A's " +
			"rollback) and revisit the Concurrency guard below.")
	}
	if string(got) != v0 {
		t.Fatalf("shared.go = %q, want A's pre-image %q — the shadow tree is "+
			"behaving as neither the broken nor the fixed version", got, v0)
	}
	// Pinned: A's rollback silently reverted B's committed edit. B was told
	// nothing — its patch applied, its verification passed, its coord.code.ready
	// is on the bus, and the file on disk is A's pre-image.
}

// TestEveryCompositionRootPassesConcurrencyOne is the guard that makes the pin
// above load-bearing instead of decorative. The hazard is only reachable when
// something raises the bound, so this fails the moment a composition root does
// — and points at the test that explains why.
//
// It reads Concurrency in coord.Config literals across the non-test sources
// with go/ast rather than grep, because a comment mentioning the field should
// not trip it. go/ast is the stdlib parser: the three-direct-dependency budget
// this project keeps is the reason this is not go/types.
func TestEveryCompositionRootPassesConcurrencyOne(t *testing.T) {
	roots := []string{".", "../../internal/coord"}
	fset := token.NewFileSet()

	found := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Concurrency" {
					return true
				}
				found++
				lit, ok := kv.Value.(*ast.BasicLit)
				if !ok || lit.Value != "1" {
					pos := fset.Position(kv.Pos())
					t.Errorf("%s: Concurrency is set to %s, not 1.\n\n"+
						"Raising this bound is not safe today. Every inflight todo shares "+
						"one shadowSnap and therefore one working copy, so one todo's "+
						"rollback reverts another todo's committed edit with no error "+
						"anywhere — see TestOneShadowTreeCannotRollBackTwoTodosIndependently, "+
						"which pins that behaviour deterministically. The Go race detector "+
						"passes at Concurrency=4; it cannot see a filesystem.\n\n"+
						"Before raising it: give each inflight todo its own working copy and "+
						"shadow tree, or teach the scheduler to refuse co-dispatching todos "+
						"whose file sets intersect.",
						pos, exprText(kv.Value))
					return true
				}
				return true
			})
		}
	}
	if found == 0 {
		t.Fatal("no coord.Config Concurrency literal found in the non-test sources — " +
			"this guard has stopped guarding anything (renamed field, or the config " +
			"moved). Re-point it before deleting it.")
	}
}

// exprText renders a literal for the failure message without pulling in
// go/printer for the common case.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	default:
		return "a non-literal expression"
	}
}
