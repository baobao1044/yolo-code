// Secrets redaction registry (File 13 §13.7). The registry masks secret shapes
// (AWS keys, GitHub tokens, PEM blocks, key=value secret assignments, JWTs) and
// is applied at three boundaries (§13.7.3): execution output (File 08 §8.4.5),
// the log line (§13.5.4), and the Sentry event (§13.6.3). A failure at any one
// does not leak — defense in depth, because logs outlive the in-memory raw
// output and a slog misconfiguration or a future fmt.Println can't leak.
//
// L12-005 ties the seams L12-003 (logRedactor) and L12-004 (sentryRedactor)
// already wired: a single *Secrets satisfies both interfaces, so one registry
// backs the two in-infra boundaries. The exec boundary (L7 normalizer) is
// wired via composition-root injection (L12-009) — exec can't import infra
// (import matrix, bottom-up), so exec keeps a Redactor seam and the root
// injects an adapter backed by *Secrets. The defaultSecretPatterns mirror the
// local patterns exec/normalizer.go used (Sprint 4), so delegation doesn't
// change behavior.

package infra

import (
	"errors"
	"regexp"
	"sync"
)

// defaultRegistry is the process-wide redaction registry returned by
// DefaultRedactor. It exists because the three boundaries are constructed at
// different points in the composition root's wiring: the exec engine (boundary
// 1) is built before — and, on the headless path, without — an *Infra, while
// the log and Sentry boundaries are built inside Start. A single package-level
// registry makes redaction on by default at all three regardless of that
// order, and makes a Register call from the root apply to all three rather
// than to whichever registry happened to be constructed first. Isolated
// registries are still available via NewSecrets.
var defaultRegistry = NewSecrets()

// DefaultRedactor returns the process-wide redaction registry — the single
// entry point the composition root wires into every boundary. *Secrets
// satisfies exec.Redactor (Redact), logRedactor (RedactAttrs) and
// sentryRedactor (RedactMap), so one value covers all three: pass it to
// exec.NewNormalizerWithRedactor for the execution-output boundary (§8.4.5);
// Start already uses it for the log line (§13.5.4) and the Sentry event
// (§13.6.3). Safe for concurrent use.
func DefaultRedactor() *Secrets { return defaultRegistry }

// SecretPattern is one redaction rule: a name, a compiled pattern, and the
// replacement token (e.g. "[REDACTED:aws_key]"). The replacement is applied
// literally (ReplaceAllLiteralString), so a "$1" in a Register'd rule's Replace
// is emitted as-is rather than expanded into a capture group.
type SecretPattern struct {
	Name    string
	Pattern *regexp.Regexp
	Replace string
}

// Secrets is the redaction registry. patterns is the ordered rule list; the mu
// guards Register (the only writer; Redact reads under RLock). A nil *Secrets
// is safe — every method passes through (the opt-out seam, §13.6.1-style).
type Secrets struct {
	patterns []SecretPattern
	mu       sync.RWMutex
}

// NewSecrets builds a registry seeded with defaultSecretPatterns (§13.7.1).
func NewSecrets() *Secrets {
	return &Secrets{patterns: defaultSecretPatterns()}
}

// Register adds a runtime-supplied pattern (e.g. from repo-local config,
// §13.7.1). Returns an error if the pattern is nil (defensive — a nil
// regexp would panic on Match). Thread-safe; appends to the end (later rules
// run after defaults, so a custom rule can refine but not preempt).
//
// This is also the only way to cover a secret with no recognizable shape: the
// composition root can register the resolved API key's literal value with
// regexp.MustCompile(regexp.QuoteMeta(key)), which turns the shape-based
// defaults into shape-plus-exact-value coverage for the key the run actually
// authenticated with. No caller does that today (see defaultSecretPatterns'
// note on prefix-less provider keys).
func (s *Secrets) Register(p SecretPattern) error {
	if p.Pattern == nil {
		return errNilPattern
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patterns = append(s.patterns, p)
	return nil
}

// Redact masks all secret shapes in in (§13.7.2). Used by L8 (Execution) before
// publishing tool output (boundary 1, via the composition-root adapter), and
// recursively by RedactAttrs/RedactMap. Nil-receiver safe: returns in unchanged.
func (s *Secrets) Redact(in string) string {
	if s == nil {
		return in
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := in
	for _, p := range s.patterns {
		out = p.Pattern.ReplaceAllLiteralString(out, p.Replace)
	}
	return out
}

// RedactAttrs redacts a slog attribute slice (§13.5.4 boundary 2). Returns a
// new slice; each element goes through redactValue, so a structured field —
// an event whose JSON carries a nested object or array — is redacted at every
// depth, not just when the whole value happens to be a top-level string.
// Nil-receiver safe.
func (s *Secrets) RedactAttrs(attrs []any) []any {
	if s == nil {
		return attrs
	}
	out := make([]any, len(attrs))
	for i, a := range attrs {
		out[i] = s.redactValue(a)
	}
	return out
}

// RedactMap returns a copy of m with values redacted (§13.6.3 boundary 3 —
// Sentry extras). Values go through redactValue, so nested maps and slices are
// covered too. Nil-receiver safe.
func (s *Secrets) RedactMap(m map[string]any) map[string]any {
	if s == nil {
		return m
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = s.redactValue(v)
	}
	return out
}

// redactValue redacts one decoded value of any shape. Strings go through
// Redact; maps and slices recurse. A structured log field arrives here as a
// map[string]any or []any (appendAttrs/sentryExtras JSON-decode the event), and
// a string-only walk left every secret one level down in the clear — the
// boundary looked wired while leaking. Unknown types (numbers, bools, nil) pass
// through untouched. Caller guarantees s != nil.
func (s *Secrets) redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return s.Redact(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = s.redactValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = s.redactValue(vv)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(t))
		for k, vv := range t {
			out[k] = s.Redact(vv)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, vv := range t {
			out[i] = s.Redact(vv)
		}
		return out
	default:
		return v
	}
}

// WouldLeak reports whether in contains any secret shape (§13.7.2). Used as a
// gate before publishing tool output that wasn't already redacted. Nil-receiver
// safe (returns false — no registry, no gate).
func (s *Secrets) WouldLeak(in string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.patterns {
		if p.Pattern.MatchString(in) {
			return true
		}
	}
	return false
}

// defaultSecretPatterns returns the §13.7.1 built-in rules — the union of what
// this registry and the local patterns in exec/normalizer.go each covered, so
// the composition root can delegate the exec boundary here without losing a
// shape. Order matters: the specific rules run before the generic key=value one
// so a leak's origin stays readable in the redaction token.
func defaultSecretPatterns() []SecretPattern {
	return []SecretPattern{
		// AWS access key IDs. ASIA (STS session) / ABIA / ACCA share AKIA's
		// 16-char body and are just as sensitive.
		{Name: "aws_access_key_id", Pattern: regexp.MustCompile(`A(?:KIA|SIA|BIA|CCA)[0-9A-Z]{16}`), Replace: "[REDACTED:aws_key]"},
		// An AWS secret access key is 40 base64 chars with no distinguishing
		// prefix — matching it alone would redact half of every hash in the
		// output, so it is matched via the variable name that carries it.
		{Name: "aws_secret_access_key", Pattern: regexp.MustCompile(`(?i)aws_secret_access_key\s*[=:]\s*['"]?[A-Za-z0-9/+=]{40}`), Replace: "[REDACTED:aws_secret]"},
		// Anthropic before the generic sk- rule: the generic rule would swallow
		// it, but the distinct token tells an operator which vendor to rotate.
		{Name: "anthropic_key", Pattern: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{16,}`), Replace: "[REDACTED:anthropic_key]"},
		// OpenAI-style keys: plain sk-, sk-proj-, and the org/service variants
		// are all "sk-" plus one unbroken run of key characters.
		{Name: "openai_key", Pattern: regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), Replace: "[REDACTED:openai_key]"},
		// {36,} not {36}: an exact count stops mid-token on a longer body and
		// leaves the tail of the secret in the clear next to the marker.
		// Provider keys from the cognitive/providers.go preset registry whose
		// vendor prefix makes them matchable on shape alone. Without these, a
		// user who authenticated with GROQ_API_KEY got nothing from this
		// registry the moment the key appeared anywhere but a KEY=value line —
		// in prose, in a JSON body, in a shell history line. Note the residual
		// gap this does NOT close: most presets (together, mistral, fireworks,
		// cohere, wandb, …) issue prefix-less hex/base62 keys that no shape rule
		// can match without redacting every hash in the output. Covering those
		// needs the composition root to Register the resolved key's literal
		// value at startup; see the note on Register.
		{Name: "groq_key", Pattern: regexp.MustCompile(`gsk_[A-Za-z0-9]{20,}`), Replace: "[REDACTED:groq_key]"},
		{Name: "huggingface_token", Pattern: regexp.MustCompile(`hf_[A-Za-z0-9]{20,}`), Replace: "[REDACTED:hf_token]"},
		{Name: "perplexity_key", Pattern: regexp.MustCompile(`pplx-[A-Za-z0-9]{20,}`), Replace: "[REDACTED:perplexity_key]"},
		{Name: "nvidia_key", Pattern: regexp.MustCompile(`nvapi-[A-Za-z0-9_-]{20,}`), Replace: "[REDACTED:nvidia_key]"},
		{Name: "github_pat", Pattern: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`), Replace: "[REDACTED:github_pat]"},
		{Name: "github_fine_grained_pat", Pattern: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), Replace: "[REDACTED:github_pat]"},
		{Name: "bearer_token", Pattern: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`), Replace: "[REDACTED:bearer]"},
		{Name: "pem_block", Pattern: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), Replace: "[REDACTED:pem]"},
		{Name: "jwt", Pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), Replace: "[REDACTED:jwt]"},
		// Credentials inline in a URL. The "://" and "@" are re-emitted in the
		// replacement so the scheme and host survive — the line stays
		// diagnosable and only the userinfo is destroyed.
		{Name: "url_credentials", Pattern: regexp.MustCompile(`://[^/\s:@]+:[^/\s:@]+@`), Replace: "://[REDACTED:url_credentials]@"},
		// .env-style assignment lines. Needed in addition to generic_kv because
		// generic_kv's keyword must sit immediately before the separator, which
		// AWS_SECRET_ACCESS_KEY=… and friends do not.
		{Name: "env_assignment", Pattern: regexp.MustCompile(`(?im)^[ \t]*(?:export[ \t]+)?[A-Za-z_][A-Za-z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|PWD|CREDENTIALS)[ \t]*=[ \t]*\S+`), Replace: "[REDACTED:env]"},
		// No \b before the keyword: the boundary required a non-word char in
		// front, so access_token=…, client_secret=… and refresh_token=… — the
		// exact shapes an env or config dump emits — matched nothing at all.
		{Name: "generic_kv", Pattern: regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|passwd|pwd)\s*[=:]\s*['"]?[^\s'"&]{6,}`), Replace: "[REDACTED:kv]"},
	}
}

// errNilPattern is returned by Register when the pattern's compiled regexp is
// nil (a nil regexp would panic on Match/ReplaceAllString).
var errNilPattern = errors.New("infra.Secrets.Register: nil pattern")

// mustCompile is a test helper (regexp.MustCompile panics on a bad pattern;
// tests want the panic localized to the test, not the package). Defined here so
// the secrets test can register a custom pattern without reaching into regexp.
func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
