package dial

import "testing"

func TestParse(t *testing.T) {
	cases := []struct{ in, netdir, proto, rem string }{
		{"tcp!host!80", "", "tcp", "host!80"},
		{"host", "", "net", "host"},
		{"/net.alt/tcp!h!1", "/net.alt", "tcp", "h!1"},
	}
	for _, c := range cases {
		n, p, r := dialparse(c.in)
		if n != c.netdir || p != c.proto || r != c.rem {
			t.Errorf("dialparse(%q) = %q %q %q", c.in, n, p, r)
		}
	}
	if got := Netmkaddr("host", "tcp", "styx"); got != "tcp!host!styx" {
		t.Errorf("netmkaddr: %q", got)
	}
	if got := Netmkaddr("tcp!host", "", "styx"); got != "tcp!host!styx" {
		t.Errorf("netmkaddr 2: %q", got)
	}
	if got := redir("/net/tcp/clone", "/net.alt"); got != "/net.alt/tcp/clone" {
		t.Errorf("redir: %q", got)
	}
}
