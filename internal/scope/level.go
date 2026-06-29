package scope

// Level is one rung of the scope ladder (File 15 §15.x). The controller moves up
// and down this ladder as verify feedback expands or contracts the task's
// scope. The ordering is the iota order: Task is the broadest problem
// statement, Verify is the verification scope.
type Level int

const (
	LevelTask     Level = iota // issue-level
	LevelRepo                  // repo exploration
	LevelFile                  // file-level
	LevelFunction              // function-level
	LevelEdit                  // edit-level
	LevelVerify                // verification
)

// String returns the uppercase name of the level.
func (l Level) String() string {
	switch l {
	case LevelTask:
		return "TASK"
	case LevelRepo:
		return "REPO"
	case LevelFile:
		return "FILE"
	case LevelFunction:
		return "FUNCTION"
	case LevelEdit:
		return "EDIT"
	case LevelVerify:
		return "VERIFY"
	default:
		return "UNKNOWN"
	}
}
