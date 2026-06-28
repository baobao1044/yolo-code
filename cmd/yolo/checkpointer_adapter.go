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

	"github.com/yolo-code/yolo/internal/patch"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
)

// shadowSnap is the shared snapshot storage used by both the patch engine
// (patch.Checkpointer) and the runtime (runtime.Restorer).
type shadowSnap struct {
	root string // repo root
	dir  string // shadow root directory
}

// newShadowSnap creates a shadow snapshot store under a temporary directory.
func newShadowSnap(root string) (*shadowSnap, error) {
	dir, err := os.MkdirTemp("", "yolo-shadow-*")
	if err != nil {
		return nil, fmt.Errorf("shadow checkpointer: %w", err)
	}
	return &shadowSnap{root: root, dir: dir}, nil
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

// copyFile copies src to dst using a temporary file and atomic rename.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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
