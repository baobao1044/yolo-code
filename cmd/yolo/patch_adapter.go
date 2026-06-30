// Adapter wiring patch.Engine into runtime.Patcher (Sprint 12 INT-003).
// The composition root owns this bridge so internal/runtime never imports patch.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/patch"
	"github.com/baobao1044/yolo-code/internal/runtime"
)

// patchFS implements patch.Filesystem through the exec sandbox so reads and
// writes stay confined to the repo root.
type patchFS struct {
	sandbox *exec.Sandbox
}

func (f *patchFS) Read(ctx context.Context, path string) (string, error) {
	full, err := f.sandbox.Resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", patch.ErrFileNotExist
		}
		return "", err
	}
	return string(data), nil
}

func (f *patchFS) Write(ctx context.Context, path, content string) error {
	full, err := f.sandbox.Resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// patchAdapter implements runtime.Patcher using patch.Engine.
type patchAdapter struct {
	engine *patch.Engine
}

func (a *patchAdapter) Apply(ctx context.Context, op runtime.PatchOp) (runtime.PatchResult, error) {
	path, body, err := opTargetAndBody(op)
	if err != nil {
		return runtime.PatchResult{}, err
	}

	blocks, perr := patch.ParseBlocks(body)
	var fullContent string
	if perr != nil {
		// No SEARCH/REPLACE blocks → treat the body as a full-content write
		// (e.g. creating a new file). The engine uses FullContent when blocks
		// are empty.
		fullContent = body
		blocks = nil
	}

	seq := op.Seq
	if seq <= 0 {
		seq = 1
	}

	res, err := a.engine.Apply(ctx, patch.Op{
		Task:        string(op.Task),
		Seq:         seq,
		Path:        path,
		Blocks:      blocks,
		FullContent: fullContent,
	})
	if err != nil {
		return runtime.PatchResult{}, err
	}

	return runtime.PatchResult{
		Accepted:   res.Accepted,
		Reason:     res.Reason,
		Checkpoint: res.Checkpoint,
		Snapshot:   []byte(res.Snapshot),
	}, nil
}

// opTargetAndBody extracts the target path and patch body. The body may be a
// JSON object with explicit "path" and "body" fields, or raw blocks with the
// path supplied by runtime.PatchOp.Path.
func opTargetAndBody(op runtime.PatchOp) (path, body string, err error) {
	var args struct {
		Path string `json:"path"`
		Body string `json:"body"`
	}
	if jerr := json.Unmarshal(op.Body, &args); jerr == nil && (args.Path != "" || args.Body != "") {
		if args.Path != "" {
			path = args.Path
		}
		if args.Body != "" {
			body = args.Body
		}
	} else {
		body = string(op.Body)
	}
	if path == "" {
		path = op.Path
	}
	if path == "" {
		return "", "", fmt.Errorf("patch adapter: missing target path")
	}
	if body == "" {
		return "", "", fmt.Errorf("patch adapter: empty patch body")
	}
	return path, body, nil
}

// newPatchEngine builds a real patch.Engine using the sandbox for FS, a shadow
// checkpointer for rollback, and the shared event bus. The composition root in
// cmd/yolo uses this to satisfy runtime.Patcher.
func newPatchEngine(sandbox *exec.Sandbox, cp patch.Checkpointer, bus *event.Bus) *patch.Engine {
	return patch.NewEngine(patch.Deps{
		FS:         &patchFS{sandbox: sandbox},
		Checkpoint: cp,
		Validator:  patch.NewValidator(),
		Bus:        bus,
	})
}
