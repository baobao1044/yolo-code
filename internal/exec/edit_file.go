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

	"github.com/yolo-code/yolo/internal/event"
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

	// Ensure parent directory exists.
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
