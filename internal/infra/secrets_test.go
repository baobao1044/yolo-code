// Tests for L12-005 — Secrets redaction registry + 3 boundaries (File 13
// §13.7). The Secrets registry masks secret shapes (AWS keys, GitHub tokens,
// PEM blocks, key=value secrets, JWTs) and is applied at three boundaries
// (§13.7.3): execution output (File 08 §8.4.5), the log line (§13.5.4), and the
// Sentry event (§13.6.3). A failure at any one does not leak.
//
// L12-005 ties the seams L12-003 (logRedactor) and L12-004 (sentryRedactor)
// already wired: a single *Secrets satisfies both interfaces, so one registry
// backs the two in-infra boundaries. The exec boundary (L7 normalizer) is
// wired via composition-root injection (L12-009) — exec can't import infra
// (import matrix, bottom-up), so exec keeps a Redactor seam and the root
// injects an adapter. L12-005 ships the registry + proves the two in-infra
// boundaries use it; the exec delegation is a thin change tested in the exec
// package.

package infra

import (
	"bytes"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestSecretsDefaultPatternsMaskKnownSecrets pins §13.7.1: the default registry
// masks the known secret shapes — AWS access key, GitHub PAT, PEM block, key=value
// secret assignment, and a bare API key assignment. Each shape becomes a
// REDACTED marker; the raw secret never survives Redact.
func TestSecretsDefaultPatternsMaskKnownSecrets(t *testing.T) {
	s := NewSecrets()
	cases := []struct {
		name  string
		input string
		// The raw secret substring must NOT appear in the redacted output.
		leak string
	}{
		{"aws key", "creds: AKIAIOSFODNN7EXAMPLE end", "AKIAIOSFODNN7EXAMPLE"},
		{"github pat", "env GHP ghp_0123456789abcdefghijABCDEFGHIJ012345 set", "ghp_0123456789abcdefghijABCDEFGHIJ012345"},
		{"pem block", "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----", "MIIE..."},
		{"kv secret", "config api_key=supersecret123 done", "supersecret123"},
		{"jwt", "auth eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKK", "eyJhbGciOiJIUzI1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := s.Redact(c.input)
			if strings.Contains(out, c.leak) {
				t.Errorf("Redact leaked %q; got %q", c.leak, out)
			}
			if !strings.Contains(out, "REDACTED") {
				t.Errorf("Redact missing REDACTED marker; got %q", out)
			}
		})
	}
}

// TestSecretsRegisterAddsCustomPattern pins §13.7.1 Register: a runtime-supplied
// pattern (e.g. repo-local config) is added and Redact masks it. A secret
// matching only the custom pattern is masked after Register.
func TestSecretsRegisterAddsCustomPattern(t *testing.T) {
	s := NewSecrets()
	// Before register: a custom token shape is NOT masked.
	before := s.Redact("key YOLO-CUSTOM-TOKEN-abc-123 end")
	if !strings.Contains(before, "YOLO-CUSTOM-TOKEN-abc-123") {
		t.Fatalf("pre-register Redact masked the custom token (no pattern yet): %q", before)
	}
	// Register a custom pattern masking the YOLO-CUSTOM-TOKEN-<hex> shape.
	if err := s.Register(SecretPattern{
		Name: "yolo_custom",
		// A simple literal-prefix pattern.
		Pattern: mustCompile(`YOLO-CUSTOM-TOKEN-[a-z0-9-]+`),
		Replace: "[REDACTED:yolo_custom]",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	after := s.Redact("key YOLO-CUSTOM-TOKEN-abc-123 end")
	if strings.Contains(after, "YOLO-CUSTOM-TOKEN-abc-123") {
		t.Errorf("post-register Redact leaked the custom token: %q", after)
	}
	if !strings.Contains(after, "[REDACTED:yolo_custom]") {
		t.Errorf("post-register Redact missing custom marker: %q", after)
	}
}

// TestSecretsWouldLeak pins §13.7.2 WouldLeak: a gate before publishing tool
// output. A string with a secret shape → true; a clean string → false.
func TestSecretsWouldLeak(t *testing.T) {
	s := NewSecrets()
	if !s.WouldLeak("has AKIAIOSFODNN7EXAMPLE in it") {
		t.Error("WouldLeak=false for a string containing an AWS key; want true")
	}
	if s.WouldLeak("just some plain text, no secrets") {
		t.Error("WouldLeak=true for a clean string; want false")
	}
}

// TestSecretsSatisfiesLogRedactorBoundary2 pins §13.5.4 second boundary: a
// *Secrets injected as a logProjector's redactor masks secret-bearing event
// fields before they hit the log. One registry, two in-infra boundaries.
func TestSecretsSatisfiesLogRedactorBoundary2(t *testing.T) {
	cfg := testConfig()
	var buf bytes.Buffer
	lp := newLogProjector(cfg, &buf)
	lp.redactor = NewSecrets() // *Secrets satisfies logRedactor

	lp.projectLog(mkEnv(1, &event.AssistantMessageEvent{
		Task: "t_1", Text: "leaked AKIAIOSFODNN7EXAMPLE in my reply", Final: true,
	}))

	out := buf.String()
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("log leaked the AWS key; §13.5.4 boundary failed: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Errorf("log missing redaction marker: %q", out)
	}
}

// TestSecretsSatisfiesSentryRedactorBoundary3 pins §13.6.3 third boundary: a
// *Secrets injected as a SentryHub's redactor masks secret-bearing error fields
// before they're captured. One registry backs both in-infra boundaries.
func TestSecretsSatisfiesSentryRedactorBoundary3(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg)
	hub.redactor = NewSecrets() // *Secrets satisfies sentryRedactor

	hub.Report(mkEnv(1, &event.ErrorEvent{Task: "t_1", Msg: "leaked AKIAIOSFODNN7EXAMPLE here"}))

	captured := hub.Captured()
	if len(captured) != 1 {
		t.Fatalf("captured %d, want 1", len(captured))
	}
	if strings.Contains(captured[0].Message, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("sentry message leaked the AWS key; §13.6.3 boundary failed: %q", captured[0].Message)
	}
	for _, v := range captured[0].Extras {
		if str, ok := v.(string); ok && strings.Contains(str, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("sentry extra leaked the AWS key: %q", str)
		}
	}
}

// TestSecretsRedactAttrsRedactsStringValues pins §13.7.2 RedactAttrs: string
// values in the alternating slog attr slice are redacted; non-strings pass
// through. A nil *Secrets is safe (zero-value Redact returns the input).
func TestSecretsRedactAttrsRedactsStringValues(t *testing.T) {
	s := NewSecrets()
	attrs := []any{"topic", "tool.result", "stdout", "key=AKIAIOSFODNN7EXAMPLE", "count", 3}
	out := s.RedactAttrs(attrs)
	// The string carrying the secret is masked; the int passes through.
	if str, ok := out[3].(string); ok && strings.Contains(str, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("RedactAttrs leaked the secret in a string value: %q", str)
	}
	if out[5] != 3 {
		t.Errorf("RedactAttrs mutated a non-string value; got %v, want 3", out[5])
	}
}

// TestSecretsRedactMapRedactsStringValues pins §13.7.2 RedactMap: string values
// in the map are redacted; non-strings pass through.
func TestSecretsRedactMapRedactsStringValues(t *testing.T) {
	s := NewSecrets()
	m := map[string]any{"msg": "leaked AKIAIOSFODNN7EXAMPLE", "count": 5}
	out := s.RedactMap(m)
	if str, ok := out["msg"].(string); ok && strings.Contains(str, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("RedactMap leaked the secret: %q", str)
	}
	if out["count"] != 5 {
		t.Errorf("RedactMap mutated a non-string value; got %v, want 5", out["count"])
	}
}

// TestSecretsNilReceiverIsSafe pins the nil-safe seam: a nil *Secrets (no
// registry wired) passes values through unchanged. The boundaries exist; a nil
// registry is the opt-out.
func TestSecretsNilReceiverIsSafe(t *testing.T) {
	var s *Secrets
	if got := s.Redact("plain text"); got != "plain text" {
		t.Errorf("nil Redact = %q, want passthrough", got)
	}
	if got := s.RedactAttrs([]any{"k", "v"}); len(got) != 2 || got[1] != "v" {
		t.Errorf("nil RedactAttrs = %v, want passthrough", got)
	}
	if got := s.RedactMap(map[string]any{"k": "v"}); got["k"] != "v" {
		t.Errorf("nil RedactMap = %v, want passthrough", got)
	}
	if s.WouldLeak("anything") {
		t.Error("nil WouldLeak = true, want false")
	}
}
