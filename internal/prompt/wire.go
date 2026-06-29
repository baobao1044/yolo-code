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
// with one blank line of separation between sections. The tag set (§6.6.2):
//
//	<system>       role + tool schemas + rules
//	<project>      AGENTS.md / project rules
//	<preferences>  recalled user preferences (File 11 §11.8, L10-006)
//	<files>        retrieved files, graph, diagnostics
//
// (Conversation turns and the current user message are emitted bare, as their
// own messages with role tags handled by the message envelope, not a section.)

package prompt

import (
	"strings"

	econtext "github.com/baobao1044/yolo-code/internal/context"
)

// render wraps a group of parts in its stable section tag. Parts within a group
// are separated by a blank line; each part's source is noted as a Markdown
// header so the model can attribute it (e.g. a file path). Empty groups render
// nothing — order() omits them entirely.
func render(tag string, parts []econtext.Part) string {
	var b strings.Builder
	b.WriteString(tag)
	b.WriteByte('\n')
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('\n')
		}
		if p.Source != "" && p.Source != "<system>" {
			b.WriteString("### ")
			b.WriteString(p.Source)
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
		if !strings.HasSuffix(p.Text, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("</")
	b.WriteString(tagName(tag))
	b.WriteString(">\n")
	return b.String()
}

// tagName extracts the inner name from a section tag like "<files>" → "files".
func tagName(tag string) string {
	return strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">")
}

// parseSections is the L5-002 round-trip parser: it splits rendered wire text
// back into a map of tag→body. It is the inverse of render, used by the
// parser-round-trips test. Tags are matched as `<tag>` … `</tag>` on their own
// (start/end of) lines.
func parseSections(s string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") && !strings.Contains(line, "/") {
			tag := strings.TrimSuffix(strings.TrimPrefix(line, "<"), ">")
			i++
			var body strings.Builder
			for i < len(lines) {
				end := strings.TrimSpace(lines[i])
				if end == "</"+tag+">" {
					out[tag] = strings.TrimRight(body.String(), "\n")
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
