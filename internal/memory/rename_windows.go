//go:build windows

package memory

import (
	"errors"
	"syscall"
	"time"
)

// Windows cannot replace a file that another handle still holds open, and Go's
// own readers are such handles: syscall.Open asks for FILE_SHARE_READ |
// FILE_SHARE_WRITE and *not* FILE_SHARE_DELETE (syscall/syscall_windows.go),
// so every os.ReadFile in flight blocks the MoveFileEx underneath os.Rename.
// Only os.Root opts into delete-sharing. Unix has no equivalent window —
// rename unlinks the target and readers keep reading the inode they opened.
//
// So the atomic write in writeJSON, which is correct on Unix, traded one bug
// for another here: it stopped exposing torn files and started failing with
// "Access is denied" whenever a reader happened to overlap. The window is
// short — a reader's handle lives only as long as the read — so waiting it out
// is enough.
//
// 50 attempts × 5ms caps the wait at 250ms. That is also what a *genuinely*
// denied write now costs before it is reported, which is the price of not
// being able to tell the two apart from the errno alone; for a store save it
// is not a price worth optimising. The error the caller finally sees is the
// one from the last attempt, not a synthesised timeout.
const (
	renameRetries = 50
	renameBackoff = 5 * time.Millisecond
)

// renameBlocked reports whether err is Windows saying "someone still has a
// handle open" rather than "you may not do this". os.Rename wraps the errno in
// an *os.LinkError, whose Unwrap makes errors.Is see through it.
//
// This deliberately includes ERROR_ACCESS_DENIED, which the read-side retry in
// isLockedByAnotherProcess (internal/session) deliberately excludes. The two
// are not inconsistent: a blocked *replace* is what CI actually observed
// reporting code 5, because the obstruction is the destination handle rather
// than the file's permissions, while a blocked *read* reporting code 5 is far
// more likely to be a file the process really may not read — retrying that one
// would only deliver a permanent fault late.
func renameBlocked(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || // destination held open
		errors.Is(err, errSharingViolation) ||
		errors.Is(err, errLockViolation)
}

// Not named constants in package syscall, which declares only the handful of
// codes the stdlib itself branches on.
const (
	errSharingViolation = syscall.Errno(32) // source held open by another process
	errLockViolation    = syscall.Errno(33) // byte-range lock held
)
