package main

import (
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"hash/crc32"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
)

// The file utilities work in a scratch tree under root/tmp, which the emu
// sees as /tmp/files.
const scratch = "/tmp/files"

func scratchDir() string { return filepath.Join(root, "tmp", "files") }

// Fixed times, so ls -l and du -t print the same text on every run.
const (
	tA = 1000000   // Jan 12 1970
	tB = 2000000   // Jan 24 1970
	tD = 3000000   // Feb 04 1970
	tX = 500000000 // Nov 05 1985
)

// resetScratch builds the scratch tree afresh:
//
//	a    "hello\n"
//	b    "world\nhello\n"
//	n    the numbers 1 to 20, one per line
//	d/x  "xx\n"
func resetScratch(t *testing.T) {
	t.Helper()
	dir := scratchDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	var nums strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&nums, "%d\n", i)
	}
	files := []struct {
		name, data string
		mtime      int64
	}{
		{"a", "hello\n", tA},
		{"b", "world\nhello\n", tB},
		{"n", nums.String(), tB},
		{"d/x", "xx\n", tX},
	}
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte(f.data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(f.mtime, 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	dt := time.Unix(tD, 0)
	if err := os.Chtimes(filepath.Join(dir, "d"), dt, dt); err != nil {
		t.Fatal(err)
	}
}

func useScratch(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetScratch(t)
	t.Cleanup(func() { os.RemoveAll(scratchDir()) })
}

// emuFS is emu, but rebuilds the scratch tree, plus extra files, before
// each wazero mode, so commands that change files see the same tree both
// times.
func emuFS(t *testing.T, extra map[string]string, stdin string, args ...string) (string, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	var outs, sts []string
	for _, m := range testModes() {
		resetScratch(t)
		for name, data := range extra {
			if err := os.WriteFile(filepath.Join(scratchDir(), name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var out lockedBuffer
		h := &host.Host{In: strings.NewReader(stdin), Out: &out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		st, err := sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", append([]string{"emuinit"}, args...))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		sys.Init.Exit(st)
		outs, sts = append(outs, out.String()), append(sts, st)
	}
	if len(outs) > 1 && (outs[0] != outs[1] || sts[0] != sts[1]) {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

type fileCase struct {
	name, stdin string
	args        []string
	out, status string
	// check, if set, inspects the scratch tree afterwards.
	check func(t *testing.T, dir string)
	// files are added to the scratch tree first.
	files map[string]string
}

func runFileCases(t *testing.T, cases []fileCase) {
	t.Helper()
	useScratch(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emuFS(t, c.files, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("%v:\ngot  %q status %q\nwant %q status %q", c.args, out, st, c.out, c.status)
			}
			if c.check != nil {
				c.check(t, scratchDir())
			}
		})
	}
}

func s(p string) string { return scratch + "/" + p }

func args(a ...string) []string { return a }

func hasFile(name, data string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		if string(b) != data {
			t.Errorf("%s holds %q, want %q", name, b, data)
		}
	}
}

func noFile(name string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s still exists", name)
		}
	}
}

func hasMode(name string, mode os.FileMode) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("%s has mode %o, want %o", name, fi.Mode().Perm(), mode)
		}
	}
}

func isDir(name string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !fi.IsDir() {
			t.Errorf("%s is not a directory (%v)", name, err)
		}
	}
}

func all(fs ...func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		for _, f := range fs {
			f(t, dir)
		}
	}
}

// owner returns the user and group names #U reports for a host file.
func owner(t *testing.T, path string) (string, string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	u := "#" + strconv.FormatUint(uint64(st.Uid), 10)
	if x, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
		u = x.Username
	}
	g := "#" + strconv.FormatUint(uint64(st.Gid), 10)
	if x, err := user.LookupGroupId(strconv.FormatUint(uint64(st.Gid), 10)); err == nil {
		g = x.Name
	}
	return u, g
}

func TestLs(t *testing.T) {
	useScratch(t)
	u, g := owner(t, filepath.Join(scratchDir(), "a"))
	long := func(mode, size, date, name string) string {
		return fmt.Sprintf("%s U 0 %s %s %s %s %s\n", mode, u, g, size, date, name)
	}
	runFileCases(t, []fileCase{
		{"dir", "", args("ls", scratch), s("a") + "\n" + s("b") + "\n" + s("d") + "\n" + s("n") + "\n", "", nil, nil},
		{"bare", "", args("ls", "-p", scratch), "a\nb\nd\nn\n", "", nil, nil},
		{"dot", "", args("ls", "-d", scratch), scratch + "\n", "", nil, nil},
		{"files sorted together", "", args("ls", s("n"), s("a"), s("d/x")),
			s("a") + "\n" + s("d/x") + "\n" + s("n") + "\n", "", nil, nil},
		{"reverse", "", args("ls", "-r", scratch), s("n") + "\n" + s("d") + "\n" + s("b") + "\n" + s("a") + "\n", "", nil, nil},
		{"by time", "", args("ls", "-t", scratch), s("d") + "\n" + s("b") + "\n" + s("n") + "\n" + s("a") + "\n", "", nil, nil},
		{"by size", "", args("ls", "-s", s("n"), s("a"), s("b")), s("a") + "\n" + s("b") + "\n" + s("n") + "\n", "", nil, nil},
		{"unsorted args", "", args("ls", "-n", s("n"), s("a")), s("n") + "\n" + s("a") + "\n", "", nil, nil},
		{"flags", "", args("ls", "-F", "-p", scratch), "a\nb\nd/\nn\n", "", nil, nil},
		{"long", "", args("ls", "-l", s("a")), long("--rw-r--r--", "6", "Jan 12  1970", s("a")), "", nil, nil},
		{"long dir", "", args("ls", "-ld", s("d")), long("d-rwxr-xr-x", "0", "Feb 04  1970", s("d")), "", nil, nil},
		{"long widths", "", args("ls", "-l", s("a"), s("n")),
			long("--rw-r--r--", " 6", "Jan 12  1970", s("a")) + long("--rw-r--r--", "51", "Jan 24  1970", s("n")), "", nil, nil},
		{"epoch", "", args("ls", "-le", s("a")), long("--rw-r--r--", "6", "1000000", s("a")), "", nil, nil},
		{"kbytes", "", args("ls", "-k", s("a")), "1 " + s("a") + "\n", "", nil, nil},
		{"muid", "", args("ls", "-m", s("a")), "[] " + s("a") + "\n", "", nil, nil},
		{"temp", "", args("ls", "-T", s("a")), "- " + s("a") + "\n", "", nil, nil},
		{"missing", "", args("ls", s("nope"), s("a")),
			"ls: " + s("nope") + ": file does not exist\n" + s("a") + "\n", "fail:errors", nil, nil},
		{"usage", "", args("ls", "-Z"), "usage: ls [-delmnpqrstucFT] [files]\n", "fail:usage", nil, nil},
	})
}

func TestLsQid(t *testing.T) {
	useScratch(t)
	out, st := emu(t, "", "ls", "-q", s("a"))
	// (path version type): 16 hex digits, the version, 2 hex digits.
	if st != "" || len(out) < 22 || out[0] != '(' || !strings.HasSuffix(out, " 00) "+s("a")+"\n") {
		t.Errorf("got %q status %q", out, st)
	}
}

func TestPwd(t *testing.T) {
	out, st := emu(t, "", "pwd")
	if out != "/\n" || st != "" {
		t.Errorf("got %q status %q", out, st)
	}
}

func TestMkdirRm(t *testing.T) {
	runFileCases(t, []fileCase{
		{"mkdir", "", args("mkdir", s("e"), s("f")), "", "", all(isDir("e"), isDir("f")), nil},
		{"mkdir exists", "", args("mkdir", s("d")), "mkdir: " + s("d") + " already exists\n", "fail:error", nil, nil},
		{"mkdir -p", "", args("mkdir", "-p", s("e/f/g"), s("d")), "", "", isDir("e/f/g"), nil},
		{"mkdir -p file", "", args("mkdir", "-p", s("a/b")),
			"mkdir: can't create " + s("a/b") + ": " + s("a") + " not a directory\n", "fail:error", nil, nil},
		{"mkdir no parent", "", args("mkdir", s("e/f")),
			"mkdir: can't create " + s("e/f") + ": file does not exist\n", "fail:error", nil, nil},
		{"rm", "", args("rm", s("a"), s("b")), "", "", all(noFile("a"), noFile("b")), nil},
		{"rm missing", "", args("rm", s("nope"), s("a")),
			"rm: " + s("nope") + ": file does not exist\n", "fail:errors", noFile("a"), nil},
		{"rm -f", "", args("rm", "-f", s("nope")), "", "", nil, nil},
		{"rm -r", "", args("rm", "-r", s("d")), "", "", noFile("d"), nil},
		{"rm usage", "", args("rm", "-x"), "Usage: rm [-fr] file ...\n", "fail: usage", nil, nil},
	})
}

func TestCpMv(t *testing.T) {
	runFileCases(t, []fileCase{
		{"cp", "", args("cp", s("a"), s("c")), "", "", all(hasFile("c", "hello\n"), hasMode("c", 0o644)), nil},
		{"cp over", "", args("cp", s("b"), s("a")), "", "", hasFile("a", "world\nhello\n"), nil},
		{"cp into dir", "", args("cp", s("a"), s("b"), s("d")), "", "",
			all(hasFile("d/a", "hello\n"), hasFile("d/b", "world\nhello\n")), nil},
		{"cp not dir", "", args("cp", s("a"), s("b"), s("n")), "cp: " + s("n") + "  not a directory\n", "fail:error", nil, nil},
		{"cp missing", "", args("cp", s("nope"), s("c")), "cp: " + s("nope") + ": file does not exist\n", "fail:error", nil, nil},
		{"cp dir", "", args("cp", s("d"), s("e")), "cp: " + s("d") + " is a directory\n", "fail:error", nil, nil},
		{"cp same", "", args("cp", s("a"), s("a")),
			"cp: " + s("a") + " and " + s("a") + " are the same file\n", "fail:error", nil, nil},
		{"cp -r", "", args("cp", "-r", s("d"), s("e")), "", "", hasFile("e/x", "xx\n"), nil},
		{"cp -r into", "", args("cp", "-r", s("a"), s("d"), s("n")), "cp: " + s("n") + "  not a directory\n", "fail:error", nil, nil},
		{"cp -r file", "", args("cp", "-r", s("a"), s("b"), s("d")), "", "",
			all(hasFile("d/a", "hello\n"), hasFile("d/b", "world\nhello\n")), nil},
		{"cp -r into itself", "", args("cp", "-r", s("d"), s("d")),
			"cp: cannot copy " + s("d") + " into itself\n", "fail:error", nil, nil},
		{"cp usage", "", args("cp", s("a")),
			"usage: \tcp [-gux] src target\n\tcp [-r] [-gux] src ... directory\n", "fail:usage", nil, nil},
		{"mv rename", "", args("mv", s("a"), s("c")), "", "", all(noFile("a"), hasFile("c", "hello\n")), nil},
		{"mv replace", "", args("mv", s("a"), s("b")), "", "", all(noFile("a"), hasFile("b", "hello\n")), nil},
		{"mv into dir", "", args("mv", s("a"), s("b"), s("d")), "", "",
			all(noFile("a"), noFile("b"), hasFile("d/a", "hello\n"), hasFile("d/b", "world\nhello\n")), nil},
		{"mv across", "", args("mv", s("a"), s("d/y")), "", "", all(noFile("a"), hasFile("d/y", "hello\n")), nil},
		{"mv dir rename", "", args("mv", s("d"), s("e")), "", "", all(noFile("d"), hasFile("e/x", "xx\n")), nil},
		{"mv dir across", "", args("mv", s("d"), s("e")+"/../d/z"),
			"mv: " + s("d") + " is a directory, not copied to " + s("e") + "/../d/z\n", "fail:error", nil, nil},
		{"mv missing", "", args("mv", s("nope"), s("c")),
			"mv: can't stat " + s("nope") + ": file does not exist\n", "fail:error", nil, nil},
		{"mv same", "", args("mv", s("a"), s("a")),
			"mv: " + s("a") + " and " + s("a") + " are the same\n", "fail:error", nil, nil},
		{"mv not dir", "", args("mv", s("a"), s("b"), s("n")), "mv: " + s("n") + " not a directory\n", "fail:error", nil, nil},
		{"mv usage", "", args("mv", s("a")),
			"usage: mv fromfile tofile\n       mv fromfile ... todir\n", "fail:usage", nil, nil},
	})
}

func TestTouchChmodTee(t *testing.T) {
	mtime := func(name string, want int64) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			t.Helper()
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Errorf("%s: %v", name, err)
			} else if fi.ModTime().Unix() != want {
				t.Errorf("%s mtime %d, want %d", name, fi.ModTime().Unix(), want)
			}
		}
	}
	recent := func(name string) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			t.Helper()
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil || time.Since(fi.ModTime()) > time.Hour {
				t.Errorf("%s not touched (%v)", name, err)
			}
		}
	}
	runFileCases(t, []fileCase{
		{"touch -t", "", args("touch", "-t", "1234567", s("a")), "", "", mtime("a", 1234567), nil},
		{"touch now", "", args("touch", s("a")), "", "", recent("a"), nil},
		{"touch create", "", args("touch", s("new")), "", "", all(hasFile("new", ""), recent("new")), nil},
		{"touch -c", "", args("touch", "-c", s("new")),
			"touch: " + s("new") + ": cannot change time: file does not exist\n", "fail:touch", noFile("new"), nil},
		{"touch no dir", "", args("touch", s("e/new")),
			"touch: " + s("e/new") + ": cannot create: file does not exist\n", "fail:touch", nil, nil},
		{"touch usage", "", args("touch"), "usage: touch [-c] [-t time] file ...\n", "fail:usage", nil, nil},
		{"chmod octal", "", args("chmod", "600", s("a"), s("b")), "", "", all(hasMode("a", 0o600), hasMode("b", 0o600)), nil},
		{"chmod 8r", "", args("chmod", "8r640", s("a")), "", "", hasMode("a", 0o640), nil},
		{"chmod +", "", args("chmod", "go+w", s("a")), "", "", hasMode("a", 0o666), nil},
		{"chmod -", "", args("chmod", "-w", s("a")), "", "", hasMode("a", 0o444), nil},
		{"chmod =", "", args("chmod", "u=rwx", s("a")), "", "", hasMode("a", 0o744), nil},
		{"chmod missing", "", args("chmod", "600", s("nope"), s("a")),
			"chmod: cannot stat " + s("nope") + ": file does not exist\n", "", hasMode("a", 0o600), nil},
		{"chmod bad", "", args("chmod", "q+z", s("a")),
			"chmod: bad mode 'q+z'\nusage: chmod [8r]777 file ... or chmod [augo][+-=][rwxal] file ...\n", "fail:usage", nil, nil},
		{"tee", "one\ntwo\n", args("tee", s("t1"), s("t2")), "one\ntwo\n", "",
			all(hasFile("t1", "one\ntwo\n"), hasFile("t2", "one\ntwo\n")), nil},
		{"tee truncates", "x\n", args("tee", s("b")), "x\n", "", hasFile("b", "x\n"), nil},
		{"tee -a", "more\n", args("tee", "-a", s("a"), s("t")), "more\n", "",
			all(hasFile("a", "hello\nmore\n"), hasFile("t", "more\n")), nil},
		{"tee bad", "x\n", args("tee", s("e/t")), "tee: cannot open " + s("e/t") + ": file does not exist\n", "fail:error", nil, nil},
		{"tee usage", "", args("tee", "-x"), "Usage: tee [-a] [file ...]\n", "fail:usage", nil, nil},
	})
}

func TestCount(t *testing.T) {
	// The no-inversion CRC that Inferno's Crc module computes.
	crc := func(c uint32, b []byte) uint32 { return ^crc32.Update(^c, crc32.IEEETable, b) }
	sum := func(data string) string {
		c := crc(0, []byte(data))
		l := uint32(len(data))
		c = crc(c, []byte{byte(l>>24) ^ 0xcc, byte(l>>16) ^ 0x55, byte(l>>8) ^ 0xcc, byte(l) ^ 0x55})
		return fmt.Sprintf("%.8x %6d", c, len(data))
	}
	runFileCases(t, []fileCase{
		{"wc", "", args("wc", s("a")), "      1       1       6 " + s("a") + "\n", "", nil, nil},
		{"wc total", "", args("wc", s("a"), s("b")),
			"      1       1       6 " + s("a") + "\n      2       2      12 " + s("b") + "\n      3       3      18 total\n", "", nil, nil},
		{"wc stdin", "héllo wörld\n", args("wc"), "      1       2      12\n", "", nil, nil},
		{"wc bytes", "héllo wörld\n", args("wc", "-lb"), "      1      14\n", "", nil, nil},
		{"wc bad utf", "a\xffb\xc3\n", args("wc", "-ce"), "      5       2\n", "", nil, nil},
		{"wc missing", "", args("wc", s("nope")), "wc: can't open " + s("nope") + ": file does not exist\n", "", nil, nil},
		{"wc usage", "", args("wc", "-x"), "usage: wc [-lwcbe] [file ...]\n", "fail:usage", nil, nil},
		{"md5sum", "", args("md5sum", s("a")), fmt.Sprintf("%x\t%s\n", md5.Sum([]byte("hello\n")), s("a")), "", nil, nil},
		{"md5sum stdin", "hello\n", args("md5sum"), "b1946ac92492d2347c6235b4d2611184\n", "", nil, nil},
		{"md5sum missing", "", args("md5sum", s("nope")),
			"md5sum: cannot open " + s("nope") + ": file does not exist\n", "fail:error", nil, nil},
		{"sha1sum", "", args("sha1sum", s("a"), s("b")),
			fmt.Sprintf("%x\t%s\n%x\t%s\n", sha1.Sum([]byte("hello\n")), s("a"), sha1.Sum([]byte("world\nhello\n")), s("b")), "", nil, nil},
		{"sha1sum stdin", "hello\n", args("sha1sum"), "f572d396fae9206628714fb2ce00f72e94f2258f\n", "", nil, nil},
		{"sum", "", args("sum", s("a"), s("d"), s("b")),
			sum("hello\n") + " " + s("a") + "\n" + sum("world\nhello\n") + " " + s("b") + "\n", "", nil, nil},
		{"sum missing", "", args("sum", s("nope")),
			"sum: cannot get status of " + s("nope") + ": file does not exist\n", "fail:error", nil, nil},
		{"du", "", args("du", scratch), "1    " + s("d") + "\n1    " + scratch + "\n", "", nil, nil},
		{"du -a", "", args("du", "-a", scratch),
			"1    " + s("a") + "\n1    " + s("b") + "\n1    " + s("d/x") + "\n1    " + s("d") + "\n1    " + s("n") + "\n1    " + scratch + "\n", "", nil, nil},
		{"du -s", "", args("du", "-s", scratch), "1    " + scratch + "\n", "", nil, nil},
		{"du -b", "", args("du", "-s", "-b", "1", scratch), "0    " + scratch + "\n", "", nil, nil},
		{"du -b k", "", args("du", "-s", "-b", "1k", s("a")), "1    " + s("a") + "\n", "", nil, nil},
		{"du -n", "", args("du", "-n", s("d")), s("d/x") + "\n", "", nil, nil},
		{"du -t", "", args("du", "-t", s("d")), "3000000 " + s("d") + "\n", "", nil, nil},
		{"du -nt", "", args("du", "-nt", s("d")), s("d/x") + " 500000000 3 0\n", "", nil, nil},
		{"du missing", "", args("du", s("nope")), "du: can't stat " + s("nope") + ": file does not exist\n", "", nil, nil},
		{"du usage", "", args("du", "-x"), "usage: du [-anstu] [-b bsize] [file ...]\n", "fail:usage", nil, nil},
	})
}

func TestNames(t *testing.T) {
	cases := []struct {
		args        []string
		out, status string
	}{
		{args("basename", "/a/b/c.b"), "c.b\n", ""},
		{args("basename", "/a/b/c.b", ".b"), "c\n", ""},
		{args("basename", "-d", "/a/b/c.b"), "/a/b\n", ""},
		{args("basename", "-d", "c.b"), ".\n", ""},
		{args("basename", "-d", "/c"), "/\n", ""},
		{args("basename"), "usage: basename [-d] string [suffix]\n", "fail:usage"},
		{args("basename", "-d", "a", "b"), "usage: basename [-d] string [suffix]\n", "fail:usage"},
		{args("cleanname", "a//b/./c/..", "/../x", "../../y", "#c/../z", ""), "a/b\n/x\n../../y\n#c/z\n.\n", ""},
		{args("cleanname", "-d", "/usr/inferno", "lib/../dis", "/abs"), "/usr/inferno/dis\n/abs\n", ""},
		{args("cleanname"), "usage: cleanname [-d pwd] name ...\n", "fail:usage"},
		{args("cleanname", "-d"), "usage: cleanname [-d pwd] name ...\n", "fail:usage"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			out, st := emu(t, "", c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	// m is n with line 3 changed, line 11 deleted and 21 added.
	var mb strings.Builder
	for i := 1; i <= 21; i++ {
		switch i {
		case 3:
			mb.WriteString("three\n")
		case 11:
		default:
			fmt.Fprintf(&mb, "%d\n", i)
		}
	}
	m := map[string]string{"m": mb.String()}
	cmpUsage := "Usage: cmp [-lsL] file1 file2 [offset1 [offset2] ]\n"
	runFileCases(t, []fileCase{
		{"cmp same", "", args("cmp", s("a"), s("a")), "", "", nil, nil},
		{"cmp differ", "", args("cmp", s("a"), s("b")), s("a") + " " + s("b") + " differ: char 1\n", "fail:differ", nil, nil},
		{"cmp -L", "", args("cmp", "-L", s("b"), s("n")), s("b") + " " + s("n") + " differ: char 1 line 1\n", "fail:differ", nil, nil},
		{"cmp -s", "", args("cmp", "-s", s("a"), s("b")), "", "fail:differ", nil, nil},
		{"cmp -l", "", args("cmp", "-l", s("a"), s("b")),
			"     1 0x68 0x77\n     2 0x65 0x6f\n     3 0x6c 0x72\n     5 0x6f 0x64\nEOF on " + s("a") + " after 6 bytes\n",
			"fail:EOF", nil, nil},
		{"cmp offsets", "", args("cmp", s("a"), s("b"), "0", "6"), "", "", nil, nil},
		{"cmp offsets 2", "", args("cmp", s("a"), s("b"), "1", "7"), "", "", nil, nil},
		{"cmp one offset", "", args("cmp", s("b"), s("a"), "6"), "", "", nil, nil},
		{"cmp missing", "", args("cmp", s("nope"), s("a")), "cmp: can't open " + s("nope") + ": file does not exist\n", "fail:open", nil, nil},
		{"cmp usage", "", args("cmp", s("a")), cmpUsage, "fail:usage", nil, nil},
		{"comm", "a\nhello\nz\n", args("comm", "-", s("a")), "a\n\t\thello\nz\n", "", nil, nil},
		{"comm second", "a\nb\nc\n", args("comm", s("a"), "-"), "\ta\n\tb\n\tc\nhello\n", "", nil, nil},
		{"comm -12", "a\nhello\nz\n", args("comm", "-12", "-", s("a")), "hello\n", "", nil, nil},
		{"comm -3", "a\nhello\nz\n", args("comm", "-3", "-", s("a")), "a\nz\n", "", nil, nil},
		{"comm -13", "a\nz\n", args("comm", "-13", "-", s("a")), "hello\n", "", nil, nil},
		{"comm -1", "a\nhello\n", args("comm", "-1", "-", s("b")), "world\nhello\n", "", nil, nil},
		{"comm missing", "", args("comm", s("a"), s("nope")), "comm: cannot open " + s("nope") + ": file does not exist\n", "fail:open", nil, nil},
		{"comm usage", "", args("comm", s("a")), "usage: comm [-123] file1 file2\n", "fail:usage", nil, nil},
		{"diff same", "", args("diff", s("a"), s("a")), "", "", nil, nil},
		{"diff", "", args("diff", s("a"), s("b")), "0a1\n> world\n", "fail:some", nil, nil},
		{"diff change", "", args("diff", s("n"), s("m")),
			"3c3\n< 3\n---\n> three\n11d10\n< 11\n20a20\n> 21\n", "fail:some", nil, m},
		{"diff -e", "", args("diff", "-e", s("n"), s("m")), "20a\n21\n.\n11d\n3c\nthree\n.\n", "fail:some", nil, m},
		{"diff -f", "", args("diff", "-f", s("n"), s("m")), "c3\nthree\n.\nd11\na20\n21\n.\n", "fail:some", nil, m},
		{"diff -w", "", args("diff", "-w", s("a"), s("w")), "", "", nil, map[string]string{"w": "he llo\t\n"}},
		{"diff -b", "", args("diff", "-b", s("a"), s("w")), "1c1\n< hello\n---\n> he llo\n", "fail:some", nil,
			map[string]string{"w": "he llo\n"}},
		{"diff binary", "", args("diff", s("a"), s("bin")), "Binary files " + s("a") + " " + s("bin") + " differ\n", "fail:some", nil,
			map[string]string{"bin": "\xff\xfe\x00\x01\n"}},
		{"diff file dir", "", args("diff", s("a"), s("d")), "1c1\n< hello\n---\n> xx\n", "fail:some", nil,
			map[string]string{"d/a": "xx\n"}},
		{"diff file dir missing", "", args("diff", s("d/x"), scratch), "diff: cannot open " + s("x") + ": file does not exist\n", "", nil, nil},
		{"diff dirs", "", args("diff", scratch, s("d")),
			"Only in " + scratch + ": a\nOnly in " + scratch + ": b\nOnly in " + scratch + ": d\nOnly in " + scratch + ": n\nOnly in " + s("d") + ": x\n", "", nil, nil},
		{"diff dirs common", "", args("diff", scratch, s("d")),
			"Only in " + scratch + ": a\ndiff " + s("b") + " " + s("d/b") + "\n1d0\n< world\nOnly in " + s("d") + ": c\n" +
				"Only in " + scratch + ": d\nOnly in " + scratch + ": n\nOnly in " + s("d") + ": x\n", "fail:some", nil,
			map[string]string{"d/b": "hello\n", "d/c": ""}},
		{"diff missing", "", args("diff", s("a"), s("nope")), "diff: can't stat " + s("nope") + ": file does not exist\n", "fail:error", nil, nil},
		{"diff usage", "", args("diff", s("a")), "diff: usage: diff [ -efbwr ] file1 ... file2\n", "fail:error", nil, nil},
	})
}

func TestText(t *testing.T) {
	bin := "\x00\x01hello, world\x02ab\x03printable text\n"
	runFileCases(t, []fileCase{
		{"tail", "", args("tail", "-3", s("n")), "18\n19\n20\n", "", nil, nil},
		{"tail default", "", args("tail", s("n")), "11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n", "", nil, nil},
		{"tail -n", "", args("tail", "-n", "2", s("n")), "19\n20\n", "", nil, nil},
		{"tail +", "", args("tail", "+19", s("n")), "19\n20\n", "", nil, nil},
		{"tail -c", "", args("tail", "-c", "6", s("n")), "19\n20\n", "", nil, nil},
		{"tail +c", "", args("tail", "+48c", s("n")), "20\n", "", nil, nil},
		{"tail -r", "", args("tail", "-r", "-n", "3", s("n")), "20\n19\n18\n", "", nil, nil},
		{"tail stdin", "a\nb\nc\n", args("tail", "-2"), "b\nc\n", "", nil, nil},
		{"tail stdin +", "a\nb\nc\n", args("tail", "+2"), "b\nc\n", "", nil, nil},
		{"tail stdin -r", "a\nb\nc", args("tail", "-r"), "c\nb\na\n", "", nil, nil},
		{"tail missing", "", args("tail", s("nope")), "tail: " + s("nope") + ": file does not exist\n", "", nil, nil},
		{"tail bad", "", args("tail", "-r", "-f", s("n")), "tail: incompatible options\n", "", nil, nil},
		{"sort", "b\na\nc\n10\n9\n", args("sort"), "10\n9\na\nb\nc\n", "", nil, nil},
		{"sort -n", "10\n9\n-1\n100\n", args("sort", "-n"), "-1\n9\n10\n100\n", "", nil, nil},
		{"sort -r", "", args("sort", "-r", s("b")), "world\nhello\n", "", nil, nil},
		{"sort missing", "", args("sort", s("nope")), "sort: cannot open " + s("nope") + ": file does not exist\n", "fail:open file", nil, nil},
		{"sort usage", "", args("sort", s("a"), s("b")), "usage: sort [-n] [file]\n", "fail:usage", nil, nil},
		{"uniq", "a\na\nb\nc\nc\nc\n", args("uniq"), "a\nb\nc\n", "", nil, nil},
		{"uniq -c", "a\na\nb\nc\nc\nc\n", args("uniq", "-c"), "2\ta\n1\tb\n3\tc\n", "", nil, nil},
		{"uniq -u", "a\na\nb\nc\nc\nc\n", args("uniq", "-u"), "b\n", "", nil, nil},
		{"uniq -d", "a\na\nb\nc\nc\nc\n", args("uniq", "-d"), "a\nc\n", "", nil, nil},
		{"uniq missing", "", args("uniq", s("nope")), "uniq: cannot open " + s("nope") + ": file does not exist\n\n", "fail:open file", nil, nil},
		{"uniq usage", "", args("uniq", "-x"), "usage: uniq [-udc] [file]\n", "fail:usage", nil, nil},
		{"tr", "hello world\n", args("tr", "a-z", "A-Z"), "HELLO WORLD\n", "", nil, nil},
		{"tr short to", "hello\n", args("tr", "a-z", "A-C"), "CCCCC\n", "", nil, nil},
		{"tr -d", "hello world\n", args("tr", "-d", "lo"), "he wrd\n", "", nil, nil},
		{"tr -s", "aaa  bbb\n", args("tr", "-s", "ab", "xy"), "x  y\n", "", nil, nil},
		{"tr -ds", "aaxxbb\n", args("tr", "-ds", "x", "ab"), "ab\n", "", nil, nil},
		{"tr -c", "a1b2\n", args("tr", "-c", "a-z", "_"), "a_b__", "", nil, nil},
		{"tr -cd", "a1b2\n", args("tr", "-cd", "a-z"), "ab", "", nil, nil},
		{"tr octal", "a\tb\n", args("tr", "\\011", "_"), "a_b\n", "", nil, nil},
		{"tr hex", "aéb\n", args("tr", "\\xe9", "e"), "aeb\n", "", nil, nil},
		{"tr ambiguous", "x\n", args("tr", "aa", "bc"), "tr: ambiguous translation\n", "fail: error", nil, nil},
		{"tr range", "x\n", args("tr", "z-a", "x"), "tr: Invalid range specification\n", "fail: error", nil, nil},
		{"tr usage", "", args("tr", "a"), "Usage: tr [-sdc] [from-set [to-set]]\n", "fail: usage", nil, nil},
		{"strings", "", args("strings", s("bin")), "       2: hello, world\n      18: printable text\n", "", nil, map[string]string{"bin": bin}},
		{"strings stdin", "short\nlonger line\n", args("strings"), "       6: longer line\n", "", nil, nil},
		{"strings files", "", args("strings", s("a"), s("bin")), s("a") + ":\n" + s("bin") + ":\n       2: hello, world\n      18: printable text\n", "", nil,
			map[string]string{"bin": bin}},
		{"strings missing", "", args("strings", s("nope")), "cannot open " + s("nope") + "\n", "", nil, nil},
		{"zeros", "", args("zeros", "4", "2"), "\x00\x00\x00\x00\x00\x00\x00\x00", "", nil, nil},
		{"zeros -v", "", args("zeros", "-v", "41", "3"), "AAA", "", nil, nil},
		{"zeros usage", "", args("zeros", "0"), "usage: zeros [-r] [-v value] blocksize [number]\n", "fail:usage", nil, nil},
	})
}

func TestXd(t *testing.T) {
	runFileCases(t, []fileCase{
		{"xd", "", args("xd", s("b")), "0000000 776f726c 640a6865 6c6c6f0a\n000000c\n", "", nil, nil},
		{"xd -c", "", args("xd", "-c", s("a")), "0000000 h  e  l  l  o  \\n\n0000006\n", "", nil, nil},
		{"xd -1x -1o", "", args("xd", "-1x", "-1o", s("a")),
			"0000000 68  65  6c  6c  6f  0a\n      0 150 145 154 154 157 012\n0000006\n", "", nil, nil},
		{"xd -2d -ad", "", args("xd", "-2d", "-ad", s("a")), "0000000 26725 27756 28426\n0000006\n", "", nil, nil},
		{"xd -s", "", args("xd", "-s", s("a")), "0000000 6c6c6568 00000a6f\n0000006\n", "", nil, nil},
		{"xd -r", "0123456789abcdef0123456789abcdef0123456789abcdefxyz", args("xd", "-r"),
			"0000000 30313233 34353637 38396162 63646566\n*\n0000030 78797a00\n0000033\n", "", nil, nil},
		{"xd stdin empty", "", args("xd"), "0000000 \n0000000\n", "", nil, nil},
		{"xd files", "", args("xd", s("a"), s("d/x")),
			s("a") + "\n0000000 68656c6c 6f0a0000\n0000006\n" + s("d/x") + "\n0000000 78780a00\n0000003\n", "", nil, nil},
		{"xd missing", "", args("xd", s("nope")), "xd: cannot open " + s("nope") + ": file does not exist\n", "fail:cannot open", nil, nil},
		{"xd usage", "", args("xd", "-q"),
			"usage: xd [-u] [-r] [-s] [-a{odx}] [-c|{b1w2l4v8}{odx}] ... file ...\n", "fail:usage", nil, nil},
	})
}

func TestGrep(t *testing.T) {
	text := "alpha\nBeta\ngamma delta\nepsilon\n"
	runFileCases(t, []fileCase{
		{"grep", text, args("grep", "a$"), "alpha\nBeta\ngamma delta\n", "", nil, nil},
		{"grep anchors", text, args("grep", "^e.*n$"), "epsilon\n", "", nil, nil},
		{"grep alt", text, args("grep", "^(alpha|eps)"), "alpha\nepsilon\n", "", nil, nil},
		{"grep class", text, args("grep", "[B-D]e"), "Beta\n", "", nil, nil},
		{"grep closure", text, args("grep", "m+a d"), "gamma delta\n", "", nil, nil},
		{"grep dot", text, args("grep", "^.....$"), "alpha\n", "", nil, nil},
		{"grep quote", "a.b\naxb\n", args("grep", "a\\.b"), "a.b\n", "", nil, nil},
		{"grep -i", text, args("grep", "-i", "BETA"), "Beta\n", "", nil, nil},
		{"grep -v", text, args("grep", "-v", "a"), "epsilon\n", "", nil, nil},
		{"grep -n", text, args("grep", "-n", "ta"), "2:Beta\n3:gamma delta\n", "", nil, nil},
		{"grep files", "", args("grep", "hello", s("a"), s("b")), s("a") + ": hello\n" + s("b") + ": hello\n", "", nil, nil},
		{"grep -n files", "", args("grep", "-n", "hello", s("a"), s("b")), s("a") + ":1: hello\n" + s("b") + ":2: hello\n", "", nil, nil},
		{"grep -l", "", args("grep", "-l", "world", s("a"), s("b")), s("b") + "\n", "", nil, nil},
		{"grep -L", "", args("grep", "-L", "world", s("a"), s("b")), s("a") + "\n", "", nil, nil},
		{"grep -s", "", args("grep", "-s", "world", s("a"), s("b")), "", "", nil, nil},
		{"grep stdin -l", text, args("grep", "-l", "a"), "stdin\n", "", nil, nil},
		{"grep none", text, args("grep", "zeta"), "", "fail:no matches", nil, nil},
		{"grep missing", "", args("grep", "x", s("nope")), "grep: cannot open " + s("nope") + ": file does not exist\n", "fail:no matches", nil, nil},
		{"grep bad", "", args("grep", "a(b"), "grep: invalid regular expression\n", "fail:bad regex", nil, nil},
		{"grep usage", "", args("grep"), "usage: grep [-lnviLs] pattern [file...]\n", "fail:usage", nil, nil},
	})
}
