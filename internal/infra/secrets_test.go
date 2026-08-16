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
	"context"
	"fmt"
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

// TestSecretsRealisticShapes is the §13.7.1 coverage table against the secret
// shapes that actually turn up in this agent's tool output: provider API keys,
// bearer headers, AWS credentials, .env dumps, and clone URLs with inline
// credentials. Each case asserts the raw secret does not survive Redact AND
// that WouldLeak flags it — the two must agree, or the pre-publish gate waves
// through output the redactor would have masked.
func TestSecretsRealisticShapes(t *testing.T) {
	s := NewSecrets()
	cases := []struct {
		name  string
		input string
		leak  string
	}{
		{"openai key", "OPENAI_API_KEY is sk-proj-Ab3dEfGhIjKlMnOpQrStUvWxYz0123456789 ok", "sk-proj-Ab3dEfGhIjKlMnOpQrStUvWxYz0123456789"},
		{"openai legacy key", "using sk-Ab3dEfGhIjKlMnOpQrStUvWxYz012345 now", "sk-Ab3dEfGhIjKlMnOpQrStUvWxYz012345"},
		{"anthropic key", "ANTHROPIC key sk-ant-api03-Ab3dEfGhIjKlMnOpQrStUvWx-Yz0123456789AA done", "sk-ant-api03-Ab3dEfGhIjKlMnOpQrStUvWx-Yz0123456789AA"},
		{"bearer token", "curl -H 'Authorization: Bearer abcDEF123456ghiJKL789' https://x", "abcDEF123456ghiJKL789"},
		{"aws access key", "creds: AKIAIOSFODNN7EXAMPLE end", "AKIAIOSFODNN7EXAMPLE"},
		{"aws sts key", "creds: ASIAIOSFODNN7EXAMPLE end", "ASIAIOSFODNN7EXAMPLE"},
		{"aws secret access key", "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"env line", "OPENAI_API_KEY=hunter2hunter2hunter2", "hunter2hunter2hunter2"},
		{"env line with export", "export GITHUB_TOKEN=ghtokenvalue1234567", "ghtokenvalue1234567"},
		{"env line db password", "DB_PASSWORD=s3cr3t-p4ssw0rd", "s3cr3t-p4ssw0rd"},
		{"url with credentials", "cloning https://alice:hunter2pass@github.com/o/r.git now", "hunter2pass"},
		{"postgres url", "DSN postgres://dbuser:dbpassword@db.internal:5432/app", "dbpassword"},
		// The \b that used to precede the keyword made these three invisible:
		// the char before the keyword is "_", a word char, so no boundary.
		{"access_token kv", "resp access_token=abcdef1234567890 end", "abcdef1234567890"},
		{"client_secret kv", "cfg client_secret=zyxwvu9876543210 end", "zyxwvu9876543210"},
		{"refresh_token kv", "resp refresh_token: qqqqwwwweeeerrrr end", "qqqqwwwweeeerrrr"},
		// Prefixed provider keys from the cognitive/providers.go presets. Only
		// the KEY=value form was covered before, so a Groq key echoed in prose
		// or inside a JSON body — which is how a curl transcript or an error
		// message carries it — went out in the clear for every user who
		// authenticated with anything but YOLO_API_KEY/OPENAI_API_KEY.
		{"groq key", "using gsk_0123456789abcdefghijABCDEFGHIJ01 now", "gsk_0123456789abcdefghijABCDEFGHIJ01"},
		{"huggingface token", "HF token hf_0123456789abcdefghijABCD set", "hf_0123456789abcdefghijABCD"},
		{"perplexity key", "key pplx-0123456789abcdefghijABCD ok", "pplx-0123456789abcdefghijABCD"},
		{"nvidia key", "key nvapi-0123456789abcdefghijABCD ok", "nvapi-0123456789abcdefghijABCD"},
		// Previously covered shapes must stay covered.
		{"github pat", "env ghp_0123456789abcdefghijABCDEFGHIJ012345 set", "ghp_0123456789abcdefghijABCDEFGHIJ012345"},
		{"pem block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEsecret\n-----END RSA PRIVATE KEY-----", "MIIEsecret"},
		{"jwt", "auth eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKK", "eyJhbGciOiJIUzI1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := s.Redact(c.input)
			if strings.Contains(out, c.leak) {
				t.Errorf("Redact leaked %q\n in: %q\nout: %q", c.leak, c.input, out)
			}
			if !strings.Contains(out, "REDACTED") {
				t.Errorf("Redact produced no marker; got %q", out)
			}
			if !s.WouldLeak(c.input) {
				t.Errorf("WouldLeak=false for %q — the pre-publish gate disagrees with Redact", c.input)
			}
		})
	}
}

// TestSecretsGithubPatTailNotLeaked pins the {36,} fix: an exact {36} count
// stopped mid-token on a longer body and left the tail of the secret sitting in
// the clear right next to the redaction marker.
func TestSecretsGithubPatTailNotLeaked(t *testing.T) {
	tok := "ghp_" + strings.Repeat("a", 36) + "TAIL42"
	out := NewSecrets().Redact("token " + tok)
	if strings.Contains(out, "TAIL42") {
		t.Errorf("Redact left the token's tail in the clear: %q", out)
	}
}

// TestSecretsKeepsURLHostReadable pins that url_credentials destroys only the
// userinfo: the scheme and host survive so a redacted log line is still
// diagnosable. Redaction that erases the whole line gets turned off in practice.
func TestSecretsKeepsURLHostReadable(t *testing.T) {
	out := NewSecrets().Redact("git clone https://alice:hunter2pass@github.com/org/repo.git")
	if strings.Contains(out, "hunter2pass") || strings.Contains(out, "alice") {
		t.Errorf("credentials survived: %q", out)
	}
	if !strings.Contains(out, "github.com/org/repo.git") {
		t.Errorf("host/path was destroyed along with the credentials: %q", out)
	}
}

// TestSecretsReplaceIsLiteral pins the documented Register contract: the
// Replace string is emitted literally, so a "$1" in a runtime-supplied rule is
// not silently expanded into a capture group.
func TestSecretsReplaceIsLiteral(t *testing.T) {
	s := NewSecrets()
	if err := s.Register(SecretPattern{
		Name:    "literal_dollar",
		Pattern: mustCompile(`ZZTOP-([a-z]+)`),
		Replace: "[REDACTED:$1]",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := s.Redact("value ZZTOP-secretword here")
	if !strings.Contains(out, "[REDACTED:$1]") {
		t.Errorf("Replace was expanded as a template; got %q", out)
	}
	if strings.Contains(out, "secretword") {
		t.Errorf("Redact leaked the captured value: %q", out)
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

// TestSecretsRedactsNestedStructuredFields pins the structured-field boundary:
// a secret one level down inside a log/Sentry field must not survive. The walk
// used to type-assert only top-level strings, so an event carrying a nested
// object or array — which is what appendAttrs/sentryExtras produce once the
// event is JSON-decoded — passed its secrets straight through while the
// boundary looked wired.
func TestSecretsRedactsNestedStructuredFields(t *testing.T) {
	s := NewSecrets()
	const secret = "AKIAIOSFODNN7EXAMPLE"

	nested := map[string]any{
		"env": map[string]any{
			"inner": map[string]any{"creds": "key " + secret},
		},
		"argv":  []any{"aws", "--key", secret},
		"lines": []string{"harmless", "leak " + secret},
		"tags":  map[string]string{"acct": secret},
		"count": 3,
	}

	attrs := s.RedactAttrs([]any{"topic", "tool.result", "payload", nested})
	if got := fmt.Sprint(attrs); strings.Contains(got, secret) {
		t.Errorf("RedactAttrs leaked the secret from a nested field: %s", got)
	}
	if extras := s.RedactMap(map[string]any{"payload": nested}); strings.Contains(fmt.Sprint(extras), secret) {
		t.Errorf("RedactMap leaked the secret from a nested field: %v", extras)
	}
	// Non-strings still pass through untouched.
	out := s.RedactMap(map[string]any{"payload": nested})
	if got := out["payload"].(map[string]any)["count"]; got != 3 {
		t.Errorf("nested non-string was mutated: got %v, want 3", got)
	}
}

// TestSecretsRedactionSurvivesStructuredLogField is the end-to-end version of
// the above at the §13.5.4 boundary: a secret nested inside an event's
// structured field must be gone from the emitted JSON log line, not merely from
// a direct Redact call.
func TestSecretsRedactionSurvivesStructuredLogField(t *testing.T) {
	cfg := testConfig()
	cfg.Log.Format = "json" // so a nested field stays a nested JSON object
	var buf bytes.Buffer
	lp := newLogProjector(cfg, &buf)

	const secret = "sk-ant-api03-Ab3dEfGhIjKlMnOpQrStUvWx-Yz0123456789AA"
	lp.projectLog(mkEnv(1, &event.ToolResultEvent{
		Task: "t_1", Tool: "bash",
		Obs: []byte(`{"stdout":"ANTHROPIC_API_KEY=` + secret + `"}`),
	}))

	if out := buf.String(); strings.Contains(out, secret) {
		t.Errorf("structured log field leaked the key; §13.5.4 boundary failed:\n%s", out)
	}
}

// TestRedactionIsOnByDefault pins the "wired zero times" defect: the redactor
// seam used to start nil, so a projector or hub built outside Start emitted raw
// values and looked correct doing it. Both boundaries must fail closed.
func TestRedactionIsOnByDefault(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"

	var buf bytes.Buffer
	lp := newLogProjector(testConfig(), &buf) // no redactor assigned
	if lp.redactor == nil {
		t.Fatal("newLogProjector left the redactor nil — redaction is off by default")
	}
	lp.projectLog(mkEnv(1, &event.AssistantMessageEvent{Task: "t_1", Text: "leaked " + secret, Final: true}))
	if strings.Contains(buf.String(), secret) {
		t.Errorf("default log projector leaked the secret: %q", buf.String())
	}

	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg) // no redactor assigned
	if hub.redactor == nil {
		t.Fatal("newSentry left the redactor nil — redaction is off by default")
	}
	hub.Report(mkEnv(1, &event.ErrorEvent{Task: "t_1", Msg: "leaked " + secret}))
	if c := hub.Captured(); len(c) != 1 || strings.Contains(c[0].Message, secret) {
		t.Errorf("default Sentry hub leaked the secret: %+v", c)
	}

	// The third boundary in this fan-out, and the one that had no seam at all
	// rather than a seam starting nil: Telemetry took eventAttrs verbatim.
	tel := newTelemetry(testConfig()) // no redactor assigned
	if tel.redactor == nil {
		t.Fatal("newTelemetry left the redactor nil — redaction is off by default")
	}
	tel.Project(context.Background(), mkEnv(1, &event.ErrorEvent{Task: "t_1", Msg: "leaked " + secret}))
	if sp := tel.Spans(); len(sp) != 1 || strings.Contains(fmt.Sprint(sp[0].Attrs), secret) {
		t.Errorf("default telemetry leaked the secret into a span: %+v", sp)
	}
}

// TestDefaultRedactorIsShared pins the composition-root entry point: every
// boundary must land on the SAME registry, or a Register from the root covers
// only whichever one happened to be built first.
func TestDefaultRedactorIsShared(t *testing.T) {
	if DefaultRedactor() == nil {
		t.Fatal("DefaultRedactor returned nil")
	}
	// Bound to locals rather than compared inline: `f() != f()` is the shape
	// staticcheck reads as a typo for a self-comparison, and the two calls
	// being distinct is the whole point of the assertion.
	first, second := DefaultRedactor(), DefaultRedactor()
	if first != second {
		t.Error("DefaultRedactor returned two different registries")
	}
	bus := event.New()
	i, err := Start(context.Background(), bus, testConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = bus.Close(); _ = i.Stop(context.Background()) }()

	if i.Secrets != DefaultRedactor() {
		t.Error("Infra.Secrets is not the default registry — the exec boundary would use a different rule set")
	}
	if i.log.redactor != i.Secrets {
		t.Error("log boundary is not backed by Infra.Secrets")
	}
	// *Secrets must satisfy exec.Redactor structurally (Redact(string) string),
	// so the root can inject it with no adapter.
	var _ interface{ Redact(string) string } = i.Secrets
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
