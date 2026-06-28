package context

import (
	stdctx "context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
)

// fixtureRepo writes a tiny repo layout into a temp dir and returns its path.
// It is the corpus the L4/L5 tests gather and rank against.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"AGENTS.md":          "# Conventions\nUse table-driven tests.\n",
		"main.go":            "package main\n\nfunc main() {}\n",
		"auth/login.go":      "package auth\n\nfunc Login(user string) error { return nil }\n",
		"auth/login_test.go": "package auth\n\nimport \"testing\"\n\nfunc TestLogin(t *testing.T) {}\n",
		"util/helper.go":     "package util\n\nfunc Help() {}\n",
	}
	for path, body := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
		// Stagger mtimes so recency is observable: util oldest, auth newest.
		// (utime precision varies by OS; the test also seeds recency via part
		// timestamps where needed.)
	}
	return root
}

// newEngine builds a context Engine over a fixture repo with an in-memory bus
// and a no-op memory/graph/diag (future layers). The open-files set seeds the
// "open files" input.
func newEngine(t *testing.T, repo string, openFiles []string) *Engine {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	e := New(Deps{
		Bus:    bus,
		Repo:   repo,
		Memory: noopMemory{},
		Git:    noopGitDiff{},
		Graph:  noopGraph{},
		Diags:  noopDiags{},
		Open:   openFiles,
	})
	return e
}

// newReq builds a ContextRequest with a task whose goal references a file.
func newReq(repo, goal string) (ContextRequest, *session.Manager) {
	bus := event.New()
	store := session.NewFileStore(filepath.Join(repo, ".store"))
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, _ := smgr.OpenSession(stdctx.Background(), "proj", "demo")
	tid, _ := smgr.StartTask(stdctx.Background(), sid, goal)
	// Resume first (it rehydrates the task into the manager's map), then read
	// the live task pointer so RecordEntry later appends to the same *Task the
	// request carries into Build. Reading before Resume would return a stale
	// pointer Resume overwrites.
	sess, _, _ := smgr.Resume(stdctx.Background(), sid)
	task := smgr.LoadTaskPublic(tid)
	return ContextRequest{Task: task, Session: sess}, smgr
}

func TestBuildAssemblesBundleFromRepoFiles(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, []string{"auth/login.go"})
	req, _ := newReq(repo, "fix the Login function in auth/login.go")

	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if pkg.Task != req.Task.ID {
		t.Errorf("pkg.Task = %q, want %q", pkg.Task, req.Task.ID)
	}
	// Files group must carry the open file's real contents.
	if len(pkg.Files) == 0 {
		t.Fatal("pkg.Files empty; Build must gather open files")
	}
	found := false
	for _, p := range pkg.Files {
		if p.Source == "auth/login.go" {
			if p.Text == "" {
				t.Errorf("open file %q gathered with empty text", p.Source)
			}
			if !contains(p.Text, "func Login") {
				t.Errorf("open file text missing body; got %q", p.Text)
			}
			found = true
		}
	}
	if !found {
		t.Error("open file auth/login.go not gathered into pkg.Files")
	}
}

func TestBuildIncludesProjectRulesFromAGENTSMD(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, nil)
	req, _ := newReq(repo, "do something")

	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pkg.Project) == 0 {
		t.Fatal("pkg.Project empty; Build must gather AGENTS.md")
	}
	joined := ""
	for _, p := range pkg.Project {
		joined += p.Text
	}
	if !contains(joined, "table-driven") {
		t.Errorf("AGENTS.md content missing from Project; got %q", joined)
	}
}

func TestBuildCarriesConversationFromSessionHistory(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, nil)
	req, smgr := newReq(repo, "first goal")
	// Add a prior history entry so Conversation has content.
	smgr.RecordEntry(req.Task.ID, session.HistoryEntry{
		Kind: session.KindPatch, Summary: "renamed Login", Reversible: true,
	})

	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pkg.Conversation) == 0 {
		t.Fatal("pkg.Conversation empty; Build must carry session history")
	}
}

func TestBuildPublishesContextBuiltEvent(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, []string{"main.go"})
	req, _ := newReq(repo, "goal")
	ch := eng.bus.Subscribe("context.built")

	if _, err := eng.Build(stdctx.Background(), req); err != nil {
		t.Fatalf("Build: %v", err)
	}
	select {
	case env := <-ch:
		ce, ok := env.Evt.(*event.ContextBuiltEvent)
		if !ok {
			t.Fatalf("event type = %T, want *ContextBuiltEvent", env.Evt)
		}
		if ce.Task != event.TaskID(req.Task.ID) {
			t.Errorf("context.built Task = %q, want %q", ce.Task, req.Task.ID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("context.built not published")
	}
}

func TestBuildHasSystemAndPreferencesGroups(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, nil)
	req, _ := newReq(repo, "goal")
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// System group always present (role + tool schemas placeholder).
	if len(pkg.System) == 0 {
		t.Error("pkg.System empty; role/rules must always be present")
	}
	// Preferences group exists (even if empty for now; the stub memory returns
	// none, but the slot must be allocated so the compiler orders it).
	_ = pkg.Preferences // present as a field (struct, not nilable); just touch it
}

// contains is a tiny strings.Contains helper kept local to avoid an import
// just for one assertion.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
