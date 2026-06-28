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

// SecretPattern is one redaction rule: a name, a compiled pattern, and the
// replacement token (e.g. "[REDACTED:aws_key]"). The replacement is a literal
// string, not a regexp replacement template, so no capture-group expansion.
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
		out = p.Pattern.ReplaceAllString(out, p.Replace)
	}
	return out
}

// RedactAttrs redacts string values in a slog attribute slice in place (§13.5.4
// boundary 2). Returns a new slice (values may be replaced with the redaction
// token); non-strings pass through. Nil-receiver safe.
func (s *Secrets) RedactAttrs(attrs []any) []any {
	if s == nil {
		return attrs
	}
	out := make([]any, len(attrs))
	for i, a := range attrs {
		if str, ok := a.(string); ok {
			out[i] = s.Redact(str)
		} else {
			out[i] = a
		}
	}
	return out
}

// RedactMap returns a copy of m with string values redacted (§13.6.3 boundary
// 3 — Sentry extras). Non-strings pass through. Nil-receiver safe.
func (s *Secrets) RedactMap(m map[string]any) map[string]any {
	if s == nil {
		return m
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if str, ok := v.(string); ok {
			out[k] = s.Redact(str)
		} else {
			out[k] = v
		}
	}
	return out
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

// defaultSecretPatterns returns the §13.7.1 built-in rules. The shapes mirror
// the local patterns exec/normalizer.go used (Sprint 4) so the registry-backed
// redaction is behavior-identical when the composition root delegates.
func defaultSecretPatterns() []SecretPattern {
	return []SecretPattern{
		{Name: "aws_access_key_id", Pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`), Replace: "[REDACTED:aws_key]"},
		{Name: "github_pat", Pattern: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`), Replace: "[REDACTED:github_pat]"},
		{Name: "pem_block", Pattern: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), Replace: "[REDACTED:pem]"},
		{Name: "generic_kv", Pattern: regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd|pwd)\s*[=:]\s*['"]?[^\s'"&]{8,}`), Replace: "[REDACTED:kv]"},
		{Name: "jwt", Pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), Replace: "[REDACTED:jwt]"},
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
