// Suicide regression for the Unix group kill (defect 4.15). killGroup signals
// `-pgid`, and pgid came from Getpgid(child) with the error ignored. If
// Setpgid was never set on the child (a caller that skipped setProcessGroup,
// or a setpgid that lost the race with the child's exec), Getpgid returns
// *our own* pgid — and kill(-pgid, SIGTERM) takes down the agent itself. On a
// Getpgid error it returned 0, and kill(-0) is "signal my own group" too.
//
// Proving that safely needs isolation: the bug signals the whole process
// group, which under `go test` is the test binary, the go tool, and the shell
// that started them. So the scenario runs in a re-exec of this test binary
// placed in its own session (Setsid), and the outer test only observes how the
// helper exited — killed by SIGTERM (bug) or exit 0 (guard held).

//go:build !windows

package exec

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// killGroupHelperEnv marks the re-exec'd subprocess half of the test below.
const killGroupHelperEnv = "YOLO_KILLGROUP_HELPER"

func TestKillGroupNeverSignalsOwnGroup(t *testing.T) {
	if os.Getenv(killGroupHelperEnv) == "1" {
		t.Skip("outer test does not run inside the helper subprocess")
	}

	helper := exec.Command(os.Args[0], "-test.run=TestKillGroupHelperSuicide", "-test.v")
	helper.Env = append(os.Environ(), killGroupHelperEnv+"=1")
	// Own session (and therefore own process group) so the blast radius of a
	// regression is the helper alone, not this test run.
	helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	out, err := helper.CombinedOutput()
	if err != nil {
		t.Fatalf("helper exited with %v — killGroup signalled its own process group.\n%s", err, out)
	}
}

// TestKillGroupHelperSuicide is the subprocess half: it starts a child WITHOUT
// giving it a process group of its own — exactly the state a failed or skipped
// Setpgid leaves us in — then calls killGroup. With the guard it survives and
// the binary exits 0; without it the group SIGTERM kills this process and the
// parent sees a signal exit.
func TestKillGroupHelperSuicide(t *testing.T) {
	if os.Getenv(killGroupHelperEnv) != "1" {
		t.Skip("only runs as the subprocess of TestKillGroupNeverSignalsOwnGroup")
	}

	cmd := exec.Command("sleep", "5")
	// Deliberately no setProcessGroup(cmd): the child shares our group.
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	if err := killGroup(cmd); err != nil {
		t.Fatalf("killGroup: %v", err)
	}
	_ = cmd.Wait()
}

// signalableGroup is the guard itself; check its edges directly so the
// forbidden pgids are pinned even on a host where the subprocess test is
// skipped.
func TestSignalableGroupRejectsSelfAndReservedPgids(t *testing.T) {
	own := syscall.Getpgrp()

	for _, pgid := range []int{-1, 0, 1, own} {
		if signalableGroup(pgid) {
			t.Fatalf("signalableGroup(%d) = true, want false (own group / reserved pgid)", pgid)
		}
	}
	// A pgid that is neither ours nor reserved must still be signalable —
	// otherwise the tree kill silently stops working.
	foreign := own + 1
	if foreign <= 1 {
		foreign = 2
	}
	if !signalableGroup(foreign) {
		t.Fatalf("signalableGroup(%d) = false, want true (a real foreign group)", foreign)
	}
}
