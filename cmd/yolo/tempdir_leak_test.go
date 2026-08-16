// Temp-directory lifecycle at the composition root. Both runners here used to
// call os.MkdirTemp and never remove the result — 1703 yolo-shadow-* and 681
// yolo-headless dirs on one dev box — so each test asserts the directory count
// directly rather than the behaviour that happens to depend on it.
//
// The headless assertion lives in this file rather than headless_test.go
// because that file was being changed by another workstream in the same pass.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// tmpRootFor redirects the process temp dir at this test and returns the root
// to count under. os.MkdirTemp("", …) re-reads it on every call, so the count
// is scoped to this test: a peer test's temp dirs can neither fail it nor fake
// it green. The t.TempDir call is deliberately made before the redirect —
// t.TempDir allocates under the temp dir itself, and it must not land inside
// the directory being counted.
func tmpRootFor(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	scopeTempDir(t, root)
	return root
}

// scopeTempDir points os.TempDir at root for the duration of the test.
//
// TMPDIR alone is not enough. os.TempDir is per-platform: on Unix it reads
// $TMPDIR, but on Windows it calls GetTempPath, which consults TMP, then TEMP,
// then USERPROFILE — and never looks at TMPDIR. Setting only TMPDIR left the
// redirect a silent no-op on Windows, so os.MkdirTemp kept allocating under the
// runner's real temp dir and every count came back 0. The tests then failed for
// a reason that had nothing to do with the leak they were written to catch.
//
// Setting all three keeps one helper honest on both platforms. Note this is
// also why the count must be a Glob of a specific prefix rather than "how many
// entries appeared": the redirect is process-wide.
// The redirect is then verified rather than assumed. A silent no-op here does
// not fail this helper, it fails a caller three assertions later with "made 0
// dirs" — which reads as a leak-fix regression and is not one. Probing with the
// same os.MkdirTemp("", …) call the production code makes means the check tests
// the mechanism actually in use, not a proxy for it.
func scopeTempDir(t *testing.T, root string) {
	t.Helper()
	t.Setenv("TMPDIR", root) // Unix
	t.Setenv("TMP", root)    // Windows, first choice of GetTempPath
	t.Setenv("TEMP", root)   // Windows, second choice

	probe, err := os.MkdirTemp("", "yolo-scopeprobe-")
	if err != nil {
		t.Fatalf("scopeTempDir probe: %v", err)
	}
	defer func() { _ = os.RemoveAll(probe) }()
	if got, want := realpath(filepath.Dir(probe)), realpath(root); got != want {
		t.Fatalf("scopeTempDir: os.MkdirTemp landed in %q, want %q — the temp-dir "+
			"redirect did not take on this platform, so every count below would be 0 "+
			"for a reason unrelated to what the test checks", got, want)
	}
}

// realpath flattens symlinks and platform path spellings (macOS /var vs
// /private/var, Windows 8.3 short names) so two paths naming the same directory
// compare equal. A path that cannot be resolved is returned as given.
func realpath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// countTemp returns the directories matching pattern directly under root.
func countTemp(t *testing.T, root, pattern string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(root, pattern))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestShadowSnapCloseRemovesTheTree pins the delete half of shadowSnap.close.
// The tree is in-process rollback state — restore recomputes its path from the
// snap's own os.MkdirTemp, so no later process can ever reach it — and the
// composition root built one per headless run, per TUI session and per coord
// todo with nothing to remove it.
func TestShadowSnapCloseRemovesTheTree(t *testing.T) {
	repo := t.TempDir()
	root := tmpRootFor(t)

	snap, err := newShadowSnap(repo)
	if err != nil {
		t.Fatalf("newShadowSnap: %v", err)
	}
	if n := len(countTemp(t, root, "yolo-shadow-*")); n != 1 {
		t.Fatalf("newShadowSnap made %d dirs under the scoped TMPDIR, want 1", n)
	}

	// A checkpoint has to be readable back before the close: the patch engine
	// rolls back mid-run, so a close that fired earlier would be a use-after-
	// delete, not a leak fix.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := snap.checkpoint(ctx, "t_1", "pre", []string{"a.txt"}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snap.restore(ctx, "t_1", "pre"); err != nil {
		t.Fatalf("restore before close: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "before" {
		t.Errorf("restore returned %q, want %q — the tree is not usable up to close", b, "before")
	}

	if err := snap.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if leaked := countTemp(t, root, "yolo-shadow-*"); len(leaked) != 0 {
		t.Errorf("close left %d yolo-shadow-* dir(s) behind (%v); nothing else in the tree removes them, so the root leaks one per run", len(leaked), leaked)
	}
}

// TestDefaultHeadlessDepsHandsBackTheShadowSnap pins the ownership half: the
// composition root builds the tree, so it has to expose a handle to it. Without
// this the caller has no way to close what defaultHeadlessDeps allocated, and
// runTUI/runHeadlessDeps leak one directory per run by construction.
func TestDefaultHeadlessDepsHandsBackTheShadowSnap(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("YOLO_REPO_ROOT", repo)
	t.Setenv("YOLO_MEMORY_DIR", t.TempDir())
	t.Setenv("YOLO_STUB", "1")
	root := tmpRootFor(t)

	bus := event.New()

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		_ = bus.Close()
		t.Fatalf("defaultHeadlessDeps: %v", err)
	}
	// Order matters and is not LIFO-friendly: the memory Store's listener drains
	// until the bus closes, so its Close blocks forever if the bus is still open.
	_ = bus.Close()
	if deps.memory != nil {
		_ = deps.memory.Close()
	}

	if deps.snap == nil {
		t.Fatal("defaultHeadlessDeps returned no shadow snap handle; the caller cannot close the tree it just made")
	}
	if err := deps.snap.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if leaked := countTemp(t, root, "yolo-shadow-*"); len(leaked) != 0 {
		t.Errorf("closing the returned snap left %d yolo-shadow-* dir(s) (%v)", len(leaked), leaked)
	}
}

// TestHeadlessRunRemovesItsSessionStore pins the cleanup half of the headless
// runner. The store is deliberately a fresh temp dir — session ids have to
// restart at s_1 for the transcript to stay byte-identical (S5), so the durable
// root that fixed the memory and session stores is the wrong fix here — which
// makes removing it the only one left. Nothing removed it.
func TestHeadlessRunRemovesItsSessionStore(t *testing.T) {
	root := tmpRootFor(t)

	out, err := runHeadless(bytes.NewBufferString("say hi\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if out == "" {
		t.Fatal("empty transcript; the run has to finish for the cleanup assertion to mean anything")
	}
	if leaked := countTemp(t, root, "yolo-headless*"); len(leaked) != 0 {
		t.Errorf("runHeadless left %d yolo-headless dir(s) behind (%v); one per run, and the store is never read again", len(leaked), leaked)
	}
	// The default path also builds a shadow tree, and the run owns that too.
	if leaked := countTemp(t, root, "yolo-shadow-*"); len(leaked) != 0 {
		t.Errorf("runHeadless left %d yolo-shadow-* dir(s) behind (%v)", len(leaked), leaked)
	}
}
