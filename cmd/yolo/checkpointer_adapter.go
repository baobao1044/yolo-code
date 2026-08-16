// Shadow-copy checkpointer and runtime.Restorer adapter (Sprint 12 INT-003).
// This is a non-git fallback: before a patch writes, the listed files are
// copied to a temp shadow tree; Restore copies them back. It satisfies both
// patch.Checkpointer and runtime.Restorer.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/baobao1044/yolo-code/internal/patch"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// shadowSnap is the shared snapshot storage used by both the patch engine
// (patch.Checkpointer) and the runtime (runtime.Restorer).
type shadowSnap struct {
	root string // repo root
	dir  string // shadow root directory
}

// newShadowSnap creates a shadow snapshot store under a temporary directory.
// The caller owns close.
func newShadowSnap(root string) (*shadowSnap, error) {
	dir, err := os.MkdirTemp("", "yolo-shadow-*")
	if err != nil {
		return nil, fmt.Errorf("shadow checkpointer: %w", err)
	}
	return &shadowSnap{root: root, dir: dir}, nil
}

// close removes the shadow tree. Nothing removed it before and the composition
// root builds one per headless run, per TUI session and per coord todo: 1703
// yolo-shadow-* dirs on one dev box, each holding a copy of the files some
// patch was about to overwrite.
//
// Deleting is safe because the snapshots are in-process rollback state with no
// reader outside the run that wrote them: restore recomputes its path from
// s.dir, which is a fresh os.MkdirTemp on every construction, and nothing
// rebuilds a shadowSnap around an existing directory — so no later process can
// reach a tree this one leaves behind. The SnapshotRef checkpoint hands back
// does travel out through runtime.PatchResult, but its only consumer is
// runtime.filesFromSnapshot, which discards it; the ref persisted in a session
// history record comes from session.InMemCheckpointer, not from here. A hard
// crash still leaves the tree on disk for forensics — a deferred close does not
// run on SIGKILL — so the copies survive exactly the case that could want them.
//
// Rooting the tree somewhere durable instead (the sessionStateDir fix applied
// to the session and memory stores) would be a bug here: checkpoints are keyed
// task/name and task ids restart at t_1 for every run, so on a shared root one
// run's rollback would restore another run's file contents.
//
// Call it once, after every Checkpointer and Restorer built from this snap is
// done — a patch that rolls back reads the tree back mid-run.
func (s *shadowSnap) close() error {
	if s == nil {
		return nil
	}
	return os.RemoveAll(s.dir)
}

// checkpoint copies the listed paths from the repo root into the shadow tree
// keyed by task+name. A missing source file for a listed path is recorded as a
// deletion sentinel so Restore can remove a newly-created file.
func (s *shadowSnap) checkpoint(ctx context.Context, task, name string, paths []string) (patch.SnapshotRef, error) {
	base := filepath.Join(s.dir, task, name)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	for _, p := range paths {
		src := filepath.Join(s.root, filepath.Clean(p))
		dst := filepath.Join(base, filepath.Clean(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			// Mark that the file did not exist at checkpoint time.
			if err := os.WriteFile(dst+".deleted", nil, 0o644); err != nil {
				return "", err
			}
			continue
		} else if err != nil {
			return "", err
		}
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
	}
	return patch.SnapshotRef(base), nil
}

// restore copies the shadow files back to the repo root. Deletion sentinels
// cause the corresponding target to be removed.
func (s *shadowSnap) restore(ctx context.Context, task, name string) error {
	base := filepath.Join(s.dir, task, name)
	if _, err := os.Stat(base); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("shadow restore: checkpoint %q not found", name)
	} else if err != nil {
		return err
	}
	return filepath.Walk(base, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, walkPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(s.root, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if filepath.Ext(rel) == ".deleted" {
			// Remove the sentinel extension to know which file to delete.
			original := target[:len(target)-len(".deleted")]
			return os.RemoveAll(original)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(walkPath, target)
	})
}

// copyFile copies src to dst through a temporary file in dst's own directory
// and an atomic rename, carrying the source's permission bits across.
//
// It said it did this and did not. It called os.Create(dst) — which truncates
// before a single byte has been read — and streamed into the live file. Two
// things followed, and both land on the restore direction, where dst is a file
// in the user's repository:
//
//   - A copy that failed after the truncate left dst empty and returned the
//     error. The mechanism whose whole purpose is to hand the file back deleted
//     its contents instead, in exactly the situation it exists for. Writing to a
//     temp file first means a failure leaves dst untouched: rename is the only
//     operation that touches it, and it either happens or it does not.
//   - os.Create picks the mode from the umask. Truncating an EXISTING file keeps
//     that file's permissions, so a patch that merely edited a script rolled
//     back fine — which is why this went unnoticed. A patch that DELETED it made
//     restore re-create the target, and it came back 0644 however it started.
//     Rolling back a removed hook or shell script handed it back unrunnable and
//     reported success. Chmod to the source's mode fixes both directions at
//     once: the shadow copy now carries the mode, so the restore has one to give
//     back.
//
// The temp file is created in filepath.Dir(dst) rather than the system temp dir
// because rename is only atomic within a filesystem, and a shadow tree under
// os.MkdirTemp is routinely on a different one from the repo.
//
// See shadow_copyfile_test.go, which pins both properties.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	// Read side: a Close error on a file we only read carries no information
	// the copy did not already report, so it is dropped explicitly.
	defer func() { _ = in.Close() }()

	fi, err := in.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Cleanup on every failure path below. After a successful rename the name is
	// gone and this is a no-op, which is why the error is dropped.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, in); err != nil {
		// Already failing; the deferred Remove is what cleans up, and this
		// Close only releases the descriptor. Its error would displace the
		// real one.
		_ = tmp.Close()
		return err
	}
	// Before the rename, so dst is never observable with the wrong mode.
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		// Already failing; the deferred Remove is what cleans up, and this
		// Close only releases the descriptor. Its error would displace the
		// real one.
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// KNOWN GAP, third instance of a hazard fixed in the other two: on Windows
	// a replacing rename fails outright if any handle holds dst open, because
	// Go's os.Open omits FILE_SHARE_DELETE. internal/memory hit this for real
	// in CI and internal/session has the same shape; both now retry through a
	// small renamer type. Not done here because package main would need a
	// third copy of that type plus its two build-tagged policy files, which is
	// more structure than this path earns: a checkpoint copies into a freshly
	// created shadow tree, so the realistic holder is an antivirus scanner
	// rather than another reader of ours. If checkpoint creation ever reports
	// "Access is denied" on Windows, this line is why.
	return os.Rename(tmpName, dst)
}

// shadowCheckpointer adapts shadowSnap to patch.Checkpointer.
type shadowCheckpointer struct {
	*shadowSnap
}

func (s *shadowCheckpointer) Checkpoint(ctx context.Context, task, name string, paths []string) (patch.SnapshotRef, error) {
	return s.shadowSnap.checkpoint(ctx, task, name, paths)
}

func (s *shadowCheckpointer) Restore(ctx context.Context, task, name string) error {
	return s.shadowSnap.restore(ctx, task, name)
}

// shadowRestorer adapts shadowSnap to runtime.Restorer.
type shadowRestorer struct {
	*shadowSnap
}

func (r *shadowRestorer) Restore(ctx context.Context, tid session.TaskID, name string) error {
	return r.shadowSnap.restore(ctx, string(tid), name)
}

// newShadowCheckpointer returns a patch.Checkpointer backed by shadow copies.
func newShadowCheckpointer(snap *shadowSnap) patch.Checkpointer {
	return &shadowCheckpointer{shadowSnap: snap}
}

// newShadowRestorer returns a runtime.Restorer backed by the same shadow copies.
func newShadowRestorer(snap *shadowSnap) runtime.Restorer {
	return &shadowRestorer{shadowSnap: snap}
}
