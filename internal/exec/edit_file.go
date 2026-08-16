// The EditFile built-in: writes content to a file through the sandbox so
// every write is confined to the repo root. This is the tool the Cognitive
// Core's system prompt advertises as "edit_file". The model sends the full new
// file content (not a diff); the tool resolves the path and writes it.

package exec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
)

// NewEditFile returns an EditFile tool confined to s.
func NewEditFile(s *Sandbox) *EditFile {
	return &EditFile{sandbox: s}
}

// EditFile writes a file inside the sandbox.
type EditFile struct {
	sandbox *Sandbox
}

func (e *EditFile) Name() string { return "edit_file" }

func (e *EditFile) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{FS: FSWrite},
		Cost:        CostMedium,
		Category:    "fs",
		Description: "edit a file by writing its full new content",
	}
}

func (e *EditFile) Schema() Schema {
	return Schema{Type: "object", Required: []string{"file", "content"}}
}

func (e *EditFile) Risk(_ ToolCall) event.Risk { return RiskHigh }

func (e *EditFile) Run(_ context.Context, in ToolInput) (ToolOutput, error) {
	var args struct {
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(in.Args, &args); err != nil {
		return ToolOutput{}, err
	}

	full, err := e.sandbox.Resolve(args.File)
	if err != nil {
		return ToolOutput{}, err
	}

	// Resolve only flattens symlinks on the *whole* path, which fails (and so
	// falls back to a purely textual check) whenever the target does not exist
	// yet — the normal case for a new file. A symlinked ancestor therefore
	// slips through: Rel sees "link/x.go" sitting under root while the write
	// lands wherever link points. Re-derive the path from the realpath of its
	// deepest existing ancestor and re-check containment before touching disk.
	full, err = containedPath(e.sandbox.Root(), full)
	if err != nil {
		return ToolOutput{}, err
	}
	// The .git denial used to sit here. It moved into Resolve (ErrGitDirWrite)
	// because a per-tool guard only protects the tool that calls it, and
	// EditFile is not the only writer — cmd/yolo's patchFS.Write resolves and
	// writes on its own, past this file and past the approval gate.

	// Ensure parent directory exists. Safe now: every component above this
	// point is confirmed real and inside the root, and the components below it
	// do not exist, so MkdirAll creates plain directories rather than
	// following someone else's link out of the sandbox.
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ToolOutput{}, err
	}

	if err := os.WriteFile(full, []byte(args.Content), 0o644); err != nil {
		return ToolOutput{}, err
	}

	// Use forward slashes for cross-platform consistency.
	rel := strings.ReplaceAll(args.File, "\\", "/")

	return ToolOutput{
		Summary: "wrote " + rel,
		Files:   []string{full},
	}, nil
}

// containedPath returns p rebuilt on top of the realpath of its deepest
// existing ancestor, or ErrPathEscapes if that realpath is not inside root.
// Resolving the whole ancestor chain (not just the final component) is what
// closes the symlinked-parent escape. The components that do not exist yet are
// carried across verbatim: nothing on disk backs them, so no symlink can hide
// in them, and they can only extend the (contained) real ancestor downward.
//
// This is a check-then-write, so a symlink planted in the window between the
// two still wins; closing that needs openat2/O_NOFOLLOW plumbing, which is out
// of scope here. It removes the attack the model can drive on its own.
func containedPath(root, p string) (string, error) {
	dir := filepath.Dir(p)
	var missing []string
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if !withinRoot(root, real) {
				return "", ErrPathEscapes
			}
			parts := append([]string{real}, missing...)
			return filepath.Join(append(parts, filepath.Base(p))...), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Walked past the filesystem root without finding anything real.
			return "", ErrPathEscapes
		}
		missing = append([]string{filepath.Base(dir)}, missing...)
		dir = parent
	}
}

// withinRoot reports whether p is root or lives beneath it. Both are expected
// to be realpaths already.
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// underGitDir reports whether p is, or is inside, a directory named .git under
// root. Any component matches, not just the top-level one, so a submodule's
// gitdir is covered too. The comparison is case-insensitive because .GIT is
// still the git directory on the case-insensitive filesystems macOS and
// Windows ship by default.
//
// Resolve calls this on the pre-resolution path as well as the resolved one.
// It has to: EvalSymlinks flattens `root/.git -> root/store` into a path with
// no `.git` component left, so a symlinked .git is invisible to a
// post-resolution check alone.
func underGitDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}
