package scope

import "testing"

// TestLevel_String pins the uppercase name of every Level.
func TestLevel_String(t *testing.T) {
	for _, tc := range []struct {
		level Level
		want  string
	}{
		{LevelTask, "TASK"},
		{LevelRepo, "REPO"},
		{LevelFile, "FILE"},
		{LevelFunction, "FUNCTION"},
		{LevelEdit, "EDIT"},
		{LevelVerify, "VERIFY"},
	} {
		if got := tc.level.String(); got != tc.want {
			t.Errorf("Level(%d).String() = %q, want %q", tc.level, got, tc.want)
		}
	}
}
