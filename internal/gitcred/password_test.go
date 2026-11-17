package gitcred

import "testing"

func TestPassword(t *testing.T) {
	for _, tc := range []struct {
		name, out, want string
	}{
		{"typical", "protocol=https\nhost=github.com\nusername=x\npassword=s3cret\n", "s3cret"},
		{"CRLF line endings", "username=x\r\npassword=s3cret\r\n", "s3cret"},
		{"keeps = in the value", "password=a=b==\n", "a=b=="},
		{"first one wins", "password=one\npassword=two\n", "one"},
		{"no password", "protocol=https\nhost=github.com\nusername=x\n", ""},
		{"a similar key", "password_expiry_utc=1700000000\n", ""},
		{"empty", "", ""},
	} {
		if got := password(tc.out); got != tc.want {
			t.Errorf("%s: password() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
