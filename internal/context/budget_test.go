// Budget allocation tests (File 06 §6.6.1). The interesting cases are all at
// the boundaries: allocate() used to be silently degenerate for small windows,
// and a degenerate Budget does not look broken downstream — it looks like a
// prompt with nothing in it.

package context

import "testing"

// TestAllocateIsNotDegenerateAtSmallWindows is the regression guard for the
// reply-reserve floor eating the whole window. With reserve floored at 1024 and
// nothing capping it, every window ≤ 1024 produced avail ≤ 0 and every group
// cap came back 0 — and downstream a cap of 0 is not "a small share", it is
// prompt.slotNoAllocation, which drops the group entirely. A model with a small
// window got a prompt with 100% of its retrieved context and RAG removed, and
// nothing reported it because an empty group is indistinguishable from a group
// that had nothing to say.
//
// window=1025 is called out separately because it is the case the obvious guard
// misses: `if reserve > window { ... }` never fires there (1024 < 1025), and the
// allocation is 1 token spread across seven groups.
func TestAllocateIsNotDegenerateAtSmallWindows(t *testing.T) {
	for _, window := range []int{2, 100, 512, 900, 1024, 1025, 1500, 2000, 2048, 4096} {
		b := allocate(window)
		if b.Reserve >= window {
			t.Errorf("window=%d: Reserve=%d leaves nothing for input", window, b.Reserve)
		}
		for _, g := range []struct {
			name string
			cap  int
		}{
			{"System", b.System}, {"Project", b.Project}, {"Preferences", b.Preferences},
			{"Conversation", b.Conversation}, {"Files", b.Files}, {"RAG", b.RAG}, {"User", b.User},
		} {
			if g.cap <= 0 {
				t.Errorf("window=%d: %s cap = %d; a positive window must never produce a zero "+
					"group cap — downstream that means 'drop this group', not 'allocate it a little'",
					window, g.name, g.cap)
			}
		}
	}
}

// TestAllocateReserveNeverExceedsHalfTheWindow pins the shape of the clamp
// rather than one arithmetic result: the reply reservation may not take more of
// the window than the input side gets. The floor still applies wherever it fits.
func TestAllocateReserveNeverExceedsHalfTheWindow(t *testing.T) {
	for _, window := range []int{1, 2, 100, 900, 1024, 1025, 2000, 2048, 4096, 8000, 128000} {
		b := allocate(window)
		if lim := window * ReserveMaxPct / 100; b.Reserve > lim && b.Reserve > 1 {
			t.Errorf("window=%d: Reserve=%d exceeds the %d%% clamp (%d)",
				window, b.Reserve, ReserveMaxPct, lim)
		}
	}
	// Where the floor fits, it is still the floor: 4096 is a real window and its
	// allocation must not have moved.
	if got := allocate(4096).Reserve; got != ReserveFloor {
		t.Errorf("allocate(4096).Reserve = %d, want the %d floor unchanged", got, ReserveFloor)
	}
}

// TestAllocateRealWindowsAreUnchangedByTheClamp: the clamp is a fix for the
// degenerate tail, not a re-tuning. Above 2*ReserveFloor it must never bind, so
// every window a real model actually has allocates exactly as before.
func TestAllocateRealWindowsAreUnchangedByTheClamp(t *testing.T) {
	for _, window := range []int{4096, 8192, 32000, 128000, 200000} {
		b := allocate(window)
		want := window * PctReserve / 100
		if want < ReserveFloor {
			want = ReserveFloor
		}
		if b.Reserve != want {
			t.Errorf("window=%d: Reserve=%d, want %d (floor-or-percentage, clamp must not bind)",
				window, b.Reserve, want)
		}
	}
}

// TestAllocateSharesComeFromTheDeclaredPercentages is the §6.6.1 single-owner
// pin. The Prompt Compiler used to re-derive Files/RAG/Preferences from its own
// copies of these literals; this asserts the constants in budget.go and the
// Budget fields are the same arithmetic, so there is nothing left to re-derive.
func TestAllocateSharesComeFromTheDeclaredPercentages(t *testing.T) {
	const window = 128_000
	b := allocate(window)
	avail := window - b.Reserve
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"System", b.System, min(avail*PctSystem/100, MaxSystem)},
		{"Project", b.Project, min(avail*PctProject/100, MaxProject)},
		{"Preferences", b.Preferences, min(avail*PctPreferences/100, MaxPreferences)},
		{"Conversation", b.Conversation, avail * PctConversation / 100},
		{"Files", b.Files, avail * PctFiles / 100},
		{"RAG", b.RAG, avail * PctRAG / 100},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (avail=%d)", c.name, c.got, c.want, avail)
		}
	}
	// §6.6.1's table gives Files 25%; the implementation's split into Files+RAG
	// must still add up to it, or the spec and the code have quietly diverged.
	if PctFiles+PctRAG != 25 {
		t.Errorf("PctFiles+PctRAG = %d, want 25 (§6.6.1's Files share)", PctFiles+PctRAG)
	}
}

// TestAllocateGivesPreferencesItsOwnShare: the Preferences group is emitted
// under its own <preferences> section and shipped, so it costs tokens and needs
// a cap of its own. Layer 5 was capping it with Budget.Project — arithmetic that
// happened to work because both are persistent guidance in the system block.
// The field makes it a contract; the equality below is the compatibility half,
// pinning that naming it moved nothing downstream.
func TestAllocateGivesPreferencesItsOwnShare(t *testing.T) {
	if b := allocate(128_000); b.Preferences <= 0 {
		t.Fatalf("Budget.Preferences = %d; the group ships to the model and must be budgeted", b.Preferences)
	}
	// The compatibility guarantee, asserted as an identity rather than an
	// argument: at every window the new field equals the value Layer 5's borrow
	// was already computing, so replacing `pref: b.Project` with
	// `pref: b.Preferences` cannot move a single prompt. That needs both the
	// percentage *and* the ceiling to match — with PctPreferences alone,
	// preferences get 8704 tokens at 128k where the borrow gave 2048, because
	// MaxProject binds there and nothing was binding Preferences.
	for _, window := range []int{2, 900, 2000, 4096, 32_000, 128_000, 200_000} {
		b := allocate(window)
		if b.Preferences != b.Project {
			t.Errorf("window=%d: Preferences=%d, Project=%d. Naming the share must reproduce "+
				"the cap Layer 5 was borrowing exactly, not quietly loosen it",
				window, b.Preferences, b.Project)
		}
	}
}

// min is a local helper: the package's own max() is int-typed and there is no
// min() beside it. Kept in the test file so budget.go's surface stays unchanged.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
