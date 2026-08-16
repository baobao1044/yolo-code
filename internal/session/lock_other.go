//go:build !windows

package session

import "time"

// isLockedByAnotherProcess is always false off Windows: POSIX rename swaps the
// directory entry with no window in which the destination is unopenable, so a
// reader arriving mid-replace gets the old file or the new one and never an
// error. readJSON's retry therefore never runs here, which is the intent —
// nothing on this platform should pay for a Windows-only hazard.
func isLockedByAnotherProcess(error) bool { return false }

// The write side of the same asymmetry: POSIX rename replaces a destination
// that readers hold open, so there is nothing to wait out and writeJSON's
// renamer makes exactly one attempt. See lock_windows.go.
const (
	renameRetries = 0
	renameBackoff = time.Duration(0)
)

func renameBlocked(error) bool { return false }
