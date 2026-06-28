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
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// readJSON unmarshals the file at path into v. A missing file returns
// ErrNotFound (translated from os.ErrNotExist) so callers can branch on "no
// record yet" vs "corrupt read".
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return json.Unmarshal(data, v)
}
