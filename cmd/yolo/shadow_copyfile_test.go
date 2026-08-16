// The shadow checkpointer's copy primitive, which is the rollback path.
//
// shadowSnap is the non-git checkpointer: before a patch writes, the listed
// files are copied into a temp tree; a failed verification copies them back.
// Both directions go through one function, copyFile, whose doc comment says it
// copies "using a temporary file and atomic rename". It does neither. It calls
// os.Create on the destination — which truncates it — and then streams into it.
//
// On the checkpoint direction that is merely a corruptible snapshot. On the
// restore direction the destination is a file in the user's repository, so the
// two properties below are not academic:
//
//   - os.Create takes the mode from the umask, not from the source, so a file
//     the restore has to RE-CREATE comes back 0644 however it started. Rolling
//     back a patch that deleted a shell script or a git hook hands it back
//     without its execute bit, and nothing reports it: the rollback is
//     announced as successful. (A file the patch only edited keeps its mode by
//     accident — os.Create truncates an existing file without touching its
//     permissions, and the first draft of this test only covered that case and
//     passed. The mode is lost exactly where the file mattered most.)
//   - If the copy fails after the truncate, the destination is left empty and
//     the error is returned. That inverts the whole point of a checkpoint — the
//     mechanism whose job is to give the file back destroys it instead, and it
//     does so precisely in the situation it exists for.
//
// The second test forces the failure through a source that opens but will not
// read (a directory: os.Open succeeds, Read returns EISDIR), because that is
// the one way to hit the window deterministically rather than by racing. What
// it pins is a property of copyFile, not of that particular source: after a
// failed copy the destination must be what it was before.

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRestorePreservesTheExecuteBit runs a real checkpoint → delete → restore
// cycle on an executable file. Deleting rather than editing is the case that
// matters and the case a careless test misses: the restore has to re-create the
// target, so it picks the mode instead of inheriting it.
func TestRestorePreservesTheExecuteBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	repo := t.TempDir()
	script := filepath.Join(repo, "hook.sh")
	const original = "#!/bin/sh\necho original\n"
	if err := os.WriteFile(script, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}

	snap, err := newShadowSnap(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.close() }()

	ctx := context.Background()
	if _, err := snap.checkpoint(ctx, "t_1", "before-patch", []string{"hook.sh"}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// The patch runs and is bad — it removed the hook. Rollback must put it
	// back as it was, which includes being runnable.
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}

	if err := snap.restore(ctx, "t_1", "before-patch"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("content not restored:\n got %q\nwant %q", got, original)
	}
	fi, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o755 {
		t.Errorf("restored mode = %04o, want 0755.\n"+
			"The rollback reported success and handed back a file the shell will "+
			"no longer run. copyFile takes the destination's mode from os.Create "+
			"(umask), never from the source, so every checkpoint→restore cycle "+
			"launders the execute bit off scripts, hooks and binaries.", perm)
	}
}

// TestFailedCopyLeavesTheDestinationIntact pins the invariant that makes a
// checkpoint worth having: a restore that cannot complete must not have
// destroyed what it was restoring.
func TestFailedCopyLeavesTheDestinationIntact(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "important.go")
	const existing = "package main // the only copy of this content\n"
	if err := os.WriteFile(dst, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	// A source that opens but will not read: os.Open on a directory succeeds on
	// unix and the first Read returns EISDIR, so io.Copy fails after the
	// destination has already been truncated.
	src := filepath.Join(dir, "subdir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err == nil {
		t.Skip("this platform's io.Copy from a directory succeeded; " +
			"the truncate window cannot be forced deterministically here")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("destination unreadable after a failed copy: %v", err)
	}
	if string(got) != existing {
		t.Errorf("failed copy left destination = %q, want it unchanged (%q).\n"+
			"copyFile truncates with os.Create before it has read a single byte, "+
			"so a copy that fails mid-stream leaves the destination empty. On the "+
			"restore path that destination is a file in the user's repo, and the "+
			"mechanism whose entire job is to give the file back has deleted its "+
			"contents instead.", got, existing)
	}
}
