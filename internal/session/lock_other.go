//go:build !windows

package session

// isLockedByAnotherProcess is always false off Windows: POSIX rename swaps the
// directory entry with no window in which the destination is unopenable, so a
// reader arriving mid-replace gets the old file or the new one and never an
// error. readJSON's retry therefore never runs here, which is the intent —
// nothing on this platform should pay for a Windows-only hazard.
func isLockedByAnotherProcess(error) bool { return false }
