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
)

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
	// target exactly as it was.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil { // on disk before the rename publishes it
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil { // CreateTemp makes 0o600
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
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
