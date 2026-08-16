// The coord/TUI half of the shadow-tree lifecycle. tempdir_leak_test.go covers
// the headless composition root; these two cover the other owner of a
// newShadowSnap, the multi-agent runner, which had no teardown hook at all.
// Both reuse tmpRootFor/countTemp from that file rather than re-deriving the
// TMPDIR-scoping trick: os.MkdirTemp("", …) re-reads TMPDIR per call, so the
// count belongs to this test and a peer's temp dirs can neither fail it nor
// fake it green.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	"github.com/baobao1044/yolo-code/internal/event"
)

// TestOrchestratedGoalRemovesItsShadowTree is the behavioural end of the fix:
// one multi-agent goal in the TUI must leave nothing behind. runOrchestrator
// builds a fresh runtimeAgentRunner per goal and each runner allocates its own
// shadow tree, so before the close a session that ran three goals left three
// directories — on top of the one runTUI's own defaultHeadlessDeps leaks.
func TestOrchestratedGoalRemovesItsShadowTree(t *testing.T) {
	repo := t.TempDir()
	// Real artifacts so the reviewer has something to read and the plan actually
	// runs its roles; an empty repo would make the assertion below vacuous.
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("some real content here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Last, after every other t.TempDir: those allocate under TMPDIR too and must
	// not land inside the directory being counted.
	root := tmpRootFor(t)

	bus := event.New()
	defer bus.Close()
	seen, stop := collect(t, bus, event.Topic("coord.task.assign"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d := &tuiDriver{ctx: ctx, cancel: cancel, bus: bus, repo: repo}
	d.runOrchestrator("add a.txt, add b.txt, add c.txt")
	stop()

	if len(seen()) == 0 {
		t.Fatal("no todo was ever assigned; the plan did not run, so the leak assertion below proves nothing")
	}
	if leaked := countTemp(t, root, "yolo-shadow-*"); len(leaked) != 0 {
		t.Errorf("one orchestrated goal left %d yolo-shadow-* dir(s) behind (%v); the runner has no teardown hook of its own, so nothing else removes them", len(leaked), leaked)
	}
}

// TestPlanRunRemovesItsShadowTree is the production end of the same fix, and
// the only one of these three that a real user hits: runPlanCtx builds a
// runtimeAgentRunner for every `--plan <goal>` invocation and nothing tore it
// down, so the shipped binary left one directory behind per run. The other two
// leaks in this file are reachable only from an interactive session.
func TestPlanRunRemovesItsShadowTree(t *testing.T) {
	repo := t.TempDir()
	// Real artifacts so the reviewer has something to read and the roles
	// actually run; an empty repo would make the assertion vacuous.
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("some real content here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("YOLO_REPO_ROOT", repo)
	// Last, after every other t.TempDir: those allocate under TMPDIR too and must
	// not land inside the directory being counted.
	root := tmpRootFor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := runPlanCtx(ctx, "add a.txt, add b.txt, add c.txt")
	if err != nil {
		t.Fatalf("runPlanCtx: %v", err)
	}
	if !strings.Contains(out, `"type":"coord.task.assign"`) {
		t.Fatalf("no todo was ever assigned; the plan did not run, so the leak assertion below proves nothing:\n%s", out)
	}

	if leaked := countTemp(t, root, "yolo-shadow-*"); len(leaked) != 0 {
		t.Errorf("one --plan run left %d yolo-shadow-* dir(s) behind (%v); the runner has no teardown hook of its own, so nothing else removes them", len(leaked), leaked)
	}
}

// TestCoordRunnerSharesOneShadowTreeAndClosesIt pins both halves of the runner's
// ownership at once. Sharing is the part that has to stay: buildAdapters caches
// its snap, and that is only safe because one session.Manager backs every todo
// and numbers its tasks t_1, t_2, … — checkpoints are keyed task/name, so a
// second Manager handing out a second t_1 would let one todo's rollback restore
// another's file contents. Closing is the part that was missing.
func TestCoordRunnerSharesOneShadowTreeAndClosesIt(t *testing.T) {
	repo := t.TempDir()
	root := tmpRootFor(t)

	bus := event.New()
	defer func() { _ = bus.Close() }()

	ctx := context.Background()
	r := newRuntimeAgentRunner(repo, cognitive.NewStubProvider(128_000), bus)
	for _, id := range []string{"t1", "t2", "t3"} {
		if _, err := r.buildRuntimeDeps(ctx, event.TaskAssignEvent{
			PlanID: "p-1", TodoID: id, Brief: "implement " + id,
		}); err != nil {
			t.Fatalf("buildRuntimeDeps(%s): %v", id, err)
		}
	}
	if n := len(countTemp(t, root, "yolo-shadow-*")); n != 1 {
		t.Fatalf("three todos made %d shadow trees, want 1 shared one", n)
	}

	// A checkpoint has to survive up to the close — the patch engine rolls a
	// failed verification back mid-plan, so an earlier close would be a
	// use-after-delete rather than a leak fix.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.snap.checkpoint(ctx, "t_1", "pre", []string{"a.txt"}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.snap.restore(ctx, "t_1", "pre"); err != nil {
		t.Fatalf("restore before close: %v", err)
	}

	if err := r.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if leaked := countTemp(t, root, "yolo-shadow-*"); len(leaked) != 0 {
		t.Errorf("close left %d yolo-shadow-* dir(s) behind (%v)", len(leaked), leaked)
	}
}
