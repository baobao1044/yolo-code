// The SEARCH/REPLACE block — the primary patch format and the engine's single
// internal application path (File 10 §10.2.1/§10.2.2). A Block is exact old
// text + exact new text; Apply locates each Search in the file and replaces
// it. Matching is exact-first: a unique hit applies, no hit is a loud
// ErrNotFound, many hits are ErrAmbiguous (the engine never guesses — File 10
// §10.3). Fuzzy/anchor disambiguation is added by later tickets; an empty
// Search is insertion at InsertAt, an empty Replace is deletion (§10.2.1).
//
// Search-and-replace beats line-numbered unified diff because LLMs miscount
// lines; content addressing doesn't (File 10 §10.1.1).

package patch

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when a block's Search text is not present in the
// file (File 10 §10.3). The model retries with correct text.
var ErrNotFound = errors.New("patch: search text not found")

// ErrAmbiguous is returned when a block's Search text matches more than once
// and no anchor disambiguates (File 10 §10.3). The model adds context.
var ErrAmbiguous = errors.New("patch: search text is ambiguous (matches multiple times)")

// Block is one SEARCH/REPLACE unit (File 10 §10.2.1). Search is the exact old
// text to locate; Replace is the new text to substitute; Fuzzy relaxes
// whitespace matching (opt-in, never default); Anchor is extra surrounding
// context to disambiguate a multi-hit Search (added in a later ticket);
// InsertAt is the byte offset for an empty-Search insertion.
type Block struct {
	Search   string
	Replace  string
	Fuzzy    bool
	Anchor   string
	InsertAt int
}

// ParseBlocks parses one or more SEARCH/REPLACE blocks from text (File 10
// §10.2.1). The markers `<<<<<<< SEARCH`, `=======`, `>>>>>>> REPLACE` are
// stable strings; whitespace between markers is significant (it IS the
// Search/Replace text). A block missing a marker is malformed and rejected
// (the engine does not guess where Search ends). Empty Search/Replace lines
// are preserved (an empty Search = insertion, an empty Replace = deletion).
func ParseBlocks(text string) ([]Block, error) {
	lines := strings.Split(text, "\n")
	var blocks []Block
	state := stateBefore // before SEARCH marker
	var search, replace strings.Builder

	flush := func() error {
		if state == stateBefore {
			return nil // no open block
		}
		return errors.New("patch: malformed block (missing >>>>>>> REPLACE marker)")
	}

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		switch {
		case trimmed == "<<<<<<< SEARCH":
			if state != stateBefore {
				return nil, fmt.Errorf("patch: nested SEARCH marker")
			}
			state = stateSearch
			search.Reset()
			replace.Reset()
		case trimmed == "=======":
			if state != stateSearch {
				return nil, fmt.Errorf("patch: '=======' outside SEARCH block")
			}
			state = stateReplace
		case trimmed == ">>>>>>> REPLACE":
			if state != stateReplace {
				return nil, fmt.Errorf("patch: '>>>>>>> REPLACE' without preceding SEARCH/=======")
			}
			blocks = append(blocks, Block{
				Search:  trimTrailingNewline(search.String()),
				Replace: trimTrailingNewline(replace.String()),
			})
			state = stateBefore
		default:
			switch state {
			case stateSearch:
				if search.Len() > 0 {
					search.WriteString("\n")
				}
				search.WriteString(trimmed)
			case stateReplace:
				if replace.Len() > 0 {
					replace.WriteString("\n")
				}
				replace.WriteString(trimmed)
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, errors.New("patch: no SEARCH/REPLACE blocks found")
	}
	return blocks, nil
}

// blockState tracks the parser's position within a block.
type blockState int

const (
	stateBefore blockState = iota
	stateSearch
	stateReplace
)

// trimTrailingNewline strips a single trailing newline if present, so a block
// parsed from "old\n" yields "old" not "old\n" — keeping the marker framing
// out of the Search/Replace text. (The trailing newline after the last line
// before the marker is framing, not content.)
func trimTrailingNewline(s string) string {
	return strings.TrimSuffix(s, "\n")
}

// Apply runs the engine's single internal application path (File 10 §10.2.2):
// locate each block's Search in content, splice in Replace. Blocks apply in
// order; a failure aborts and returns the error (the file is unchanged — the
// caller checkpoints before, File 10 §10.5).
func Apply(content string, blocks []Block) (string, error) {
	out := content
	for _, b := range blocks {
		idx, err := locate(out, b)
		if err != nil {
			return "", err
		}
		if b.Search == "" {
			// Insertion at InsertAt.
			out = out[:idx] + b.Replace + out[idx:]
		} else {
			out = out[:idx] + b.Replace + out[idx+len(b.Search):]
		}
	}
	return out, nil
}

// locate finds where a block's Search applies in content (File 10 §10.2.2).
// Exact single match → apply. No match → ErrNotFound (fuzzy fallback added
// later). Many matches → ErrAmbiguous (anchor disambiguation added later).
// An empty Search returns the block's InsertAt (insertion point, §10.2.1).
func locate(content string, b Block) (int, error) {
	if b.Search == "" {
		return b.InsertAt, nil
	}
	hits := allIndices(content, b.Search)
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		// Fuzzy fallback (L9-001+): opt-in per block; tolerates whitespace
		// differences. Added when a ticket needs it; ErrNotFound is the safe
		// default — never guess.
		return 0, ErrNotFound
	default:
		// Anchor disambiguation (later ticket) picks the hit surrounded by
		// the anchor text; without it, ambiguous is a loud reject.
		return 0, fmt.Errorf("%w (matched %d times)", ErrAmbiguous, len(hits))
	}
}

// allIndices returns every byte offset at which s appears in content (used by
// locate to decide single/no/many matches). Empty s returns nil.
func allIndices(content, s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	i := 0
	for {
		j := strings.Index(content[i:], s)
		if j < 0 {
			break
		}
		out = append(out, i+j)
		i += j + len(s)
	}
	return out
}
