package auth

import "testing"

func TestLogin(t *testing.T) {
	cases := []struct {
		name string
		user string
		want bool
	}{
		{"empty fails", "", false},
		{"named ok", "alice", true},
	}
	for _, c := range cases {
		err := Login(c.user)
		got := err == nil
		if got != c.want {
			t.Errorf("%s: Login(%q) err=%v, want ok=%v", c.name, c.user, err, c.want)
		}
	}
}
