// Token budget allocation (File 06 §6.6.1). The window is split into a reply
// reservation (a hard floor, never borrowed from) and a waterfall across input
// groups with strict priorities: system > project > preferences > conversation
// > files > user. The Context Engine computes this; the Prompt Compiler
// enforces it.

package context

// The §6.6.1 group shares, as percentages of the *available* window (the window
// less the reply reserve). This block is the single owner of the split.
//
// It is written down here because it was previously written down twice: the
// Prompt Compiler's effectiveSlots re-derived Files/RAG/Preferences from its own
// copies of these literals, not out of preference but because allocate() could
// hand it a Budget in which every cap was 0 (see reserveFor) and it had no way
// to reach into this file. With the degenerate allocation fixed and Preferences
// given a real field, downstream reads Budget and these constants are the only
// place a share is written.
//
// §6.6.1's table gives Files 25%. The implementation splits that into
// PctFiles 17% + PctRAG 8% (File 11 §11.6 retrieved chunks) so semantic recall
// has an allocation that a couple of open files cannot swallow; the two still
// sum to the spec's 25%.
//
// PctPreferences equals PctProject deliberately. Layer 5 had been capping the
// Preferences group with b.Project — legitimate arithmetic, since both are
// persistent guidance ordered together in the system block, but a coincidence
// rather than a contract. Naming the share at the same 8% turns it into a
// contract while moving no downstream behaviour.
const (
	PctReserve      = 15
	PctSystem       = 12
	PctProject      = 8
	PctPreferences  = 8
	PctConversation = 45
	PctFiles        = 17
	PctRAG          = 8

	// MaxSystem and MaxProject are §6.6.1's absolute ceilings on the two
	// fixed-content groups: past them a larger window buys nothing, because the
	// role/tool text and AGENTS.md do not grow with the window.
	MaxSystem  = 4096
	MaxProject = 2048
	// MaxPreferences matches MaxProject for the same reason PctPreferences
	// matches PctProject: it is the value Layer 5's borrow was already using, so
	// naming the share is provably behaviour-neutral. Without the ceiling the
	// percentage alone would quietly hand preferences 8704 tokens at a 128k
	// window where the borrow gave 2048 — a four-fold loosening of the cap on
	// the one group that is unbounded and accumulating, smuggled in under a
	// change whose whole purpose was to stop relying on a coincidence. Whether
	// preferences deserve a different ceiling is a real question and not this
	// one; the answer to an unbounded group is a top-K where it is produced.
	MaxPreferences = 2048

	// ReserveFloor is the reply reservation's absolute floor (§6.6.1): fewer
	// tokens than this and the model has no room to answer at all.
	ReserveFloor = 1024
	// ReserveMaxPct caps the reserve as a share of the whole window, so the
	// floor can never take the window's input side with it. Half is not a tuned
	// number: it is the point at which the statement inverts — you cannot
	// reserve more of a window for the answer than you allow for the question.
	ReserveMaxPct = 50
)

// reserveFor returns the reply reservation for a window: the §6.6.1 percentage,
// floored at ReserveFloor, then capped at ReserveMaxPct of the window.
//
// The cap is the fix for a silently degenerate allocation. The floor alone made
// allocate() return an all-zero budget for every window ≤ ReserveFloor (avail
// went negative and clamped to 0) and a one-token budget at window 1025 — so a
// small-window model got group caps of 0, and downstream a cap of 0 does not
// read as "a very small share", it reads as "this group has no allocation, drop
// it" (prompt.slotNoAllocation). 100% of retrieved context and RAG vanished
// from the prompt with nothing reporting it.
//
// The obvious guard — `if reserve > window { reserve = window * 15 / 100 }` —
// is not enough, and measurement is why: it only fires below the floor, so
// window 1025 still reserves 1024 of 1025 and still allocates 1 token across
// six groups. The cap has to bound the reserve as a *fraction*, not merely keep
// it inside the window. Above 2*ReserveFloor the cap never binds, so every real
// window (4096 and up) allocates exactly as it did before.
func reserveFor(window int) int {
	if window <= 0 {
		return 0
	}
	r := window * PctReserve / 100
	if r < ReserveFloor {
		r = ReserveFloor
	}
	if lim := window * ReserveMaxPct / 100; r > lim {
		r = lim
	}
	if r < 1 {
		r = 1 // a positive window always reserves *something* for the reply
	}
	return r
}

// share is avail*pct/100, floored at 1 whenever there is anything to share.
//
// The floor is the same defect as reserveFor's cap, one scale down. Integer
// truncation sends a group's cap to 0 for any avail below 100/pct — RAG and
// Project at 8% lose their cap below avail 13 — and downstream that 0 deletes
// the group rather than shrinking it. A cap of 1 keeps the group's single
// highest-ranked part, because prompt.trimGroup admits the first part even when
// it alone overflows the slot; a cap of 0 keeps nothing.
//
// For a window small enough that several floors bind at once the caps sum above
// avail. That is deliberate: the caps are per-group maxima enforced
// independently of one another, and the prompt's real total is checked against
// Budget.Window downstream regardless. Over-allocating a 20-token window is not
// a failure mode; silently emptying six groups is.
func share(avail, pct int) int {
	if avail <= 0 {
		return 0
	}
	if s := avail * pct / 100; s > 0 {
		return s
	}
	return 1
}

// allocate splits a token window into the §6.6.1 budget: reserve first (see
// reserveFor), then the group shares above off what is left, with System and
// Project clamped to their ceilings and User taking the remainder. RAG
// (retrieved code chunks, File 11 §11.6) takes a slice off Files — semantic
// recall is cheaper to recompute than re-reading disk, so RAG trims before
// Files when over budget.
//
// Invariant, worth stating because its absence was the bug: for any window ≥ 2
// every field of the returned Budget is ≥ 1. A zero cap therefore means "no
// window", never "no room".
func allocate(window int) Budget {
	reserve := reserveFor(window)
	avail := window - reserve
	if avail < 0 {
		avail = 0
	}
	sys := share(avail, PctSystem)
	if sys > MaxSystem {
		sys = MaxSystem
	}
	proj := share(avail, PctProject)
	if proj > MaxProject {
		proj = MaxProject
	}
	pref := share(avail, PctPreferences)
	if pref > MaxPreferences {
		pref = MaxPreferences
	}
	conv := share(avail, PctConversation)
	files := share(avail, PctFiles)
	rag := share(avail, PctRAG)
	// User is the remainder rather than a percentage, so the System/Project
	// ceilings hand their unused share to the current request instead of
	// dropping it. Preferences' 8% comes out of that remainder: nothing else
	// moves, and the current user message is never trimmed downstream (§6.7.3),
	// so its cap is a ceiling nobody enforces against a one-line goal.
	user := avail - sys - proj - pref - conv - files - rag
	if user < 0 {
		user = 0
	}
	if avail > 0 && user == 0 {
		user = 1 // same floor, same reason, as share()
	}
	return Budget{
		Window: window, Reserve: reserve,
		System: sys, Project: proj, Preferences: pref,
		Conversation: conv, Files: files, RAG: rag, User: user,
	}
}
