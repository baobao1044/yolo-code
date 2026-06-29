// The Grep built-in: searches file contents in the repo using ripgrep (rg) or
// falls back to grep. Returns matching lines with file paths and line numbers.
// This is essential for a coding agent to locate code patterns across a repo.

package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/yolo-code/yolo/internal/event"
)

// NewGrep returns a Grep tool confined to s.
func NewGrep(s *Sandbox) *Grep {
	return &Grep{sandbox: s}
}

// Grep searches file contents in the repository.
type Grep struct {
	sandbox *Sandbox
}

func (g *Grep) Name() string { return "grep" }

func (g *Grep) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{FS: FSRead},
		Cost:        CostCheap,
		Category:    "fs",
		Description: "search file contents for a pattern (regex)",
	}
}

func (g *Grep) Schema() Schema {
	return Schema{Type: "object", Required: []string{"pattern"}}
}

func (g *Grep) Risk(_ ToolCall) event.Risk { return RiskLow }

type grepArgs struct {
	Pattern string `json:"pattern"` // regex pattern to search for
	Path    string `json:"path"`    // optional: directory or file to search in (default: repo root)
}

func (g *Grep) Run(ctx context.Context, in ToolInput) (ToolOutput, error) {
	var args grepArgs
	if err := json.Unmarshal(in.Args, &args); err != nil {
		return ToolOutput{}, err
	}
	if args.Pattern == "" {
		return ToolOutput{}, fmt.Errorf("grep: pattern is required")
	}

	searchDir := g.sandbox.Root()
	if args.Path != "" {
		resolved, err := g.sandbox.Resolve(args.Path)
		if err != nil {
			return ToolOutput{}, fmt.Errorf("grep: %w", err)
		}
		searchDir = resolved
	}

	// Try ripgrep first (much faster, better defaults), fall back to grep.
	if path, err := exec.LookPath("rg"); err == nil {
		return g.runRg(ctx, path, args.Pattern, searchDir)
	}
	if path, err := exec.LookPath("grep"); err == nil {
		return g.runGrep(ctx, path, args.Pattern, searchDir)
	}
	return ToolOutput{}, fmt.Errorf("grep: neither rg nor grep found on PATH")
}

// runRg runs ripgrep with sensible defaults for a coding agent:
// -n: line numbers, -C 2: two lines of context, --max-columns 200: truncate long lines.
func (g *Grep) runRg(ctx context.Context, rg, pattern, dir string) (ToolOutput, error) {
	cmd := exec.CommandContext(ctx, rg, "-n", "-C", "2", "--max-columns", "200",
		"--max-count", "50", "--glob", "!.git", pattern, dir)
	out, err := cmd.Output()
	// rg exits with code 1 when no matches — that's not an error.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return ToolOutput{Stdout: "", Summary: "no matches"}, nil
	}
	if err != nil {
		return ToolOutput{}, fmt.Errorf("rg: %w", err)
	}
	return ToolOutput{
		Stdout:  string(out),
		Summary: summarizeGrep(string(out)),
	}, nil
}

// runGrep runs POSIX grep as a fallback.
func (g *Grep) runGrep(ctx context.Context, grep, pattern, dir string) (ToolOutput, error) {
	var args []string
	if runtime.GOOS != "windows" {
		args = []string{"-rn", "-E", "--max-count=50", pattern, dir}
	} else {
		args = []string{"-rn", "-E", pattern, dir}
	}
	cmd := exec.CommandContext(ctx, grep, args...)
	out, err := cmd.Output()
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return ToolOutput{Stdout: "", Summary: "no matches"}, nil
	}
	if err != nil {
		return ToolOutput{}, fmt.Errorf("grep: %w", err)
	}
	return ToolOutput{
		Stdout:  string(out),
		Summary: summarizeGrep(string(out)),
	}, nil
}

// summarizeGrep produces a one-line summary of the grep output.
func summarizeGrep(output string) string {
	if output == "" {
		return "no matches"
	}
	lines := strings.Count(output, "\n")
	if lines == 0 && output != "" {
		lines = 1
	}
	return fmt.Sprintf("%d matching lines", lines)
}
