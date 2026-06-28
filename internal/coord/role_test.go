// Tests for L11-003 — agent roles with scoped tools (File 12 §12.2.1).
//
// Role tool sets are enforced at the registry level (File 12 §12.2.1), not
// just by prompting: Planner and Reviewer are read-only (no Write/Patch);
// Coder is the only role that can modify files; Tester is confined to
// test/build commands; Researcher is read-only investigation.

package coord

import (
	"testing"
)

// TestRoleCoderTools: Coder = Read, Write, Patch, Bash (the only writer).
func TestRoleCoderTools(t *testing.T) {
	got := ScopedTools(RoleCoder)
	if !sameSet(got, []string{"Read", "Write", "Patch", "Bash"}) {
		t.Errorf("Coder tools = %v, want [Read Write Patch Bash]", got)
	}
	if !RoleAllowed(RoleCoder, "Write") {
		t.Errorf("RoleAllowed(RoleCoder, Write) = false, want true (Coder is the writer)")
	}
	if !RoleAllowed(RoleCoder, "Patch") {
		t.Errorf("RoleAllowed(RoleCoder, Patch) = false, want true")
	}
}

// TestRoleReviewerReadOnly: Reviewer is read-only — no Write, no Patch.
func TestRoleReviewerReadOnly(t *testing.T) {
	got := ScopedTools(RoleReviewer)
	if !sameSet(got, []string{"Read", "Grep", "Glob"}) {
		t.Errorf("Reviewer tools = %v, want [Read Grep Glob]", got)
	}
	for _, forbidden := range []string{"Write", "Patch", "Bash", "Browser"} {
		if RoleAllowed(RoleReviewer, forbidden) {
			t.Errorf("RoleAllowed(RoleReviewer, %s) = true, want false (read-only)", forbidden)
		}
	}
	if !RoleAllowed(RoleReviewer, "Read") {
		t.Errorf("RoleAllowed(RoleReviewer, Read) = false, want true")
	}
}

// TestRolePlannerReadOnly: Planner is read-only — no Write, no Patch.
func TestRolePlannerReadOnly(t *testing.T) {
	got := ScopedTools(RolePlanner)
	if !sameSet(got, []string{"Read", "Grep", "Glob"}) {
		t.Errorf("Planner tools = %v, want [Read Grep Glob]", got)
	}
	for _, forbidden := range []string{"Write", "Patch", "Bash"} {
		if RoleAllowed(RolePlanner, forbidden) {
			t.Errorf("RoleAllowed(RolePlanner, %s) = true, want false (read-only)", forbidden)
		}
	}
}

// TestRoleTesterTools: Tester = Bash (test), Read — confined to test/build.
func TestRoleTesterTools(t *testing.T) {
	got := ScopedTools(RoleTester)
	if !sameSet(got, []string{"Bash", "Read"}) {
		t.Errorf("Tester tools = %v, want [Bash Read]", got)
	}
	if !RoleAllowed(RoleTester, "Bash") {
		t.Errorf("RoleAllowed(RoleTester, Bash) = false, want true (test runner)")
	}
	for _, forbidden := range []string{"Write", "Patch", "Browser"} {
		if RoleAllowed(RoleTester, forbidden) {
			t.Errorf("RoleAllowed(RoleTester, %s) = true, want false", forbidden)
		}
	}
}

// TestRoleResearcherTools: Researcher = Read, Grep, Glob, Browser — read-only
// investigation (the only role with Browser/HTTP).
func TestRoleResearcherTools(t *testing.T) {
	got := ScopedTools(RoleResearcher)
	if !sameSet(got, []string{"Read", "Grep", "Glob", "Browser"}) {
		t.Errorf("Researcher tools = %v, want [Read Grep Glob Browser]", got)
	}
	if !RoleAllowed(RoleResearcher, "Browser") {
		t.Errorf("RoleAllowed(RoleResearcher, Browser) = false, want true (only role)")
	}
	for _, forbidden := range []string{"Write", "Patch"} {
		if RoleAllowed(RoleResearcher, forbidden) {
			t.Errorf("RoleAllowed(RoleResearcher, %s) = true, want false (read-only)", forbidden)
		}
	}
}

// TestRoleUnknownDefaultsEmpty: an unknown role has no tools and allows
// nothing (defense — never silently grant all tools to an undefined role).
func TestRoleUnknownDefaultsEmpty(t *testing.T) {
	if got := ScopedTools(Role("nonexistent")); len(got) != 0 {
		t.Errorf("ScopedTools(unknown) = %v, want empty", got)
	}
	for _, tool := range []string{"Read", "Write", "Patch", "Bash", "Browser"} {
		if RoleAllowed(Role("nonexistent"), tool) {
			t.Errorf("RoleAllowed(unknown, %s) = true, want false (deny by default)", tool)
		}
	}
}

// TestRolePriority: the §12.5.2 priority order for pre-emption is
// user > planner > coder > reviewer > tester > researcher.
func TestRolePriority(t *testing.T) {
	cases := []struct {
		role Role
		want int
	}{
		{RoleUser, 0},
		{RolePlanner, 1},
		{RoleCoder, 2},
		{RoleReviewer, 3},
		{RoleTester, 4},
		{RoleResearcher, 5},
	}
	for _, c := range cases {
		got := RolePriority(c.role)
		if got != c.want {
			t.Errorf("RolePriority(%v) = %d, want %d", c.role, got, c.want)
		}
	}
	// Higher priority (lower number) pre-empts.
	if RolePriority(RolePlanner) >= RolePriority(RoleCoder) {
		t.Errorf("Planner (priority %d) should pre-empt Coder (priority %d)",
			RolePriority(RolePlanner), RolePriority(RoleCoder))
	}
}

// sameSet reports whether two slices contain the same elements regardless of
// order. Tool sets are unordered; this avoids asserting a specific order
// that the implementation isn't required to keep.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}
