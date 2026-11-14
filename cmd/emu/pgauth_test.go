package main

import "testing"

// TestPgauth runs pgauth's mount handshake through the kernel's export
// and #M (docs/auth.md): versions 4 and 3, a server that takes both,
// the refused downgrades each way, a server that trusts nothing, the
// server's proof, and a relay that sees no file name or data once the
// conversation is encrypted (and sees both with -A); and styxlisten over
// TCP with the mount command, -S, the other protocol and -A, and import -S.
func TestPgauth(t *testing.T) {
	ok := "read 340 bytes\n"
	cases := []struct {
		args []string
		out  string
	}{
		{[]string{"hybrid", "hybrid"}, ok},
		{[]string{"classical", "classical"}, ok},
		{[]string{"classical", "hybrid,classical"}, ok},
		{[]string{"hybrid", "hybrid,classical"}, ok},
		{[]string{"hybrid", "classical"}, "authenticate: no hybrid key offered\n"},
		{[]string{"classical", "hybrid"}, "authenticate: hybrid authentication required\n"},
		{[]string{"hybrid", "hybrid", "none"}, "authenticate: server trusts no keys\n"},
		{[]string{"-S", "hybrid", "hybrid"}, ok},
		{[]string{"-E", "hybrid", "hybrid"}, ok + "mount again: conversation is encrypted\n"},
		{[]string{"-S", "classical", "classical"}, ok},
		{[]string{"-R", "hybrid", "hybrid"}, ok + "file text in the clear: false; name in the clear: false\n"},
		{[]string{"-R", "classical", "classical"}, ok + "file text in the clear: false; name in the clear: false\n"},
		{[]string{"-R", "none", "none"}, ok + "file text in the clear: true; name in the clear: true\n"},
	}
	net := func(p, other string) string {
		return "mount -p " + p + " -S: status \"\", read 340 bytes true\n" +
			"mount: authentication failed: " + other + "\n" +
			"mount -p " + map[string]string{"hybrid": "classical", "classical": "hybrid"}[p] + ": status \"fail:error\", read 0 bytes false\n" +
			"mount: mount failed: authentication required\n" +
			"mount -p " + p + " -A: status \"fail:mount failed\", read 0 bytes false\n" +
			"import -p " + p + " -S: status \"\", read 340 bytes true\n"
	}
	cases = append(cases,
		struct {
			args []string
			out  string
		}{[]string{"net", "hybrid"}, net("hybrid", "hybrid authentication required")},
		struct {
			args []string
			out  string
		}{[]string{"net", "classical"}, net("classical", "no hybrid key offered")})
	for _, c := range cases {
		out, st := emu(t, "", append([]string{"/bin/test/pgauth"}, c.args...)...)
		if out != c.out || st != "" {
			t.Errorf("%v: got %q %q, want %q", c.args, out, st, c.out)
		}
	}
}
