package context

import (
	stdctx "context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
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

// TestSystemPromptContainsDirectAnswerRules pins the Phase 1 fix: the system
// prompt instructs the model to answer directly (not self-introduce) and lists
// grep. Without these, the model greets instead of answering non-coding
// questions.
func TestSystemPromptContainsDirectAnswerRules(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, nil)
	req, _ := newReq(repo, "goal")
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pkg.System) == 0 {
		t.Fatal("pkg.System empty")
	}
	text := pkg.System[0].Text
	for _, want := range []string{
		"Answer the user's actual question",
		"Do NOT introduce yourself",
		"Be concise",
		"grep",
	} {
		if !contains(text, want) {
			t.Errorf("system prompt missing %q (the model would self-introduce instead of answering)", want)
		}
	}
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

// fakeMemory is a test Memory seam returning canned Preferences + RAG Parts.
// It implements the full Memory interface (Preferences/Project/Retrieve) so
// the gather RAG path is testable without wiring a real vector store.
type fakeMemory struct {
	prefs []Part
	rag   []Part
}

func (f fakeMemory) Preferences(stdctx.Context, string) []Part { return f.prefs }
func (f fakeMemory) Project(stdctx.Context, string) []Part     { return nil }
func (f fakeMemory) Retrieve(stdctx.Context, string, int) []Part {
	return f.rag
}

// TestBuildGathersRAGFromMemoryRetrieve: when the Memory seam returns RAG
// Parts, Build's gather feeds them into pkg.RAG (File 11 §11.6.2 → §6.1).
func TestBuildGathersRAGFromMemoryRetrieve(t *testing.T) {
	repo := fixtureRepo(t)
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	rag := []Part{{Kind: KindRAG, Source: "auth/login.go", Text: "func Login(user string) error"}}
	eng := New(Deps{
		Bus:    bus,
		Repo:   repo,
		Memory: fakeMemory{rag: rag},
		Open:   nil,
	})
	req, _ := newReq(repo, "fix the Login function")
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pkg.RAG) == 0 {
		t.Fatal("pkg.RAG empty; Build must gather the Memory seam's retrieved chunks")
	}
	if pkg.RAG[0].Source != "auth/login.go" {
		t.Errorf("pkg.RAG[0].Source = %q, want auth/login.go", pkg.RAG[0].Source)
	}
	if !contains(pkg.RAG[0].Text, "func Login") {
		t.Errorf("pkg.RAG[0].Text = %q, want the Login body", pkg.RAG[0].Text)
	}
}

// TestBuildRetainsRetrievedChunkUnderATightBudget is the end-to-end guard on
// the whole gather → rank → compress path: the user asks to fix a function, the
// store retrieves the chunk holding it, and two files the user happens to have
// open — unrelated directory, not one token in common with the goal — must not
// evict it.
//
// Both halves of the path used to break it. rank gave every file part a
// constant 0.25 of proximity (0.294 after a renormalisation that has also
// gone), because a goal naming no file made each part its own reference point;
// that floor put both noise files above any realistic cosine. compress then
// walked one globally ordered list against one shared byte budget, so the two
// files consumed all 900 bytes and the RAG group came out empty — and an empty
// group is invisible downstream, so nothing reported the loss.
//
// The cosine here is measured, not invented: 0.1529 is what the default hashing
// embedder (internal/memory/embed.go) actually returns for this goal against
// this Login body. Note the noise files still score 0.30 apiece on recency
// alone — a freshly written file is genuinely recent — so this asserts the
// chunk *survives*, which is the harm. Ordering against a bare recency score is
// pinned separately in TestRankProximityAbstainsWhenTheGoalNamesNoFile.
func TestBuildRetainsRetrievedChunkUnderATightBudget(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"auth/login.go":     "package auth\n\nfunc Login(user, pass string) error { return nil }\n",
		"docs/CHANGELOG.md": strings.Repeat("z", 400),
		"docs/NOTES.md":     strings.Repeat("z", 400),
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	rag := []Part{
		{Kind: KindRAG, Source: "auth/login.go#1", Score: 0.1529,
			Text: "func Login(user, pass string) error {\n\tif pass == \"\" {\n\t\treturn errors.New(\"bad password\")\n\t}\n\treturn nil\n}"},
		{Kind: KindRAG, Source: "auth/session.go#1", Score: 0,
			Text: "func NewSession(user string) *Session { return &Session{User: user} }"},
	}
	eng := New(Deps{
		Bus: bus, Repo: root, Memory: fakeMemory{rag: rag},
		Open:       []string{"docs/CHANGELOG.md", "docs/NOTES.md"},
		SoftBudget: 900,
	})
	req := ContextRequest{Task: &session.Task{
		ID: "t1", Goal: "fix the Login function so bad passwords are rejected",
	}}

	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	kept := false
	for _, p := range pkg.RAG {
		if p.Source == "auth/login.go#1" {
			kept = true
		}
	}
	if !kept {
		got := make([]string, 0, len(pkg.Files)+len(pkg.RAG))
		for _, p := range pkg.Files {
			got = append(got, "file "+p.Source)
		}
		for _, p := range pkg.RAG {
			got = append(got, "rag "+p.Source)
		}
		t.Errorf("auth/login.go#1 evicted; package kept %v. The retrieved chunk holding "+
			"the function under repair must survive two unrelated open files", got)
	}
	// The eviction has to land somewhere: with 900 bytes, keeping the chunk
	// means one of the two 400-byte noise files goes.
	if len(pkg.Files) == 2 {
		t.Errorf("both noise files kept (%d bytes of budget 900) — the budget was not "+
			"actually tight, so this test proves nothing", 800)
	}
}

// TestBuildRAGEmptyWhenMemoryReturnsNone: a noop memory seam leaves pkg.RAG
// empty (the default path before L10-006 wires a real store).
func TestBuildRAGEmptyWhenMemoryReturnsNone(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, nil) // noopMemory
	req, _ := newReq(repo, "do something")
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pkg.RAG) != 0 {
		t.Errorf("pkg.RAG = %d parts, want 0 (noop memory)", len(pkg.RAG))
	}
}

// TestBuildKeepsTheSystemFrameAtARealisticBudget is the end-to-end half of the
// compress-level guard: the same fixture as
// TestBuildRetainsRetrievedChunkUnderATightBudget, which measured pkg.System at
// 0 parts. The retrieved chunk surviving is worth nothing if the prompt it
// survives into has no rules, no tool list, and no role.
func TestBuildKeepsTheSystemFrameAtARealisticBudget(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"docs/CHANGELOG.md": strings.Repeat("z", 400),
		"docs/NOTES.md":     strings.Repeat("z", 400),
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	rag := []Part{{Kind: KindRAG, Source: "auth/login.go#1", Score: 0.1529,
		Text: "func Login(user, pass string) error { return nil }"}}
	eng := New(Deps{
		Bus: bus, Repo: root, Memory: fakeMemory{rag: rag},
		Open:       []string{"docs/CHANGELOG.md", "docs/NOTES.md"},
		SoftBudget: 900,
	})
	req := ContextRequest{Task: &session.Task{
		ID: "t1", Goal: "fix the Login function so bad passwords are rejected",
	}}
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pkg.System) != 1 {
		t.Fatalf("pkg.System = %d parts at SoftBudget 900, want 1: the 1505-byte frame is "+
			"not gathered repository state and is not the thing to evict", len(pkg.System))
	}
	if !contains(pkg.System[0].Text, "AVAILABLE TOOLS") {
		t.Error("the surviving system part is not the frame")
	}
	// And the exemption must not have starved what the budget is actually for.
	if len(pkg.Files) == 0 && len(pkg.RAG) == 0 {
		t.Error("every gathered group empty: the frame is exempt from the budget, " +
			"not entitled to spend it")
	}
}

// TestSystemPromptToolListFollowsTheInjectedToolSet pins the one direction that
// matters for the fourth copy of the tool list: when the composition root
// injects the offered set (cognitive.DefaultTools(), which is also what produces
// the provider's schemas and the Core's allowlist), the prompt lists that set
// and nothing else. A tool removed upstream disappears here without anyone
// editing this package, and a tool added upstream appears — visibly undescribed
// if nobody has written its line, which is the point: silence is what the old
// hand-typed list gave.
func TestSystemPromptToolListFollowsTheInjectedToolSet(t *testing.T) {
	repo := fixtureRepo(t)
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	eng := New(Deps{Bus: bus, Repo: repo, Tools: []string{"read_file", "web_fetch"}})
	req, _ := newReq(repo, "goal")
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	text := pkg.System[0].Text
	if !contains(text, "- read_file: read a file's contents") {
		t.Error("an offered tool with prose must be listed with it")
	}
	if !contains(text, "- web_fetch: (no description available)") {
		t.Error("an offered tool with no prose must still be listed, visibly undescribed — " +
			"the model can call it, so the prompt may not pretend it does not exist")
	}
	// Assert on the list form specifically. The CODING STRATEGY paragraph below
	// the list names read_file/grep/edit_file as prose advice about a workflow;
	// that is writing, not a registry, and it is not what drifts.
	for _, gone := range []string{"edit_file", "list_files", "bash"} {
		if contains(text, "- "+gone+": ") {
			t.Errorf("prompt lists %q, which was not offered: the injected set is the "+
				"source of truth for which tools appear", gone)
		}
	}
}

// TestSystemPromptFallsBackToTheLocalToolListWhenNoneIsInjected: an Engine built
// without Deps.Tools (tests, and any caller with no Cognitive Core to ask) keeps
// the standalone list, so this package still builds a usable prompt alone.
func TestSystemPromptFallsBackToTheLocalToolListWhenNoneIsInjected(t *testing.T) {
	repo := fixtureRepo(t)
	eng := newEngine(t, repo, nil)
	req, _ := newReq(repo, "goal")
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{"list_files", "read_file", "edit_file", "bash", "grep"} {
		if !contains(pkg.System[0].Text, "- "+want+": ") {
			t.Errorf("fallback tool list missing %q", want)
		}
	}
}
