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

// ErrGitDirWrite is returned when a path resolves inside a .git directory.
// That is never a legitimate target and it is two disasters at once:
// scribbling on the object store loses work irrecoverably, and dropping a file
// in .git/hooks is code execution on the user's next commit. Denied outright
// rather than left to the HITL gate, which a distracted user can wave through.
//
// The guard lives in Resolve rather than in the writing tool because EditFile
// is not the only writer: cmd/yolo's patchFS.Write calls Resolve and then
// os.WriteFile directly, and the `patch` tool reaches patch.Engine on its own
// route rather than through the exec registry. A per-tool guard only protects
// the tool that remembers to call it; a hook dropped in .git/hooks would be a
// model-chosen write, and no single writer can be trusted to be the last one.
//
// The name says Write because writing is the disaster; the guard denies reads
// too, because Resolve cannot see its caller's intent (see Resolve).
var ErrGitDirWrite = errors.New("exec: refusing to access .git")

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
// rejected, and Resolve returns ErrPathEscapes if the real path is not root or
// beneath it. This is the single gate Read/Grep/Bash and every future tool
// share, so it has to hold for paths that do not exist yet too.
//
// That last part is where it used to leak. EvalSymlinks on the whole path
// fails whenever any component is missing — the normal case for a file about
// to be created — and the old fallback was a purely textual Rel check on the
// *un*-flattened path. So `root/link/new.txt`, with `link` a symlink pointing
// out of the repo, read as "under root" textually while the write landed
// wherever link pointed. containedPath (edit_file.go) closes it: resolve the
// deepest ancestor that really exists, check *that* realpath for containment,
// and re-attach the not-yet-existing components verbatim.
//
// Resolve also denies anything inside .git (ErrGitDirWrite), on BOTH the
// pre-resolution path and the resolved one. Both halves are load-bearing:
// post-resolution catches a path that reaches a real .git by another route,
// and pre-resolution catches a symlinked .git — with `root/.git -> root/store`
// EvalSymlinks hands back a path with no `.git` component left in it at all,
// which is how a hook got written past the old tool-level guard.
//
// The denial is blanket rather than write-only because Resolve cannot see its
// caller's intent: cmd/yolo's patchFS calls it for both Read and Write, so a
// write-only guard here would have to be opted into by the very caller that
// forgets. Nothing in the tree reads .git through the sandbox (the memory
// indexer skips it, Grep's ripgrep invocation already excludes it), and the
// model keeps full access to git state through `git` in Bash at RiskLow. If a
// legitimate .git reader ever appears, give it a ResolveForRead that runs
// confine alone — do not weaken this.
func (s *Sandbox) Resolve(p string) (string, error) {
	full := p
	if !filepath.IsAbs(p) {
		full = filepath.Join(s.cwd, p)
	}
	resolved, err := s.confine(full)
	if err != nil {
		// Containment is reported first: an escaping path is a worse and more
		// specific answer than "that is inside .git".
		return "", err
	}
	if underGitDir(s.root, full) || underGitDir(s.root, resolved) {
		return "", ErrGitDirWrite
	}
	return resolved, nil
}

// confine is Resolve's containment half: flatten symlinks and check the result
// is root or beneath it, with containedPath handling a path whose tail does not
// exist yet. Split out so Resolve's .git guard reads as a separate decision.
func (s *Sandbox) confine(full string) (string, error) {
	// Every component exists: flatten the lot and check the realpath directly.
	if real, err := filepath.EvalSymlinks(full); err == nil {
		if !withinRoot(s.root, real) {
			return "", ErrPathEscapes
		}
		return real, nil
	}
	// Something on the path does not exist yet — resolve what does.
	return containedPath(s.root, full)
}

// Classify sorts a shell command into a risk class (File 08 §8.4.3).
//
// A command *string* is not one command. bash.go hands the whole string to
// `sh -c`, so `echo hi; rm -rf /` runs two commands and only the second one
// matters. Classify therefore splits the string into every segment the shell
// will actually run (`;`, `&&`, `||`, `|`, `&`, newlines, subshells, group
// braces, `if`/`while` bodies) and returns the MAXIMUM risk over all of them —
// a compound command is exactly as dangerous as its worst part.
//
// Tradeoff (deliberate): we take the max instead of refusing every compound
// that mixes risk tiers. Refusing would kill the common, legitimate case —
// `ls | head`, `go build && go test` — and make Bash close to useless. The
// price is that a mixed-tier compound is gated at its highest tier rather than
// run silently at its lowest, which is the safe direction: the HITL gate
// prompts, it does not block.
//
// Per segment: peel sudo/env/time wrappers and shell keywords, then match the
// head against the safe-read / build-test / mutating-fs / network / disk-heavy
// tables. Substitutions whose body we cannot see (`$(…)`, backticks, `<(…)`)
// are denied for the whole string, since their contents never reach the
// splitter. The classification drives the HITL gate (low runs silently, medium
// prompts, high prompts red, critical blocks).
func (s *Sandbox) Classify(cmd string) event.Risk {
	c := strings.TrimSpace(cmd)
	if c == "" {
		return RiskLow
	}
	// Substitutions introduce sub-execution we cannot inspect; deny outright.
	if hasShellEscape(c) {
		return RiskCritical
	}
	worst := RiskLow
	for _, seg := range splitSegments(c) {
		worst = maxRisk(worst, s.classifySegment(seg))
		if worst == RiskCritical {
			break // nothing outranks critical
		}
	}
	return worst
}

// maxRisk returns the higher of two risk classes on the low→critical ladder.
func maxRisk(a, b event.Risk) event.Risk {
	if riskLevel(b) > riskLevel(a) {
		return b
	}
	return a
}

// classifySegment scores a single command — one segment of the split.
// Classify runs it once per segment and keeps the maximum.
func (s *Sandbox) classifySegment(seg string) event.Risk {
	tokens, floor := peelPrefixes(strings.Fields(seg))
	if len(tokens) == 0 {
		// Nothing but prefixes: `fi`/`done` score low, a bare `>file` keeps the
		// floor its redirection earned.
		return floor
	}
	// A content-search tool's head does not settle the question: modern search
	// tools take flags that run another program, so the argv gets a second
	// look. Escalation only — never a relaxation of the head's own class.
	return maxRisk(floor, maxRisk(s.headRisk(tokens), searchFlagRisk(tokens)))
}

// headRisk is the head-token classifier: match the (already peeled) command
// name against the safe-read / build-test / mutating-fs / network /
// disk-heavy tables.
// A dangerous command keeps its teeth when it is spelled with a path: the
// program `/bin/rm -rf /etc` runs is the same one `rm -rf /etc` runs, and
// `/usr/bin/rg --pre=…` is the same ripgrep as `rg --pre=…`. Matching only the
// literal head scored both of those medium. So the dangerous tables are
// matched against the basename as well.
//
// The benign tables (safeRead/buildTest) deliberately do NOT get the basename
// treatment. `./ls` may be a script the model wrote into the repo a moment
// ago, so a path-qualified name must never *buy* a lower class than the bare
// name — basename matching escalates only. The cost is that `/bin/ls` scores
// medium instead of low, which is a prompt, not a block.
func (s *Sandbox) headRisk(tokens []string) event.Risk {
	head := tokens[0]
	base := filepath.Base(head)
	// eval/source (and its `.` alias) run text as code — sub-execution by
	// another name. Matched on the head so `ls;eval foo` cannot slip past on
	// the absence of a leading space.
	if head == "." || base == "eval" || base == "source" {
		return RiskCritical
	}
	// Shell interpreters with -c are sub-execution gateways too.
	if (shellInterpreter[head] || shellInterpreter[base]) && hasFlag(tokens, "-c") {
		return RiskCritical
	}
	// Network commands are high unless the host is allowlisted (L7-005 gates
	// the actual connection; Classify flags the risk for HITL).
	if networkCmds[head] || networkCmds[base] {
		return RiskHigh
	}
	if diskHeavy[head] || diskHeavy[base] ||
		(base == "rm" && hasRecursiveForce(tokens) && len(tokens) > 2) {
		return RiskCritical
	}
	if mutatingFS[head] || mutatingFS[base] {
		return RiskMedium
	}
	if safeRead[head] || buildTest[head] {
		return RiskLow
	}
	// Unknown command: default to medium (local side effects assumed) so the
	// HITL gate prompts rather than silently running an unvetted command.
	return RiskMedium
}

// hasShellEscape reports whether c contains a substitution construct whose
// contents we cannot statically classify: `$(…)`, backticks, and bash process
// substitution `<(…)` / `>(…)`. Their bodies are commands we never get to see,
// so the whole string is denied. (`eval`/`source` are handled per-segment in
// classifySegment, where a head match is exact instead of a substring guess.)
// A real parser would tokenize properly; this conservative match errs on deny.
func hasShellEscape(c string) bool {
	return strings.Contains(c, "$(") || strings.Contains(c, "`") ||
		strings.Contains(c, "<(") || strings.Contains(c, ">(")
}

// splitSegments splits a shell command string into the individual commands it
// will actually run. Separators are the unquoted, unescaped control operators
// `;`, `|`, `&`, a newline, and the subshell delimiters `(` / `)`. Both bytes
// of `&&` and `||` flush; the empty segment between them is dropped.
//
// Two things are deliberately NOT separators. Quoted and backslash-escaped
// text is data (`ls "a; b"` is one command with an odd filename), and an `&`
// that trails a redirection operator (`2>&1`) is part of its word — the common
// `go test 2>&1` must not be shredded into a bogus `1` segment.
//
// `&>` IS a separator; see the `case ch == '&'` arm for why.
func splitSegments(c string) []string {
	var segs []string
	var cur strings.Builder
	var prev byte // last non-space byte written to cur, to spot `>&` / `<&`

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
		prev = 0
	}
	write := func(b byte) {
		cur.WriteByte(b)
		if b != ' ' && b != '\t' {
			prev = b
		}
	}

	var quote byte // 0 outside quotes, else '\'' or '"'
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch {
		case quote != 0:
			// Inside quotes everything is data; a backslash inside "…" still
			// escapes the next byte (and can hide the closing quote).
			if ch == '\\' && quote == '"' && i+1 < len(c) {
				write(ch)
				i++
				write(c[i])
				continue
			}
			if ch == quote {
				quote = 0
			}
			write(ch)
		case ch == '\'' || ch == '"':
			quote = ch
			write(ch)
		case ch == '\\' && i+1 < len(c):
			write(ch)
			i++
			write(c[i])
		case ch == '&':
			// `2>&1` is a redirection: the `&` trails its `>`/`<`, so it stays.
			//
			// `&>` does NOT stay, even though bash reads it as one redirection
			// token. bash.go runs commands through `sh -c`, and on a host where
			// /bin/sh is dash (Debian/Ubuntu) POSIX tokenization applies: `&` is
			// a control operator that ENDS the command, and `>` starts a new one
			// with a leading redirection. Exempting `&>` collapsed
			// `ls &>out rm -rf /` into a single `ls` segment — RiskLow, no HITL
			// prompt, and dash then ran the `rm`. There is no OS sandbox behind
			// this classifier, so that was a full filesystem escape.
			//
			// A classifier whose correctness depends on which /bin/sh the host
			// ships is broken by construction on the other host, so pick the
			// tokenization that errs safely on both: splitting here
			// over-classifies on a bash host (`go test &>log` becomes a Medium
			// the user can approve) and under-classifies on neither. Do not
			// restore the exemption as an ergonomics fix.
			if prev == '>' || prev == '<' {
				write(ch)
				continue
			}
			flush()
		case ch == ';' || ch == '|' || ch == '\n' || ch == '\r' || ch == '(' || ch == ')':
			flush()
		default:
			write(ch)
		}
	}
	flush()
	return segs
}

// peelPrefixes strips everything standing in front of the real command word so
// a prefix cannot launder a dangerous command (File 08 §8.4.3 "peels
// wrappers"). It returns the remaining tokens and a risk floor for what it
// peeled, since some prefixes are a side effect in their own right.
//
// Four kinds of prefix:
//
//   - Shell keywords and group punctuation a segment can begin with once
//     splitSegments has cut on the operators — `{ rm -rf /; }` arrives as
//     `{ rm -rf /`, `if true; then rm -rf /; fi` arrives as `then rm -rf /`.
//   - Leading redirections. A redirection may precede the command word
//     (`>out rm -rf /etc` runs `rm -rf /etc`), and that is exactly the shape
//     dash produces from a `&>` payload once splitSegments cuts on the `&`.
//     Without this the head is `>out`, an unknown command scoring medium, and
//     the `rm -rf` behind it is never looked at.
//   - Assignments. `FOO=1 rm -rf /etc` is a valid command with a prefix
//     assignment; the head used to read as `FOO=1`.
//   - Wrappers (sudo/env/timeout/nice/…) AND their own options. Peeling only
//     the wrapper name is what the reviewer walked past: `sudo -u root rm -rf
//     /etc` peeled `sudo`, saw the head `-u`, gave up, and scored medium — the
//     one wrapper this function exists for, defeated by handing it a flag. A
//     wrapper's flags, the operand a flag like `-u` takes, and the wrapper's
//     own positional operands (timeout's DURATION) all have to be stepped over.
//
// A segment that is nothing but keywords (`fi`, `done`) peels to empty and
// scores low.
func peelPrefixes(tokens []string) ([]string, event.Risk) {
	floor := RiskLow
	for len(tokens) > 0 {
		head := tokens[0]
		switch {
		case shellKeywords[head]:
			tokens = tokens[1:]
		case isRedirection(head):
			n := 1
			if redirectionTakesNextWord(head) {
				n = 2
			}
			if n > len(tokens) {
				n = len(tokens)
			}
			tokens = tokens[n:]
			// A `>` redirection creates or truncates a file at a path nothing
			// confines, so even a segment that is *only* a redirection is a
			// filesystem mutation and must not score low. A `<` read does not.
			if strings.ContainsRune(head, '>') {
				floor = maxRisk(floor, RiskMedium)
			}
		case isAssignment(head):
			tokens = tokens[1:]
		default:
			spec, ok := wrappers[filepath.Base(head)]
			if !ok {
				return tokens, floor
			}
			tokens = spec.peel(tokens)
			floor = maxRisk(floor, spec.floor)
		}
	}
	return tokens, floor
}

// wrapperSpec describes how to step over a wrapper's own argv to reach the
// command it wraps.
type wrapperSpec struct {
	// valueFlags are the options that consume the FOLLOWING token. Getting
	// this list wrong in the greedy direction would eat the command word, so
	// only options certain to take a separate value belong here.
	valueFlags map[string]bool
	// operands is how many of the wrapper's own positional words come before
	// the command (timeout's DURATION, chroot's NEWROOT).
	operands int
	// floor is the risk the wrapper carries whatever it wraps.
	floor event.Risk
}

// peel consumes the wrapper name, its options (and their values), and its own
// positional operands, returning the tokens from the wrapped command onward.
// It always consumes at least the name, so peelPrefixes cannot spin.
func (w wrapperSpec) peel(tokens []string) []string {
	tokens = tokens[1:] // the wrapper name itself
	operands := w.operands
	for len(tokens) > 0 {
		t := tokens[0]
		switch {
		case t == "--":
			return tokens[1:] // end of the wrapper's options
		case len(t) > 1 && strings.HasPrefix(t, "-"):
			// `--user=root` carries its value; `-u root` takes the next word.
			if w.valueFlags[flagName(t)] && !strings.Contains(t, "=") && len(tokens) > 1 {
				tokens = tokens[1:]
			}
			tokens = tokens[1:]
		case operands > 0:
			operands--
			tokens = tokens[1:]
		default:
			return tokens
		}
	}
	return tokens
}

// flagName strips an attached value so `--pre=CMD` and `--user=root` look up
// as `--pre` and `--user`.
func flagName(t string) string {
	if i := strings.IndexByte(t, '='); i >= 0 {
		return t[:i]
	}
	return t
}

// isRedirection reports whether tok opens a redirection: an optional file
// descriptor number followed by `<` or `>` (`>out`, `>>log`, `<in`, `2>&1`).
func isRedirection(tok string) bool {
	i := 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	return i < len(tok) && (tok[i] == '<' || tok[i] == '>')
}

// redirectionTakesNextWord reports whether tok is the bare operator, so its
// target is the following word (`> out`) rather than attached (`>out`, `2>&1`).
func redirectionTakesNextWord(tok string) bool {
	i := 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	for i < len(tok) && strings.IndexByte("<>&|", tok[i]) >= 0 {
		i++
	}
	return i == len(tok)
}

// isAssignment reports whether tok is a `NAME=value` prefix assignment rather
// than a command word. The name must be a shell identifier, so a command whose
// path happens to contain `=` is not mistaken for one.
func isAssignment(tok string) bool {
	i := strings.IndexByte(tok, '=')
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := tok[j]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case j > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// hasRecursiveForce reports whether an rm argv asks for a recursive forced
// delete, in any spelling the tool accepts: `-rf`, `-fr`, `-r -f`, `-Rf`,
// `--recursive --force`. Matching the literal string "-rf" left `rm -fr /etc`
// scoring medium — the same laundering as the wrapper bypass, one flag-shuffle
// away.
func hasRecursiveForce(tokens []string) bool {
	var recursive, force bool
	for _, t := range tokens[1:] {
		if t == "--" {
			break
		}
		if len(t) < 2 || !strings.HasPrefix(t, "-") {
			continue
		}
		if strings.HasPrefix(t, "--") {
			switch flagName(t) {
			case "--recursive":
				recursive = true
			case "--force":
				force = true
			}
			continue
		}
		for _, c := range t[1:] {
			switch c {
			case 'r', 'R':
				recursive = true
			case 'f':
				force = true
			}
		}
	}
	return recursive && force
}

// searchFlagRisk grades a content-search command by what its argv actually
// asks for. `grep` sits in the safe-read table because reading is all the
// name promises, but a search tool's flags are an execution surface: ripgrep's
// `--pre=CMD` runs CMD over every candidate file (arbitrary code execution
// wearing a search command's costume), `find -exec` is the classic form, and
// `find -delete` destroys without spawning anything at all. The Grep *tool*
// was hardened against a flag-shaped pattern in the same sprint; a shell
// `rg --pre=… foo` composed by the model would have walked straight around
// that fix at RiskLow, because Classify only ever looked at the head.
//
// The ladder: a program-executing or destructive flag is critical; any other
// flag-shaped operand is medium, because we cannot enumerate every flag of
// every search tool and "unknown flag on a tool that has RCE flags" is not
// something to run silently; a search with no flags at all stays low.
// Returns RiskLow for any command that is not a search tool, so the caller can
// take it as a pure escalation.
//
// Tradeoff: this makes the everyday `grep -rn foo .` prompt instead of running
// silently. That is the cost of not having an argv boundary here — Classify
// sees a string, not an argv, so the alternative is an allowlist of every
// benign flag of every search tool, which fails open on the ones we forget.
// Erring toward a prompt is recoverable; erring toward silent RCE is not.
func searchFlagRisk(tokens []string) event.Risk {
	// Basename, so `/usr/bin/rg --pre=…` and `./rg --pre=…` are recognized as
	// the ripgrep they are; a path prefix used to skip this scan entirely.
	if len(tokens) == 0 || !searchCmds[filepath.Base(tokens[0])] {
		return RiskLow
	}
	risk := RiskLow
	for _, t := range tokens[1:] {
		if t == "--" {
			break // end-of-flags sentinel: the rest are literal operands
		}
		if len(t) < 2 || !strings.HasPrefix(t, "-") {
			continue // an operand, or a bare "-" meaning stdin
		}
		name := t
		if i := strings.Index(name, "="); i >= 0 {
			name = name[:i] // --pre=CMD → --pre
		}
		if searchExecFlags[name] {
			return RiskCritical
		}
		risk = RiskMedium
	}
	return risk
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

	// searchCmds are the content-search tools searchFlagRisk re-examines.
	// grep and find also sit in safeRead: the head match gives them their
	// baseline, the flag scan can only push it up.
	searchCmds = set("grep", "egrep", "fgrep", "rg", "ripgrep", "ag", "ack", "find")
	// searchExecFlags are the flags on those tools that run another program
	// (or, for find's -delete, destroy files outright). "=" values are
	// stripped before the lookup, so --pre and --pre=CMD both match.
	searchExecFlags = set("--pre", "--hostname-bin", "--pager",
		"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprintf")

	// shellKeywords are the keywords and group punctuation a segment can begin
	// with once splitSegments has cut on the control operators.
	shellKeywords = set("{", "}", "!", "if", "then", "elif", "else", "fi",
		"while", "until", "do", "done", "case", "esac")

	// wrappers are the commands that exist to run ANOTHER command, keyed by
	// basename. The old list held only sudo/env/time/nohup, so `timeout 5 rm
	// -rf /etc` and five siblings scored medium on a head that was never the
	// command. Each entry says how to step over the wrapper's own argv; see
	// wrapperSpec for why valueFlags must stay conservative.
	//
	// sudo/doas/chroot carry a medium floor: changing user or root directory is
	// never something to run silently, however harmless the wrapped command
	// looks after peeling.
	wrappers = map[string]wrapperSpec{
		"sudo": {floor: RiskMedium, valueFlags: set(
			"-u", "--user", "-g", "--group", "-U", "-C", "--close-from",
			"-p", "--prompt", "-r", "--role", "-t", "--type",
			"-T", "--command-timeout", "-h", "--host", "-R", "--chroot",
			"-D", "--chdir")},
		"doas":   {floor: RiskMedium, valueFlags: set("-u", "-C")},
		"chroot": {floor: RiskMedium, operands: 1, valueFlags: set("--userspec", "--groups")},

		"env":     {valueFlags: set("-u", "--unset", "-C", "--chdir", "-S", "--split-string")},
		"time":    {valueFlags: set("-o", "--output", "-f", "--format")},
		"nohup":   {},
		"setsid":  {},
		"command": {},
		"builtin": {},
		"exec":    {},
		"busybox": {},
		"timeout": {operands: 1, valueFlags: set("-s", "--signal", "-k", "--kill-after")},
		"nice":    {valueFlags: set("-n", "--adjustment")},
		"ionice":  {valueFlags: set("-c", "--class", "-n", "--classdata", "-p", "--pid")},
		"stdbuf":  {valueFlags: set("-i", "--input", "-o", "--output", "-e", "--error")},
		"watch":   {valueFlags: set("-n", "--interval")},
		"strace":  {valueFlags: set("-o", "-e", "-p", "-s", "-E")},
		"ltrace":  {valueFlags: set("-o", "-e", "-p", "-s")},
	}
)

// set builds a lookup map from its arguments.
func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
