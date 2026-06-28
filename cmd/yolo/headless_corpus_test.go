package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	econtext "github.com/yolo-code/yolo/internal/context"
	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/session"
)

// corpusPath returns the absolute path to the committed fixture corpus under
// cmd/yolo/testdata/corpus.
func corpusPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "corpus"))
	if err != nil {
		t.Fatalf("abs corpus path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p, "AGENTS.md")); err != nil {
		t.Fatalf("corpus missing AGENTS.md at %s: %v", p, err)
	}
	return p
}

// goldenPath returns the path to the committed golden prompt fixture. The
// fixture is the expected rendered <system>+<project>+user+<files> block the
// L5-003 corpus task must produce, byte-identical across runs (S5).
func goldenPath() string { return filepath.Join("testdata", "golden_login_prompt.txt") }

// TestHeadlessWiresRealContextAndPromptToStubCore is the L5-003 / Sprint 2 exit
// bar (S6): with the real Layer 4 Context Engine and Layer 5 Prompt Compiler
// wired into the headless runner, the stub cognitive core receives a compiled
// prompt that carries the real file contents from the fixture corpus. The
// asserting core records whether it saw the open file's function body.
func TestHeadlessWiresRealContextAndPromptToStubCore(t *testing.T) {
	repo := corpusPath(t)
	eng := econtext.New(econtext.Deps{
		Bus:  event.New(),
		Repo: repo,
		Open: []string{"auth/login.go"},
	})
	comp := prompt.New(nil, nil)
	cog := &assertCognitive{want: "func Login", answer: "saw the file"}

	_, err := runHeadlessDeps(context.Background(), bytes.NewBufferString("explain @auth/login.go\n"), 0,
		&headlessDeps{
			context: contextAdapter{eng: eng},
			prompt:  promptAdapter{comp: comp},
			cog:     cog,
		})
	if err != nil {
		t.Fatalf("runHeadlessDeps: %v", err)
	}

	if !cog.saw {
		t.Fatal("assertCognitive.Think was never called; the stub core did not receive a prompt")
	}
	if !cog.ok {
		t.Error("compiled prompt did NOT contain the open file's body (func Login); the stub core saw no real file contents")
	}
}

// TestCompiledPromptMatchesGoldenFixture locks the L5-003 wire output: the
// compiled prompt for the corpus task is byte-identical to the committed
// golden fixture. If the pipeline changes intentionally, regenerate the
// fixture by deleting it and re-running (UPDATE_GOLDEN=1). Otherwise a drift
// here is a determinism regression (S5).
func TestCompiledPromptMatchesGoldenFixture(t *testing.T) {
	repo := corpusPath(t)
	eng := econtext.New(econtext.Deps{
		Bus:  event.New(),
		Repo: repo,
		Open: []string{"auth/login.go"},
	})
	comp := prompt.New(nil, nil)

	// Capture the compiled prompt directly: build the package and compile, the
	// same path the headless runner takes via the adapters. This isolates the
	// golden assertion from the event transcript (which carries bus seqs the
	// golden would have to reproduce).
	pkg := buildCorpusPackage(t, eng, "explain @auth/login.go")
	msgs := comp.CompilePackage(pkg)
	joined := joinMessages(msgs)

	gp := goldenPath()
	if _, err := os.Stat(gp); err != nil || os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(gp), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(gp, []byte(joined), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("regenerated golden fixture %s", gp)
		return
	}
	want, err := os.ReadFile(gp)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if joined != string(want) {
		t.Errorf("compiled prompt drifted from golden fixture (S5)\n--- got ---\n%s\n--- want ---\n%s", joined, want)
	}
}

// buildCorpusPackage drives the Context Engine over the corpus the same way the
// headless runner's contextAdapter does, returning the package for direct
// compilation. It needs a real session task, so it builds one in a temp store.
func buildCorpusPackage(t *testing.T, eng *econtext.Engine, goal string) *econtext.ContextPackage {
	t.Helper()
	dir, err := os.MkdirTemp("", "yolo-golden")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir), Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(context.Background(), "golden", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	tid, err := smgr.StartTask(context.Background(), sid, goal)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	sess, _, _ := smgr.Resume(context.Background(), sid)
	task := smgr.LoadTaskPublic(tid)
	pkg, err := eng.Build(context.Background(), econtext.ContextRequest{Task: task, Session: sess})
	if err != nil {
		t.Fatalf("Engine.Build: %v", err)
	}
	return pkg
}

// joinMessages concatenates the compiled prompt's messages with their role tags
// so the golden is a faithful, readable snapshot of the wire output.
func joinMessages(msgs []prompt.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString("## role: ")
		b.WriteString(m.Role)
		b.WriteByte('\n')
		b.WriteString(m.Content)
		if !strings.HasSuffix(m.Content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}
