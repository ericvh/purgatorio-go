package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/styx"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

// These commands work in a scratch tree under root/tmp, which the emu
// sees as /tmp/cmds2d.
func dScratch() string { return filepath.Join(root, "tmp", "cmds2d") }

// dReset builds the scratch tree afresh: n holds the numbers 1 to 5, one
// per line, and d/x and d/e/y make a small tree to copy.
func dReset(t *testing.T) {
	t.Helper()
	dir := dScratch()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"n":     "1\n2\n3\n4\n5\n",
		"a":     "hello\n",
		"b":     "",
		"words": "one\ntwo\nthree\nfour\nfive\nsix\nseven\n",
		"d/x":   "xx\n",
		"d/e/y": "yy\n",
		"t/k":   "",
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
}

func dRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dScratch(), name))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// emuD is emu with the scratch tree rebuilt before each wazero mode. check,
// if set, looks at the tree after each mode.
func emuD(t *testing.T, check func(t *testing.T), stdin string, args ...string) (string, string) {
	t.Helper()
	return dRun(t, check, stdin, nil, args)
}

// dRun boots a system for each wazero mode and runs one command through
// emuinit; or, given bg, starts bg in the background, waits for the file
// named by its last argument to appear, and runs each step in turn in a
// prog of its own. It returns the console output and the last status.
func dRun(t *testing.T, check func(t *testing.T), stdin string, bg []string, steps ...[]string) (string, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	var outs, sts []string
	for _, m := range []wazero.Mode{wazero.Compiler, wazero.Interpreter} {
		dReset(t)
		var out bytes.Buffer
		h := &host.Host{In: strings.NewReader(stdin), Out: &out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		var st string
		if bg != nil {
			fds := [][2]int{{0, 0}, {1, 1}, {2, 2}}
			pid, err := sys.Machine.Spawn(sys.Init, "/bin/"+bg[0]+".wasm", bg, abi.NEWFD, fds)
			if err != nil {
				t.Fatal(err)
			}
			path := bg[len(bg)-1]
			for i := 0; ; i++ {
				if _, err := sys.Init.Stat(path); err == nil {
					break
				}
				if i == 500 {
					t.Fatalf("%s did not appear", path)
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, args := range steps {
				child, err := sys.Init.Spawn(abi.NEWFD, fds)
				if err != nil {
					t.Fatal(err)
				}
				st, err = sys.Machine.Run(child, "/bin/"+args[0]+".wasm", args)
				if err != nil {
					t.Fatalf("%s: %v", m, err)
				}
				child.Exit(st)
			}
			sys.Kernel.Progs.Kill(pid)
		} else {
			st, err = sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", append([]string{"emuinit"}, steps[0]...))
			if err != nil {
				t.Fatalf("%s: %v", m, err)
			}
		}
		sys.Init.Exit(st)
		if check != nil {
			check(t)
		}
		outs, sts = append(outs, out.String()), append(sts, st)
	}
	if outs[0] != outs[1] || sts[0] != sts[1] {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

type dCase struct {
	name, stdin string
	args        []string
	out, status string
	check       func(t *testing.T)
}

func dRunCases(t *testing.T, cases []dCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emuD(t, c.check, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

func dWant(name, want string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		if got := dRead(t, name); got != want {
			t.Errorf("%s holds %q, want %q", name, got, want)
		}
	}
}

func TestCmds2DXargs(t *testing.T) {
	dRunCases(t, []dCase{
		{"usage", "", []string{"xargs"},
			"Usage: xargs command [command args] <[list of last command arg]\n", "", nil},
		{"echo", "x y\n\n  z\t\nw", []string{"xargs", "echo", "a"}, "a x y\na z\na w\n", "", nil},
		{"no args", "p q\n", []string{"xargs", "echo"}, "p q\n", "", nil},
		{"missing", "x\n", []string{"xargs", "nosuch"},
			"xargs: can't load /bin/nosuch.wasm: file does not exist\n", "", nil},
		{"fails", "/nonexistent\n/lib/hello.txt\n", []string{"xargs", "cat"},
			"cat: cannot open /nonexistent: file does not exist\n", "fail:bad open", nil},
	})
}

func TestCmds2DP(t *testing.T) {
	n := "/tmp/cmds2d/n"
	dRunCases(t, []dCase{
		{"pages", "\n\n", []string{"p", "-2", n}, "1\n2\n3\n4\n5\n", "", nil},
		{"quit", "q\n", []string{"p", "-2", n}, "1\n2\n", "", nil},
		{"eof quits", "", []string{"p", "-3", n}, "1\n2\n3\n", "", nil},
		{"default", "", []string{"p", n}, "1\n2\n3\n4\n5\n", "", nil},
		{"usage", "", []string{"p", "-x", n}, "Usage: p [-number] [file...]\n", "fail:usage", nil},
		{"zero", "", []string{"p", "-0", n}, "Usage: p [-number] [file...]\n", "fail:usage", nil},
		{"missing", "", []string{"p", "/tmp/cmds2d/nosuch", n},
			"p: can't open /tmp/cmds2d/nosuch: file does not exist\n1\n2\n3\n4\n5\n", "", nil},
	})
}

func TestCmds2DStream(t *testing.T) {
	usage := "Usage: stream [-a] [-b bufsize] file1 [file2]\n"
	dRunCases(t, []dCase{
		{"file", "", []string{"stream", "/lib/hello.txt"}, "hello from the root\n", "", nil},
		{"bufsize", "", []string{"stream", "-b", "3", "/lib/hello.txt"}, "hello from the root\n", "", nil},
		{"bufsize joined", "", []string{"stream", "-b3", "/lib/hello.txt"}, "hello from the root\n", "", nil},
		{"usage", "", []string{"stream"}, usage, "fail:usage", nil},
		{"bad option", "", []string{"stream", "-x", "/lib/hello.txt"}, usage, "fail:usage", nil},
		{"missing b", "", []string{"stream", "-b"}, usage, "fail:usage", nil},
		{"missing", "", []string{"stream", "/nonexistent"},
			"stream: can't open /nonexistent: file does not exist\n", "fail:open", nil},
		{"two files", "", []string{"stream", "/tmp/cmds2d/a", "/tmp/cmds2d/b"}, "", "",
			func(t *testing.T) { dWant("b", "hello\n")(t); dWant("a", "hello\n")(t) }},
	})
}

func TestCmds2DChgrp(t *testing.T) {
	dRunCases(t, []dCase{
		{"usage", "", []string{"chgrp"}, "usage: chgrp [-uo] group file ...\n", "fail:usage", nil},
		{"bad option", "", []string{"chgrp", "-x", "g", "f"},
			"usage: chgrp [-uo] group file ...\n", "fail:usage", nil},
		{"no files", "", []string{"chgrp", "g"}, "", "", nil},
		{"missing", "", []string{"chgrp", "-u", "u", "/tmp/cmds2d/nosuch", "/tmp/cmds2d/nosuch2"},
			"chgrp: can't change /tmp/cmds2d/nosuch: file does not exist\n" +
				"chgrp: can't change /tmp/cmds2d/nosuch2: file does not exist\n", "fail:error", nil},
	})
}

func TestCmds2DMc(t *testing.T) {
	dRunCases(t, []dCase{
		{"columns", "", []string{"mc", "/tmp/cmds2d/words"},
			"one   two   three four  five  six   seven\n", "", nil},
		{"narrow", "", []string{"mc", "-c", "13", "/tmp/cmds2d/words"},
			"one   five\ntwo   six\nthree seven\nfour\n", "", nil},
		{"one column", "", []string{"mc", "-c1", "/tmp/cmds2d/n"}, "1\n2\n3\n4\n5\n", "", nil},
		{"stdin", "a\nbb\nc\n", []string{"mc"}, "a  bb c\n", "", nil},
		{"two files", "", []string{"mc", "/tmp/cmds2d/a", "/tmp/cmds2d/d/x"}, "hello xx\n", "", nil},
		{"missing", "", []string{"mc", "/tmp/cmds2d/nosuch", "/tmp/cmds2d/a"},
			"mc: can't open /tmp/cmds2d/nosuch: file does not exist\nhello\n", "", nil},
		{"usage", "", []string{"mc", "-z"}, "usage: mc [-c columns] [file ...]\n", "fail:usage", nil},
		{"no arg", "", []string{"mc", "-c"}, "usage: mc [-c columns] [file ...]\n", "fail:usage", nil},
	})
}

func TestCmds2DFcp(t *testing.T) {
	s := "/tmp/cmds2d/"
	usage := "usage: \tfcp [-r] [-R nproc] [-W nproc] src target\n\tfcp [-r] [-R nproc] [-W nproc] src ... directory\n"
	dRunCases(t, []dCase{
		{"file", "", []string{"fcp", s + "n", s + "m"}, "", "", dWant("m", "1\n2\n3\n4\n5\n")},
		{"serial", "", []string{"fcp", "-R", "1", "-W1", s + "n", s + "m"}, "", "", dWant("m", "1\n2\n3\n4\n5\n")},
		{"into dir", "", []string{"fcp", s + "n", s + "a", s + "d"}, "", "",
			func(t *testing.T) { dWant("d/n", "1\n2\n3\n4\n5\n")(t); dWant("d/a", "hello\n")(t) }},
		{"not a dir", "", []string{"fcp", s + "n", s + "a", s + "b"},
			"fcp: " + s + "b  not a directory\n", "fail:error", nil},
		{"same", "", []string{"fcp", s + "a", s + "a"},
			"fcp: " + s + "a and " + s + "a are the same file\n", "fail:error", nil},
		{"directory", "", []string{"fcp", s + "d", s + "z"},
			"fcp: " + s + "d is a directory\n", "fail:error", nil},
		{"missing", "", []string{"fcp", s + "nosuch", s + "z"},
			"fcp: " + s + "nosuch: file does not exist\n", "fail:error", nil},
		{"recursive", "", []string{"fcp", "-r", s + "d", s + "c"}, "", "",
			func(t *testing.T) { dWant("c/x", "xx\n")(t); dWant("c/e/y", "yy\n")(t) }},
		{"recursive into", "", []string{"fcp", "-r", s + "d", s + "a", s + "t"}, "", "",
			func(t *testing.T) { dWant("t/d/e/y", "yy\n")(t); dWant("t/a", "hello\n")(t) }},
		{"into itself", "", []string{"fcp", "-r", s + "d", s + "d"},
			"fcp: cannot copy " + s + "d into itself\n", "fail:error", nil},
		{"onto file", "", []string{"fcp", "-r", s + "d", s + "a"},
			"fcp: " + s + "a: destination not a directory\n", "fail:error", nil},
		{"usage", "", []string{"fcp", s + "a"}, usage, "fail:usage", nil},
		{"bad R", "", []string{"fcp", "-R", "0", s + "a", s + "z"}, usage, "fail:usage", nil},
	})
}

func TestCmds2DLogfile(t *testing.T) {
	usage := "usage: logfile [-size] file\n"
	dRunCases(t, []dCase{
		{"usage", "", []string{"logfile"}, usage, "fail: usage", nil},
		{"two", "", []string{"logfile", "a", "b"}, usage, "fail: usage", nil},
		{"zero", "", []string{"logfile", "-0", "f"}, "logfile: can't have a zero buffer size\n" + usage, "fail: usage", nil},
		{"bind", "", []string{"logfile", "/nonexistent/log"},
			"logfile: bind #s failed: file does not exist\n", "", nil},
	})
	log := "/tmp/cmds2d/log"
	t.Run("keeps the last bytes", func(t *testing.T) {
		out, st := dRun(t, nil, "q\n", []string{"logfile", "-8", log},
			[]string{"fcp", "-R1", "-W1", "/tmp/cmds2d/a", log},
			[]string{"fcp", "-R1", "-W1", "/tmp/cmds2d/d/x", log},
			[]string{"p", "-2", log})
		if want := "ello\nxx\n"; out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	t.Run("too long", func(t *testing.T) {
		out, st := dRun(t, nil, "", []string{"logfile", "-4", log},
			[]string{"fcp", "-R1", "-W1", "/tmp/cmds2d/a", log})
		if want := "fcp: error writing " + log + ": write too long for buffer\n"; out != want || st != "fail:error" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
}

// dStyxServer listens on a local TCP port and, for each of two
// connections in turn (one per wazero mode), reads
// n T-messages, then answers each: Rversion, Rattach, Rread of "hi" for
// Tread, and Rerror for the rest. It returns the port.
func dStyxServer(t *testing.T, n int) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer l.Close()
		for range 2 {
			c, err := l.Accept()
			if err != nil {
				return
			}
			dServe(c, n)
		}
	}()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func dServe(c net.Conn, n int) {
	defer c.Close()
	var ts []*styx.Fcall
	for range n {
		var sz [4]byte
		if _, err := io.ReadFull(c, sz[:]); err != nil {
			return
		}
		b := make([]byte, binary.LittleEndian.Uint32(sz[:]))
		copy(b, sz[:])
		if _, err := io.ReadFull(c, b[4:]); err != nil {
			return
		}
		f, err := styx.Unmarshal(b, 0)
		if err != nil {
			return
		}
		ts = append(ts, f)
	}
	for _, f := range ts {
		r := &styx.Fcall{Tag: f.Tag}
		switch f.Type {
		case styx.Tversion:
			r.Type, r.Msize, r.Version = styx.Rversion, f.Msize, f.Version
		case styx.Tattach:
			r.Type, r.Qid = styx.Rattach, styx.Qid{Type: styx.QTDIR, Path: 0x10}
		case styx.Tread:
			r.Type, r.Data = styx.Rread, []byte("hi\x01")
		default:
			r.Type, r.Ename = styx.Rerror, "permission denied"
		}
		b, _ := styx.Marshal(nil, r)
		c.Write(b)
	}
}

func TestCmds2DStyxchat(t *testing.T) {
	usage := "usage: styxchat [-nsv] [-m messagesize] [dest]\n"
	dRunCases(t, []dCase{
		{"usage", "", []string{"styxchat", "-x"}, usage, "fail:usage", nil},
		{"two dests", "", []string{"styxchat", "a", "b"}, usage, "fail:usage", nil},
		{"missing", "", []string{"styxchat", "/nonexistent"},
			"styxchat: can't open /nonexistent: file does not exist\n", "fail:error", nil},
	})
	t.Run("client", func(t *testing.T) {
		in := "# a comment\n" +
			"Tversion 8192 9P2000\n" +
			"Tbogus\n" +
			"Tattach 1 nofid me ''\n" +
			"Tclunk\n" +
			"nexttag\n" +
			"Twalk 1 2 a 'b c'\n" +
			"Twrite 2 0 hello\n" +
			"Tread 2 0 16r10\n" +
			"\n"
		port := dStyxServer(t, 5)
		out, st := emu(t, in, "styxchat", "-v", "-n", "tcp!127.0.0.1!"+port)
		want := "-> Tmsg.Version(65535,8192,\"9P2000\")\n" +
			"?unknown message type\n" +
			"-> Tmsg.Attach(1,1,4294967295,\"me\",\"\")\n" +
			"?usage: Tclunk fid\n" +
			"?next tag is 2\n" +
			"-> Tmsg.Walk(2,1,2,array[] of {\"a\",\"b c\"})\n" +
			"-> Tmsg.Write(3,2,0,array[5] of byte)\n" +
			"\ttext(\"hello\")\n" +
			"-> Tmsg.Read(4,2,0,16)\n" +
			"<- Rmsg.Version(65535,8192,\"9P2000\")\n" +
			"<- Rmsg.Attach(1,Qid(16r10,0,16r80))\n" +
			"<- Rmsg.Error(2,\"permission denied\")\n" +
			"<- Rmsg.Error(3,\"permission denied\")\n" +
			"<- Rmsg.Read(4,array[3] of byte)\n" +
			"\tdump(686901)\n" +
			"styxchat: server hungup\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
	t.Run("bad reply", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for range 2 {
				c, err := l.Accept()
				if err != nil {
					return
				}
				var sz [4]byte
				io.ReadFull(c, sz[:])
				io.ReadFull(c, make([]byte, binary.LittleEndian.Uint32(sz[:])-4))
				c.Write([]byte{3, 0, 0, 0})
				defer c.Close()
			}
		}()
		port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
		out, st := emu(t, "Tversion 8192 9P2000\n", "styxchat", "-n", "tcp!127.0.0.1!"+port)
		want := "-> Tmsg.Version(65535,8192,\"9P2000\")\n" +
			"<- Rmsg.Readerror(0,\"invalid 9P message size\")\n"
		if out != want || st != "killed" {
			t.Errorf("got %q status %q, want %q status killed", out, st, want)
		}
	})
	t.Run("server", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
		l.Close()
		in := "Rversion 65535 8192 9P2000\n" +
			"Rattach 1 16r20.3.d\n" +
			"Rwalk 2\n" +
			"Rstat 3 1.0.a 8r644 0 10 5 f u g m\n" +
			"Rbogus\n" +
			"Rwstat 4\n"
		// The client reads the four replies, then sends three requests
		// and hangs up; once for each wazero mode. A connection that
		// fails (it can reach the first run's listener as it closes) is
		// tried again.
		session := func() bool {
			c, err := net.Dial("tcp", "127.0.0.1:"+port)
			if err != nil {
				return false
			}
			defer c.Close()
			for range 4 {
				var sz [4]byte
				if _, err := io.ReadFull(c, sz[:]); err != nil {
					return false
				}
				b := make([]byte, binary.LittleEndian.Uint32(sz[:])-4)
				if _, err := io.ReadFull(c, b); err != nil {
					return false
				}
			}
			for _, f := range []*styx.Fcall{
				{Type: styx.Tversion, Tag: styx.NOTAG, Msize: 8192, Version: "9P2000"},
				{Type: styx.Topen, Tag: 1, Fid: 7, Mode: 1},
				{Type: styx.Tcreate, Tag: 2, Fid: 7, Name: "new", Perm: 0o755, Mode: 2},
			} {
				b, _ := styx.Marshal(nil, f)
				c.Write(b)
			}
			return true
		}
		done := make(chan bool, 1)
		go func() {
			for range 2 {
				for i := 0; !session(); i++ {
					if i == 1000 {
						done <- false
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			done <- true
		}()
		out, st := emu(t, in, "styxchat", "-s", "-n", "tcp!127.0.0.1!"+port)
		if !<-done {
			t.Fatal("client could not finish")
		}
		want := "-> Rmsg.Version(65535,8192,\"9P2000\")\n" +
			"-> Rmsg.Attach(1,Qid(16r20,3,16r80))\n" +
			"-> Rmsg.Walk(2,array[] of {})\n" +
			"-> Rmsg.Stat(3,Dir(\"f\",\"u\",\"g\",Qid(16r1,0,16r40),8r10000000644,0,10,5,16r0,0))\n" +
			"?unknown message type\n" +
			"?usage: Rwstat tag\n" +
			"<- Tmsg.Version(65535,8192,\"9P2000\")\n" +
			"<- Tmsg.Open(1,7,1)\n" +
			"<- Tmsg.Create(2,7,\"new\",8r755,2)\n" +
			"styxchat: clients hungup\n"
		if out != want || st != "" {
			t.Errorf("got %q status %q, want %q", out, st, want)
		}
	})
}
