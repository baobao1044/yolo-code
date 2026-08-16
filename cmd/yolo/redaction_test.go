// Composition-root redaction tests. The lower layers cannot import infra
// (§15.15.2), so what is tested here is the injection: that the durability log
// really is wired to the process-wide registry, and that the one secret no
// pattern can recognise — a prefix-less provider key — is registered as a
// literal before anything can publish it.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/infra"
)

// TestDurabilityLogRedactsTheResolvedProviderKey is the end-to-end form: a real
// preset, a real key in its real env var, a real bus over a real file. The key
// is prefix-less hex, which is what most of the 28 presets in
// cognitive/providers.go issue and which no shape rule in infra/secrets.go can
// match — so this passes only because the root registered the literal value.
func TestDurabilityLogRedactsTheResolvedProviderKey(t *testing.T) {
	const key = "7f3c9a1e5b8d240c6e0f9a3b7c1d5e8f"
	t.Setenv("YOLO_PROVIDER", "together")
	t.Setenv("TOGETHER_API_KEY", key)

	// The message deliberately carries the key with no "Bearer" prefix and no
	// key=/token: framing. The first draft of this test used a curl line with
	// "Authorization: Bearer <key>" and passed before the literal was ever
	// registered — the built-in bearer_token rule had caught it, so the test was
	// green for the wrong reason. WouldLeak on the *whole message* is the check
	// that keeps it honest: it must be false here, meaning nothing in the
	// registry recognises this text, so a redacted log can only be the literal
	// registration's doing.
	msg := "provider together rejected the request (HTTP 401); credential " + key + " was refused"
	// A pristine registry, not DefaultRedactor: this test also has to hold under
	// -count=2, where the second pass finds the key already registered in the
	// process-wide one.
	if infra.NewSecrets().WouldLeak(msg) {
		t.Fatalf("a built-in pattern already redacts this message; the test would "+
			"pass without registerResolvedAPIKey:\n%s", infra.NewSecrets().Redact(msg))
	}

	registerResolvedAPIKey()

	path := filepath.Join(t.TempDir(), "events.log")
	bus, err := event.Open(path)
	if err != nil {
		t.Fatalf("open bus: %v", err)
	}
	if err := bus.Publish(context.Background(), &event.ErrorEvent{
		Task: "t_1", Layer: "exec", Code: "E1", Msg: msg,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(raw), key) {
		t.Errorf("YOLO_EVENT_LOG contains the provider key verbatim:\n%s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED:api_key]") {
		t.Errorf("the key was not redacted:\n%s", raw)
	}
	// Replay is the reason the log exists; redaction must not cost it.
	envs, err := event.Replay(path)
	if err != nil {
		t.Fatalf("replay a redacted log: %v", err)
	}
	if len(envs) != 1 || envs[0].Evt.Type() != "error" {
		t.Fatalf("replay returned %d envelopes (first type %v), want 1 error event", len(envs), envs[0].Evt.Type())
	}
}

// TestShortAPIKeyIsNeverRegistered pins the load-bearing guard in
// registerResolvedAPIKey. regexp.QuoteMeta("") compiles to a pattern that
// matches at every position, so registering an empty or near-empty value would
// splice the replacement token between every character of every string that
// crosses any boundary — the log, the transcript, tool output. The guard is the
// only thing between "no key configured" and a redactor that destroys all text.
func TestShortAPIKeyIsNeverRegistered(t *testing.T) {
	const canary = "the quick brown fox jumps over the lazy dog"

	for _, tc := range []struct{ name, key string }{
		{"unset", ""},
		{"one char", "x"},
		{"seven chars, one below the floor", "abcdefg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YOLO_PROVIDER", "together")
			t.Setenv("TOGETHER_API_KEY", tc.key)
			registerResolvedAPIKey()
			if got := infra.DefaultRedactor().Redact(canary); got != canary {
				t.Fatalf("a %d-char key was registered and now mangles ordinary text:\n got  %q\n want %q",
					len(tc.key), got, canary)
			}
		})
	}

	// Eight is the floor, and it must actually take effect — a guard that
	// rejected everything would pass the assertions above for the wrong reason.
	// The probe says "credential", not "key=": the generic_kv default matches
	// key=<6+ chars> and would have redacted the probe on its own.
	t.Setenv("YOLO_PROVIDER", "together")
	t.Setenv("TOGETHER_API_KEY", "Qx7pLm2v")
	const probe = "credential Qx7pLm2v was refused"
	if infra.NewSecrets().WouldLeak(probe) {
		t.Fatalf("a built-in pattern already covers the probe; it proves nothing")
	}
	registerResolvedAPIKey()
	if got := infra.DefaultRedactor().Redact(probe); strings.Contains(got, "Qx7pLm2v") {
		t.Errorf("an 8-char key was not registered: %q", got)
	}
}

// TestResolvedAPIKeyFollowsProviderResolution checks the lookup itself: a
// preset reads only its own key env var (no cross-provider fallback), a
// keyless local preset resolves to nothing, and the env-only path falls back
// the way cognitive does.
func TestResolvedAPIKeyFollowsProviderResolution(t *testing.T) {
	t.Run("preset reads its own key env var", func(t *testing.T) {
		t.Setenv("YOLO_PROVIDER", "groq")
		t.Setenv("GROQ_API_KEY", "gsk_abcdefghijklmnopqrstuvwxyz")
		t.Setenv("OPENAI_API_KEY", "sk-should-not-be-picked-up")
		if got := resolvedAPIKey(); got != "gsk_abcdefghijklmnopqrstuvwxyz" {
			t.Errorf("resolvedAPIKey() = %q", got)
		}
	})
	t.Run("local preset needs no key", func(t *testing.T) {
		t.Setenv("YOLO_PROVIDER", "ollama")
		t.Setenv("OPENAI_API_KEY", "sk-should-not-be-picked-up")
		if got := resolvedAPIKey(); got != "" {
			t.Errorf("resolvedAPIKey() = %q, want empty for a keyless preset", got)
		}
	})
	t.Run("env-only path", func(t *testing.T) {
		t.Setenv("YOLO_PROVIDER", "")
		t.Setenv("YOLO_API_KEY", "env-only-key-value")
		if got := resolvedAPIKey(); got != "env-only-key-value" {
			t.Errorf("resolvedAPIKey() = %q", got)
		}
	})
	t.Run("unknown preset resolves to nothing", func(t *testing.T) {
		t.Setenv("YOLO_PROVIDER", "not-a-real-provider")
		t.Setenv("YOLO_API_KEY", "env-only-key-value")
		if got := resolvedAPIKey(); got != "" {
			t.Errorf("resolvedAPIKey() = %q, want empty: an unknown preset aborts the run", got)
		}
	})
}
