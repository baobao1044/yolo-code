// JSON file helpers for the persistent memory sub-stores (File 11). The
// session layer (internal/session/store.go) has the same helpers but they are
// package-private, so memory keeps its own copy — stdlib-only, no shared util
// package (the import matrix would forbid importing session anyway, File 15
// §15.15.2). The shape mirrors session's: 2-space indent, 0o644, lazy
// MkdirAll, os.ErrNotExist → ErrNotFound.

package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// renamer is os.Rename plus the "wait for the other handle to close" policy
// Windows needs to replace a file at all (rename_windows.go explains why; on
// Unix the policy is a no-op and this is a plain os.Rename).
//
// Its collaborators are fields rather than direct calls for a testing reason
// that is worth stating: the production wiring makes blocked() constantly
// false on Unix, so the retry arithmetic would be unreachable on every
// platform the tests actually run on, and the loop would be verified only by
// the CI job that happens to have a reader racing a writer. With the policy
// injectable, rename_test.go exercises the budget, the give-up path and the
// narrowness everywhere.
type renamer struct {
	rename  func(from, to string) error
	blocked func(error) bool
	retries int
	backoff time.Duration
}

// shareRenamer is the production wiring.
func shareRenamer() renamer {
	return renamer{
		rename:  os.Rename,
		blocked: renameBlocked,
		retries: renameRetries,
		backoff: renameBackoff,
	}
}

// do renames from→to, waiting out a destination that is merely held open. It
// returns the last attempt's own error rather than a synthesised timeout, so
// the caller sees what the filesystem actually said.
func (r renamer) do(from, to string) error {
	for attempt := 0; ; attempt++ {
		err := r.rename(from, to)
		if err == nil || attempt >= r.retries || !r.blocked(err) {
			return err
		}
		time.Sleep(r.backoff)
	}
}

// writeJSON marshals v to a 2-space-indented JSON file at path, creating the
// parent directory lazily (matches session.writeJSON so the two stores look
// alike). 0o644 is the cross-platform default; the parent dir is 0o755.
//
// The replacement is atomic: bytes go to a temp file in the SAME directory,
// get fsynced, and are renamed over the target. os.WriteFile truncates the
// target first and then streams — a crash (or a concurrent reader) in that
// window found a half-written memory file where a good one used to be, which
// on the next Open reads as corruption. Rename within a directory is atomic,
// so a reader sees the old file or the new one and nothing in between. The
// temp name is dot-prefixed so IndexRepo's dotfile skip never indexes it.
//
// "Atomic" is the Unix guarantee. Windows cannot replace a file that another
// handle holds open at all, so the rename goes through shareRenamer, which
// waits the reader out; see rename_windows.go.
func writeJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Past this point every failure removes the temp file and leaves the
	// target exactly as it was. The cleanup's own errors are dropped on
	// purpose, and spelled `_ =` so that is visible rather than inferred: the
	// caller is about to be handed the failure that actually matters, and
	// replacing it with "could not remove the scratch file" would report the
	// consequence instead of the cause. A leaked temp file is dot-prefixed, so
	// IndexRepo skips it and the next successful write does not trip over it.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil { // on disk before the rename publishes it
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil { // CreateTemp makes 0o600
		_ = os.Remove(name)
		return err
	}
	if err := shareRenamer().do(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// readJSON unmarshals the file at path into v. A missing file returns
// ErrNotFound (translated from os.ErrNotExist) so callers can branch on "no
// record yet"; a file that exists but won't decode returns *CorruptError so
// callers can branch on "corrupt or truncated" separately from a real I/O
// failure. Callers decode into a local and only publish it into the store on
// success, so a partially-decoded v never becomes live state.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return &CorruptError{Path: path, Err: err}
	}
	return nil
}

// CorruptError reports a persisted memory file that exists but cannot be
// decoded — a truncated write from before writeJSON became atomic, a disk
// fault, or a hand-edit. It is deliberately non-fatal: Open quarantines the
// file and continues with an empty store (§11.3.3 — losing a memory file must
// not stop the agent, and overwriting the bytes in place would be a silent
// wipe of whatever the user might still recover).
type CorruptError struct {
	Path string
	Err  error
}

func (e *CorruptError) Error() string {
	return "memory: corrupt store file " + e.Path + ": " + e.Err.Error()
}

// Unwrap exposes the decode failure to errors.Is/As.
func (e *CorruptError) Unwrap() error { return e.Err }
