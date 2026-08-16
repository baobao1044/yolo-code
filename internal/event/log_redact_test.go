package event

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// secretRedactor is a stand-in for infra.Secrets: event cannot import infra
// (§15.15.2), and the point of the seam is that it does not have to. The
// patterns mirror two real defaults (an sk- key and a bearer token) so the
// tests exercise the same shapes the shipped registry does.
type secretRedactor struct{ rules []*regexp.Regexp }

func newSecretRedactor(patterns ...string) *secretRedactor {
	r := &secretRedactor{}
	for _, p := range patterns {
		r.rules = append(r.rules, regexp.MustCompile(p))
	}
	return r
}

func (r *secretRedactor) Redact(in string) string {
	out := in
	for _, re := range r.rules {
		out = re.ReplaceAllLiteralString(out, "[REDACTED]")
	}
	return out
}

// useRedactor installs r for the duration of the test. The redactor is
// process-wide (see SetLogRedactor), so every test that touches it must put it
// back — otherwise the package's other log tests would start comparing
// redacted bytes.
func useRedactor(t *testing.T, r Redactor) {
	t.Helper()
	SetLogRedactor(r)
	t.Cleanup(func() { SetLogRedactor(nil) })
}

// TestDurabilityLogRedactsEveryEventOnDisk is the empirical form of the leak:
// drive a real bus over a real file with a real secret, then read the bytes
// back off disk. Asserting "the redactor was called" would pass while the file
// still leaked, which is exactly how this went unnoticed — event.Log.Append had
// no redaction hook of any kind, so *every* event reached YOLO_EVENT_LOG in the
// clear, not just the patch.applied diff people looked at.
func TestDurabilityLogRedactsEveryEventOnDisk(t *testing.T) {
	useRedactor(t, newSecretRedactor(`sk-[A-Za-z0-9_-]{16,}`))

	const secret = "sk-proj-Ab3dEfGhIjKlMnOpQrStUvWxYz0123456789"
	path := filepath.Join(t.TempDir(), "bus.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// One event per shape that was proven to leak: the diff a patch carries and
	// the message an error carries. The error is the control — nothing about
	// patch.applied was special, the sink was.
	stream := []Event{
		&PatchAppliedEvent{Task: "t_1", Files: []PatchFile{{Path: "a.go", Insertions: 1}},
			Diff: "--- a/a.go\n+++ b/a.go\n+const key = \"" + secret + "\"\n"},
		&ErrorEvent{Task: "t_1", Layer: "verify", Code: "E1",
			Msg: "build failed: curl -H 'Authorization: " + secret + "' returned 401"},
	}
	for i, e := range stream {
		if err := bus.Publish(context.Background(), e); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("the durability log contains the secret verbatim:\n%s", raw)
	}
	if n := strings.Count(string(raw), "[REDACTED]"); n != 2 {
		t.Errorf("expected one redaction per event, got %d:\n%s", n, raw)
	}
}

// TestRedactedLogStillReplays is the constraint that made the placement of the
// hook a real decision: the log is the crash-replay medium, so a redaction that
// produced bytes Replay cannot read would trade a leak for a lost recovery.
// Redacting the marshaled line as text risked exactly that — the envelope's
// "type" tag is a plain string on the same line, and a rule that rewrote it
// would make Replay fail with "no factory registered".
func TestRedactedLogStillReplays(t *testing.T) {
	// A rule that matches the topic name itself, to prove the tag is out of
	// reach of redaction rather than merely unlikely to be hit.
	useRedactor(t, newSecretRedactor(`sk-[A-Za-z0-9_-]{16,}`, `verification\.failed`))

	const secret = "sk-ant-api03-QQQQQQQQQQQQQQQQQQQQQQQQ"
	path := filepath.Join(t.TempDir(), "bus.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := bus.Publish(context.Background(), &VerificationFailedEvent{
		Task: "t_1", Reason: "go vet: leaked " + secret,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	envs, err := Replay(path)
	if err != nil {
		t.Fatalf("replay a redacted log: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("replayed %d envelopes, want 1", len(envs))
	}
	if envs[0].Seq != 1 {
		t.Errorf("Seq = %d, want 1", envs[0].Seq)
	}
	if envs[0].Evt.Type() != "verification.failed" {
		t.Fatalf("Type = %q, want verification.failed (the tag must survive redaction)", envs[0].Evt.Type())
	}
	got, ok := envs[0].Evt.(*VerificationFailedEvent)
	if !ok {
		t.Fatalf("replayed event is %T, want *VerificationFailedEvent", envs[0].Evt)
	}
	if strings.Contains(got.Reason, secret) {
		t.Errorf("Reason still carries the secret: %q", got.Reason)
	}
	if got.Reason != "go vet: leaked [REDACTED]" {
		t.Errorf("Reason = %q, want the masked text with the rest of the line intact", got.Reason)
	}
}

// TestRedactionCannotCorruptJSONEscapes pins the failure mode that ruled out
// masking the marshaled line as text. Quotes, backslashes and newlines in the
// payload are escape sequences once encoded, and a replacement that lands
// across one leaves a dangling backslash or an early-terminated string — a
// record no reader can parse. Redacting the decoded values and re-encoding
// means the escaping is regenerated, whatever the replacement text is.
func TestRedactionCannotCorruptJSONEscapes(t *testing.T) {
	// A rule that ends on a backslash, which is the first half of an escape
	// once the payload is encoded.
	useRedactor(t, newSecretRedactor(`PASSWORD=[^"]*\\`, `sk-[A-Za-z0-9_-]{16,}`))

	const secret = "sk-live-ZZZZZZZZZZZZZZZZZZZZZZZZ"
	path := filepath.Join(t.TempDir(), "bus.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	msg := "line1: PASSWORD=hunter2\\\nline2: he said \"" + secret + "\"\ttab"
	if err := bus.Publish(context.Background(), &ErrorEvent{
		Task: "t_1", Layer: "exec", Code: "E2", Msg: msg,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	envs, err := Replay(path)
	if err != nil {
		t.Fatalf("replay: %v (a redaction that lands on an escape must not corrupt the line)", err)
	}
	if len(envs) != 1 {
		t.Fatalf("replayed %d envelopes, want 1", len(envs))
	}
	got := envs[0].Evt.(*ErrorEvent)
	if strings.Contains(got.Msg, secret) {
		t.Errorf("Msg still carries the secret: %q", got.Msg)
	}
	// The surrounding text — including the tab and the newline — must survive,
	// or the log has stopped being diagnosable.
	if !strings.Contains(got.Msg, "line2: he said") || !strings.Contains(got.Msg, "\ttab") {
		t.Errorf("Msg lost its non-secret text: %q", got.Msg)
	}
}

// TestNoRedactorLeavesTheLogByteIdentical is the opt-out half of the seam: a
// Log with no redactor installed writes exactly what it always did, so every
// package below infra — and every test in this one — is unaffected.
func TestNoRedactorLeavesTheLogByteIdentical(t *testing.T) {
	write := func(t *testing.T) []byte {
		t.Helper()
		path := filepath.Join(t.TempDir(), "bus.log")
		l, err := OpenLog(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		env := Envelope{Seq: 7, Evt: &ErrorEvent{Task: "t_1", Layer: "exec", Code: "E3", Msg: "boom <&> \"q\""}}
		if err := l.Append(env); err != nil {
			t.Fatalf("append: %v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return raw
	}

	SetLogRedactor(nil)
	plain := write(t)

	// A redactor that masks nothing must not change the bytes either: the
	// decode/re-encode round trip has to be format-preserving, not just
	// parseable.
	useRedactor(t, newSecretRedactor(`this-matches-nothing-at-all`))
	redacted := write(t)

	if string(plain) != string(redacted) {
		t.Errorf("a no-op redactor changed the on-disk format\n plain    %s redacted %s", plain, redacted)
	}
	if !strings.Contains(string(plain), `"type":"error"`) {
		t.Errorf("unexpected wire format: %s", plain)
	}
}
