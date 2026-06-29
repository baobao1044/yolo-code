// The patch engine (File 10 §10.5/§10.6): the orchestrator that ties the
// parser (L9-001/002), conflict detection (L9-003), and AST validator (L9-004)
// together into one Apply path with two-layer rollback (§10.5.1):
//   1. read the original → keep it as the in-memory snapshot (pre-write
//      rollback: an apply or AST failure rejects before any byte is written);
//   2. checkpoint the affected path with the Session Manager's Checkpointer
//      (post-write rollback: a write failure or a later Verify failure (File 09)
//      restores the git snapshot, §10.5.2/§10.5.4).
//
// The seams live here, not in the session/sysio packages, because the import
// matrix (File 15 §15.15.2) forbids patch importing session or sysio. The
// composition root (cmd/yolo) wires the real session.Manager.Checkpoint /
// session.Checkpointer.Rollback behind these interfaces — patch stays a pure
// leaf depending only on event (and, transitively, the stdlib parser its
// Validator uses).
//
// Non-git fallback (§10.5.3): a project that isn't a git repo wires a
// shadow-copy Checkpointer instead; the engine is identical — the guarantee
// (rollback is always possible) holds regardless of the backend.

package patch

import (
	"context"
	"errors"
	"fmt"

	"github.com/baobao1044/yolo-code/internal/event"
)

// ErrFileNotExist signals a path the Filesystem doesn't have. Apply treats it
// as "new file" rather than an error (a patch to a not-yet-existing file is a
// FullContent write, File 10 §10.6). It mirrors os.ErrNotExist's role in the
// spec while keeping patch free of an os import at the seam.
var ErrFileNotExist = errors.New("patch: file does not exist")

// SnapshotRef is an opaque handle to a restorable state (a git tree or shadow
// copy). The engine treats it as a passthrough token: the composition root's
// Checkpointer mints and consumes it; the engine only carries it on the Result
// so the runtime (and the patch.applied event, L9-006) can name the snapshot.
type SnapshotRef string

// Filesystem is the FS seam Apply uses (read original, write new). The
// composition root wires the sandbox-confined reader/writer; patch stays free
// of sysio. Read returns ErrFileNotExist for a missing path (new-file case).
type Filesystem interface {
	Read(ctx context.Context, path string) (string, error)
	Write(ctx context.Context, path, content string) error
}

// Checkpointer is the snapshot seam (File 10 §10.5.2). Checkpoint captures the
// paths before the write and returns an opaque ref; Restore rolls the tree
// back to that ref. The composition root adapts this to session.Manager's
// Checkpoint/Restore (which delegate to session.Checkpointer). The name is the
// human-readable checkpoint id ("patch_<seq>") the runtime and event carry.
type Checkpointer interface {
	Checkpoint(ctx context.Context, task, name string, paths []string) (SnapshotRef, error)
	Restore(ctx context.Context, task, name string) error
}

// Deps are the engine's collaborators. All three are interfaces so the
// composition root wires concretes and tests wire fakes. Bus is the event bus
// the engine publishes patch.applied to (patch may import event, File 15
// §15.15.2); nil Bus skips publishing (the composition root always wires one).
type Deps struct {
	FS         Filesystem
	Checkpoint Checkpointer
	Validator  *Validator
	Bus        *event.Bus
}

// Engine applies patches with checkpoint + rollback (File 10 §10.6).
type Engine struct {
	fs         Filesystem
	checkpoint Checkpointer
	validator  *Validator
	bus        *event.Bus
}

// NewEngine returns an Engine wired to its deps.
func NewEngine(d Deps) *Engine {
	return &Engine{fs: d.FS, checkpoint: d.Checkpoint, validator: d.Validator, bus: d.Bus}
}

// Op is one apply request (File 10 §10.6). For an existing file, Blocks apply
// via the SEARCH/REPLACE path; for a new file or full overwrite, FullContent
// is written directly (Blocks empty). Task + Seq name the checkpoint
// ("patch_<seq>") the runtime can later Restore on a Verify failure.
type Op struct {
	Task        string
	Seq         int
	Path        string
	Blocks      []Block
	FullContent string // set for new files / overwrite; empty otherwise
}

// Result reports the outcome (File 10 §10.6). Exactly one of Accepted/Rejected
// is set; Reason carries the cause on reject (the model reads it and retries,
// §10.5.4); Checkpoint + Snapshot let the runtime Restore and the event name
// the snapshot; Summary is the diff stats (set on accept) for the runtime and
// the patch.applied event.
type Result struct {
	Accepted   bool
	Rejected   bool
	Reason     string
	Checkpoint string // human-readable id ("patch_3"), for Restore + the event
	Snapshot   SnapshotRef
	Summary    Summary
}

// Apply runs the engine's single application path (File 10 §10.6):
//
//	read original → checkpoint → build next (blocks or full content) →
//	AST validate → write.
//
// Rollback is two-layer (§10.5.1):
//   - apply failure or AST failure: reject before writing — the in-memory
//     original is the rollback, nothing touches disk;
//   - write failure: the file may be half-written on disk, so the engine
//     Restore's the checkpoint (the git snapshot / shadow copy) before
//     returning the error — the tree is left clean (§10.5.4).
//
// A returned error means an infrastructure failure (FS/Checkpoint) the model
// can't fix by retrying the patch; a rejected Result means the patch itself was
// bad and the model should read Reason and retry (§10.5.4).
func (e *Engine) Apply(ctx context.Context, op Op) (Result, error) {
	original, err := e.fs.Read(ctx, op.Path)
	isNew := errors.Is(err, ErrFileNotExist)
	if err != nil && !isNew {
		return Result{}, fmt.Errorf("patch: read %s: %w", op.Path, err)
	}

	name := fmt.Sprintf("patch_%d", op.Seq)
	snap, err := e.checkpoint.Checkpoint(ctx, op.Task, name, []string{op.Path})
	if err != nil {
		return Result{}, fmt.Errorf("patch: checkpoint: %w", err)
	}
	res := Result{Checkpoint: name, Snapshot: snap}

	// Build the next content. New file or full overwrite → FullContent; else
	// run the SEARCH/REPLACE blocks against the original.
	var next string
	if isNew || op.FullContent != "" {
		next = op.FullContent
	} else {
		n, aerr := Apply(original, op.Blocks)
		if aerr != nil {
			res.Rejected = true
			res.Reason = aerr.Error()
			return res, nil // in-memory rollback: nothing written
		}
		next = n
	}

	// A new/overwrite with empty content is a no-op write the model didn't
	// intend — reject rather than truncate a file to empty (§10.6 "empty +
	// FullContent for full overwrite" implies FullContent is the real body).
	if next == "" {
		res.Rejected = true
		res.Reason = "patch: empty content"
		return res, nil
	}

	// AST validation is the first gate (File 10 §10.4/§10.6). Fails before any
	// write → in-memory rollback (the original is the rollback).
	if e.validator != nil {
		if verr := e.validator.Validate(op.Path, next); verr != nil {
			res.Rejected = true
			res.Reason = "ast: " + verr.Error()
			return res, nil
		}
	}

	// Write. A failure here may leave a half-written file on disk → restore the
	// checkpoint so the tree is clean (§10.5.4 two-layer rollback, post-write).
	if werr := e.fs.Write(ctx, op.Path, next); werr != nil {
		if rerr := e.checkpoint.Restore(ctx, op.Task, name); rerr != nil {
			return Result{}, fmt.Errorf("patch: write %s: %v (and rollback failed: %v)", op.Path, werr, rerr)
		}
		return Result{}, fmt.Errorf("patch: write %s: %w (rolled back to %s)", op.Path, werr, name)
	}

	// Success: summarize the diff and publish patch.applied (File 10 §10.6 +
	// File 05 §5.4.4) so the transcript shows what changed. The summary is
	// computed from original→next (a new file: original ""). Publish errors
	// don't fail the apply — the write already succeeded; a dropped event is
	// survivable, a rolled-back success would corrupt the tree.
	res.Accepted = true
	res.Summary = Summarize([]Change{{Path: op.Path, Original: original, Next: next}})
	if e.bus != nil {
		_ = e.bus.Publish(ctx, &event.PatchAppliedEvent{
			Task:       event.TaskID(op.Task),
			Snapshot:   []byte(fmt.Sprintf("%q", string(snap))),
			Files:      toEventFiles(res.Summary.Files),
			Insertions: res.Summary.Insertions,
			Deletions:  res.Summary.Deletions,
		})
	}
	return res, nil
}

// toEventFiles copies the patch-internal FileStat slice into the event's
// PatchFile slice (the two mirror each other; patch keeps its own copy so the
// engine summary doesn't depend on the event package's shape).
func toEventFiles(fs []FileStat) []event.PatchFile {
	out := make([]event.PatchFile, len(fs))
	for i, f := range fs {
		out[i] = event.PatchFile{
			Path:       f.Path,
			Insertions: f.Insertions,
			Deletions:  f.Deletions,
			New:        f.New,
		}
	}
	return out
}
