// Cross-restart persistence, concurrent-access safety, and corruption
// reporting. These cover the three properties the Sprint 1 store claimed but
// never exercised: that a second process does not overwrite the first one's
// work, that two goroutines may touch one Manager (the runtime's user-event
// loop calls Cancel while the drive goroutine records history), and that a
// damaged record is reported as damaged rather than as absent.

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// drainingBus returns a bus with a wildcard subscriber that is actually read.
// Subscriber channels are bounded, so a subscription nobody drains blocks
// Publish once the buffer fills — and these tests publish thousands of events.
func drainingBus(t *testing.T) *event.Bus {
	t.Helper()
	bus := event.New()
	ch := bus.Subscribe(">")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch {
		}
	}()
	t.Cleanup(func() {
		_ = bus.Close()
		<-done
	})
	return bus
}

// TestSecondLaunchDoesNotOverwriteFirstLaunch is the durable-root scenario:
// runTUI roots the store at ~/.config/yolo-code/sessions and builds a brand-new
// Manager at every launch. With per-process counters both launches allocated
// s_1/t_1 and the second os.WriteFile truncated the first launch's records, so
// yesterday's title, goal, task status and undo history were destroyed on
// startup. It goes through the real FileStore on a real directory on purpose —
// an in-memory store would pass while the disk still lost the data.
func TestSecondLaunchDoesNotOverwriteFirstLaunch(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	bus := drainingBus(t)

	// Launch 1 (Monday): open a session, run a task to completion.
	m1 := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})
	sid1, err := m1.OpenSession(ctx, "proj", "MONDAY: add a feature")
	if err != nil {
		t.Fatalf("launch 1 OpenSession: %v", err)
	}
	tid1, err := m1.StartTask(ctx, sid1, "monday goal")
	if err != nil {
		t.Fatalf("launch 1 StartTask: %v", err)
	}
	m1.RecordEntry(tid1, HistoryEntry{Kind: KindPatch, Summary: "monday patch", Reversible: true})
	if err := m1.CompleteTask(ctx, tid1); err != nil {
		t.Fatalf("launch 1 CompleteTask: %v", err)
	}

	// Launch 2 (Tuesday): a brand-new Manager over the SAME durable root.
	m2 := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})
	sid2, err := m2.OpenSession(ctx, "proj", "TUESDAY: fix a typo")
	if err != nil {
		t.Fatalf("launch 2 OpenSession: %v", err)
	}
	tid2, err := m2.StartTask(ctx, sid2, "tuesday goal")
	if err != nil {
		t.Fatalf("launch 2 StartTask: %v", err)
	}

	if sid1 == sid2 {
		t.Fatalf("launch 2 reissued session id %q; ids must be unique across processes", sid1)
	}
	if tid1 == tid2 {
		t.Fatalf("launch 2 reissued task id %q; ids must be unique across processes", tid1)
	}

	// Read back through a third, independent store handle: Monday survives
	// verbatim and Tuesday is alongside it, not on top of it.
	rd := NewFileStore(root)
	monday, err := rd.LoadSession(ctx, sid1)
	if err != nil {
		t.Fatalf("Monday's session gone: %v", err)
	}
	if monday.Title != "MONDAY: add a feature" {
		t.Errorf("Monday's title = %q, want %q (launch 2 overwrote it)", monday.Title, "MONDAY: add a feature")
	}
	mt, err := rd.LoadTask(ctx, tid1)
	if err != nil {
		t.Fatalf("Monday's task gone: %v", err)
	}
	if mt.Goal != "monday goal" || mt.Status != StatusDone {
		t.Errorf("Monday's task = goal %q / %q, want %q / %q", mt.Goal, mt.Status, "monday goal", StatusDone)
	}
	if len(mt.History) != 1 || mt.History[0].Summary != "monday patch" {
		t.Errorf("Monday's undo history = %+v, want one \"monday patch\" entry", mt.History)
	}

	tuesday, err := rd.LoadSession(ctx, sid2)
	if err != nil {
		t.Fatalf("Tuesday's session gone: %v", err)
	}
	if tuesday.Title != "TUESDAY: fix a typo" {
		t.Errorf("Tuesday's title = %q", tuesday.Title)
	}
	tt, err := rd.LoadTask(ctx, tid2)
	if err != nil {
		t.Fatalf("Tuesday's task gone: %v", err)
	}
	if tt.Goal != "tuesday goal" || tt.Status != StatusPending {
		t.Errorf("Tuesday's task = goal %q / %q, want %q / %q", tt.Goal, tt.Status, "tuesday goal", StatusPending)
	}

	// A session picker must see both launches.
	list, err := rd.ListSessions(ctx, "proj")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSessions after two launches = %d session(s), want 2", len(list))
	}

	// Both launches' task files coexist on disk.
	for _, tid := range []TaskID{tid1, tid2} {
		if _, err := os.Stat(filepath.Join(root, "tasks", string(tid)+".json")); err != nil {
			t.Errorf("task file for %q missing: %v", tid, err)
		}
	}
}

// TestFreshRootStillStartsAtOne pins the determinism the headless demo depends
// on (S5): its store is a disposable temp dir, so an empty root must still
// yield s_1/t_1 and keep the golden transcript byte-identical. Uniqueness comes
// from what is already on disk, never from randomness — which is why the ids
// stay short and human-typable rather than becoming UUIDs.
func TestFreshRootStillStartsAtOne(t *testing.T) {
	ctx := context.Background()
	bus := drainingBus(t)

	for i := 0; i < 3; i++ {
		m := New(Deps{Store: NewFileStore(t.TempDir()), Bus: bus, Git: NewInMemCheckpointer()})
		sid, err := m.OpenSession(ctx, "headless", "demo")
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		tid, err := m.StartTask(ctx, sid, "hello world")
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		if sid != "s_1" || tid != "t_1" {
			t.Fatalf("run %d over a fresh root = %q/%q, want s_1/t_1 (S5 byte-identical transcript)", i, sid, tid)
		}
	}
}

// TestConcurrentManagersOnOneRootNeverCollide covers the case seed-from-max
// alone cannot: two Managers over one root allocating at the same time, as two
// yolo processes on the same durable directory do. Every id must be distinct
// and every record must survive.
func TestConcurrentManagersOnOneRootNeverCollide(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	bus := drainingBus(t)

	const managers, each = 4, 8
	var mu sync.Mutex
	seen := make(map[ID]string)
	var wg sync.WaitGroup
	for g := 0; g < managers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			m := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})
			for i := 0; i < each; i++ {
				title := "m" + itoa(uint64(g)) + "-s" + itoa(uint64(i))
				sid, err := m.OpenSession(ctx, "proj", title)
				if err != nil {
					t.Errorf("OpenSession: %v", err)
					return
				}
				mu.Lock()
				if prev, dup := seen[sid]; dup {
					t.Errorf("id %q issued twice: %q and %q", sid, prev, title)
				}
				seen[sid] = title
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()

	list, err := NewFileStore(root).ListSessions(ctx, "proj")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != managers*each {
		t.Fatalf("ListSessions = %d, want %d (a collision truncated someone's record)", len(list), managers*each)
	}
	// Every title written is still readable — no record landed on another.
	got := make(map[string]bool, len(list))
	for _, s := range list {
		got[s.Title] = true
	}
	for _, title := range seen {
		if !got[title] {
			t.Errorf("session %q was overwritten", title)
		}
	}
}

// TestConcurrentMutatorsAndSavesAreRaceFree exercises what the package never
// did: two goroutines against one Manager. runtime.Core runs userEventLoop as
// its own goroutine and calls Cancel from there while the drive goroutine is
// inside Checkpoint/RecordEntry/CompleteTask — each of which used to release
// m.mu and then hand the live *Task to json.Marshal. Meaningful only under
// -race.
func TestConcurrentMutatorsAndSavesAreRaceFree(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	bus := drainingBus(t)
	m := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})

	sid, err := m.OpenSession(ctx, "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tid, err := m.StartTask(ctx, sid, "goal")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	const n = 150
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				f()
			}
		}()
	}
	// The drive goroutine's writes.
	run(func() { m.RecordEntry(tid, HistoryEntry{Kind: KindPatch, Summary: "p", Reversible: true}) })
	run(func() { _, _ = m.Checkpoint(ctx, tid, "cp", []string{"a.go"}) })
	run(func() { _ = m.CompleteTask(ctx, tid) })
	// The user-event goroutine's writes.
	run(func() { _ = m.Cancel(ctx, tid, "user") })
	run(func() { _ = m.Pause(ctx, tid) })
	// The TUI's reads.
	run(func() { _ = m.History(tid) })
	wg.Wait()
}

// TestResumeReportsCorruptSessionDistinctlyFromMissing: a truncated file (the
// signature of a crash mid-save) used to collapse into ErrUnknownSession, so
// the user's response was to start a new session — which, with the id bug
// above, then wrote straight over the damaged-but-recoverable file. The two
// must be distinguishable.
func TestResumeReportsCorruptSessionDistinctlyFromMissing(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	bus := drainingBus(t)
	m := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})

	sid, err := m.OpenSession(ctx, "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	path := filepath.Join(root, "sessions", string(sid)+".json")
	if err := os.WriteFile(path, []byte(`{"id":"`+string(sid)+`","proj`), 0o644); err != nil {
		t.Fatalf("truncate session file: %v", err)
	}

	_, _, err = m.Resume(ctx, sid)
	if err == nil {
		t.Fatal("Resume(corrupt) = nil error, want a decode failure")
	}
	if errors.Is(err, ErrUnknownSession) {
		t.Errorf("Resume(corrupt) = %v, which is ErrUnknownSession — a damaged file must not look like a missing one", err)
	}
	if !hasSubstring(err.Error(), string(sid)) {
		t.Errorf("Resume(corrupt) = %q, want the session id %q in the message", err, sid)
	}

	// A genuinely absent session still reports the not-found sentinel, and
	// errors.Is must be the way callers detect it.
	if _, _, err := m.Resume(ctx, "s_nope"); !errors.Is(err, ErrUnknownSession) {
		t.Errorf("Resume(missing) = %v, want ErrUnknownSession", err)
	}
}

// TestSaveIsAtomicForConcurrentReaders: writeJSON was a bare os.WriteFile,
// which truncates the destination and then fills it, so a reader that arrives
// mid-write sees a half-written file. A crash in that same window leaves the
// half-written file behind permanently. Writing to a temp file and renaming
// makes every read see either the old record or the new one.
func TestSaveIsAtomicForConcurrentReaders(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store := NewFileStore(root)

	// A payload big enough that the write spans a meaningful window.
	tasks := make([]TaskID, 4000)
	for i := range tasks {
		tasks[i] = TaskID("t_" + itoa(uint64(i)))
	}
	sess := &Session{
		ID: "s_atomic", ProjectID: "proj", Title: "atomic",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Tasks: tasks,
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("seed SaveSession: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = store.SaveSession(ctx, sess)
			}
		}()
	}

	var readErrs int
	var firstErr error
	for i := 0; i < 400; i++ {
		got, err := store.LoadSession(ctx, "s_atomic")
		if err != nil {
			readErrs++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(got.Tasks) != len(tasks) {
			readErrs++
			if firstErr == nil {
				firstErr = errors.New("short read: " + itoa(uint64(len(got.Tasks))) + " tasks")
			}
		}
	}
	close(stop)
	wg.Wait()

	if readErrs > 0 {
		t.Errorf("%d/400 concurrent reads saw a partial file; first: %v (writes must be atomic)", readErrs, firstErr)
	}
	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Errorf("leftover scratch file in sessions/: %q", e.Name())
		}
	}
}

// TestListSessionsOrdersByCreatedAt: the doc comment promised CreatedAt order
// but the function returned os.ReadDir order, i.e. lexicographic by filename —
// so s_10 sorted between s_1 and s_2. Invisible while only one session file
// ever existed; a session picker needs the real order.
func TestListSessionsOrdersByCreatedAt(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store := NewFileStore(root)

	// Written oldest-first with explicit timestamps so the assertion does not
	// depend on clock resolution. Twelve entries make lexicographic order
	// (s_1, s_10, s_11, s_12, s_2, …) differ from creation order.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var want []ID
	for i := 1; i <= 12; i++ {
		sid := ID("s_" + itoa(uint64(i)))
		want = append(want, sid)
		s := &Session{
			ID: sid, ProjectID: "proj", Title: "s" + itoa(uint64(i)),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			UpdatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := store.SaveSession(ctx, s); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}

	list, err := store.ListSessions(ctx, "proj")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != len(want) {
		t.Fatalf("ListSessions = %d sessions, want %d", len(list), len(want))
	}
	for i, s := range list {
		if s.ID != want[i] {
			var got []ID
			for _, x := range list {
				got = append(got, x.ID)
			}
			t.Fatalf("ListSessions order = %v, want %v (ordered by CreatedAt)", got, want)
		}
	}
}

// hasSubstring keeps the assertion above readable without importing strings
// for a single call, matching the package's existing habit.
func hasSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestLegacyRecordsAreAdoptedNotOverwritten covers the directories that already
// exist on users' disks. The ID format is unchanged, so an s_1.json written by
// the old build stays readable and simply becomes the seed's floor — there is
// no migration step. The orphaned t_7 here is the case the seed alone cannot
// see (no session references it), so it also exercises the store's atomic claim
// rejecting an ID the counter would otherwise have reused.
func TestLegacyRecordsAreAdoptedNotOverwritten(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	bus := drainingBus(t)

	// Hand-write records in exactly the shape the previous build left behind.
	legacy := NewFileStore(root)
	old := &Session{
		ID: "s_1", ProjectID: "proj", Title: "last week's work",
		CreatedAt: time.Now().UTC().Add(-time.Hour), UpdatedAt: time.Now().UTC().Add(-time.Hour),
		Tasks: []TaskID{"t_1"},
	}
	if err := legacy.SaveSession(ctx, old); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	if err := legacy.SaveTask(ctx, &Task{ID: "t_1", SessionID: "s_1", Goal: "old goal", Status: StatusDone}); err != nil {
		t.Fatalf("seed legacy task: %v", err)
	}
	// A task file no session points at — invisible to the seed.
	if err := legacy.SaveTask(ctx, &Task{ID: "t_7", SessionID: "s_1", Goal: "orphan", Status: StatusFailed}); err != nil {
		t.Fatalf("seed orphan task: %v", err)
	}

	m := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})
	// The legacy session is still readable through the new build.
	got, _, err := m.Resume(ctx, "s_1")
	if err != nil {
		t.Fatalf("Resume legacy session: %v", err)
	}
	if got.Title != "last week's work" {
		t.Errorf("legacy title = %q, want %q", got.Title, "last week's work")
	}

	sid, err := m.OpenSession(ctx, "proj", "today")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if sid == "s_1" {
		t.Fatal("new session reused the legacy id s_1")
	}
	tid, err := m.StartTask(ctx, sid, "today goal")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if tid == "t_1" || tid == "t_7" {
		t.Fatalf("new task reused an existing id %q", tid)
	}

	// Every legacy record survives untouched.
	rd := NewFileStore(root)
	if s, err := rd.LoadSession(ctx, "s_1"); err != nil || s.Title != "last week's work" {
		t.Errorf("legacy session damaged: %+v / %v", s, err)
	}
	if x, err := rd.LoadTask(ctx, "t_1"); err != nil || x.Goal != "old goal" {
		t.Errorf("legacy task damaged: %+v / %v", x, err)
	}
	if x, err := rd.LoadTask(ctx, "t_7"); err != nil || x.Goal != "orphan" {
		t.Errorf("orphan task damaged: %+v / %v", x, err)
	}
}

// TestTwoManagersOnOneRootMintDisjointIDs pins the scope of the uniqueness
// guarantee, and validates the remedy for the one case it does not cover.
//
// Uniqueness is per store root. cmd/yolo builds two Managers publishing to one
// bus — the interactive one on sessionStateDir() and the coord runner's on the
// "coord" subdirectory of it — and because those are two directories, two
// counters, both still mint t_1. A TUI peer confirmed the consequence: the
// header adopted a coord sub-agent's task as the user's own, because two tasks
// that agree on their name are one task to every consumer of the bus.
//
// Pointing both Managers at ONE root fixes it, which is what this asserts. That
// was not an option before: a shared root is precisely what used to make the
// second Manager truncate the first one's s_1.json, which is why the coord
// store was pushed into a subdirectory in the first place. With ids claimed
// atomically against the root, the subdirectory is no longer load-bearing.
func TestTwoManagersOnOneRootMintDisjointIDs(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	bus := drainingBus(t)

	// Two Managers over one root, as a shared-root composition root would build.
	interactive := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})
	subagent := New(Deps{Store: NewFileStore(root), Bus: bus, Git: NewInMemCheckpointer()})

	isid, err := interactive.OpenSession(ctx, "tui", "interactive")
	if err != nil {
		t.Fatalf("interactive OpenSession: %v", err)
	}
	itid, err := interactive.StartTask(ctx, isid, "the user's task")
	if err != nil {
		t.Fatalf("interactive StartTask: %v", err)
	}
	ssid, err := subagent.OpenSession(ctx, "coord", "todo 1")
	if err != nil {
		t.Fatalf("subagent OpenSession: %v", err)
	}
	stid, err := subagent.StartTask(ctx, ssid, "a sub-agent's task")
	if err != nil {
		t.Fatalf("subagent StartTask: %v", err)
	}

	if itid == stid {
		t.Errorf("both Managers minted task id %q; a bus consumer cannot tell the user's task from a sub-agent's", itid)
	}
	if isid == ssid {
		t.Errorf("both Managers minted session id %q", isid)
	}

	// And neither record was written over the other.
	rd := NewFileStore(root)
	iu, err := rd.LoadTask(ctx, itid)
	if err != nil || iu.Goal != "the user's task" {
		t.Errorf("interactive task = %+v / %v", iu, err)
	}
	su, err := rd.LoadTask(ctx, stid)
	if err != nil || su.Goal != "a sub-agent's task" {
		t.Errorf("subagent task = %+v / %v", su, err)
	}
}
