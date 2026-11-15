package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
)

// cmds3dUser is the host owner, as the emu names it.
func cmds3dUser() string { return (&host.Host{}).User() }

// cmds3dSetup makes the scratch tree root/tmp/cmds3d with the given
// files (relative path -> contents) and returns its host path.
func cmds3dSetup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, "tmp", "cmds3d")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		if name[len(name)-1] == '/' {
			if err := os.MkdirAll(p, 0o777); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestCmds3DNs checks nsbuild, with a name space file that binds,
// includes another file, expands variables and mounts a styxlisten
// export over TCP, its errors, and runas.
func TestCmds3DNs(t *testing.T) {
	cmds3dSetup(t, map[string]string{
		"a/f": "hi\n",
		"b/":  "",
		"m/":  "",
		"ns": `# built by nsbuild
bind $d/a $d/b
. $d/ns2
mount -A tcp!$port $d/m
`,
		"ns2": "bind -bi /nonexistent $d/b\nbind -a $d/x$nosuch /x\n",
		"t.sh": `d=/tmp/cmds3d
styxlisten -A 'tcp!127.0.0.1!0' export /lib
port=` + "`" + `{cat /net/tcp/*/local}
nsbuild $d/ns
echo nsbuild $status
ls $d/b
cat $d/b/f $d/m/hello.txt
echo bogus x > $d/bad
nsbuild $d/bad; echo $status
echo 'mount -A tcp!127.0.0.1!1 /n/x' > $d/bad
nsbuild $d/bad; echo $status
echo 'mount tcp!127.0.0.1!1 /n/x' > $d/bad
nsbuild $d/bad; echo $status
echo 'bind /nonexistent /n' > $d/bad
nsbuild $d/bad; echo $status
nsbuild a b; echo $status
nsbuild; echo $status
runas bob sh -c 'cat /dev/user; echo; runas alice echo no; echo $status'
runas; echo $status
`,
	})
	out, st := emu(t, "", "sh", "/tmp/cmds3d/t.sh")
	want := "nsbuild \n" + `/tmp/cmds3d/b/f
hi
hello from the root
nsbuild: error building namespace: invalid namespace command
newns
nsbuild: error building namespace: dial: tcp!127.0.0.1!1: connection refused
newns
nsbuild: error building namespace: cannot read certificate from /usr/` + cmds3dUser() + `/keyring/default: keyring authentication not implemented
newns
nsbuild: error building namespace: bind /nonexistent /n: file does not exist
newns
Usage: nsbuild [nsfile]
usage
nsbuild: error building namespace: cannot open namespace: file does not exist
newns
bob
runas: cannot set user: permission denied
error
usage: runas user cmd [args...]
usage
`
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}

// TestCmds3DNdb checks ndb/query against /lib/ndb/local and the hash
// file ndb/mkhash writes.
func TestCmds3DNdb(t *testing.T) {
	dir := cmds3dSetup(t, map[string]string{
		"db": "#c\na=x b=1\n\tb=2\n# inside\na=y\n",
		"t.sh": `ndb/query tcp rstyx port
ndb/query port 564
ndb/query -a tcp
ndb/query infernosite
ndb/query -a database '' file
ndb/query nosuch; echo $status
ndb/query -f /tmp/cmds3d/db -a b 2 a
ndb/query -f /nope x; echo $status
ndb/query; echo $status
ndb/mkhash /tmp/cmds3d/db b; echo $status
ndb/mkhash; echo $status
ndb/mkhash /nope x; echo $status
`,
	})
	mtime := time.Unix(1600000000, 0)
	if err := os.Chtimes(filepath.Join(dir, "db"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	out, st := emu(t, "", "sh", "/tmp/cmds3d/t.sh")
	want := `6668
tcp=9fs port=564
tcp=styx port=6666
tcp=rstyx port=6668
tcp=registry port=6675
tcp=exportfs port=17007
tcp=9fs port=564
tcp=ticket port=567
tcp=smtp port=25
infernosite=''
/lib/ndb/local
/lib/ndb/common

x
query: can't open /nope: file does not exist
error
usage: query [-a] [-f dbfile] attr [value [rattr]]
usage

mkhash: usage: mkhash file attr
error
mkhash: can't open /nope: file does not exist
error
`
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
	// Both values are in the one entry at offset 0; hash("1") and
	// hash("2") are slots 3 and 4 of 5.
	got, err := os.ReadFile(filepath.Join(dir, "db.b"))
	if err != nil {
		t.Fatal(err)
	}
	hash := []byte{0x00, 0x10, 0x5e, 0x5f, 5, 0, 0, 0,
		1, 0, 0x80, 1, 0, 0x80, 1, 0, 0x80, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(got, hash) {
		t.Errorf("db.b: % x, want % x", got, hash)
	}
}

// TestCmds3DRemote runs Inferno's rcmd and cpu (authproto=keyring) against
// auxi/rstyxd started by listen -A on loopback: remote commands see the caller's name space in
// /n/client and use its /dev/cons, here the emu's input and output.
func TestCmds3DRemote(t *testing.T) {
	cmds3dSetup(t, map[string]string{
		"t.sh": `authproto=keyring
listen -A 'tcp!127.0.0.1!0' auxi/rstyxd >[2] /tmp/cmds3d/rstyxd.log
port=` + "`" + `{cat /net/tcp/*/local}
rcmd tcp!$port echo x; echo $status
rcmd -A tcp!$port echo hello remote
rcmd -A tcp!$port cat /n/client/lib/hello.txt
rcmd -A tcp!$port
rcmd -A tcp!$port nonesuch
cpu -A tcp!$port echo 'a b' c
cpu -A tcp!$port cat /n/client/dev/user
echo
cpu -A tcp!$port nonesuch
rcmd -A tcp!127.0.0.1!1 echo x; echo $status
rcmd; echo $status
cpu; echo $status
cpu tcp!$port echo x; echo $status
cat /tmp/cmds3d/rstyxd.log
`,
	})
	out, st := emu(t, "echo from the remote sh\n", "sh", "/tmp/cmds3d/t.sh")
	user := cmds3dUser()
	// rcmd without -A authenticates; with no key it gets nowhere, and the
	// rstyxd behind listen -A takes its first message for arguments.
	want := "rcmd: authentication failed: hungup\n" +
		"auth failed\n" +
		"hello remote\n" +
		"hello from the root\n" +
		"; from the remote sh\n" +
		"; a b c\n" +
		user + "\n" +
		"cpuslave: can't load nonesuch: file does not exist\n" +
		"rcmd: dial tcp!127.0.0.1!1 failed: connection refused\n" +
		"errors\n" +
		"usage: rcmd [-A] [-f keyfile] [-e alg] [-x exportpath] tcp!mach cmd\n" +
		"usage\n" +
		"Usage: cpu [-C cryptoalg] mach command args...\n" +
		"usage\n" +
		"cpu: cannot find certificate in /usr/" + user + "/keyring/; use getauthinfo\n" +
		"no certificate\n" +
		"rstyxd: cmd: 1 \n" +
		"rstyxd: can't load /bin/1.wasm: file does not exist\n" +
		"rstyxd: cmd: echo hello remote \n" +
		"rstyxd: cmd: cat /n/client/lib/hello.txt \n" +
		"rstyxd: cmd: sh \n" +
		"rstyxd: cmd: nonesuch \n" +
		"rstyxd: can't load /bin/nonesuch.wasm: file does not exist\n" +
		"rstyxd: cmd: auxi/cpuslave -- echo a b c \n" +
		"rstyxd: cmd: auxi/cpuslave -- cat /n/client/dev/user \n" +
		"rstyxd: cmd: auxi/cpuslave -- nonesuch \n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
