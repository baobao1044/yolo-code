package workflow

import "testing"

// TestClassify exercises the heuristic classifier over each keyword group plus
// the order rules (bugfix before refactor before feature) and the default
// fallback. Goals are chosen so a single keyword unambiguously selects a group.
func TestClassify(t *testing.T) {
	c := NewClassifier()
	for _, tc := range []struct {
		name string
		goal string
		want WorkflowType
	}{
		// Bugfix keywords.
		{"bugfix fix", "fix the login bug", TypeBugfix},
		{"bugfix bug", "there is a bug in parsing", TypeBugfix},
		{"bugfix error", "error when reading config", TypeBugfix},
		{"bugfix fail", "the test fails on CI", TypeBugfix},
		{"bugfix crash", "the app crashes on startup", TypeBugfix},
		{"bugfix broken", "broken link in the header", TypeBugfix},
		// Refactor keywords.
		{"refactor refactor", "refactor the auth module", TypeRefactor},
		{"refactor rename", "rename UserService to AccountService", TypeRefactor},
		{"refactor restructure", "restructure the handlers package", TypeRefactor},
		{"refactor clean up", "clean up the utils package", TypeRefactor},
		{"refactor cleanup", "cleanup the dead code", TypeRefactor},
		// Feature keywords.
		{"feature add", "add dark mode", TypeFeature},
		{"feature implement", "implement CSV export", TypeFeature},
		{"feature feature", "enable the feature flag for the beta channel", TypeFeature},
		{"feature support", "add support for webhooks", TypeFeature},
		{"feature create", "create a settings page", TypeFeature},
		{"feature build", "build a dashboard", TypeFeature},
		// Order rules: first match wins (bugfix > refactor > feature).
		{"order bugfix beats feature", "add a fix for the crash", TypeBugfix},
		{"order refactor beats feature", "refactor and add tests", TypeRefactor},
		// Default fallback (no keyword).
		{"default doc update", "update the documentation", TypeDefault},
		{"default greeting", "hello world", TypeDefault},
		{"default empty", "", TypeDefault},
		// Case-insensitivity: the goal is lowercased before matching.
		{"case insensitive", "FIX the LOGIN Bug", TypeBugfix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Classify(tc.goal); got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.goal, got, tc.want)
			}
		})
	}
}
