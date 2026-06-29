// The sandbox (File 08 §8.4): the model is untrusted — a hallucinated or
// adversarial tool call must never read outside the repo, write outside it,
// escape the sandbox, exfiltrate, or run forever. Resolve confines every
// filesystem path to the repo root; Classify sorts a shell command into a
// risk class so the dispatcher and HITL gate can admit, prompt, or deny it.
// Escapes are surfaced as normal errors (ErrPathEscapes), never a panic.

package exec

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
)

// ErrPathEscapes is returned when a path resolves outside the sandbox root
// (File 08 §8.4.2). It is a normal error so the dispatcher surfaces it to the
// model as a tool result, not a crash.
var ErrPathEscapes = errors.New("exec: path escapes sandbox root")

// ErrNetworkDenied is returned when a tool whose metadata declares Net:true
// targets a host not on the sandbox's allowlist (File 08 §8.4.4). Default-
// deny: with no allowlist, every network attempt is blocked before Run.
var ErrNetworkDenied = errors.New("exec: network access denied (host not allowlisted)")

// Sandbox confines filesystem access to root, with cwd as the relative base
// for non-absolute paths (File 08 §8.4.2). Resolve is the single gate every
// Read/Write/Grep/Glob passes through.
type Sandbox struct {
	root string
	cwd  string
	// hosts is the network allowlist (L7-005). Empty → default-deny.
	hosts map[string]bool
}

// NewSandbox builds a Sandbox rooted at root with cwd as the working directory.
// The composition root uses this so cmd/yolo can wire a real sandbox without
// reaching into unexported fields (Sprint 12 integration).
//
// Both root and cwd are normalized through filepath.EvalSymlinks so that
// Resolve's later EvalSymlinks call on resolved paths compares like-for-like.
// Without this, Windows short-name vs long-name mismatches cause false
// ErrPathEscapes (e.g. C:\Users\ADMIN~1\... vs C:\Users\Admin\...).
func NewSandbox(root, cwd string) *Sandbox {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	return &Sandbox{root: root, cwd: cwd}
}

// Root returns the sandbox root directory.
func (s *Sandbox) Root() string { return s.root }

// Resolve confines p to the repo root (File 08 §8.4.2). A relative path is
// joined to cwd; an absolute one is taken as-is. Symlinks are flattened with
// EvalSymlinks before the confinement check so a symlink pointing outside is
// rejected. If the resolved path is not under root (its Rel to root starts
// with ".."), Resolve returns ErrPathEscapes.
func (s *Sandbox) Resolve(p string) (string, error) {
	full := p
	if !filepath.IsAbs(p) {
		full = filepath.Join(s.cwd, p)
	}
	// EvalSymlinks flattens a symlink target; if it fails (file doesn't exist
	// yet) we fall through to the Rel check against the un-flattened path,
	// which still catches `../`-style textual escapes.
	if real, err := filepath.EvalSymlinks(full); err == nil {
		full = real
	}
	rel, err := filepath.Rel(s.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrPathEscapes
	}
	return full, nil
}

// Classify sorts a shell command into a risk class (File 08 §8.4.3): peel
// sudo/env/time wrappers, then match the head against the safe-read /
// build-test / mutating-fs / network / shell-escape / disk-heavy tables. A
// shell-escape metacharacter (`eval`, `source`, `$(…)`, backticks) is denied
// outright (critical); a network command without an allowlisted host is high.
// The classification drives the HITL gate (low runs silently, medium prompts,
// high prompts red, critical blocks).
func (s *Sandbox) Classify(cmd string) event.Risk {
	c := strings.TrimSpace(cmd)
	if c == "" {
		return RiskLow
	}
	// Shell-escape metacharacters introduce sub-execution; deny outright.
	if hasShellEscape(c) {
		return RiskCritical
	}
	tokens := strings.Fields(c)
	tokens = peelWrappers(tokens)
	if len(tokens) == 0 {
		return RiskLow
	}
	head := tokens[0]
	// Shell interpreters with -c are sub-execution gateways too.
	if shellInterpreter[head] && hasFlag(tokens, "-c") {
		return RiskCritical
	}
	// Network commands are high unless the host is allowlisted (L7-005 gates
	// the actual connection; Classify flags the risk for HITL).
	if networkCmds[head] {
		return RiskHigh
	}
	if diskHeavy[head] || (head == "rm" && hasFlag(tokens, "-rf") && len(tokens) > 2) {
		return RiskCritical
	}
	if mutatingFS[head] {
		return RiskMedium
	}
	if safeRead[head] || buildTest[head] {
		return RiskLow
	}
	// Unknown command: default to medium (local side effects assumed) so the
	// HITL gate prompts rather than silently running an unvetted command.
	return RiskMedium
}

// hasShellEscape reports whether c contains a shell-escape construct
// (`eval`/`source` as a head-ish word, or `$(`, backticks). A real parser
// would tokenize properly; this conservative match errs on deny.
func hasShellEscape(c string) bool {
	if strings.HasPrefix(c, "eval ") || strings.Contains(c, " eval ") || strings.HasPrefix(c, "source ") {
		return true
	}
	if strings.Contains(c, "$(") || strings.Contains(c, "`") {
		return true
	}
	return false
}

// peelWrappers strips leading sudo/env/time prefixes so a wrapper cannot
// launder a dangerous command (File 08 §8.4.3 "peels wrappers").
func peelWrappers(tokens []string) []string {
	for {
		if len(tokens) == 0 {
			return tokens
		}
		switch tokens[0] {
		case "sudo", "env", "time":
			tokens = tokens[1:]
		default:
			return tokens
		}
	}
}

// hasFlag reports whether tokens contain flag (e.g. "rm" "-rf" "/").
func hasFlag(tokens []string, flag string) bool {
	for _, t := range tokens {
		if t == flag {
			return true
		}
	}
	return false
}

// Command-class tables (File 08 §8.4.3). Kept as package vars so a later
// ticket can let config extend them without rewriting Classify.
var (
	safeRead         = set("ls", "cat", "grep", "find", "git", "stat", "wc", "head", "tail", "diff")
	buildTest        = set("go", "make", "cargo", "npm", "yarn", "pnpm", "pytest", "rake")
	mutatingFS       = set("mv", "cp", "touch", "mkdir", "chmod", "chown", "git")
	networkCmds      = set("curl", "wget", "ssh", "scp", "rsync", "nc", "ftp", "telnet")
	diskHeavy        = set("dd", "mkfs", "fdisk", "shred")
	shellInterpreter = set("bash", "sh", "zsh", "fish", "cmd", "powershell")
)

// set builds a lookup map from its arguments.
func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
