package newns

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func TestUnquoted(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   \t\n", nil},
		{"bind -a /a /b\n", []string{"bind", "-a", "/a", "/b"}},
		{"cd 'a dir'", []string{"cd", "a dir"}},
		{"x 'it''s' a''b", []string{"x", "it's", "ab"}},
		{"x ''", []string{"x", ""}},
		{"x 'unbalanced y", []string{"x", "unbalanced y"}},
		{"a\tb", []string{"a", "b"}},
	}
	for _, c := range cases {
		if got := Unquoted(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Unquoted(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExpand(t *testing.T) {
	env := map[string]string{"user": "bob", "home": "/usr/bob", "e": ""}
	getenv := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	cases := []struct {
		in, want []string
	}{
		{[]string{"bind", "$home/tmp", "/tmp"}, []string{"bind", "/usr/bob/tmp", "/tmp"}},
		{[]string{"x", "/n/$user.d"}, []string{"x", "/n/bob.d"}},
		{[]string{"x", "$e"}, []string{"x", ""}},
		{[]string{"x", "$"}, []string{"x", "$"}},
		{[]string{"x", "a$/b"}, []string{"x", "a$/b"}},
		// The character after an expansion is not looked at again.
		{[]string{"x", "$user$user"}, []string{"x", "bob$user"}},
		{[]string{"x", "$nosuch"}, nil},
	}
	for _, c := range cases {
		if got := Expand(c.in, getenv); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseOpts(t *testing.T) {
	r, err := ParseOpts([]string{"mount", "-bcA", "-k", "key", "-C", "rc4", "tcp!h", "/n"})
	if err != nil {
		t.Fatal(err)
	}
	want := &Mopts{Argv: []string{"tcp!h", "/n"}, Flags: sys.MBEFORE | sys.MCREATE, Alg: "rc4", Keyfile: "key"}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("got %+v, want %+v", r, want)
	}
	for _, c := range []struct {
		argv []string
		err  string
	}{
		{[]string{"bind", "-x", "a", "b"}, "mount: bad option -x"},
		{[]string{"mount", "-k"}, "mount: missing arg to -k option"},
		{[]string{"mount", "-C"}, "mount: missing arg to -C option"},
	} {
		if _, err := ParseOpts(c.argv); err == nil || err.Error() != c.err {
			t.Errorf("%q: got %v, want %q", c.argv, err, c.err)
		}
	}
}

// fake replaces ops with a recorder for the duration of a test.
func fake(t *testing.T, files map[string]string, fail map[string]string) *[]string {
	var log []string
	saved := ops
	t.Cleanup(func() { ops = saved })
	failed := func(op string) error {
		if e, ok := fail[op]; ok {
			return sys.Error(e)
		}
		return nil
	}
	ops = sysops{
		stat: func(name string) (*sys.Dir, error) {
			if _, ok := files[name]; ok {
				return &sys.Dir{Name: name}, nil
			}
			return nil, sys.Error("file does not exist")
		},
		open: func(name string) (io.ReadCloser, error) {
			if s, ok := files[name]; ok {
				return io.NopCloser(strings.NewReader(s)), nil
			}
			return nil, sys.Error("file does not exist")
		},
		factotum: func() bool { return false },
		capuse:   func(string) error { return errors.New("no #¤") },
		pctl: func(flags int) error {
			log = append(log, fmt.Sprintf("pctl %#x", flags))
			return failed("pctl")
		},
		bind: func(name, old string, flags int) error {
			log = append(log, fmt.Sprintf("bind %d %s %s", flags, name, old))
			return failed("bind")
		},
		mount: func(addr, old string, flags int, spec string, use9 bool) error {
			log = append(log, fmt.Sprintf("mount %d %s %s %q %v", flags, addr, old, spec, use9))
			return failed("mount")
		},
		unmount: func(name, old string) error {
			log = append(log, fmt.Sprintf("unmount %q %s", name, old))
			return failed("unmount")
		},
		chdir: func(dir string) error {
			log = append(log, "chdir "+dir)
			return failed("chdir")
		},
		getenv: func(name string) (string, bool) {
			if name == "user" {
				return "bob", true
			}
			return "", false
		},
		user: func() string { return "bob" },
	}
	return &log
}

func TestNewns(t *testing.T) {
	files := map[string]string{
		"/lib/namespace": `# a comment
	# an indented comment

bind -a '#I' /net
bind -bc /usr/$user/tmp /tmp
bind $nosuch /x
mount -A tcp!fs /n/fs spec
mount -9A tcp!fs!564 /n/9
mount -9 -a fs /n/9
unmount /n/fs
unmount /a /b
. /lib/more
cd /usr/$user
fork
new
nodev
clear
`,
		"/lib/more": "bind /more /b\n",
	}
	log := fake(t, files, nil)
	if err := Newns("", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bind 2 #I /net",
		"bind 5 /usr/bob/tmp /tmp",
		`mount 0 tcp!fs!styx /n/fs "spec" false`,
		`mount 0 tcp!fs!564 /n/9 "" false`,
		`mount 2 net!fs!styx /n/9 "" true`,
		`unmount "" /n/fs`,
		`unmount "/a" /b`,
		"bind 0 /more /b",
		"chdir /usr/bob",
		"pctl 0x8",
		"pctl 0x84",
		"pctl 0x20",
		"pctl 0x8", "bind 0 #/ /", "chdir /", "pctl 0x4",
	}
	if !reflect.DeepEqual(*log, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(*log, "\n"), strings.Join(want, "\n"))
	}
}

func TestNewnsErrors(t *testing.T) {
	cases := []struct {
		file string
		fail map[string]string
		err  string
	}{
		{"bogus x\n", nil, "invalid namespace command"},
		{"bind /a\n", nil, "bind: too few args"},
		{"bind -i /a\n", nil, "bind: too few args"},
		{"bind /a /b\n", map[string]string{"bind": "file does not exist"}, "bind /a /b: file does not exist"},
		{"bind -i /a /b\nbind /c\n", map[string]string{"bind": "file does not exist"}, "bind: too few args"},
		{"mount -A x\n", nil, "mount: too few args"},
		{"mount -iA x\ncd\n", nil, "cd: must have one argument"},
		{"mount -A tcp!h!1 /n\n", map[string]string{"mount": "connection refused"}, "mount tcp!h!1 /n: connection refused"},
		{"mount tcp!h /n\n", nil, "cannot read certificate from /usr/bob/keyring/default: keyring authentication not implemented"},
		{"mount -k key tcp!h /n\n", nil, "cannot find certificate /usr/bob/keyring/key: file does not exist"},
		{"import h /n\n", nil, "import: no factotum"},
		{"import h\n", nil, "import: too few args"},
		{"unmount\n", nil, "unmount: takes 1 or 2 args"},
		{"unmount /n\n", map[string]string{"unmount": "not found"}, "unmount: not found"},
		{"cd /x\n", map[string]string{"chdir": "file does not exist"}, "file does not exist"},
		{".\n", nil, ".: needs a filename"},
		{". /nope\n", nil, "can't open /nope for read file does not exist"},
		{"fork\n", map[string]string{"pctl": "no memory"}, "no memory"},
	}
	for _, c := range cases {
		fake(t, map[string]string{"ns": c.file}, c.fail)
		err := Newns("", "ns")
		if err == nil || err.Error() != c.err {
			t.Errorf("%q: got %v, want %q", c.file, err, c.err)
		}
	}
	fake(t, nil, nil)
	if err := Newns("", ""); err == nil || err.Error() != "cannot open /lib/namespace: file does not exist" {
		t.Errorf("missing file: got %v", err)
	}
	if err := Newuser("bob", "", ""); err == nil || err.Error() != "no capability" {
		t.Errorf("Newuser: got %v", err)
	}
}
