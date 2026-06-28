// The unified-diff fallback parser (File 10 §10.2.4): the engine accepts a
// `git diff`-style diff and converts each hunk to a SEARCH/REPLACE block, so
// it has one internal application path (Apply, L9-001) while accepting the
// most common external patch format. The conversion reads the current file
// and extracts the old text at the hunk's line range — line numbers are used
// only to *locate* the Search text, not to apply (§10.2.4 last para), so the
// content-addressing guarantee holds. A stale line number that picks the
// wrong text is caught downstream when Apply can't find the Search
// (ErrNotFound), never silently corrupting.
//
// The FS seam is an interface the composition root wires to a real reader
// later (the matrix forbids patch importing sysio).

package patch

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// FS is the read seam FromUnifiedDiff uses to get a file's current content.
// The composition root wires the real reader (sandbox-confined); patch stays
// free of sysio.
type FS interface {
	Read(path string) (string, error)
}

// hunk is one `@@ -oldStart,oldCount +newStart,newCount @@` unit.
type hunk struct {
	Path     string
	OldStart int    // 1-based, from the diff header
	OldCount int    // lines the hunk removes (context + removed)
	NewBody  string // the new text: context + added lines, joined by \n
}

// hunkHeaderRe parses `@@ -oldStart,oldCount +newStart,newCount @@`. The
// counts are optional (default 1) per the unified-diff spec.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// FromUnifiedDiff parses a unified diff and converts each hunk to a Block by
// reading the file's current content and extracting the old text at the hunk's
// line range (File 10 §10.2.4). Search = the file's actual lines at that range
// (content, not line numbers); Replace = the new hunk body. A path the FS
// doesn't have is ErrNotFound — the old text can't be read to build the Search,
// and the engine doesn't guess.
func FromUnifiedDiff(diff string, fs FS) ([]Block, error) {
	hunks, err := diffParse(diff)
	if err != nil {
		return nil, err
	}
	var blocks []Block
	for _, h := range hunks {
		original, err := fs.Read(h.Path)
		if err != nil {
			return nil, errors.New("patch: cannot read " + h.Path + ": " + err.Error())
		}
		search := extractRange(original, h.OldStart, h.OldCount)
		blocks = append(blocks, Block{
			Search:  search,
			Replace: h.NewBody,
		})
	}
	return blocks, nil
}

// diffParse splits a unified diff into hunks. Each hunk's Path comes from the
// preceding `--- a/path` / `+++ b/path` pair; the line range from the
// `@@ -oldStart,oldCount ...` header; the new body from the hunk body (context
// ` ` + added `+` lines, with the prefix stripped).
func diffParse(diff string) ([]hunk, error) {
	var hunks []hunk
	var cur hunk
	var path string
	inBody := false
	var bodyLines []string

	flush := func() {
		if inBody {
			cur.NewBody = strings.Join(bodyLines, "\n")
			hunks = append(hunks, cur)
		}
		inBody = false
		bodyLines = nil
	}

	for _, line := range splitDiffLines(diff) {
		switch {
		case strings.HasPrefix(line, "--- "):
			// `--- a/path` — strip the leading "a/" convention.
			path = stripDiffPathPrefix(line[4:])
		case strings.HasPrefix(line, "+++ "):
			// `+++ b/path` — the hunk's path. Prefer the +++ side if both
			// disagree (rename diffs); else fall back to --- (set above).
			if p := stripDiffPathPrefix(line[4:]); p != "" {
				path = p
			}
		case strings.HasPrefix(line, "@@"):
			flush()
			m := hunkHeaderRe.FindStringSubmatch(line)
			if m == nil {
				return nil, errors.New("patch: malformed hunk header: " + line)
			}
			oldStart, _ := strconv.Atoi(m[1])
			oldCount := 1
			if m[2] != "" {
				oldCount, _ = strconv.Atoi(m[2])
			}
			cur = hunk{Path: path, OldStart: oldStart, OldCount: oldCount}
			inBody = true
		case inBody:
			switch {
			case strings.HasPrefix(line, " "):
				bodyLines = append(bodyLines, line[1:]) // context line → part of new body
			case strings.HasPrefix(line, "+"):
				bodyLines = append(bodyLines, line[1:]) // added line → part of new body
			case strings.HasPrefix(line, "-"):
				// removed line → not part of the new body
			case line == "":
				// an empty line in the diff body is a context line "\n"
				bodyLines = append(bodyLines, "")
			}
		}
	}
	flush()
	if len(hunks) == 0 {
		return nil, errors.New("patch: no hunks found in diff")
	}
	return hunks, nil
}

// stripDiffPathPrefix removes the conventional "a/" or "b/" prefix from a
// diff path header (git prepends these).
func stripDiffPathPrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}

// splitDiffLines splits diff on newlines but drops a single trailing empty
// element (the artifact of a string ending in "\n" — that last "\n" is the
// line terminator, not a separate empty line). This keeps an empty line
// *inside* the diff body (a real context line) while not mistaking the final
// terminator for one.
func splitDiffLines(diff string) []string {
	lines := strings.Split(diff, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// extractRange returns the lines [start, start+count) of content as a single
// string joined by \n, 1-based (File 10 §10.2.4 extractRange). The count
// covers the hunk's context + removed lines — the text Apply will Search for.
// A range past EOF returns what's available (a stale header then yields a
// Search that won't match → ErrNotFound downstream, never a corruption).
func extractRange(content string, start, count int) string {
	lines := strings.Split(content, "\n")
	// Unified-diff line numbers are 1-based; convert to 0-based slice index.
	lo := start - 1
	if lo < 0 {
		lo = 0
	}
	hi := lo + count
	if hi > len(lines) {
		hi = len(lines)
	}
	if lo > len(lines) {
		lo = len(lines)
	}
	return strings.Join(lines[lo:hi], "\n")
}
