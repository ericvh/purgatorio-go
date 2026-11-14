package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

// emuModes is emu for output that depends on timing: it runs args on
// each wazero mode and returns every mode's output and status without
// requiring them to agree.
func emuModes(t *testing.T, stdin string, args ...string) (outs, sts []string) {
	t.Helper()
	emu(t, "", "date", "0") // checks that the guests are built
	for _, m := range []wazero.Mode{wazero.Compiler, wazero.Interpreter} {
		var out bytes.Buffer
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
	return outs, sts
}

// The system and name-space commands, run directly by emuinit. Output
// that depends on the clock or the host is matched by pattern.

func TestSysCommands(t *testing.T) {
	cases := []struct {
		name, stdin string
		args        []string
		out, status string
	}{
		{"date secs", "", []string{"date", "0"}, "Thu Jan 01 00:00:00 GMT 1970\n", ""},
		{"date -u", "", []string{"date", "-u", "1000000000"}, "Sun Sep 09 01:46:40 GMT 2001\n", ""},
		{"date -n secs", "", []string{"date", "-n", "12"}, "12\n", ""},
		{"date usage", "", []string{"date", "x"}, "usage: date [-un] [seconds]\n", "fail:usage"},
		{"date opt", "", []string{"date", "-x"}, "usage: date [-un] [seconds]\n", "fail:usage"},

		{"sleep", "", []string{"sleep", "0"}, "", ""},
		{"sleep usage", "", []string{"sleep", "x"}, "usage: sleep time\n", "fail:usage"},
		{"env usage", "", []string{"env", "x"}, "Usage: env\n", "fail:usage"},

		{"ns", "", []string{"ns"}, bootNS, ""},
		{"ns -r", "", []string{"ns", "-r"}, bootNS, ""},
		{"ns pid", "", []string{"ns", "1"}, bootNS, ""},
		{"ns missing", "", []string{"ns", "99"}, "ns: can't open /prog/99/ns: file does not exist\n", "fail:open"},
		{"ns usage", "", []string{"ns", "1", "2"}, "usage: ns [-r] [pid]\n", "fail:usage"},

		{"bind", "", []string{"bind", "-b", "/lib", "/tmp"}, "", ""},
		{"bind -ac", "", []string{"bind", "-ac", "/lib", "/tmp"}, "", ""},
		{"bind usage", "", []string{"bind", "-ab", "/lib", "/tmp"},
			"usage: bind [-a|-b|-c|-ac|-bc] [-q] source target\n", "fail:usage"},
		{"bind source", "", []string{"bind", "/nonexistent", "/tmp"},
			"bind: /nonexistent: file does not exist\n", "fail:bind"},
		{"bind target", "", []string{"bind", "/lib", "/nonexistent"},
			"bind: /nonexistent: file does not exist\n", "fail:bind"},
		{"bind -q", "", []string{"bind", "-q", "/nonexistent", "/tmp"}, "", ""},
		{"bind dir on file", "", []string{"bind", "/lib", "/lib/hello.txt"},
			"bind: cannot bind /lib onto /lib/hello.txt: inconsistent mount\n", "fail:bind"},

		{"unmount", "", []string{"unmount", "#c", "/dev"}, "", ""},
		{"unmount all", "", []string{"unmount", "/dev"}, "", ""},
		{"unmount not mounted", "", []string{"unmount", "/lib", "/dev"}, "unmount: not mounted\n", ""},
		{"unmount usage", "", []string{"unmount"}, "Usage: unmount [source] target\n", ""},

		{"kill pid", "", []string{"kill", "99"}, "kill: cannot open /prog/99/ctl: file does not exist\n", "fail:nothing killed"},
		{"kill name", "", []string{"kill", "nosuch"}, "kill: cannot find nosuch\n", "fail:nothing killed"},
		{"kill usage", "", []string{"kill"}, "usage: kill [-g] pid|module [...]\n", "fail: usage"},

		{"time usage", "", []string{"time"}, "usage: time cmd ...\n", ""},
		{"time missing", "", []string{"time", "nosuch"}, "time: nosuch: file does not exist\n", ""},

		{"cal month", "", []string{"cal", "2", "2024"}, feb2024, ""},
		{"cal name", "", []string{"cal", "feb", "2024"}, feb2024, ""},
		{"cal 1752", "", []string{"cal", "sept", "1752"}, sep1752, ""},
		{"cal bad", "", []string{"cal", "13", "2024"}, "cal: bad argument\n", "fail:bad argument"},
		{"cal word", "", []string{"cal", "foo"}, "cal: bad argument\n", "fail:bad argument"},
		{"cal usage", "", []string{"cal", "1", "2", "3"}, "usage: cal [month] [year]\n", "fail:usage"},

		{"tokenize", "", []string{"tokenize", "a,b,,c d", ", "}, "a\nb\nc\nd\n", ""},
		{"tokenize usage", "", []string{"tokenize", "a"}, "Usage: tokenize string delimiters\n", "fail: usage"},

		{"read", "hello world\n", []string{"read", "5"}, "hello", ""},
		{"read all", "hello world\n", []string{"read"}, "hello world\n", ""},
		{"read zero", "hello\n", []string{"read", "0"}, "", ""},
		{"read eof", "", []string{"read"}, "", "fail:eof"},
		{"read usage", "", []string{"read", "-x", "1"}, "usage: read [-[ero] offset] count\n", "fail:usage"},

		{"newer same", "", []string{"newer", "/lib/hello.txt", "/lib/hello.txt"}, "", ""},
		{"newer dir", "", []string{"newer", "/lib/hello.txt", "/lib"}, "", "fail:old:directory"},
		{"newer new dir", "", []string{"newer", "/lib", "/lib/hello.txt"}, "", "fail:new:directory"},
		{"newer missing", "", []string{"newer", "/nonexistent", "/lib"}, "", "fail:new:file does not exist"},
		{"newer usage", "", []string{"newer", "a"}, "usage: newer newfile oldfile\n", "fail:usage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, st := emu(t, c.stdin, c.args...)
			if out != c.out || st != c.status {
				t.Errorf("got %q status %q, want %q status %q", out, st, c.out, c.status)
			}
		})
	}
}

// bootNS is the emu's boot name space as ns shows it.
const bootNS = `bind -ac '#U' /
bind -b '#m' /dev
bind -b '#c' /dev
bind '#p' /prog
bind '#d' /fd
bind -a '#I' /net
bind -a /dev /dev
bind -c '#e' /env
cd /
`

const feb2024 = `   February 2024
 S  M Tu  W Th  F  S
             1  2  3
 4  5  6  7  8  9 10
11 12 13 14 15 16 17
18 19 20 21 22 23 24
25 26 27 28 29

`

const sep1752 = `   September 1752
 S  M Tu  W Th  F  S
       1  2 14 15 16
17 18 19 20 21 22 23
24 25 26 27 28 29 30



`

func TestTest(t *testing.T) {
	cases := []struct {
		args   []string
		status string
	}{
		{[]string{"-d", "/lib"}, ""},
		{[]string{"-d", "/lib/hello.txt"}, "fail:false"},
		{[]string{"-f", "/lib/hello.txt"}, ""},
		{[]string{"-f", "/lib"}, "fail:false"},
		{[]string{"-e", "/nonexistent"}, "fail:false"},
		{[]string{"-r", "/lib/hello.txt"}, ""},
		{[]string{"-w", "/dev/null"}, ""},
		{[]string{"-s", "/lib/hello.txt"}, ""},
		{[]string{"-s", "/dev/null"}, "fail:false"},
		{[]string{"-t"}, ""},
		{[]string{"-n", ""}, "fail:false"},
		{[]string{"-z", ""}, ""},
		{[]string{"abc"}, ""},
		{[]string{""}, "fail:false"},
		{[]string{"a", "=", "a"}, ""},
		{[]string{"a", "!=", "a"}, "fail:false"},
		{[]string{"1", "-lt", "2"}, ""},
		{[]string{"10", "-le", "2"}, "fail:false"},
		{[]string{"2", "-eq", "2", "-a", "3", "-gt", "1"}, ""},
		{[]string{"2", "-eq", "3", "-o", "3", "-ge", "3"}, ""},
		{[]string{"!", "-d", "/lib"}, "fail:false"},
		{[]string{"(", "-d", "/lib", "-a", "-f", "/lib/hello.txt", ")"}, ""},
		{[]string{"/lib/hello.txt", "-older", "1s"}, ""},
		{[]string{"/lib/hello.txt", "-older", "1000y"}, "fail:false"},
		{[]string{"/lib/hello.txt", "-nt", "/lib/hello.txt"}, "fail:false"},
		{[]string{"/lib/hello.txt", "-ot", "/nonexistent"}, "fail:false"},
		{nil, "fail:usage"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			out, st := emu(t, "", append([]string{"test"}, c.args...)...)
			if out != "" || st != c.status {
				t.Errorf("got %q status %q, want status %q", out, st, c.status)
			}
		})
	}
	for _, c := range []struct {
		args     []string
		out, sts string
	}{
		{[]string{"1", "-foo", "2"}, "test: bad syntax: unknown operator -foo\n", "fail:bad syntax"},
		{[]string{"(", "-d", "/lib"}, "test: bad syntax: argument expected\n", "fail:bad syntax"},
		{[]string{"/lib", "-older", "1x"}, "test: bad syntax: bad time syntax, 1x\n", "fail:bad syntax"},
	} {
		out, st := emu(t, "", append([]string{"test"}, c.args...)...)
		if out != c.out || st != c.sts {
			t.Errorf("test %q: got %q status %q, want %q status %q", c.args, out, st, c.out, c.sts)
		}
	}
}

func TestSysPatterns(t *testing.T) {
	cases := []struct {
		name string
		args []string
		re   string
	}{
		{"date", []string{"date"}, `^(Sun|Mon|Tue|Wed|Thu|Fri|Sat) [A-Z][a-z]{2} \d\d \d\d:\d\d:\d\d GMT \d{4}\n$`},
		{"date -n", []string{"date", "-n"}, `^\d{10}\n$`},
		{"env", []string{"env"}, `(?m)^emuhost=\S+$[\s\S]*^cputype=\S+$`},
		{"ps", []string{"ps"}, `^ {7}1 {8}1 +\S+ +\d+:\d\d\.\d +\S+ +0K ps\n$`},
		{"time", []string{"time", "sleep", "0"}, `^\d[\d.e-]*l \d[\d.e-]*r \d[\d.e-]*t\n$`},
		{"time ps", []string{"time", "ps"},
			`^ +1 +1 .* time\n +2 +1 .* ps\n\S+l \S+r \S+t\n$`},
		{"time status", []string{"time", "cat", "/nonexistent"},
			`^cat: cannot open /nonexistent: file does not exist\n2 "cat":fail:bad open\n\S+l \S+r \S+t\n$`},
		{"time ns", []string{"time", "ns", "2"}, `^` + regexp.QuoteMeta(bootNS) + `\S+l \S+r \S+t\n$`},
		{"cal", []string{"cal"}, `^   [A-Z][a-z]+ \d{4}\n S  M Tu  W Th  F  S\n`},
		{"cal year", []string{"cal", "2026"}, `^\n\n\n {32}2026\n\n {9}Jan {20}Feb {20}Mar\n`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			outs, sts := emuModes(t, "", c.args...)
			for i, out := range outs {
				if sts[i] != "" || !regexp.MustCompile(c.re).MatchString(out) {
					t.Errorf("got %q status %q, want %s", out, sts[i], c.re)
				}
			}
		})
	}
}

func TestKill(t *testing.T) {
	// kill finds its own prog by module name and kills it.
	for _, args := range [][]string{{"kill", "kill"}, {"kill", "-g", "kill"}} {
		_, sts := emuModes(t, "", args...)
		for _, st := range sts {
			if st != "killed" {
				t.Errorf("%q: status %q, want killed", args, st)
			}
		}
	}
	// Killing time's prog by pid ends time, waiting for its child.
	_, sts := emuModes(t, "", "time", "kill", "1")
	for _, st := range sts {
		if st != "killed" {
			t.Errorf("time kill 1: status %q, want killed", st)
		}
	}
}
