// Unix process-group setup and kill (File 08 §8.4.7). The runtime only
// cancels the context; this file translates that into the correct group-kill
// on Unix: the shell runs in its own process group (Setpgid), and cancel
// sends SIGTERM to the whole group before killing the parent, so any children
// (backgrounded jobs) die with it — "no orphaned child after exit".
//
// Unix has no Job Object; the process group + kill(-pgid) is the native tree
// kill, so the job helpers are no-ops here (Bash.Run calls them uniformly).

//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

// jobHandle is a no-op placeholder on Unix (no Job Object); 0 means "none".
type jobHandle struct{}

// setProcessGroup makes cmd the leader of a new process group so its children
// can be signalled together (File 08 §8.4.7).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// prepareJob is a no-op on Unix (the process group is the native tree-kill
// mechanism). Returns a zero handle.
func prepareJob() (jobHandle, error) { return jobHandle{}, nil }

// assignJobToProcess is a no-op on Unix (Setpgid already groups the children).
func assignJobToProcess(_ jobHandle, _ int) error { return nil }

// closeJob is a no-op on Unix (killGroup handles the tree kill).
func closeJob(_ jobHandle) {}

// killGroup sends SIGTERM to the process group led by cmd, then kills the
// parent (File 08 §8.4.7). The group signal is what kills children; the
// parent kill is a backstop. Errors are ignored because the process may
// already be dead by the time we signal (cancel raced with exit).
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		pgid, _ := syscall.Getpgid(cmd.Process.Pid)
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		return cmd.Process.Kill()
	}
	return nil
}

// isWindows reports the host OS (used by bash.go to pick the shell). Always
// false on this build.
func isWindows() bool { return false }
