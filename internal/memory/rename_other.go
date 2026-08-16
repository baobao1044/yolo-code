//go:build !windows

package memory

import "time"

// Unix renames over open files without complaint, so there is nothing to wait
// for: a budget of zero makes renamer.do run its single attempt and return
// exactly what os.Rename returned. The retry loop is still compiled here
// rather than hidden behind a build tag so that only the *policy* varies by
// platform and the code path taken on Unix is the same one Windows takes on
// its first attempt. See rename_windows.go for why Windows needs the wait.
const (
	renameRetries = 0
	renameBackoff = time.Duration(0)
)

// renameBlocked is constantly false here, which is what makes the budget above
// unreachable. It is also why renamer takes its collaborators as fields: with
// this wiring the retry arithmetic would be dead code on every platform the
// tests actually run on. See TestRenamer* in rename_test.go.
func renameBlocked(error) bool { return false }
