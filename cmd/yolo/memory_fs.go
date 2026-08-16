// The memory.FS adapter (File 11 §11.7.5). memory may import only event +
// stdlib (File 15 §15.15.2 import matrix), so it exposes a tiny FS interface
// the composition root satisfies. This file bridges the exec Sandbox's
// path-confined reader to memory.FS so the LexicalStore can reindex a path's
// content on patch.applied. The sandbox confines p to the repo root (rejecting
// escapes via ErrPathEscapes), then a plain os.ReadFile loads the body.
//
// Composition-root only: this lives in cmd/yolo, exactly as the context/prompt
// adapters do (the binary is the seam between layers the matrix forbids from
// importing each other).

package main

import (
	"context"
	"os"

	execpkg "github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/memory"
)

// memoryFS adapts the exec Sandbox to memory.FS.
type memoryFS struct {
	sb *execpkg.Sandbox
}

// newMemoryFS returns a memory.FS that reads through the sandbox's confined
// resolver. A nil sandbox returns nil (the LexicalStore's Reindex then runs
// without an FS — cold-start IndexRepo still works via its own walk).
func newMemoryFS(sb *execpkg.Sandbox) memory.FS {
	if sb == nil {
		return nil
	}
	return memoryFS{sb: sb}
}

// Read satisfies memory.FS. It resolves p through the sandbox (rejecting
// escapes) and reads the file's bytes. A missing or unreadable file returns
// the os error; the LexicalStore treats that as "nothing to index" (§11.7.5).
func (f memoryFS) Read(_ context.Context, path string) ([]byte, error) {
	full, err := f.sb.Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}
