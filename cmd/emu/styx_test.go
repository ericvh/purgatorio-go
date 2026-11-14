package main

import (
	"os"
	"testing"
)

// TestStyx runs the Styx and network commands from a test guest that
// spawns each and checks what it did.
func TestStyx(t *testing.T) {
	if err := os.MkdirAll(root+"/tmp", 0o777); err != nil {
		t.Fatal(err)
	}
	out, st := emu(t, "", "/bin/test/styx.wasm")
	want := `memfs read "hello memfs" "0123ab6789"
memfs dir [d], f length 10 gid memfs
cs tcp!localhost!styx -> /net/tcp/clone 127.0.0.1!6666
cs net!10.0.0.1!9fs -> ["/net/tcp/clone 10.0.0.1!564"]
cs tcp!*!styx -> ["/net/tcp/clone 6666"]
cs bogus!a!b -> [] error network unavailable bogus
cs tcp!x -> [] error bad format request
cs !tcp=styx -> ["tcp=styx port=6666"]
mount without -A: fail:error
export/mount pipe: hello from the root
styxlisten without -A: fail:error
styxlisten client 0: hello from the root
styxlisten client 1: hello from the root
mntgen made anything
listen echo 0: ping
listen echo 1: ping
dial without -A: fail:bad keyfile
dial sent "hello from dial\n", status ""
netstat: 3 announced
`
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}

// TestStyxUsage checks the commands' usage messages and exit statuses.
func TestStyxUsage(t *testing.T) {
	cases := []struct {
		args        []string
		out, status string
	}{
		{[]string{"mount"}, "usage: mount [-a|-b] [-coA9] [-C cryptoalg] [-k keyfile] [-q] net!addr|file|{command} mountpoint [spec]\n", "fail:usage"},
		{[]string{"export"}, "Usage: export [-a] dir [connection]\n", "fail:usage"},
		{[]string{"memfs", "-x"}, "usage: memfs [-s] [-rab] [-m size] [mountpoint]\n", "fail:usage"},
		{[]string{"mount", "-A", "/nonexistent", "/n"}, "mount: can't open /nonexistent: file does not exist\n", "fail:open failed"},
		{[]string{"mount", "-A", "-o", "-9", "x", "/n"}, "mount: cannot combine -o and -9 options\n", "fail:usage"},
		{[]string{"export", "/lib", "/nonexistent"}, "export: can't open /nonexistent: file does not exist\n", "fail:open"},
		{[]string{"import", "tcp!127.0.0.1!1", "/"}, "import: can't open /mnt/factotum/rpc: file does not exist\n", "fail:factotum"},
		{[]string{"cs", "extra"}, "usage: cs [-v] [-x mntpt] [-f database] [-d dialmod]\n", "fail:usage"},
	}
	for _, c := range cases {
		t.Run(c.args[0], func(t *testing.T) {
			out, st := emu(t, "", c.args...)
			if out != c.out || st != c.status {
				t.Errorf("%q: got %q status %q, want %q status %q", c.args, out, st, c.out, c.status)
			}
		})
	}
}
