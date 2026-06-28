// Preference memory — per-user, cross-project (§11.5.2): style, tooling,
// do/don't, preferred model. The ONE store the user can edit directly (it's
// their data); agent-originated updates are still event-driven (routed through
// a "user asked to remember" event, L10-002). JSON-file backed under
// root/preference.json — a single key/value map shared across projects so a
// preference set in session A is recalled in session B (the L10-005 exit bar).
//
// stdlib-only: SQLite is the documented future upgrade; L10 uses a JSON file so
// the single-binary constraint survives and the surface (Get/Set/All) is reused
// on the swap.

package memory

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
)

// PreferenceStore persists the user's key/value preferences to a single JSON
// file under root/preference.json. The in-memory map is a warm cache; a Set
// both updates the cache and writes the file so a re-open sees the change.
type PreferenceStore struct {
	root  string
	mu    sync.Mutex
	prefs map[string]string
}

// NewPreferenceStore returns a JSON-file preference store rooted at dir.
func NewPreferenceStore(dir string) *PreferenceStore {
	return &PreferenceStore{root: dir, prefs: make(map[string]string)}
}

func (s *PreferenceStore) path() string {
	return filepath.Join(s.root, "preference.json")
}

// Get returns the value for key, or "" with ErrNotFound if unset.
func (s *PreferenceStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.prefs[key]; ok {
		return v, nil
	}
	return "", ErrNotFound
}

// Set stores key=value in the cache and persists the whole map to the JSON file
// (a small map; a full rewrite is fine for MVP). The user-editable store.
func (s *PreferenceStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefs[key] = value
	return writeJSON(s.path(), s.prefs)
}

// All returns a copy of the whole preference map (the Context Engine's
// preference input, File 06 §6.1).
func (s *PreferenceStore) All(_ context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.prefs))
	for k, v := range s.prefs {
		out[k] = v
	}
	return out, nil
}

// Load re-reads the preference file into the warm cache (cross-session resume).
func (s *PreferenceStore) Load(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p map[string]string
	if err := readJSON(s.path(), &p); err != nil {
		if err == ErrNotFound {
			return nil // no file yet → empty prefs, not an error
		}
		return err
	}
	s.prefs = p
	return nil
}

// Preferences returns the preferences as Parts for the Context Engine: one
// KindPreferences part per key (text = "key: value"). The composition root's
// adapter forwards these to the preference group.
func (s *PreferenceStore) Preferences(_ context.Context) []Part {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.prefs))
	for k := range s.prefs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // S5: deterministic order
	out := make([]Part, 0, len(keys))
	for _, k := range keys {
		out = append(out, Part{
			Kind:   KindPreferences,
			Source: k,
			Text:   k + ": " + s.prefs[k],
		})
	}
	return out
}
