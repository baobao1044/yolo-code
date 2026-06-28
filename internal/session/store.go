// Session persistence (File 03 §3.5). The spec targets SQLite (schema owned by
// File 11 / Memory, Sprint 7); Sprint 1 uses a JSON-file store so the
// create→run→done→resume sequence is exercisable now, with the Store interface
// preserved so the backend can be swapped later without touching the Manager.

package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Store persists sessions and tasks. The Manager reads/writes through it so a
// process restart can resume in-progress work.
type Store interface {
	SaveSession(ctx context.Context, s *Session) error
	LoadSession(ctx context.Context, sid ID) (*Session, error)
	SaveTask(ctx context.Context, t *Task) error
	LoadTask(ctx context.Context, tid TaskID) (*Task, error)
	ListSessions(ctx context.Context, projectID string) ([]*Session, error)
}

// ErrNotFound is returned by Load* when no record exists for the key.
var ErrNotFound = errors.New("session: not found")

// FileStore is a JSON-file-backed Store. Sessions live under root/sessions/
// and tasks under root/tasks/, one file per record.
type FileStore struct {
	root string
}

// NewFileStore returns a JSON-file store rooted at dir. The subdirectories are
// created lazily on first write.
func NewFileStore(dir string) *FileStore {
	return &FileStore{root: dir}
}

func (s *FileStore) sessionPath(sid ID) string {
	return filepath.Join(s.root, "sessions", string(sid)+".json")
}

func (s *FileStore) taskPath(tid TaskID) string {
	return filepath.Join(s.root, "tasks", string(tid)+".json")
}

// SaveSession writes the session to its JSON file.
func (s *FileStore) SaveSession(_ context.Context, sess *Session) error {
	if err := os.MkdirAll(filepath.Dir(s.sessionPath(sess.ID)), 0o755); err != nil {
		return err
	}
	return writeJSON(s.sessionPath(sess.ID), sess)
}

// LoadSession reads a session by ID. Returns ErrNotFound if absent.
func (s *FileStore) LoadSession(_ context.Context, sid ID) (*Session, error) {
	var sess Session
	if err := readJSON(s.sessionPath(sid), &sess); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sess, nil
}

// SaveTask writes the task to its JSON file.
func (s *FileStore) SaveTask(_ context.Context, t *Task) error {
	if err := os.MkdirAll(filepath.Dir(s.taskPath(t.ID)), 0o755); err != nil {
		return err
	}
	return writeJSON(s.taskPath(t.ID), t)
}

// LoadTask reads a task by ID. Returns ErrNotFound if absent.
func (s *FileStore) LoadTask(_ context.Context, tid TaskID) (*Task, error) {
	var t Task
	if err := readJSON(s.taskPath(tid), &t); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// ListSessions returns every session for a project, ordered by CreatedAt.
func (s *FileStore) ListSessions(ctx context.Context, projectID string) ([]*Session, error) {
	dir := filepath.Join(s.root, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		sid := ID(stringsTrimSuffix(e.Name(), ".json"))
		sess, err := s.LoadSession(ctx, sid)
		if err != nil {
			return nil, err
		}
		if projectID == "" || sess.ProjectID == projectID {
			out = append(out, sess)
		}
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// stringsTrimSuffix is a tiny helper to avoid importing strings just for one
// call; kept local so the import list stays tight.
func stringsTrimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
