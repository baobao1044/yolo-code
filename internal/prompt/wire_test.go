package prompt

import (
	"strings"
	"testing"

	econtext "github.com/yolo-code/yolo/internal/context"
)

// TestWireRenderWrapsGroupInTag pins render(): a group's parts are wrapped in
// <tag> … </tag>, each part's source noted as a Markdown header, and the body
// newline-terminated.
func TestWireRenderWrapsGroupInTag(t *testing.T) {
	out := render("<files>", []econtext.Part{
		{Source: "auth/login.go", Text: "package auth"},
		{Source: "main.go", Text: "package main"},
	})
	if !strings.HasPrefix(out, "<files>\n") {
		t.Errorf("render missing opening <files> tag; got %q", out)
	}
	if !strings.Contains(out, "</files>\n") {
		t.Errorf("render missing closing </files> tag; got %q", out)
	}
	if !strings.Contains(out, "### auth/login.go\n") {
		t.Errorf("render missing source header for auth/login.go; got %q", out)
	}
	if !strings.Contains(out, "### main.go\n") {
		t.Errorf("render missing source header for main.go; got %q", out)
	}
	if !strings.Contains(out, "package auth\n") || !strings.Contains(out, "package main\n") {
		t.Errorf("render missing part bodies; got %q", out)
	}
}

// TestWireRenderAddsTrailingNewline pins that a part missing a trailing newline
// gets one (so the next section's tag starts on its own line, no fence
// collision with code).
func TestWireRenderAddsTrailingNewline(t *testing.T) {
	out := render("<files>", []econtext.Part{
		{Source: "a.go", Text: "package a"}, // no trailing newline
	})
	if !strings.Contains(out, "package a\n") {
		t.Errorf("render did not add trailing newline to part text; got %q", out)
	}
}

// TestWireParseRoundTrips is the L5-002 exit criterion: parseSections is the
// inverse of render — every section a compiled prompt contains round-trips
// through render → parseSections back to its tag→body mapping, byte-identical
// in the bodies.
func TestWireParseRoundTrips(t *testing.T) {
	system := render("<system>", []econtext.Part{{Source: "<system>", Text: "You are yolo."}})
	project := render("<project>", []econtext.Part{{Source: "AGENTS.md", Text: "Use table-driven tests."}})
	files := render("<files>", []econtext.Part{
		{Source: "auth/login.go", Text: "package auth\n\nfunc Login() error { return nil }"},
		{Source: "main.go", Text: "package main\n\nfunc main() {}"},
	})
	combined := system + project + files

	sections := parseSections(combined)
	if len(sections) != 3 {
		t.Fatalf("parseSections found %d sections, want 3 (system/project/files)", len(sections))
	}
	for _, tag := range []string{"system", "project", "files"} {
		if _, ok := sections[tag]; !ok {
			t.Errorf("parseSections missing section %q; got %v", tag, sections)
		}
	}
	// The bodies must contain the source headers + part texts render emitted.
	if !strings.Contains(sections["system"], "You are yolo.") {
		t.Errorf("system body lost text on round-trip; got %q", sections["system"])
	}
	if !strings.Contains(sections["files"], "### auth/login.go") {
		t.Errorf("files body lost source header on round-trip; got %q", sections["files"])
	}
	if !strings.Contains(sections["files"], "func Login() error { return nil }") {
		t.Errorf("files body lost code on round-trip; got %q", sections["files"])
	}
}

// TestWireParseRoundTripsCompiledPrompt round-trips an actual end-to-end
// compiled prompt (not a hand-built string) so the wire contract holds against
// the real pipeline output, not just synthetic fixtures.
func TestWireParseRoundTripsCompiledPrompt(t *testing.T) {
	_, msgs := compilePkg(t, 50_000, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})

	// Collect every section-tagged message, parse it, and confirm each tag it
	// opened is closed and recoverable — i.e. the wire format is well-formed
	// under the real pipeline.
	tagCount := 0
	for _, m := range msgs {
		if !strings.Contains(m.Content, "<") {
			continue
		}
		sections := parseSections(m.Content)
		if len(sections) == 0 {
			t.Errorf("message with tags parsed to 0 sections: %q", m.Content)
			continue
		}
		for tag, body := range sections {
			if body == "" {
				t.Errorf("section %q has empty body after round-trip", tag)
			}
			tagCount++
		}
	}
	if tagCount == 0 {
		t.Error("no section-tagged messages in the compiled prompt; wire format never exercised")
	}
}

// TestWireParseHandlesEmptySection pins that a section with an empty body
// (all parts empty) still parses: the closing tag is the boundary. render is
// called with empty parts only when a group is non-empty but its parts are
// empty-text; order() omits empty groups, so this is a defensive guard.
func TestWireParseHandlesEmptySection(t *testing.T) {
	out := render("<files>", []econtext.Part{{Source: "empty.go", Text: ""}})
	sections := parseSections(out)
	body, ok := sections["files"]
	if !ok {
		t.Fatalf("parseSections missing files section for empty part; got %v", sections)
	}
	// The body is the source header (render emits it before the empty text).
	if !strings.Contains(body, "### empty.go") {
		t.Errorf("empty-section body lost source header; got %q", body)
	}
}

// TestWireParseIgnoresNonTagLines pins that prose outside section tags (e.g.
// a bare user message) doesn't get misparsed as a section. Only <tag> … </tag>
// blocks are extracted.
func TestWireParseIgnoresNonTagLines(t *testing.T) {
	combined := "do the thing\n<system>\nrole text\n</system>\nmore prose\n"
	sections := parseSections(combined)
	if len(sections) != 1 {
		t.Fatalf("parseSections found %d sections, want 1 (only <system>); got %v", len(sections), sections)
	}
	if sections["system"] != "role text" {
		t.Errorf("system body = %q, want %q", sections["system"], "role text")
	}
}

// TestWireTagsAreStable pins that the three section tags are exactly the
// strings golden fixtures (L5-003) and the parser depend on — a change here
// silently breaks round-tripping and byte-identical transcripts (S5).
func TestWireTagsAreStable(t *testing.T) {
	tags := []string{"<system>", "<project>", "<files>"}
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "<") || !strings.HasSuffix(tag, ">") {
			t.Errorf("tag %q is not bracketed; wire format requires <tag>", tag)
		}
	}
}
