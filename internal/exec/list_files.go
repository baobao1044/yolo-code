// The ListFiles built-in: lists files in the repo root recursively, returning
// relative paths. This is the tool the Cognitive Core's system prompt
// advertises as "list_files". It walks the sandbox directory so every path is
// confined to the repo root.

package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
)

// NewListFiles returns a ListFiles tool confined to s.
func NewListFiles(s *Sandbox) *ListFiles {
	return &ListFiles{sandbox: s}
}

// ListFiles lists files in the repo directory.
type ListFiles struct {
	sandbox *Sandbox
}

func (l *ListFiles) Name() string { return "list_files" }

func (l *ListFiles) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{FS: FSRead},
		Cost:        CostCheap,
		Category:    "fs",
		Description: "list files in the repository",
	}
}

func (l *ListFiles) Schema() Schema {
	return Schema{Type: "object", Required: []string{}}
}

func (l *ListFiles) Risk(_ ToolCall) event.Risk { return RiskLow }

func (l *ListFiles) Run(_ context.Context, _ ToolInput) (ToolOutput, error) {
	var files []string
	root := l.sandbox.cwd
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() {
			// Skip common non-source directories to keep output manageable.
			// The list is shared with the bash change detector (fschange.go) so
			// the two walkers cannot drift apart. The path != root guard is
			// load-bearing: a repository checked out at a directory named
			// `dist` or `vendor` would otherwise be skipped whole and this tool
			// would report an empty repository.
			if path != root && skipWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		// Use forward slashes for cross-platform consistency.
		rel = strings.ReplaceAll(rel, "\\", "/")
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return ToolOutput{}, err
	}
	return ToolOutput{
		Stdout:  strings.Join(files, "\n"),
		Summary: "listed files",
	}, nil
}
