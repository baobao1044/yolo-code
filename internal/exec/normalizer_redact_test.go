// Tests for the L12-005 exec redaction boundary (File 13 §13.7.3 boundary 1 +
// File 08 §8.4.5). exec can't import infra (import matrix, bottom-up — infra is
// L12), so the normalizer keeps a Redactor seam and the composition root
// injects an adapter backed by infra.Secrets. These tests prove the delegation:
// a custom Redactor masks a secret the local patterns wouldn't, and a nil
// Redactor falls back to the local redact() (behavior-identical to Sprint 4).

package exec

import (
	"strings"
	"testing"
)

// stubRedactor masks one custom token string, standing in for the real
// infra.Secrets-backed adapter (the composition root wires that in L12-009).
type stubRedactor struct{ token, replacement string }

func (r stubRedactor) Redact(s string) string {
	return strings.ReplaceAll(s, r.token, r.replacement)
}

// TestNormalizerWithInjectedRedactorDelegates pins boundary 1: when a Redactor
// is injected (the composition-root adapter backed by infra.Secrets), the
// normalizer delegates to it. A custom token the local patterns don't know is
// masked by the injected redactor — proving the registry backs the exec
// boundary, not the local copy.
func TestNormalizerWithInjectedRedactorDelegates(t *testing.T) {
	out := ToolOutput{Stdout: "leaked YOLO-CUSTOM-TOKEN-xyz in stdout", ExitCode: 0}
	meta := Metadata{Permission: Permission{FS: FSRead}}

	n := NewNormalizerWithRedactor(DefaultLimits(), nil,
		stubRedactor{token: "YOLO-CUSTOM-TOKEN-xyz", replacement: "[REDACTED:custom]"})

	obs := n.Normalize(out, meta)
	if strings.Contains(obs.Stdout, "YOLO-CUSTOM-TOKEN-xyz") {
		t.Errorf("injected redactor did not mask the custom token; got %q", obs.Stdout)
	}
	if !strings.Contains(obs.Stdout, "[REDACTED:custom]") {
		t.Errorf("missing custom redaction marker; got %q", obs.Stdout)
	}
}

// TestNormalizerWithoutRedactorFallsBackToLocal pins the fallback: a default
// NewNormalizer (no Redactor) keeps the Sprint 4 local redact() behavior — an
// AWS key is masked by the local pattern. This guards against the delegation
// change silently dropping the local redaction for an engine that wasn't wired
// with an infra-backed adapter.
func TestNormalizerWithoutRedactorFallsBackToLocal(t *testing.T) {
	out := ToolOutput{Stdout: "creds: AKIAIOSFODNN7EXAMPLE end", ExitCode: 0}
	meta := Metadata{Permission: Permission{FS: FSRead}}

	n := NewNormalizer(DefaultLimits(), nil)
	obs := n.Normalize(out, meta)

	if strings.Contains(obs.Stdout, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("local fallback did not mask the AWS key; got %q", obs.Stdout)
	}
	if !strings.Contains(obs.Stdout, "***AWS_KEY***") {
		t.Errorf("local fallback missing the AWS key marker; got %q", obs.Stdout)
	}
}
