package scope

// scopePermissions is the W2 scope-gated tool-access table (File 15 §15.x):
// each Level admits only the tools listed for it. Lower (broader) levels are
// read-only; mutating tools are only permitted at LevelEdit; verification
// tooling only at LevelVerify.
var scopePermissions = map[Level][]string{
	LevelTask:     {"plan", "decompose"},               // no edit
	LevelRepo:     {"list_files", "grep", "read_file"}, // read-only
	LevelFile:     {"read_file", "grep"},
	LevelFunction: {"read_file", "view_function", "call_graph"},
	LevelEdit:     {"edit_file", "write_file"},
	LevelVerify:   {"run_test", "bash", "git_diff"},
}

// AllowedTools returns the slice of tools permitted at level. The returned
// slice is the table's backing slice; callers must not mutate it.
func AllowedTools(level Level) []string {
	return scopePermissions[level]
}

// LevelAllowsTool reports whether tool is permitted at level.
func LevelAllowsTool(level Level, tool string) bool {
	for _, t := range scopePermissions[level] {
		if t == tool {
			return true
		}
	}
	return false
}
