// The Grep built-in: searches file contents in the repo using ripgrep (rg) or
// falls back to grep. Returns matching lines with file paths and line numbers.
// This is essential for a coding agent to locate code patterns across a repo.

package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
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

// Risk grades per call instead of returning a flat tier. An ordinary in-repo
// search is the same shape as read_file (RiskLow): sandbox-confined, cheap,
// and the normalizer redacts secret-shaped output before the model sees it —
// making it RiskMedium would prompt on the agent's main navigation tool and
// train the user to click through. What is *not* ordinary is a call whose only
// purpose is to attack a boundary: a flag-shaped pattern or path (the `--pre=`
// argv-injection class) or a path reaching outside the root. Those escalate to
// RiskMedium so the HITL gate shows them rather than running them silently.
func (g *Grep) Risk(call ToolCall) event.Risk {
	var args grepArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		// Args the gate cannot read are args the gate cannot vouch for.
		return RiskMedium
	}
	if isFlagShaped(args.Pattern) || isFlagShaped(args.Path) {
		return RiskMedium
	}
	if args.Path != "" && g.sandbox != nil {
		if _, err := g.sandbox.Resolve(args.Path); err != nil {
			return RiskMedium
		}
	}
	return RiskLow
}

// isFlagShaped reports whether s would be parsed as an option rather than an
// operand by a getopt-style CLI.
func isFlagShaped(s string) bool { return strings.HasPrefix(s, "-") }

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

// neutralizePattern makes a search pattern safe to hand a getopt-style CLI as
// an operand. The `--` sentinel is the real fix, but this is the belt to its
// braces: a pattern beginning with `-` is otherwise parsed as a flag, and
// ripgrep's `--pre=<cmd>` runs an arbitrary program per file — a model-chosen
// (therefore prompt-injectable) string must never reach that. Backslash-
// escaping the leading `-` is semantics-preserving: outside a character class
// `-` is already a literal in both ERE and Rust regex, so `\-foo` and `-foo`
// match the same text.
func neutralizePattern(p string) string {
	if isFlagShaped(p) {
		return `\` + p
	}
	return p
}

// neutralizeDir does the same for the search directory. Resolve and Root
// normally yield an absolute path, but a sandbox rooted at a relative path
// beginning with `-` would otherwise produce a flag-shaped operand; `./` keeps
// it positional even if the sentinel is ever lost in a refactor.
func neutralizeDir(d string) string {
	if isFlagShaped(d) {
		return "." + string(filepath.Separator) + d
	}
	return d
}

// runRg runs ripgrep with sensible defaults for a coding agent:
// -n: line numbers, -C 2: two lines of context, --max-columns 200: truncate long lines.
// The `--` sentinel ends flag parsing so pattern and dir are always operands.
func (g *Grep) runRg(ctx context.Context, rg, pattern, dir string) (ToolOutput, error) {
	cmd := exec.CommandContext(ctx, rg, "-n", "-C", "2", "--max-columns", "200",
		"--max-count", "50", "--glob", "!.git", "--",
		neutralizePattern(pattern), neutralizeDir(dir))
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

// runGrep runs POSIX grep as a fallback. Same `--` sentinel as runRg: grep has
// no `--pre`, but a flag-shaped pattern still lets the model rewrite the search
// (`-f/etc/shadow`, `-r /`) rather than perform it.
func (g *Grep) runGrep(ctx context.Context, grep, pattern, dir string) (ToolOutput, error) {
	pattern, dir = neutralizePattern(pattern), neutralizeDir(dir)
	var args []string
	if runtime.GOOS != "windows" {
		args = []string{"-rn", "-E", "--max-count=50", "--", pattern, dir}
	} else {
		args = []string{"-rn", "-E", "--", pattern, dir}
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
