// Grep is an argv boundary: the pattern is model-chosen and therefore
// prompt-injectable, and ripgrep has flags (--pre) that execute programs. These
// tests pin the boundary — the pattern must always land as a positional search
// pattern, never as a flag.

package exec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRg installs a shell script named rg on PATH that appends its argv to
// argvFile (one arg per line, NUL-free) and exits 0. It lets the test assert
// the exact argv Grep builds without needing a real ripgrep.
func fakeRg(t *testing.T) (argvFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub is POSIX-only")
	}
	bin := t.TempDir()
	argvFile = filepath.Join(bin, "argv.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " +
		argvFile + "; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "rg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return argvFile
}

func runGrepTool(t *testing.T, root string, args map[string]string) (ToolOutput, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	g := NewGrep(NewSandbox(root, root))
	return g.Run(context.Background(), ToolInput{Args: raw})
}

// TestGrepRgSentinelBeforePattern is the core injection test: a `--pre=`
// pattern must arrive after a `--` sentinel and must not be flag-shaped, so
// ripgrep can only ever read it as a search pattern.
func TestGrepRgSentinelBeforePattern(t *testing.T) {
	argvFile := fakeRg(t)
	root := t.TempDir()

	if _, err := runGrepTool(t, root, map[string]string{
		"pattern": "--pre=/bin/sh",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("stub recorded no argv: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")

	sep := -1
	for i, a := range argv {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("no %q sentinel in argv: %q", "--", argv)
	}
	if sep+1 >= len(argv) {
		t.Fatalf("nothing after the sentinel: %q", argv)
	}
	pat := argv[sep+1]
	if strings.HasPrefix(pat, "-") {
		t.Errorf("pattern arg is still flag-shaped: %q", pat)
	}
	// Everything before the sentinel must be one of our own fixed flags —
	// the model's pattern must not have leaked into the flag region.
	for _, a := range argv[:sep] {
		if strings.Contains(a, "--pre") {
			t.Errorf("pattern leaked into the flag region: %q", argv)
		}
	}
}

// TestGrepFallbackSentinelBeforePattern pins the same boundary on the POSIX
// grep fallback, which had the identical missing-sentinel defect.
func TestGrepFallbackSentinelBeforePattern(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub is POSIX-only")
	}
	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " +
		argvFile + "; done\nexit 0\n"
	// Only grep on PATH, so Run takes the fallback branch.
	if err := os.WriteFile(filepath.Join(bin, "grep"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	root := t.TempDir()
	if _, err := runGrepTool(t, root, map[string]string{
		"pattern": "-rf/etc/passwd",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("stub recorded no argv: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	sep := -1
	for i, a := range argv {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("no %q sentinel in fallback argv: %q", "--", argv)
	}
	if strings.HasPrefix(argv[sep+1], "-") {
		t.Errorf("fallback pattern arg is still flag-shaped: %q", argv[sep+1])
	}
}

// TestGrepFlagShapedPatternStillMatchesLiterally proves the neutralisation is
// semantics-preserving: a real search for the literal text "--pre=/bin/sh"
// still finds it. Runs against whichever of rg/grep the host has.
func TestGrepFlagShapedPatternStillMatchesLiterally(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"),
		[]byte("harmless\nrg --pre=/bin/sh is a flag\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runGrepTool(t, root, map[string]string{"pattern": "--pre=/bin/sh"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.Stdout, "--pre=/bin/sh is a flag") {
		t.Errorf("literal flag-shaped pattern did not match; stdout=%q summary=%q",
			out.Stdout, out.Summary)
	}
}

// TestGrepRiskGrading pins the per-call risk tiering: an ordinary in-repo
// search stays silent (low), an argv-shaped or escaping call prompts (medium).
func TestGrepRiskGrading(t *testing.T) {
	root := t.TempDir()
	g := NewGrep(NewSandbox(root, root))

	call := func(args map[string]string) ToolCall {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return ToolCall{Tool: "grep", Args: raw}
	}

	if got := g.Risk(call(map[string]string{"pattern": "func main"})); got != RiskLow {
		t.Errorf("plain search: got %q, want %q", got, RiskLow)
	}
	if got := g.Risk(call(map[string]string{"pattern": "--pre=/bin/sh"})); got != RiskMedium {
		t.Errorf("flag-shaped pattern: got %q, want %q", got, RiskMedium)
	}
	if got := g.Risk(call(map[string]string{
		"pattern": "secret", "path": "../../etc",
	})); got != RiskMedium {
		t.Errorf("escaping path: got %q, want %q", got, RiskMedium)
	}
	if got := g.Risk(ToolCall{Tool: "grep", Args: []byte("not json")}); got != RiskMedium {
		t.Errorf("unparsable args: got %q, want %q", got, RiskMedium)
	}
}
