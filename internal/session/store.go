// Session persistence (File 03 §3.5). The spec targets SQLite (schema owned by
// File 11 / Memory, Sprint 7); Sprint 1 uses a JSON-file store so the
// create→run→done→resume sequence is exercisable now, with the Store interface
// preserved so the backend can be swapped later without touching the Manager.
//
// Two properties the first cut claimed but did not have, both of which cost the
// user real work: writes are atomic (a crash or a concurrent reader never sees
// a half-written record), and a brand-new record can be claimed exclusively so
// two processes over one durable root cannot both decide they own s_1.

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Store persists sessions and tasks. The Manager reads/writes through it so a
// process restart can resume in-progress work.
type Store interface {
	// CreateSession persists a session that must not exist yet, reporting
	// ErrIDTaken if a record is already filed under that ID. It is how the
	// Manager claims an ID: the existence check and the write are one
	// indivisible step, so of two processes racing for the same number exactly
	// one wins and the other is told to move on.
	CreateSession(ctx context.Context, s *Session) error
	SaveSession(ctx context.Context, s *Session) error
	LoadSession(ctx context.Context, sid ID) (*Session, error)
	// CreateTask is CreateSession's counterpart for tasks.
	CreateTask(ctx context.Context, t *Task) error
	SaveTask(ctx context.Context, t *Task) error
	LoadTask(ctx context.Context, tid TaskID) (*Task, error)
	ListSessions(ctx context.Context, projectID string) ([]*Session, error)
}

// ErrNotFound is returned by Load* when no record exists for the key. It means
// "there is nothing here", never "there is something here and I could not read
// it" — see LoadSession.
var ErrNotFound = errors.New("session: not found")

// ErrIDTaken is returned by Create* when a record already occupies the ID. The
// Manager reads it as "someone else got there first" and tries the next number
// rather than overwriting a record it did not write.
var ErrIDTaken = errors.New("session: id already taken")

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

// CreateSession claims the session's ID and writes it, failing with ErrIDTaken
// if the ID is already on disk.
func (s *FileStore) CreateSession(_ context.Context, sess *Session) error {
	return createJSON(s.sessionPath(sess.ID), sess)
}

// SaveSession writes the session to its JSON file, replacing any earlier
// version atomically.
func (s *FileStore) SaveSession(_ context.Context, sess *Session) error {
	if err := os.MkdirAll(filepath.Dir(s.sessionPath(sess.ID)), 0o755); err != nil {
		return err
	}
	return writeJSON(s.sessionPath(sess.ID), sess)
}

// LoadSession reads a session by ID. Returns ErrNotFound if absent — and a
// wrapped, descriptive error for anything else. The distinction is the whole
// point: "no such session" tells the user to pick another one, while "this
// session's file is truncated" tells them their data is damaged but present.
// Collapsing the second into the first invited them to start over, which is
// exactly the action that overwrites the recoverable remains.
func (s *FileStore) LoadSession(_ context.Context, sid ID) (*Session, error) {
	var sess Session
	if err := readJSON(s.sessionPath(sid), &sess); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load session %s: %w", sid, err)
	}
	return &sess, nil
}

// CreateTask claims the task's ID and writes it, failing with ErrIDTaken if the
// ID is already on disk.
func (s *FileStore) CreateTask(_ context.Context, t *Task) error {
	return createJSON(s.taskPath(t.ID), t)
}

// SaveTask writes the task to its JSON file, replacing any earlier version
// atomically.
func (s *FileStore) SaveTask(_ context.Context, t *Task) error {
	if err := os.MkdirAll(filepath.Dir(s.taskPath(t.ID)), 0o755); err != nil {
		return err
	}
	return writeJSON(s.taskPath(t.ID), t)
}

// LoadTask reads a task by ID. Returns ErrNotFound if absent, and a wrapped
// error otherwise, for the reason spelled out on LoadSession.
func (s *FileStore) LoadTask(_ context.Context, tid TaskID) (*Task, error) {
	var t Task
	if err := readJSON(s.taskPath(tid), &t); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load task %s: %w", tid, err)
	}
	return &t, nil
}

// ListSessions returns every session for a project, oldest first by CreatedAt.
// The order used to be os.ReadDir's — lexicographic by filename, which files
// s_10 between s_1 and s_2. That was invisible only for as long as a durable
// root could hold just one session file.
//
// An entry that will not load is skipped rather than failing the call. A picker
// that shows nothing because one file in fifty is truncated is worse than one
// that shows the other forty-nine; the damage is reported by Resume, for the
// session the user actually picks.
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
			continue
		}
		if projectID == "" || sess.ProjectID == projectID {
			out = append(out, sess)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		// Two records stamped in the same instant still need one fixed order,
		// or the picker's list would shuffle between renders.
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// createJSON writes v to path only if nothing is there yet, returning
// ErrIDTaken if something is. O_EXCL folds the check and the claim into one
// syscall, which is what closes the gap between "no s_4.json on disk" and "I
// wrote s_4.json" for two yolo processes on one durable root — a gap that
// checking-then-writing cannot close no matter how small it is made.
//
// The write itself is a plain write-and-fsync rather than the rename dance
// below: the file is brand new, so a torn create can only damage a record that
// no caller has been told about yet, and it cannot destroy an older one.
func createJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrIDTaken
		}
		return err
	}
	if err := writeAndSync(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// writeJSON replaces path atomically. It used to be a bare os.WriteFile, which
// truncates first and fills second: a crash or a full disk in that window left
// a permanently truncated record, and a reader in that window saw one too. The
// JSON now goes to a scratch file in the same directory, is fsynced, and is
// renamed over the destination — rename being atomic, every observer sees
// either the whole old record or the whole new one.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	// A unique scratch name per writer: two processes saving the same record
	// must not land on one another's half-written temp file.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := writeAndSync(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// CreateTemp opens 0600; the records are 0644 like every other file here.
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// writeAndSync writes data and flushes it to the platter. Without the fsync the
// rename can publish a directory entry pointing at contents still sitting in
// the page cache, which is the same truncated file by another route.
func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// syncDir fsyncs a directory so the create or rename inside it is itself
// durable. Best-effort on purpose: some filesystems reject a directory fsync,
// and failing a write whose data already landed would be the worse answer.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// readShareRetries and readShareBackoff bound the retry in readJSON. Twenty
// attempts at 2ms is ~40ms of patience, which is orders of magnitude longer
// than a rename's replace window and short enough that a genuinely locked file
// still reports its error while the user is still looking at the screen.
// Vars, not consts, so a test can shrink the budget without waiting it out.
var (
	readShareRetries = 20
	readShareBackoff = 2 * time.Millisecond
)

// readJSON reads and decodes path, retrying briefly while the file is locked by
// a concurrent writer.
//
// The retry is Windows' share of the atomic-write story. writeJSON's rename is
// atomic on both platforms in the sense that matters — no reader ever sees a
// half-written record — but POSIX rename and Windows MoveFileEx differ in what
// a reader arriving mid-replace sees. On POSIX it sees the old inode or the new
// one, always one of them. On Windows the destination is briefly unopenable and
// CreateFile returns ERROR_SHARING_VIOLATION, so the reader gets an error where
// its POSIX counterpart got data: 9 of 400 concurrent reads failed that way in
// CI, and none of them saw a torn file.
//
// So this is not a weaker guarantee papered over — the write stays atomic and
// the read stays all-or-nothing. It closes the one gap Windows adds, which is
// that "come back in a moment" arrives spelled as a fatal error. Only the two
// lock errnos are retried; ErrNotExist still answers immediately, because a
// missing record is the ErrNotFound path and must not be slowed by 40ms of
// hoping it shows up.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	for i := 0; err != nil && i < readShareRetries && isLockedByAnotherProcess(err); i++ {
		time.Sleep(readShareBackoff)
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		// Name the file: "unexpected end of JSON input" on its own tells the
		// user nothing about which record is damaged.
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

// stringsTrimSuffix is a tiny helper to avoid importing strings just for one
// call; kept local so the import list stays tight.
func stringsTrimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
