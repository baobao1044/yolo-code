// Tests for the portable process-group kill (File 08 §8.4.7) and the Bash
// built-in (File 08 §8.1.3 / §8.4.3). Bash runs a shell command under the
// sandbox's command classifier; cancelling a command's context kills the
// whole process tree (the shell and any children), not just the parent — the
// "no orphaned child after exit" guarantee (File 08 §8.4.7).
//
// The tree-kill test spawns a shell that launches a grandchild scheduled to
// write a marker file *after* we cancel. If the tree is killed, the marker is
// never written; if only the parent dies, the grandchild survives and writes
// it. Wide margins (the grandchild writes at 3s, we assert at ~5s) make the
// test robust on a slow CI box while still catching a tree-kill regression.

package exec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

func newBash(t *testing.T) *Bash {
	t.Helper()
	root := t.TempDir()
	return NewBash(&Sandbox{root: root, cwd: root})
}

func TestBashAllowlistedCommandRuns(t *testing.T) {
	bash := newBash(t)

	out, err := bash.Run(context.Background(), ToolInput{Args: []byte(`{"command":"echo hi"}`)})
	if err != nil {
		t.Fatalf("Bash(echo hi) = %v, want nil", err)
	}
	if !strings.Contains(out.Stdout, "hi") {
		t.Fatalf("Bash stdout = %q, want it to contain 'hi'", out.Stdout)
	}
}

func TestBashDeniesCriticalCommand(t *testing.T) {
	bash := newBash(t)

	// rm -rf / is classified critical (File 08 §8.4.3); Bash must refuse to
	// even spawn it (critical is explicitly denied, §8.5.1).
	_, err := bash.Run(context.Background(), ToolInput{Args: []byte(`{"command":"rm -rf /"}`)})
	if err == nil {
		t.Fatal("Bash(rm -rf /) = nil, want deny error (critical command refused)")
	}
}

func TestCancelKillsParent(t *testing.T) {
	bash := newBash(t)

	// A long-running single command. Cancel after a short delay; Bash.Run must
	// return a ctx error promptly and the process must be gone (deterministic,
	// no marker file needed — the parent is directly killed).
	cmd := longSleepCommand()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := bash.Run(ctx, ToolInput{Args: []byte(`{"command":"` + cmd + `"}`)})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Bash.Run on cancelled ctx returned nil, want ctx error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Bash.Run did not return after cancel — parent kill not honored")
	}
}

func TestCancelKillsProcessGroup(t *testing.T) {
	bash := newBash(t)

	// Spawn a shell that launches a *grandchild* which writes a marker file
	// after 3s, then the shell sleeps long enough to outlive the grandchild's
	// scheduled write. We cancel at 100ms. If the process group is killed, the
	// grandchild dies before its 3s write and the marker is absent at ~5s; if
	// only the parent dies, the grandchild survives, writes the marker, and
	// the test fails (the bug we're guarding against).
	marker := filepath.Join(t.TempDir(), "marker")
	spawn := groupSpawnScript(marker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := bash.Run(ctx, ToolInput{Args: []byte(`{"command":"` + spawn + `"}`)})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done // Bash.Run returns once the parent (and its group) is killed.

	// Wait past the grandchild's scheduled write (3s) plus margin for a slow
	// box, then assert the marker was never written — the tree was killed.
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker file was written — the grandchild survived cancel; tree-kill is broken")
	}
}

// longSleepCommand returns a shell command that sleeps ~30s on its own (no
// child), per-platform. Used to assert the parent itself is killed on cancel.
func longSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1 > nul"
	}
	return "sleep 30"
}

// groupSpawnScript returns a shell script that launches a background
// grandchild which writes marker after 3s, then sleeps 30s. The grandchild is
// the orphan we assert does NOT survive a group kill.
func groupSpawnScript(marker string) string {
	if runtime.GOOS == "windows" {
		// cmd: start a grandchild that pings (sleeps) and writes the marker
		// after ~3s. `ping -n 4` ≈ 3s on Windows.
		return `cmd /c "(ping -n 4 127.0.0.1 > nul & echo > ` + marker + `) & ping -n 30 127.0.0.1 > nul"`
	}
	// sh: background a grandchild that waits 3s then writes the marker; the
	// shell itself sleeps 30s so it doesn't exit before the grandchild.
	return `sh -c "(sleep 3 && touch ` + marker + `) & sleep 30"`
}

// keep event import used across tickets that accrete here.
var _ = event.Risk("")
