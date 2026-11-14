package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// cmds3fDir is the group's scratch directory on the host; the emu sees
// it as /tmp/cmds3f.
var cmds3fDir = filepath.Join(root, "tmp", "cmds3f")

// cmds3fFiles writes files (name -> contents) under the scratch
// directory, after clearing it.
func cmds3fFiles(t *testing.T, files map[string]string) {
	t.Helper()
	if err := os.RemoveAll(cmds3fDir); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		p := filepath.Join(cmds3fDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// cmds3fRun runs script with sh in the emu and checks its output.
func cmds3fRun(t *testing.T, script, want string) {
	t.Helper()
	out, st := emu(t, "", "sh", "-c", script)
	if out != want || st != "" {
		t.Errorf("got status %q output:\n%s\nwant:\n%s", st, out, want)
	}
}

// cmds3fAr is an ar(1) archive holding members dated 100 and 300.
func cmds3fAr() string {
	hdr := func(name string, date, size int) string {
		return fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, date, 0, 0, "644", size)
	}
	return "!<arch>\n" + hdr("m1.o", 100, 3) + "abc\n" + hdr("m2.o", 300, 2) + "xy"
}

func TestCmds3FCd(t *testing.T) {
	cmds3fFiles(t, map[string]string{"d/f": "in d\n"})
	cmds3fRun(t, `cd /tmp/cmds3f/d; pwd; cat f
cd /tmp/cmds3f/nonexist; echo $status
cd a b; echo $status; pwd
`, `/tmp/cmds3f/d
in d
cd: /tmp/cmds3f/nonexist: file does not exist
failed
Usage: cd [directory]
usage
/tmp/cmds3f/d
`)
}

func TestCmds3FMkBasic(t *testing.T) {
	cmds3fFiles(t, map[string]string{
		"a/mkfile": `# a program from two objects
O=o
NAMES=x y
OBJ=${NAMES:%=%.$O}
prog: $OBJ
	echo link $prereq
	cat $prereq > $target
%.$O: %.c
	echo compile $stem
	cat $stem.c > $target
clean:V:
	rm -f prog x.o y.o
`,
		"a/x.c": "X\n",
		"a/y.c": "Y\n",
	})
	cmds3fRun(t, `cd /tmp/cmds3f/a
rm -f prog x.o y.o
touch -t 100 x.c y.c
mk -n
mk
echo status $status
cat prog
mk
touch -t 150 prog x.o y.o
touch -t 200 y.c
mk
mk -t
touch -t 150 prog
mk -t
mk clean
ls
mk nothere x.o
echo status $status
mk -x
echo status $status
mk -f nomkfile
cd ..
mk
echo status $status
`, `echo compile x
cat x.c > x.o
echo compile y
cat y.c > y.o
echo link x.o y.o
cat x.o y.o > prog
echo compile x
cat x.c > x.o
compile x
echo compile y
cat y.c > y.o
compile y
echo link x.o y.o
cat x.o y.o > prog
link x.o y.o
status 
X
Y
mk: 'prog' is up to date
echo compile y
cat y.c > y.o
compile y
echo link x.o y.o
cat x.o y.o > prog
link x.o y.o
mk: 'prog' is up to date
touch(prog)
rm -f prog x.o y.o
mkfile
x.c
y.c
mk: don't know how to make 'nothere'
status error
Usage: mk [-f file] [-n] [-a] [-e] [-t] [-k] [-i] [-d[egp]] [targets ...]
status error
mk: nomkfile: file does not exist
mk: nothing to mk
status error
`)
}

func TestCmds3FMkRules(t *testing.T) {
	cmds3fFiles(t, map[string]string{
		"b/mkfile": `V=` + "`" + `{echo one two}
<inc
<|cat pinc
<missing
all:V: f1 out.x sub/q.dis
	echo all $prereq V $V I $I P $P
f1:Q:
	echo quiet $target
out.x:D: f1
	echo making $target; echo partial > $target; cat nofile
([a-z]+)/(.*)\.dis:R: \1/\2.b
	echo regexp $stem1 $stem2 $stem0 $prereq
sub/q.b:
	echo $target >[1=2]
&.x: &.c
	echo amp $stem
`,
		"b/inc":  "I=included\n",
		"b/pinc": "P=piped\n",
		"c/mkfile": `NAMES=a b
t: s1 s2
	echo build $target from $newprereq
	echo ` + "`" + `{echo $NAMES $target $nproc $stem} > t
v:V: t
	echo virtual
n:N: t
amb: s1
	echo one
amb: s2
	echo two
cyc: cyc2
	echo x
cyc2: cyc
	echo y
p:Pcmp -s: s1
	echo p is out of date
X=U=secret
Y=visible
e:V:
	echo ` + "`" + `{echo $X $Y $MKFLAGS}
`,
		"c/bad1":     "x:Z: y\n",
		"c/bad2":     "x y=z\n",
		"c/bad3":     "junk line\n",
		"c/bad4":     "x: '\n",
		"c/bad5":     "<\n",
		"c/s1":       "s1\n",
		"c/p":        "s1\n",
		"d/lib.orig": cmds3fAr(),
		"d/m1.c":     "",
		"d/m2.c":     "",
		"d/mkfile":   "SRC=m1.c m2.c\nLIB=lib.a\nOBJ=${SRC:%.c=$LIB(%.o)}\nall:V: $OBJ\n\techo members $newmember\n$LIB(%.o):N: %.c\n",
	})
	cmds3fRun(t, `cd /tmp/cmds3f/b
rm -f out.x a.c
mk -n 'V=cmd'
mk
echo status $status
ls
mk -k
echo status $status
touch a.c
mk a.x b.x/c.x
echo status $status
cd /tmp/cmds3f/c
touch -t 100 s1 s2
touch -t 200 t
touch -t 300 s2
mk -e
touch -t 400 t
mk -n -w s1
mk -n -a
mk e
mk 'Y=cmd' e
mk v
mk n
mk amb
mk cyc
echo s1 > s1
mk p
echo s2 > s1
mk p
mk -f bad1
mk -f bad2
mk -f bad3
mk -f bad4
mk -f bad5
cd /tmp/cmds3f/d
cp lib.orig lib.a
touch -t 200 m1.c m2.c
mk
mk -t
mk -t
mk -n
`, `warning: skipping missing include file: mk: missing: file does not exist
echo quiet f1
echo making out.x; echo partial > out.x; cat nofile
echo sub/q.b >[1=2]
echo regexp sub q sub/q.dis sub/q.b
echo all f1 out.x sub/q.dis V cmd I included P piped
warning: skipping missing include file: mk: missing: file does not exist
quiet f1
echo sub/q.b >[1=2]
sub/q.b
echo making out.x; echo partial > out.x; cat nofile
making out.x
cat: cannot open nofile: file does not exist
mk: echo making out.x; ... nofile : exit status=18 "sh":fail:bad open, deleting 'out.x'
status error
inc
mkfile
pinc
warning: skipping missing include file: mk: missing: file does not exist
quiet f1
echo sub/q.b >[1=2]
sub/q.b
echo making out.x; echo partial > out.x; cat nofile
making out.x
cat: cannot open nofile: file does not exist
mk: echo making out.x; ... nofile : exit status=33 "sh":fail:bad open, deleting 'out.x'
echo regexp sub q sub/q.dis sub/q.b
regexp sub q sub/q.dis sub/q.b
status 
warning: skipping missing include file: mk: missing: file does not exist
echo amp a
amp a
mk: don't know how to make 'b.x/c.x'
status error
t(200) < s2(300)
echo build t from s2
echo `+"`"+`{echo $NAMES $target $nproc $stem} > t
build t from s2
echo build t from s1
echo `+"`"+`{echo $NAMES $target $nproc $stem} > t
echo build t from 
echo `+"`"+`{echo $NAMES $target $nproc $stem} > t
echo `+"`"+`{echo $X $Y $MKFLAGS}
visible
echo `+"`"+`{echo $X $Y $MKFLAGS}
cmd Y=cmd
echo virtual
virtual
mk: 'n' is up to date
mk: ambiguous recipes for amb:
	amb <-(mkfile:9)- s1
	amb <-(mkfile:11)- s2
mk: cycle in graph detected at target cyc
mk: 'p' is up to date
echo p is out of date
p is out of date
mk: bad1:2: syntax error; unknown attribute 'Z'
mk: bad2:2: syntax error; multiple vars on left side of assignment
mk: bad3:2: syntax error; expected one of :<=
mk: bad4:1: syntax error; missing closing '
mk: bad5:2: syntax error; missing include file name
echo members m1.o m2.o
members m1.o m2.o
touch(lib.a(m1.o))
echo members m1.o m2.o
`)
}
