package auth

// Login authenticates a user by name. It is the entry point of the auth
// module and the subject of the L5-003 fixture task.
func Login(user string) error {
	if user == "" {
		return errEmpty
	}
	return nil
}

var errEmpty = strErr("auth: empty user")

type strErr string

func (e strErr) Error() string { return string(e) }
