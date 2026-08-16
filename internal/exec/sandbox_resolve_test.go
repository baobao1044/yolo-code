// Containment and classifier regressions handed over from the grep/edit_file
// review. All three land on the shared gate in sandbox.go:
//
//   1. Resolve let a symlinked *ancestor* through whenever the target did not
//      exist yet — the whole-path EvalSymlinks failed and the fallback was a
//      textual Rel check that a symlink is invisible to.
//   2. The same textual check rejected a real file named "..foo" as an escape.
//   3. Classify called a shell `grep`/`rg` RiskLow on its head alone, so
//      `rg --pre=CMD` — arbitrary execution — ran silently.

package exec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

func TestResolveRejectsSymlinkedAncestor(t *testing.T) {
	s := newSandbox(t)

	// A symlink inside the root pointing at a directory outside it. The target
	// file does not exist, which is exactly when the old whole-path
	// EvalSymlinks gave up and the textual check waved "link/new.txt" through.
	outside := t.TempDir()
	link := filepath.Join(s.root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}

	for _, p := range []string{
		"link/new.txt",              // one missing component past the link
		"link/deeper/still/new.txt", // several missing components
		filepath.Join(link, "new.txt"),
	} {
		if got, err := s.Resolve(p); err != ErrPathEscapes {
			t.Fatalf("Resolve(%q) = (%q, %v), want ErrPathEscapes — the ancestor is a symlink out of the root", p, got, err)
		}
	}
}

func TestResolveAllowsMissingPathUnderRealAncestor(t *testing.T) {
	s := newSandbox(t)

	// The legitimate half of the same code path: a file that does not exist
	// yet, under a directory that does, must still resolve.
	got, err := s.Resolve("sub/new.txt")
	if err != nil {
		t.Fatalf("Resolve(sub/new.txt) = %v, want nil (missing file under a real, contained dir)", err)
	}
	if !withinRoot(s.root, got) {
		t.Fatalf("Resolve(sub/new.txt) = %q, want a path under root %q", got, s.root)
	}
}

func TestResolveAllowsFileNamedDotDotPrefix(t *testing.T) {
	s := newSandbox(t)

	// "..foo" is a perfectly ordinary filename. The old HasPrefix(rel, "..")
	// check read it as a traversal and refused to open it.
	name := "..foo"
	if err := os.WriteFile(filepath.Join(s.root, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(name)
	if err != nil {
		t.Fatalf("Resolve(%q) = %v, want nil — a file whose name merely starts with '..' is not an escape", name, err)
	}
	if filepath.Base(got) != name {
		t.Fatalf("Resolve(%q) = %q, want it to end in %q", name, got, name)
	}

	// A directory named "..bar" must work as a parent too.
	if err := os.Mkdir(filepath.Join(s.root, "..bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("..bar/x.txt"); err != nil {
		t.Fatalf("Resolve(..bar/x.txt) = %v, want nil", err)
	}

	// The real traversal must still be refused.
	if _, err := s.Resolve("../outside.txt"); err != ErrPathEscapes {
		t.Fatalf("Resolve(../outside.txt) = %v, want ErrPathEscapes", err)
	}
}

func TestClassifySearchToolFlags(t *testing.T) {
	s := newSandbox(t)

	tests := []struct {
		name string
		cmd  string
		want event.Risk
	}{
		// Program-executing flags are RCE in a search command's costume.
		{"rg --pre joined", "rg --pre=/tmp/evil foo", RiskCritical},
		{"rg --pre separate", "rg --pre /tmp/evil foo", RiskCritical},
		{"rg --hostname-bin", "rg --hostname-bin=/tmp/evil foo", RiskCritical},
		{"find -exec", "find . -name x -exec /tmp/evil {} ;", RiskCritical},
		{"find -execdir", "find . -execdir /tmp/evil {} ;", RiskCritical},
		{"find -delete", "find . -name '*.go' -delete", RiskCritical},
		{"ack --pager", "ack --pager=/tmp/evil foo", RiskCritical},
		{"hidden in a compound tail", "ls; rg --pre=/tmp/evil foo", RiskCritical},

		// Any other flag on a tool that has RCE flags: prompt, don't run blind.
		{"grep -rn", "grep -rn foo .", RiskMedium},
		{"grep --include", "grep --include=*.go -r foo .", RiskMedium},
		{"find -name", "find . -name '*.go'", RiskMedium},

		// No flags at all: unchanged, still the safe read it looks like.
		{"plain grep", "grep foo file.txt", RiskLow},
		{"plain find", "find .", RiskLow},
		{"pipeline of plain searches", "cat a | grep foo", RiskLow},
		{"end-of-flags sentinel", "grep -- --pre file.txt", RiskLow},
		{"bare dash is stdin", "grep foo -", RiskLow},

		// Escalation only: a search tool the head tables already rate higher
		// must not be talked down by a flagless argv.
		{"rg is not in the safe-read table", "rg foo", RiskMedium},

		// Non-search commands keep their existing flag-blind treatment.
		{"ls with flags stays low", "ls -la", RiskLow},
		{"head with flags stays low", "head -n 20 file.txt", RiskLow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Classify(tt.cmd); got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
