package cognitive

import (
	"strings"
	"testing"
)

// TestBuildToolDefsIncludesAllAdvertisedTools guards against the regression
// where a tool is registered in the exec registry and advertised in the docs
// but missing from the LLM tool schema (toolDefs) — a tool-calling model can
// never invoke a tool it was never told about. The README advertises five
// built-in tools: list_files, read_file, edit_file, bash, grep.
func TestBuildToolDefsIncludesAllAdvertisedTools(t *testing.T) {
	want := []string{"list_files", "read_file", "edit_file", "bash", "grep"}
	defs := buildToolDefs(want)
	if len(defs) != len(want) {
		t.Fatalf("buildToolDefs returned %d defs, want %d (a tool is missing its schema)", len(defs), len(want))
	}
	got := make(map[string]bool, len(defs))
	for _, d := range defs {
		got[d.Function.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q missing from buildToolDefs output — the model cannot call it", name)
		}
	}
}

// TestBuildToolDefsSkipsUnknownNames documents the silent-skip contract: an
// unknown tool name must not break request building, it is simply omitted.
func TestBuildToolDefsSkipsUnknownNames(t *testing.T) {
	defs := buildToolDefs([]string{"list_files", "no_such_tool", "read_file"})
	if len(defs) != 2 {
		t.Fatalf("buildToolDefs returned %d defs, want 2 (unknown should be skipped)", len(defs))
	}
}

// TestGrepToolSchemaHasRequiredParameters ensures the grep schema (the tool that
// was previously missing) carries the `pattern` parameter as required so the
// model is forced to supply it.
func TestGrepToolSchemaHasRequiredParameters(t *testing.T) {
	def, ok := toolDefs["grep"]
	if !ok {
		t.Fatal(`toolDefs["grep"] missing — grep was re-removed from the schema`)
	}
	if def.Function.Name != "grep" {
		t.Errorf("grep Function.Name = %q, want %q", def.Function.Name, "grep")
	}
	body := string(def.Function.Parameters)
	if !strings.Contains(body, `"pattern"`) {
		t.Errorf("grep schema missing `pattern` property: %s", body)
	}
	if !strings.Contains(body, `"required":["pattern"]`) {
		t.Errorf("grep schema missing `pattern` in required: %s", body)
	}
}
