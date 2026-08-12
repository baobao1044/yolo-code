// Tests for the provider registry (Phase 1): LookupProvider, ListProviders,
// ProviderFromPreset, and ResolveProvider. The preset list is deterministic
// (S5): two calls to ListProviders return the same slice in the same order.

package cognitive

import (
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

// TestResolveProviderFallback pins: no YOLO_PROVIDER + no YOLO_API_KEY → stub.
func TestResolveProviderFallback(t *testing.T) {
	t.Setenv("YOLO_PROVIDER", "")
	t.Setenv("YOLO_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	p := ResolveProvider()
	if _, ok := p.(*StubProvider); !ok {
		t.Errorf("ResolveProvider() = %T, want *StubProvider (no key, no provider)", p)
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
