package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// cmds3EFiles writes the plumber test's rule files into root/tmp/cmds3e.
func cmds3EFiles(t *testing.T) {
	t.Helper()
	dir := filepath.Join(root, "tmp", "cmds3e")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"rules": `# rules for the plumber test
kind is text
data matches 'run ([a-z]+)'
plumb to run
plumb start plumb -d run2 started $1

data matches 'wm (.*)'
plumb to wmport
plumb start /bin/wmapp 'arg one' $1

src is quoter
plumb to q

data matches 'auto (.*)'
plumb to auto
plumb start nosuchcmd $1

dst is never
plumb to run2
`,
		"bad1": "kind is text\nplumb start x\n",
		"bad2": "kind is text\nplumb to\n",
		"bad3": "kind is text\ndata matches (\nplumb to x\n",
		"bad4": "kind\n",
		"bad5": "plumb to x\nkind is text\n",
		"bad6": "kind frob text\nplumb to x\n",
		"bad7": "kind is text extra\nplumb to x\n",
		"wm":   "",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(dir, "nofile"))
}

func cmds3ECheck(t *testing.T, script, want, wantst string) {
	t.Helper()
	out, st := emu(t, "", "sh", "-c", script)
	if out != want || st != wantst {
		t.Errorf("got %q status %q\nwant %q status %q", out, st, want, wantst)
	}
}

// TestCmds3EPlumbDefault routes messages with the default rules file,
// root/lib/plumbing, and checks rule-file and usage errors.
func TestCmds3EPlumbDefault(t *testing.T) {
	cmds3EFiles(t)
	u := "inferno"
	if cu, err := user.Current(); err == nil {
		u = cu.Username
	}
	script := strings.Join([]string{
		"plumb x; echo $status",
		"plumber; echo $status",
		"plumber /tmp/cmds3e/bad1; echo $status",
		"plumber /tmp/cmds3e/bad2; plumber /tmp/cmds3e/bad3; plumber /tmp/cmds3e/bad4",
		"plumber /tmp/cmds3e/bad5; plumber /tmp/cmds3e/bad6; plumber /tmp/cmds3e/bad7",
		"plumber /tmp/cmds3e/nofile; plumber -x; echo $status",
		"plumber /lib/plumbing; echo $status; ls /chan",
		"plumb; echo $status; plumb -i x",
		"plumb -s edit -d plumb start; plumb -s web -d plumb start; plumb -s man -d plumb start",
		"plumb /lib/./; plumb -w /lib plumbing:7; plumb 'ls(1)'; plumb http://example.com/a/b.html",
		"plumb no such thing; echo $status",
		"read < /chan/plumb.edit; echo; read < /chan/plumb.edit; echo",
		"read < /chan/plumb.man; echo; read < /chan/plumb.web; echo",
	}, "\n")
	usage := "usage: plumb [-s src] [-d dest] [-w wdir] [-t type] [-a name val] -i | ... data ...\n"
	want := `plumb: can't connect to plumb: file does not exist
error
plumb: can't read /usr/` + u + `/lib/plumbing: file does not exist
init
plumb: /tmp/cmds3e/bad1:1: rule must have "plumb to" action
init
plumb: /tmp/cmds3e/bad2:2: syntax error: too few words on line
plumb: /tmp/cmds3e/bad3:2: error in regular expression '('
plumb: /tmp/cmds3e/bad4:1: syntax error: too few words on line
plumb: /tmp/cmds3e/bad5:1: actions must follow patterns in rule
plumb: /tmp/cmds3e/bad6:1: illegal predicate frob in pattern
plumb: /tmp/cmds3e/bad7:1: too many words in 'kind' pattern
plumb: can't read /tmp/cmds3e/nofile: file does not exist
Usage: plumb [-vw] [-c wmchan] [initfile ...]
usage

/chan/plumb.edit
/chan/plumb.input
/chan/plumb.man
/chan/plumb.web
` + usage + "usage\n" + usage + `plumb: send "/lib" to edit
plumb: send "/lib/plumbing:7" to edit
plumb: send "ls(1)" to man
plumb: send "http://example.com/a/b.html" to web
plumb: don't know who message goes to
plumb: can't plumb message: no matching plumb rule
error
plumb
edit
/
text

4
/lib
plumb
edit
/lib
text

15
/lib/plumbing:7
plumb
man
/
text

5
ls(1)
plumb
web
/
text

27
http://example.com/a/b.html
`
	cmds3ECheck(t, script, want, "")
}

// TestCmds3EPlumbRules checks routing, queueing, starting programs
// (directly and through a window manager channel), click matching, -i,
// -v, control messages and errors.
func TestCmds3EPlumbRules(t *testing.T) {
	cmds3EFiles(t)
	script := strings.Join([]string{
		"plumber /tmp/cmds3e/rules; echo $status",
		"plumb -s run2 -d plumb start",
		"plumb -w /tmp/cmds3e run foo; read < /chan/plumb.run2; echo",
		"plumb run bar; read < /chan/plumb.run; echo",
		"plumb -a click 5 -a k v 'xx run abc yy'; read < /chan/plumb.run; echo",
		"plumb wm x y",
		"plumb -s quoter not started",
		"plumb -s q -d plumb start; plumb -s quoter -a click 3 -a k v hi there",
		"read 10 < /chan/plumb.q; read < /chan/plumb.q; echo",
		"echo from stdin | plumb -i -s quoter -a x 1; read < /chan/plumb.q",
		"plumb -d nope zz; echo $status",
		"plumb -s bogus -d plumb start; plumb -d plumb -s run frob",
		"plumb auto x; echo done",
		"@{unmount /chan; plumber -v -w -c /tmp/cmds3e/wm /tmp/cmds3e/rules; plumb -w /tmp/cmds3e wm x y; read < /tmp/cmds3e/wm; echo}",
		"plumber /tmp/cmds3e/rules; echo $status",
	}, "\n")
	want := `
plumb: start plumb port run
plumb: send "started foo" to run2
plumb
run2
/tmp/cmds3e
text

11
started foo
plumb: send "run bar" to run
plumb
run
/
text

7
run bar
plumb: send "run abc" to run
plumb
run
/
text
k=v
7
run abc
plumb: start /bin/wmapp port wmport
plumb: can't load /bin/wmapp: file does not exist
plumb: can't start application
plumb: send "hi there" to q
read: read error: buffer too short for message
quoter
q
/
text
click=3	k=v
8
hi there
plumb: send "from stdin" to q
quoter
q
/
text
x=1	action=showdata
11
from stdin
plumb: unrecognized port nope
plumb: don't know who message goes to
plumb: can't plumb message: no matching plumb rule
error
plumb: "start" message from unrecognized port bogus
plumb: unrecognized control message from run: frob
plumb: start nosuchcmd port auto
plumb: can't load nosuchcmd: file does not exist
done
plumb: msg: src=plumb; dst=; dir=/tmp/cmds3e; kind=text; attr=''; data='wm x y'
plumb: 'text' is 'text'
plumb: 'wm x y' matches 'run ([a-z]+)'
plumb: no
plumb: 'wm x y' matches 'wm (.*)'
plumb: yes
plumb: start /bin/wmapp port wmport
s /tmp/cmds3e /bin/wmapp 'arg one' 'x y'
plumb: can't establish /chan/plumb.input: file already exists
error
`
	cmds3ECheck(t, script, want, "")
}
