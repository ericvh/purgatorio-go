package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRcmdPgauth runs rcmd and cpu over pgauth's rstyx protocol
// (docs/rcmd.md) against auxi/rstyxd under listen on the loopback: the
// hybrid and the classical protocol, standard input and output, the
// caller's namespace as /n/client, -S, a refused protocol and a client
// that gives no proof, and cpu's devices.
func TestRcmdPgauth(t *testing.T) {
	kd := filepath.Join(root, "rcmdpg")
	os.MkdirAll(kd, 0o755)
	t.Cleanup(func() { os.RemoveAll(kd) })
	script := (`bind /lib/test/keys /lib/keys
listen 'tcp!127.0.0.1!0' auxi/rstyxd > /dev/null >[2] /dev/null </dev/null
P1=tcp!` + "`" + `{cat /net/tcp/0/local}
listen -p classical 'tcp!127.0.0.1!0' auxi/rstyxd > /dev/null >[2] /dev/null </dev/null
P2=tcp!` + "`" + `{cat /net/tcp/1/local}
rcmd $P1 echo hello remote </dev/null
rcmd $P1 cat /n/client/lib/hello.txt </dev/null
echo from stdin | rcmd $P1 cat
rcmd -S $P1 echo proved </dev/null
rcmd $P1 'echo to stderr >[1=2]' </dev/null
rcmd $P1 nonesuch </dev/null
echo status $status
rcmd -p classical $P1 echo x </dev/null
rcmd -A $P1 echo x </dev/null
cpu $P1 cat /dev/user </dev/null > /rcmdpg/user
cmp /rcmdpg/user /dev/user
echo cpu $status
rcmd -p classical -S $P2 echo classical </dev/null
rcmd $P2 echo x </dev/null
`)
	out, st := emu(t, "", "sh", "-c", "{"+script+"} > /rcmdpg/out >[2=1]; cat /rcmdpg/out")
	want := `hello remote
hello from the root
from stdin
proved
to stderr
sh: nonesuch: file does not exist
status file does not exist
rcmd: hybrid authentication required
rcmd: authentication required
cpu 
classical
rcmd: classical authentication required
`
	if out != want || st != "" {
		t.Errorf("got %q %q\nwant %q", out, st, want)
	}
}
