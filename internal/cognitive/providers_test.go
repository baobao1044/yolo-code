// Tests for the provider registry (Phase 1): LookupProvider, ListProviders,
// ProviderFromPreset, and ResolveProvider. The preset list is deterministic
// (S5): two calls to ListProviders return the same slice in the same order.

package cognitive

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestLookupProviderFound pins case-insensitive lookup: "OpenAI", "OPENAI",
// "openai" all resolve to the same preset.
func TestLookupProviderFound(t *testing.T) {
	for _, name := range []string{"openai", "OpenAI", "OPENAI", "groq", "Groq", "ollama"} {
		p, ok := LookupProvider(name)
		if !ok {
			t.Errorf("LookupProvider(%q) = not found, want found", name)
			continue
		}
		if p.BaseURL == "" {
			t.Errorf("LookupProvider(%q).BaseURL = empty, want non-empty", name)
		}
	}
}

// TestLookupProviderNotFound pins the miss case.
func TestLookupProviderNotFound(t *testing.T) {
	if _, ok := LookupProvider("nonexistent"); ok {
		t.Error("LookupProvider(\"nonexistent\") = found, want not found")
	}
}

// TestListProvidersDeterministic pins S5: ListProviders returns the same
// slice in the same order across calls.
func TestListProvidersDeterministic(t *testing.T) {
	first := ListProviders()
	second := ListProviders()
	if len(first) < 20 {
		t.Errorf("ListProviders() = %d entries, want >=20", len(first))
	}
	if len(first) != len(second) {
		t.Fatalf("ListProviders() length differs across calls: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Errorf("ListProviders()[%d].Name = %q vs %q (non-deterministic order)", i, first[i].Name, second[i].Name)
		}
	}
}

// TestProviderFromPreset pins the builder: BaseURL + model are carried, window
// is parsed from string.
func TestProviderFromPreset(t *testing.T) {
	p := ProviderFromPreset(ProviderPreset{
		BaseURL:      "https://api.example.com/v1",
		DefaultModel: "test-model",
		Window:       "64000",
	}, "fake-key")
	if p.baseURL != "https://api.example.com/v1" {
		t.Errorf("baseURL = %q, want https://api.example.com/v1", p.baseURL)
	}
	if p.model != "test-model" {
		t.Errorf("model = %q, want test-model", p.model)
	}
	if p.window != 64000 {
		t.Errorf("window = %d, want 64000", p.window)
	}
	if p.apiKey != "fake-key" {
		t.Errorf("apiKey = %q, want fake-key", p.apiKey)
	}
}

// TestProviderFromPresetInvalidWindow pins fallback: invalid Window string
// defaults to 128000.
func TestProviderFromPresetInvalidWindow(t *testing.T) {
	p := ProviderFromPreset(ProviderPreset{
		BaseURL:      "https://api.example.com/v1",
		DefaultModel: "m",
		Window:       "not-a-number",
	}, "")
	if p.window != 128_000 {
		t.Errorf("window = %d, want 128000 (fallback for invalid)", p.window)
	}
}

// TestResolveProviderWithPreset pins: YOLO_PROVIDER set → ResolveProvider uses
// the preset. Local provider (Ollama, NeedsKey=false) builds without a key.
func TestResolveProviderWithPreset(t *testing.T) {
	t.Setenv("YOLO_PROVIDER", "ollama")
	t.Setenv("YOLO_API_KEY", "")
	p := ResolveProvider()
	if p == nil {
		t.Fatal("ResolveProvider() = nil, want a provider (ollama)")
	}
	if p.Window() == 0 {
		t.Error("Window() = 0, want non-zero")
	}
}

// TestResolveProviderNoKeyIsNotSilentStub pins the anti-degradation rule: with
// nothing configured, ResolveProvider must NOT hand back the keyword-matching
// stub dressed up as a model. It returns a provider that fails loudly on the
// first Stream, and the fail-fast form returns ErrNoProvider.
func TestResolveProviderNoKeyIsNotSilentStub(t *testing.T) {
	clearProviderEnv(t)
	p := ResolveProvider()
	if _, ok := p.(*StubProvider); ok {
		t.Fatal("ResolveProvider() = *StubProvider, want a loud failure (the stub must be opt-in)")
	}
	if _, err := p.Stream(context.Background(), Request{}); err == nil {
		t.Error("Stream() = nil error, want an actionable 'no provider configured' error")
	}
	if _, err := ResolveProviderErr(); !errors.Is(err, ErrNoProvider) {
		t.Errorf("ResolveProviderErr() err = %v, want ErrNoProvider", err)
	}
}

// TestResolveProviderStubOptIn pins the explicit opt-in: YOLO_STUB=1 (and
// YOLO_PROVIDER=stub) select the stub deliberately.
func TestResolveProviderStubOptIn(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("YOLO_STUB", "1")
	if p, err := ResolveProviderErr(); err != nil {
		t.Fatalf("ResolveProviderErr() with YOLO_STUB=1: %v", err)
	} else if _, ok := p.(*StubProvider); !ok {
		t.Errorf("ResolveProviderErr() = %T, want *StubProvider (YOLO_STUB=1)", p)
	}

	t.Setenv("YOLO_STUB", "")
	t.Setenv("YOLO_PROVIDER", "stub")
	if p, err := ResolveProviderErr(); err != nil {
		t.Fatalf("ResolveProviderErr() with YOLO_PROVIDER=stub: %v", err)
	} else if _, ok := p.(*StubProvider); !ok {
		t.Errorf("ResolveProviderErr() = %T, want *StubProvider (YOLO_PROVIDER=stub)", p)
	}
}

// TestResolveAPIKeyNoCrossProviderLeak is the credential-leak guard: with only
// OPENAI_API_KEY exported, no preset other than "openai" may resolve a key.
// Otherwise selecting e.g. groq would ship the user's OpenAI credential to
// api.groq.com in an Authorization header.
func TestResolveAPIKeyNoCrossProviderLeak(t *testing.T) {
	clearProviderEnv(t)
	const leaked = "sk-openai-secret"
	t.Setenv("OPENAI_API_KEY", leaked)
	for _, p := range ListProviders() {
		if p.Name == "openai" {
			continue
		}
		if got := resolveAPIKey(p); got != "" {
			t.Errorf("resolveAPIKey(%q) = %q, want \"\" (OPENAI_API_KEY must not cross providers)", p.Name, got)
		}
	}
}

// TestResolveAPIKeyOpenAIPresetStillWorks pins the other half: OPENAI_API_KEY
// is still the openai preset's own key env, so it must keep resolving.
func TestResolveAPIKeyOpenAIPresetStillWorks(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	p, ok := LookupProvider("openai")
	if !ok {
		t.Fatal("openai preset not found")
	}
	if got := resolveAPIKey(p); got != "sk-openai-secret" {
		t.Errorf("resolveAPIKey(openai) = %q, want sk-openai-secret", got)
	}
}

// TestResolveProviderMissingKeyDoesNotFallThrough pins the second half of the
// leak: a preset that needs a key but has none must NOT fall through to the
// env-only path, which would build a provider for whatever YOLO_BASE_URL says
// and hand it OPENAI_API_KEY.
func TestResolveProviderMissingKeyDoesNotFallThrough(t *testing.T) {
	clearProviderEnv(t)
	const leaked = "sk-openai-secret"
	t.Setenv("OPENAI_API_KEY", leaked)
	t.Setenv("YOLO_PROVIDER", "groq")
	t.Setenv("YOLO_BASE_URL", "https://api.groq.com/openai/v1") // what /provider groq sets

	if oai, ok := ResolveProvider().(*OpenAICompatProvider); ok && oai.apiKey == leaked {
		t.Fatalf("ResolveProvider() built %s with the OpenAI key — credential leak", oai.baseURL)
	}
	_, err := ResolveProviderErr()
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("ResolveProviderErr() err = %v, want ErrNoProvider", err)
	}
	if !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Errorf("err = %q, want it to name GROQ_API_KEY (actionable)", err)
	}
}

// TestEveryKeyedPresetHasKeyEnv pins the invariant the leak fix relies on:
// removing the cross-provider fallback is only safe because every preset that
// needs a key names its own env var.
func TestEveryKeyedPresetHasKeyEnv(t *testing.T) {
	for _, p := range ListProviders() {
		if p.NeedsKey && p.KeyEnv == "" {
			t.Errorf("preset %q NeedsKey but has no KeyEnv — no way to supply a key without a cross-provider fallback", p.Name)
		}
	}
}

// clearProviderEnv unsets every env var provider resolution reads, so a test
// starts from a known-empty configuration regardless of the developer's shell.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"YOLO_PROVIDER", "YOLO_API_KEY", "YOLO_BASE_URL", "YOLO_MODEL", "YOLO_WINDOW", "YOLO_STUB", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	for _, p := range providerPresets {
		if p.KeyEnv != "" {
			t.Setenv(p.KeyEnv, "")
		}
	}
}

// TestResolveProviderWithAPIKey pins: no YOLO_PROVIDER + YOLO_API_KEY set →
// OpenAICompatProvider from env.
func TestResolveProviderWithAPIKey(t *testing.T) {
	t.Setenv("YOLO_PROVIDER", "")
	t.Setenv("YOLO_API_KEY", "sk-test")
	t.Setenv("YOLO_BASE_URL", "https://api.openai.com/v1")
	p := ResolveProvider()
	oai, ok := p.(*OpenAICompatProvider)
	if !ok {
		t.Fatalf("ResolveProvider() = %T, want *OpenAICompatProvider", p)
	}
	if oai.apiKey != "sk-test" {
		t.Errorf("apiKey = %q, want sk-test", oai.apiKey)
	}
}

// TestProviderPresetLocalNeedsNoKey pins: Ollama preset has NeedsKey=false.
func TestProviderPresetLocalNeedsNoKey(t *testing.T) {
	p, ok := LookupProvider("ollama")
	if !ok {
		t.Fatal("ollama preset not found")
	}
	if p.NeedsKey {
		t.Error("ollama NeedsKey = true, want false (local server)")
	}
}

// TestUnsubstitutedPlaceholder pins the detector itself, including the shapes
// that must NOT read as a placeholder — a base URL is a URL, and a stray brace
// with no closing partner is not a variable anybody forgot to fill in.
func TestUnsubstitutedPlaceholder(t *testing.T) {
	cases := map[string]string{
		"https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1": "{account_id}",
		"https://{region}.example.com/v1":                                  "{region}",
		"https://api.openai.com/v1":                                        "",
		"http://localhost:11434/v1":                                        "",
		"https://api.example.com/v1?q=a{b":                                 "",
	}
	for in, want := range cases {
		if got := unsubstitutedPlaceholder(in); got != want {
			t.Errorf("unsubstitutedPlaceholder(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPresetWithPlaceholderFailsFast is the F9 guard: a preset whose BaseURL
// still carries a `{...}` the registry never substitutes must be refused with
// an error naming the variable, not built into a provider that sends the
// literal text as part of the request path. The Cloudflare preset needs an
// account id in its URL; nothing in yolo fills it in, so `/provider cloudflare`
// used to hand back a working-looking provider whose every request went to
// .../accounts/%7Baccount_id%7D/... and came back 404.
//
// The key is exported here on purpose: the placeholder must be the ONLY reason
// this fails, so the test cannot pass on the missing-key error instead.
func TestPresetWithPlaceholderFailsFast(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("CLOUDFLARE_API_KEY", "cf-key")
	t.Setenv("YOLO_PROVIDER", "cloudflare")

	p, err := ResolveProviderErr()
	if err == nil {
		oai, _ := p.(*OpenAICompatProvider)
		t.Fatalf("ResolveProviderErr() built a provider for an unsubstituted base URL (%v)", oai.baseURL)
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v, want it to wrap ErrNoProvider", err)
	}
	if !strings.Contains(err.Error(), "{account_id}") {
		t.Errorf("err = %q, want it to name the placeholder {account_id} (actionable)", err)
	}
	if !strings.Contains(err.Error(), "YOLO_BASE_URL") {
		t.Errorf("err = %q, want it to name the way out (YOLO_BASE_URL)", err)
	}
}

// TestNoPresetSilentlyShipsAPlaceholder is the registry-wide audit. It does not
// forbid placeholder presets — whether Cloudflare stays in the list is the
// owner's call — it forbids one resolving into a provider. The only validation
// the registry had was `BaseURL != ""`
// (TestProviderPresetsHaveRequiredFields), which `{account_id}` passes.
func TestNoPresetSilentlyShipsAPlaceholder(t *testing.T) {
	for _, preset := range ListProviders() {
		ph := unsubstitutedPlaceholder(preset.BaseURL)
		if ph == "" {
			continue
		}
		t.Run(preset.Name, func(t *testing.T) {
			clearProviderEnv(t)
			if preset.KeyEnv != "" {
				t.Setenv(preset.KeyEnv, "k")
			}
			t.Setenv("YOLO_PROVIDER", preset.Name)
			_, err := ResolveProviderErr()
			if err == nil {
				t.Fatalf("preset %q resolved despite the placeholder %s in %s", preset.Name, ph, preset.BaseURL)
			}
			if !strings.Contains(err.Error(), ph) {
				t.Errorf("preset %q: err = %q, want it to name %s", preset.Name, err, ph)
			}
		})
	}
}

// TestPresetsWithoutPlaceholdersStillResolve is the control: the placeholder
// check must not stand between a well-formed preset and its provider.
func TestPresetsWithoutPlaceholdersStillResolve(t *testing.T) {
	for _, preset := range ListProviders() {
		if unsubstitutedPlaceholder(preset.BaseURL) != "" {
			continue
		}
		t.Run(preset.Name, func(t *testing.T) {
			clearProviderEnv(t)
			if preset.KeyEnv != "" {
				t.Setenv(preset.KeyEnv, "k")
			}
			t.Setenv("YOLO_PROVIDER", preset.Name)
			p, err := ResolveProviderErr()
			if err != nil {
				t.Fatalf("preset %q did not resolve: %v", preset.Name, err)
			}
			oai, ok := p.(*OpenAICompatProvider)
			if !ok {
				t.Fatalf("preset %q resolved to %T, want *OpenAICompatProvider", preset.Name, p)
			}
			if strings.ContainsAny(oai.baseURL, "{}") {
				t.Errorf("preset %q built a provider with braces in its base URL: %s", preset.Name, oai.baseURL)
			}
		})
	}
}

// TestProviderPresetsHaveRequiredFields pins every preset has a non-empty
// Name, BaseURL, DefaultModel, and Description — a missing field would break
// the /provider list and the build.
func TestProviderPresetsHaveRequiredFields(t *testing.T) {
	for _, p := range ListProviders() {
		if p.Name == "" {
			t.Error("preset with empty Name")
		}
		if p.BaseURL == "" {
			t.Errorf("preset %q has empty BaseURL", p.Name)
		}
		if p.DefaultModel == "" {
			t.Errorf("preset %q has empty DefaultModel", p.Name)
		}
		if p.Description == "" {
			t.Errorf("preset %q has empty Description", p.Name)
		}
	}
}
