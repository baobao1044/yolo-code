// Tests for the rename retry loop that lets writeJSON replace a file on
// Windows (rename_windows.go). They drive renamer.do with stub collaborators
// rather than a real filesystem, which is the only way to reach the loop at
// all on Unix: there renameBlocked is constantly false and renameRetries is 0,
// so the production wiring runs exactly one attempt. Without these, the
// arithmetic would be exercised only by whichever CI job happened to have a
// reader racing a writer — which is precisely how the bug got in.
//
// What these do NOT prove is the errno mapping: whether Windows really answers
// a blocked replace with code 5 rather than 32. That came from CI
// (build-and-test (windows-latest) reporting "Access is denied" from
// os.Rename) and only Windows can confirm it.

package memory

import (
	"errors"
	"testing"
	"time"
)

// errBlocked is what the stubs report when they want to be retried; the tests
// pair it with a predicate that recognises exactly it, standing in for
// renameBlocked's errno check.
var errBlocked = errors.New("stub: destination held open")

func isErrBlocked(err error) bool { return errors.Is(err, errBlocked) }

// failNTimes returns a rename stub that reports errBlocked for its first n
// calls and then succeeds, along with a pointer to its call count.
func failNTimes(n int) (func(string, string) error, *int) {
	calls := 0
	return func(string, string) error {
		calls++
		if calls <= n {
			return errBlocked
		}
		return nil
	}, &calls
}

// TestRenamerSucceedsOnTheFirstAttemptWithoutWaiting pins the common case: a
// rename that works costs exactly one call and no sleep. A loop that always
// slept once before returning would pass every other test here.
func TestRenamerSucceedsOnTheFirstAttemptWithoutWaiting(t *testing.T) {
	rename, calls := failNTimes(0)
	// A backoff long enough to measure but short enough that a loop which
	// wrongly sleeps fails in a quarter second instead of hanging. (An
	// hour-long tripwire was the first draft; a test whose failure mode is a
	// hang reports nothing useful.)
	r := renamer{rename: rename, blocked: isErrBlocked, retries: 50, backoff: 250 * time.Millisecond}

	start := time.Now()
	if err := r.do("from", "to"); err != nil {
		t.Fatalf("do() = %v, want nil", err)
	}
	if *calls != 1 {
		t.Errorf("rename called %d times, want 1", *calls)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("success path slept %v, want none", elapsed)
	}
}

// TestRenamerRetriesWhileBlockedThenSucceeds pins the point of the loop: a
// destination that is held open briefly is waited out, not reported.
func TestRenamerRetriesWhileBlockedThenSucceeds(t *testing.T) {
	rename, calls := failNTimes(3)
	r := renamer{rename: rename, blocked: isErrBlocked, retries: 50, backoff: 0}

	if err := r.do("from", "to"); err != nil {
		t.Fatalf("do() = %v, want nil after the handle closes", err)
	}
	if *calls != 4 {
		t.Errorf("rename called %d times, want 4 (3 blocked + 1 success)", *calls)
	}
}

// TestRenamerStopsAtTheBudgetAndReportsTheLastError pins both halves of giving
// up: the budget is retries+1 attempts (not retries, and not unbounded), and
// the error handed back is the filesystem's own rather than a synthesised
// timeout — the caller should see what actually went wrong.
func TestRenamerStopsAtTheBudgetAndReportsTheLastError(t *testing.T) {
	calls := 0
	alwaysBlocked := func(string, string) error {
		calls++
		return errBlocked
	}
	r := renamer{rename: alwaysBlocked, blocked: isErrBlocked, retries: 5, backoff: 0}

	err := r.do("from", "to")
	if !errors.Is(err, errBlocked) {
		t.Errorf("do() = %v, want the last attempt's own error", err)
	}
	if calls != 6 {
		t.Errorf("rename called %d times, want 6 (initial attempt + 5 retries)", calls)
	}
}

// TestRenamerDoesNotRetryAnUnrelatedError pins the narrowness, and is the
// assertion most worth having: a rename that fails because the source is
// missing or the disk is full must fail immediately. Widening blocked() to
// "any error" would still pass every test above, and would turn every real
// failure into a 250ms pause before the same failure.
func TestRenamerDoesNotRetryAnUnrelatedError(t *testing.T) {
	notBlocked := errors.New("no such file or directory")
	calls := 0
	rename := func(string, string) error {
		calls++
		return notBlocked
	}
	// Zero backoff so a widened predicate fails on the call count immediately
	// rather than after 50 sleeps.
	r := renamer{rename: rename, blocked: isErrBlocked, retries: 50, backoff: 0}

	if err := r.do("from", "to"); !errors.Is(err, notBlocked) {
		t.Errorf("do() = %v, want the original error unwrapped", err)
	}
	if calls != 1 {
		t.Errorf("rename called %d times, want 1 (a non-blocking error is final)", calls)
	}
}

// TestShareRenamerIsAPlainRenameOffWindows pins the platform contract from the
// Unix side: the production policy must add nothing here. If renameRetries
// ever became non-zero on Unix, every writeJSON failure would start costing a
// wait for a condition that cannot occur.
func TestShareRenamerIsAPlainRenameOffWindows(t *testing.T) {
	r := shareRenamer()
	if r.blocked(errBlocked) {
		// True on Windows, where the constants below are also non-zero.
		t.Skip("windows: the retry policy is supposed to be active")
	}
	if r.retries != 0 {
		t.Errorf("renameRetries = %d on a platform where nothing blocks a rename, want 0", r.retries)
	}
}
