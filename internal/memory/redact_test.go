// Tests for the Deps.Redactor seam. These are deliberately end-to-end on the
// filesystem: a test that asserted "the redactor was called" would pass while
// knowledge.json on disk still carried the key, which is precisely how this
// boundary stayed open. Every assertion here reads the artifact back off disk.

package memory

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// fakeRedactor stands in for infra.Secrets (memory may not import infra). The
// pattern is one of the shipped defaults, so the test exercises a real shape.
type fakeRedactor struct{ re *regexp.Regexp }

func (f fakeRedactor) Redact(in string) string {
	return f.re.ReplaceAllLiteralString(in, "[REDACTED:openai_key]")
}

// waitFor polls until cond holds or the deadline passes. The listener is a
// separate goroutine, so "the event has been applied" is not observable
// synchronously after Publish.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// TestListenerRedactsTextItPersists drives the real path — real bus, real
// files — with a secret the redactor recognises, then reads the two artifacts
// the listener writes. Before the seam existed, knowledge.json held
//
//	[{"text":"build failed: curl -H 'Authorization: sk-proj-…' returned 401", …}]
//
// verbatim, because verify runs its own commands and never passes through
// exec's output normalizer: the text is raw at birth and nothing between the
// bus and the file touched it.
func TestListenerRedactsTextItPersists(t *testing.T) {
	const secret = "sk-proj-Ab3dEfGhIjKlMnOpQrStUvWxYz0123456789"
	root := t.TempDir()
	bus := event.New()
	s, err := Open(Deps{
		Root:     root,
		Bus:      bus,
		Redactor: fakeRedactor{re: regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`)},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx := context.Background()
	// verification.failed → KnowledgeStore.Record → knowledge.json
	if err := bus.Publish(ctx, &event.VerificationFailedEvent{
		Task:   "t_1",
		Reason: "build failed: curl -H 'Authorization: " + secret + "' returned 401",
	}); err != nil {
		t.Fatalf("publish verification.failed: %v", err)
	}
	// verification.stage → same store, via the stage/detail path
	if err := bus.Publish(ctx, &event.VerificationStageEvent{
		Task: "t_1", Stage: "go vet", Status: "fail",
		Detail: "leaked " + secret + " in /tmp/x/main.go",
	}); err != nil {
		t.Fatalf("publish verification.stage: %v", err)
	}
	// assistant.message → ConversationStore → conversations/<sid>.json
	if err := bus.Publish(ctx, &event.AssistantMessageEvent{
		Task: "s_1", Text: "I will use the key " + secret + " for this call.", Final: true,
	}); err != nil {
		t.Fatalf("publish assistant.message: %v", err)
	}

	waitFor(t, "the listener to apply all three events", func() bool {
		return len(s.insights.All()) == 2 && len(s.conversation.Messages("s_1")) == 1
	})

	// Close flushes every durable sub-store, which is what puts the bytes on
	// disk. The bus closes first so the drain goroutine can exit.
	if err := bus.Close(); err != nil {
		t.Fatalf("bus close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("store close: %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, "knowledge.json"),
		filepath.Join(root, "conversations", "s_1.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s contains the secret verbatim:\n%s", filepath.Base(path), raw)
		}
		if !strings.Contains(string(raw), "[REDACTED:openai_key]") {
			t.Errorf("%s was not redacted at all:\n%s", filepath.Base(path), raw)
		}
		// The surrounding text must survive — a redacted insight that lost its
		// lesson is worth no more than no insight.
		if !strings.Contains(string(raw), "returned 401") && !strings.Contains(string(raw), "for this call") {
			t.Errorf("%s lost its non-secret text:\n%s", filepath.Base(path), raw)
		}
	}
}

// TestNilRedactorPassesTextThrough is the opt-out half: memory sits below infra
// in the import matrix and must keep working with no redactor at all, which is
// what every other test in this package relies on.
func TestNilRedactorPassesTextThrough(t *testing.T) {
	s, bus := newListenerStore(t)
	if err := bus.Publish(context.Background(), &event.VerificationFailedEvent{
		Task: "t_1", Reason: "plain text, no redactor wired",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the insight to be recorded", func() bool { return len(s.insights.All()) == 1 })
	if got := s.insights.All()[0].Text; got != "plain text, no redactor wired" {
		t.Errorf("text = %q, want it unchanged", got)
	}
}
