// The XML+Markdown wire format (File 06 §6.6.2). The compiled prompt is XML-
// tagged structure inside an otherwise Markdown body: prose stays Markdown,
// code and tool I/O get unambiguous section tags. Rationale (File 06): no fence
// collision with code files, explicit delimiters, Claude-family models trained
// on it. The tags are STABLE — the parser (L5-002) and golden fixtures (L5-003)
// depend on them never changing.
//
// A rendered section is:
//
//	<tag>
//	<body>
//	</tag>
//
// with one blank line of separation between sections — except when the body
// contains a line that could be mistaken for a delimiter, in which case the
// section is framed with a content-derived nonce (`<tag id="…">` … `</tag
// id="…">`) so retrieved content cannot close the compiler's own section. See
// render/delimiters. The tag set (§6.6.2):
//
//	<system>       role + tool schemas + rules
//	<project>      AGENTS.md / project rules
//	<preferences>  recalled user preferences (File 11 §11.8, L10-006)
//	<files>        retrieved files, graph, diagnostics
//	<rag>          semantically retrieved code chunks (File 11 §11.6, L10-006)
//
// (Conversation turns and the current user message are emitted bare, as their
// own messages with role tags handled by the message envelope, not a section.)

package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	econtext "github.com/baobao1044/yolo-code/internal/context"
)

// render wraps a group of parts in its stable section tag. Parts within a group
// are separated by a blank line; each part's source is noted as a Markdown
// header so the model can attribute it (e.g. a file path). Empty groups render
// nothing — order() omits them entirely.
//
// Framing is the renderer's guarantee, not a hope about the content. Everything
// render is handed — a file body the context engine retrieved, a Source label,
// a tool result — is DATA, and no data may terminate the section that carries
// it. A repo file containing a line `</files>` used to close the compiler's own
// <files> section: the rest of the file then sat unframed at top level, where a
// `<system>` line in it parsed back out as a genuine system section (the
// forgery round-tripped through parseSections). That is an injection primitive
// available from any file the engine retrieves, including one the agent just
// cloned.
//
// The fix is a nonce delimiter rather than a filter on the content: when the
// body contains any line the parser could read as a delimiter, the section is
// framed as `<files id="…">` … `</files id="…">` with an id derived from — and
// guaranteed absent from — that body. Content is never rewritten, so the model
// still reads the file's true bytes; and the guarantee is structural, derived
// from the tag render is given, so a tag added later inherits it instead of
// depending on someone remembering to extend a list of known tag names.
func render(tag string, parts []econtext.Part) string {
	var body strings.Builder
	for i, p := range parts {
		if i > 0 {
			body.WriteByte('\n')
		}
		if p.Source != "" && p.Source != "<system>" {
			body.WriteString("### ")
			body.WriteString(p.Source)
			body.WriteByte('\n')
		}
		body.WriteString(p.Text)
		if !strings.HasSuffix(p.Text, "\n") {
			body.WriteByte('\n')
		}
	}
	open, close := delimiters(tagName(tag), body.String())

	var b strings.Builder
	b.WriteString(open)
	b.WriteByte('\n')
	b.WriteString(body.String())
	b.WriteString(close)
	b.WriteByte('\n')
	return b.String()
}

// delimiters picks the opening and closing tags framing a section body. The
// plain `<name>` … `</name>` form — the stable §6.6.2 wire format, byte-for-
// byte what the golden fixtures pin — is used whenever no line of the body can
// be mistaken for a delimiter, which is every ordinary prompt. A body that does
// carry such a line is escalated to the nonce form, which fails closed: the
// closing tag is unguessable from the content, so nothing inside can end the
// section early.
//
// The escalation predicate is deliberately wider than what parseSections acts
// on today (it flags any `<…>` line, not just this section's own closing tag).
// Over-triggering is nearly free — the delimiter grows, the content does not
// change — while under-triggering is the defect, so the cheap side is the
// conservative one.
func delimiters(name, body string) (open, close string) {
	if !hasDelimiterShapedLine(body) {
		return "<" + name + ">", "</" + name + ">"
	}
	id := sectionNonce(body)
	return "<" + name + ` id="` + id + `">`, "</" + name + ` id="` + id + `">`
}

// hasDelimiterShapedLine reports whether any line of body, once trimmed, has
// the shape parseSections treats as a tag: `<…>` alone on a line. It matches
// both opening and closing forms.
func hasDelimiterShapedLine(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if len(t) > 2 && strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">") {
			return true
		}
	}
	return false
}

// sectionNonce derives a section id from its body: deterministic (S5 — the same
// package compiles to the same bytes every run) and guaranteed not to occur in
// the body, which is what makes the closing tag unforgeable. A 12-hex prefix of
// the digest already makes a clash a 2^-48 coincidence; growing the prefix and
// then appending a counter turns "almost certainly absent" into "absent", since
// every later candidate is strictly longer than the last and the body is
// finite.
func sectionNonce(body string) string {
	sum := sha256.Sum256([]byte(body))
	d := hex.EncodeToString(sum[:])
	for n := 12; n <= len(d); n++ {
		if !strings.Contains(body, d[:n]) {
			return d[:n]
		}
	}
	for n := 1; ; n++ {
		cand := d + "-" + strconv.Itoa(n)
		if !strings.Contains(body, cand) {
			return cand
		}
	}
}

// tagName extracts the inner name from a section tag like "<files>" → "files".
func tagName(tag string) string {
	return strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">")
}

// parseSections is the L5-002 round-trip parser: it splits rendered wire text
// back into a map of tag→body. It is the inverse of render, used by the
// parser-round-trips test. Tags are matched as `<tag>` … `</tag>` on their own
// (start/end of) lines.
//
// A section opened with a nonce (`<files id="…">`, see delimiters) closes only
// on that exact delimiter, which is what keeps content from ending it early.
// The map is still keyed by the bare section name, so the nonce is a framing
// detail and never leaks into the tag set callers match on.
func parseSections(s string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") && !strings.Contains(line, "/") {
			tag := strings.TrimSuffix(strings.TrimPrefix(line, "<"), ">")
			name := tag
			if sp := strings.IndexByte(tag, ' '); sp >= 0 {
				name = tag[:sp]
			}
			i++
			var body strings.Builder
			for i < len(lines) {
				end := strings.TrimSpace(lines[i])
				if end == "</"+tag+">" {
					out[name] = strings.TrimRight(body.String(), "\n")
					i++
					break
				}
				body.WriteString(lines[i])
				body.WriteByte('\n')
				i++
			}
			continue
		}
		i++
	}
	return out
}
