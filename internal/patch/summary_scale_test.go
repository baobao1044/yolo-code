// What lineDiff costs on a real file, and the invariant that makes it cheap.
//
// summary.go's lcsLen carried the comment "O(n*m) is fine here — file diffs are
// small". The premise measures the wrong quantity: n and m are the line counts
// of the whole before/after file, not of the diff between them. A one-line edit
// to a large file is a "small diff" by any reading and still fills the entire
// n×m table. Measured before the prefix/suffix trim, on this repo's own
// internal/runtime/core.go replicated to grow the line count, with a one-line
// edit in every case:
//
//	 1298 non-empty lines →   8ms
//	 2596 non-empty lines →  23ms
//	 5192 non-empty lines →  85ms
//	10384 non-empty lines → 459ms
//
// This runs on the drive loop, once per patch apply, purely to print an
// "N insertions, M deletions" line. Half a second of a task's latency for a
// cosmetic count is not a trade anyone made deliberately.
//
// The two tests below are the two halves of the fix. The first pins the numbers
// lineDiff reports across the shapes the trim has to get right — trimming is
// only worth having if it is exactly equivalent, and an optimisation to a
// counting function that quietly changes the count is worse than the cost it
// saves. The second pins the cost itself.

package patch

import (
	"strings"
	"testing"
	"time"
)

// TestLineDiffCountsAreExact locks the reported counts for the shapes the
// prefix/suffix trim reasons about: a change in the middle (both trims fire), at
// the head (only the suffix trim), at the tail (only the prefix trim), a total
// rewrite (neither), and the degenerate empties. These are the answers the
// untrimmed full-table LCS gives; the trim must not move any of them.
func TestLineDiffCountsAreExact(t *testing.T) {
	body := func(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

	cases := []struct {
		name     string
		old, new string
		ins, del int
	}{
		{"identical", body("a", "b", "c"), body("a", "b", "c"), 0, 0},
		{"middle replaced", body("a", "b", "c"), body("a", "X", "c"), 1, 1},
		{"middle inserted", body("a", "c"), body("a", "b", "c"), 1, 0},
		{"middle deleted", body("a", "b", "c"), body("a", "c"), 0, 1},
		{"head replaced", body("a", "b", "c"), body("X", "b", "c"), 1, 1},
		{"tail replaced", body("a", "b", "c"), body("a", "b", "X"), 1, 1},
		{"total rewrite", body("a", "b", "c"), body("x", "y", "z"), 3, 3},
		{"empty to content", "", body("a", "b"), 2, 0},
		{"content to empty", body("a", "b"), "", 0, 2},
		{"both empty", "", "", 0, 0},
		// Framing, per lineDiff's contract: blank lines are not content.
		{"only trailing newline changes", "a\nb", "a\nb\n", 0, 0},
		{"blank line inserted", body("a", "b"), "a\n\nb\n", 0, 0},
		// A repeated line is where a careless trim goes wrong: the suffix scan
		// must stop at the boundary the prefix scan already consumed, or the
		// same line is credited to both and the count comes out short.
		{"repeated lines, one added", body("a", "a", "a"), body("a", "a", "a", "a"), 1, 0},
		{"repeated lines, one removed", body("a", "a", "a"), body("a", "a"), 0, 1},
		{"prefix equals whole of other", body("a", "b"), body("a", "b", "c", "d"), 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins, del := lineDiff(tc.old, tc.new)
			if ins != tc.ins || del != tc.del {
				t.Errorf("lineDiff = %d insertions, %d deletions; want %d, %d",
					ins, del, tc.ins, tc.del)
			}
		})
	}
}

// TestLineDiffIsNotQuadraticInFileSize is the cost half. It is a wall-clock
// assertion, which is normally a bad idea, and it is defensible only because
// the gap it straddles is three orders of magnitude: the full-table LCS takes
// ~1.8s on this input and the trimmed one takes under a millisecond. The bound
// sits an order of magnitude below the old cost and two above the new, so it
// separates the two implementations without being sensitive to how loaded the
// machine is.
//
// If this ever fails on a slow machine rather than on a real regression, raise
// the bound — do not delete the test. The thing being protected is that a
// one-line edit does not pay for the whole file, and any bound at all still
// says that.
func TestLineDiffIsNotQuadraticInFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("timing assertion")
	}
	// ~20k distinct content lines: distinct so no accidental matches make the
	// table cheap, and large enough that the quadratic version is unmissable.
	const n = 20000
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%17))
		b.WriteByte(' ')
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	old := b.String()
	// One line changed, in the middle, which is the shape the old comment
	// described as "small" and charged the full n×m for anyway.
	mid := "line " + strings.Repeat("x", (n/2)%17) + " " + itoa(n/2)
	next := strings.Replace(old, mid+"\n", "line CHANGED\n", 1)
	if next == old {
		t.Fatal("test fixture did not actually change a line")
	}

	start := time.Now()
	ins, del := lineDiff(old, next)
	elapsed := time.Since(start)

	if ins != 1 || del != 1 {
		t.Fatalf("lineDiff = %d/%d, want 1/1 — the fixture is not the one-line edit this measures", ins, del)
	}
	const bound = 200 * time.Millisecond
	if elapsed > bound {
		t.Errorf("one-line edit in a %d-line file took %v (bound %v). "+
			"lineDiff is paying for the whole file again: the common prefix and "+
			"suffix must be trimmed before the LCS table is built", n, elapsed.Round(time.Millisecond), bound)
	} else {
		t.Logf("one-line edit in a %d-line file: %v", n, elapsed.Round(time.Microsecond))
	}
}

// lcsLenUntrimmed is the plain full-table LCS the trimmed version replaced,
// kept here as the oracle. A table-driven test can only check the shapes
// somebody thought of; this checks the identity the trim rests on
// (LCS(P·a'·S, P·b'·S) = |P| + LCS(a',b') + |S|) against inputs nobody chose.
func lcsLenUntrimmed(a, b []string) int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return 0
	}
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] > cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
		for j := range cur {
			cur[j] = 0
		}
	}
	return prev[m]
}

// TestTrimmedLCSMatchesTheFullTable is the differential check. It draws short
// sequences from a deliberately tiny alphabet, because that is where a trim
// goes wrong: with few distinct lines the prefix and suffix runs are long and
// overlapping, which is exactly the double-counting the bounds have to prevent.
// A large alphabet would make almost every case trim to nothing and prove
// nothing.
//
// The PRNG is a fixed-seed LCG rather than math/rand so a failure reproduces
// exactly, on any Go version, from the seed printed in the message.
func TestTrimmedLCSMatchesTheFullTable(t *testing.T) {
	alphabet := []string{"a", "b", "c"}
	seed := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return seed >> 33
	}
	seq := func(maxLen int) []string {
		n := int(next()) % (maxLen + 1)
		out := make([]string, n)
		for i := range out {
			out[i] = alphabet[int(next())%len(alphabet)]
		}
		return out
	}
	for i := 0; i < 20000; i++ {
		a, b := seq(9), seq(9)
		got, want := lcsLen(a, b), lcsLenUntrimmed(a, b)
		if got != want {
			t.Fatalf("case %d: lcsLen(%v, %v) = %d, full table says %d — "+
				"the prefix/suffix trim is not exact", i, a, b, got, want)
		}
	}
}

// itoa avoids pulling strconv in for a test fixture.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d [20]byte
	p := len(d)
	for i > 0 {
		p--
		d[p] = byte('0' + i%10)
		i /= 10
	}
	return string(d[p:])
}
