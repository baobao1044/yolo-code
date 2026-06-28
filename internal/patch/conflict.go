// Conflict detection (File 10 §10.3): before applying, the engine checks the
// patch won't fight the current state. L9-001 covered ErrNotFound (no match)
// and ErrAmbiguous (many matches); this file adds:
//   - anchor disambiguation: a multi-hit Search resolves via the surrounding
//     Anchor context; still-ambiguous → reject (§10.2.2);
//   - the staleness race: the file changed since the model read it → ErrStale,
//     forcing a re-read (§10.3).
//
// ErrConflict (a concurrent agent's pending patch touches overlapping lines)
// is deferred to the Coordination Layer (File 12); it serializes patches so
// the engine here sees one at a time.

package patch

import (
	"errors"
	"strings"
	"time"
)

// ErrStale is returned when the file's modified-at is newer than the model's
// read time (File 10 §10.3): the Search may no longer match, so the model
// must re-read and re-patch rather than corrupt a diverged file.
var ErrStale = errors.New("patch: file changed since read (stale)")

// disambiguate picks the Search hit preceded by the Anchor text (File 10
// §10.2.2). "Anchor immediately precedes the hit" means the Anchor text
// occurs right before a hit's start byte. If the anchor narrows it to exactly
// one hit, that hit is returned; if zero or many hits survive the anchor,
// the patch is still ambiguous and rejected.
func disambiguate(content, search, anchor string) (int, error) {
	hits := allIndices(content, search)
	var matching []int
	for _, h := range hits {
		// The anchor must immediately precede this hit: the bytes right before
		// the hit's start should be the anchor (allowing the anchor to be the
		// text on the preceding line(s), so we check the content slice ending
		// at the hit start).
		if h >= len(anchor) && content[h-len(anchor):h] == anchor {
			matching = append(matching, h)
			continue
		}
		// Also accept the anchor appearing anywhere *before* the hit on the
		// same or earlier lines — the common case where the model quotes a
		// function signature preceding the edit. We look for the anchor in
		// the text preceding the hit.
		if strings.Contains(content[:h], anchor) {
			matching = append(matching, h)
		}
	}
	switch len(matching) {
	case 1:
		return matching[0], nil
	case 0:
		return 0, errors.New("patch: anchor does not precede any search hit")
	default:
		return 0, errors.New("patch: anchor still ambiguous (matches N hits)")
	}
}

// ApplyAt runs Apply with a staleness gate (File 10 §10.3): if fileMod is
// newer than readTime, the file changed since the model read it and the patch
// is rejected with ErrStale (the model re-reads). Otherwise Apply runs as
// usual. The pure Apply (L9-001) has no time check; ApplyAt layers it on.
func ApplyAt(content string, blocks []Block, readTime, fileMod time.Time) (string, error) {
	if fileMod.After(readTime) {
		return "", ErrStale
	}
	return Apply(content, blocks)
}
