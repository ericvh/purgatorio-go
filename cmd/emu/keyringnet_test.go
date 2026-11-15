package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestKeyringRemote runs Inferno's rcmd and cpu (authproto=keyring)
// against auxi/rstyxd under listen with keyring authentication on the
// loopback, as Inferno does: listen authenticates each caller with
// bob's key and pushes the ssl algorithm the caller asks for; a key from
// another signer is refused at both ends, and an algorithm the listener
// does not allow is refused by the listener (the client, as Inferno's,
// does not hear why). cpu's output is the caller's /dev/cons. The
// command runs as the user the caller proved.
func TestKeyringRemote(t *testing.T) {
	authKeys(t)
	user := cmds3dUser()
	kr := filepath.Join(root, "usr", user, "keyring")
	if _, err := os.Stat(kr); err == nil {
		t.Skip(kr + " exists; cpu would use it")
	}
	if err := os.MkdirAll(kr, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(root, "usr", user)) })
	b, _ := os.ReadFile(filepath.Join(root, "keydb", "alice"))
	os.WriteFile(filepath.Join(kr, "default"), b, 0o600)
	script := `authproto=keyring
listen -k /keydb/bob -a none -a rc4_256 -a sha1 'tcp!127.0.0.1!0' auxi/rstyxd >[2] /keydb/listen.log < /dev/null
P=tcp!` + "`" + `{cat /net/tcp/0/local}
rcmd -f /keydb/alice $P echo hello keyring </dev/null
rcmd -f /keydb/alice -e 'rc4_256 sha1' $P cat /n/client/lib/hello.txt </dev/null
cpu -C sha1 $P cat /n/client/dev/user </dev/null
echo
rcmd -f /keydb/mallory $P echo x </dev/null
echo status $status
rcmd -f /keydb/alice -e 'des_56_cbc' $P echo x </dev/null
echo status $status
rcmd -f /keydb/alice $P echo still serving </dev/null
rcmd -f /keydb/alice $P cat '#c/user' </dev/null
echo
cat /keydb/listen.log
`
	out, _ := emu(t, "", "sh", "-c", script)
	want := "hello keyring\n" +
		"hello from the root\n" +
		user + "\n" +
		"rcmd: authentication failed: pk doesn't match certificate\n" +
		"status auth failed\n" +
		"status \n" +
		"still serving\n" +
		"alice\n" +
		"rstyxd: cmd: echo hello keyring \n" +
		"rstyxd: cmd: cat /n/client/lib/hello.txt \n" +
		"rstyxd: cmd: auxi/cpuslave -- cat /n/client/dev/user \n" +
		"listen: auth on tcp!127.0.0.1!0 failed: pk doesn't match certificate\n" +
		"listen: auth on tcp!127.0.0.1!0 failed: unsupported client algorithm: des_56_cbc\n" +
		"rstyxd: cmd: echo still serving \n" +
		"rstyxd: cmd: cat #c/user \n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}
