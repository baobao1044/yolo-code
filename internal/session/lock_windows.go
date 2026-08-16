//go:build windows

package session

import (
	"errors"
	"syscall"
)

// Windows lock errnos. Not named constants in syscall, so they are spelled out
// with the message the user would otherwise see verbatim.
const (
	errorSharingViolation = syscall.Errno(32) // "used by another process"
	errorLockViolation    = syscall.Errno(33) // byte-range lock held
)

// isLockedByAnotherProcess reports whether err is Windows' transient
// "somebody else has this file open" — the state a reader lands in while
// MoveFileEx is replacing the destination. See readJSON for why that is worth
// waiting out rather than reporting.
//
// Deliberately just these two. ERROR_ACCESS_DENIED also appears during some
// replace windows, but it is likewise how a genuinely unreadable file answers,
// and retrying it would turn a permanent permission fault into the same fault
// delivered late.
func isLockedByAnotherProcess(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}
