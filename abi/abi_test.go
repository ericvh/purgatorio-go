package abi

import "testing"

func TestErrno(t *testing.T) {
	if got := Errno(Enonexist); got != ErrnoNoent {
		t.Errorf("Errno(%q) = %d, want %d", Enonexist, got, ErrnoNoent)
	}
	if got := Errno("some server's own error"); got != ErrnoIO {
		t.Errorf("unknown string gave %d, want EIO", got)
	}
}
