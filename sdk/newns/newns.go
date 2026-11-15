// Package newns builds a name space from a description file, as
// Inferno's Newns module (newns(2)) does. The file holds one operation
// per line (namespace(6)): bind, mount, import, unmount, cd, new, clear,
// fork, nodev and ". file" to include another file. Arguments are split
// with the shell's quoting rules and $name is replaced by the value of
// the environment variable name.
//
// Keyring and factotum authentication are not implemented yet: mount
// works with -A (no authentication) and import, which always needs
// factotum, fails as it does in Inferno when factotum is absent.
package newns

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Error is a name space error, worded as Inferno's newns words it.
type Error string

func (e Error) Error() string { return string(e) }

// Newns builds the name space described in file for user. An empty file
// means "namespace" in the current directory, or /lib/namespace when
// there is none. As in Inferno, user is not used yet.
func Newns(user, file string) error {
	_ = user
	if file == "" {
		file = "namespace"
		if _, err := ops.stat(file); err != nil {
			file = "/lib/namespace"
		}
	}
	r, err := ops.open(file)
	if err != nil {
		return Error(fmt.Sprintf("cannot open %s: %s", quote.Quote(file), errstr(err)))
	}
	defer r.Close()
	b := &builder{factotum: ops.factotum()}
	return b.nsfile(r)
}

// Newuser uses the capability cap to become user, then builds the name
// space in file as Newns does.
func Newuser(user, cap, file string) error {
	if cap == "" {
		return Error("no capability")
	}
	if err := ops.capuse(cap); err != nil {
		return err
	}
	ops.unmount("", "/mnt/factotum")
	ops.bind("#sfactotum", "/mnt/factotum", sys.MREPL)
	return Newns(user, file)
}

// Nsfile applies the operations read from r to the current name space.
func Nsfile(r io.Reader) error {
	b := &builder{factotum: ops.factotum()}
	return b.nsfile(r)
}

// sysops is the system interface newns uses, so the host tests can
// record the operations instead of performing them.
type sysops struct {
	stat     func(name string) (*sys.Dir, error)
	open     func(name string) (io.ReadCloser, error)
	factotum func() bool
	capuse   func(cap string) error
	pctl     func(flags int) error
	bind     func(name, old string, flags int) error
	mount    func(addr, old string, flags int, spec string, use9 bool) error
	unmount  func(name, old string) error
	chdir    func(dir string) error
	getenv   func(name string) (string, bool)
	user     func() string
}

var ops = sysops{
	stat: sys.Stat,
	open: func(name string) (io.ReadCloser, error) {
		fd, err := sys.Open(name, sys.OREAD)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), name), nil
	},
	factotum: func() bool {
		fd, err := sys.Open("/mnt/factotum/rpc", sys.ORDWR)
		if err != nil {
			return false
		}
		sys.Close(fd)
		return true
	},
	capuse: func(cap string) error {
		fd, err := sys.Open("#¤/capuse", sys.OWRITE)
		if err != nil {
			return Error("opening #¤/capuse: " + errstr(err))
		}
		f := os.NewFile(uintptr(fd), "capuse")
		defer f.Close()
		if _, err := f.Write([]byte(cap)); err != nil {
			return Error("writing " + cap + " to #¤/capuse: " + sys.Errstr())
		}
		return nil
	},
	pctl: func(flags int) error { _, err := sys.Pctl(flags, nil); return err },
	bind: func(name, old string, flags int) error { _, err := sys.Bind(name, old, flags); return err },
	mount: func(addr, old string, flags int, spec string, use9 bool) error {
		c, err := dial.Dial(addr, "")
		if err != nil {
			return Error("dial: " + addr + ": " + errstr(err))
		}
		defer c.Cfd.Close()
		defer c.Dfd.Close()
		fd := int(c.Dfd.Fd())
		if use9 {
			// Without factotum a server that wants authentication
			// cannot be answered; one that does not is mounted.
			if afd, err := sys.Fauth(fd, spec); err == nil {
				sys.Close(afd)
				return errNoFactotum
			}
		}
		_, err = sys.Mount(fd, -1, old, flags, spec)
		return err
	},
	unmount: sys.Unmount,
	chdir:   sys.Chdir,
	getenv: func(name string) (string, bool) {
		fd, err := sys.Open("#e/"+name, sys.OREAD)
		if err != nil {
			return "", false
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		b := make([]byte, 256)
		n, _ := f.Read(b)
		if n <= 0 {
			return "", true
		}
		b = b[:n]
		if i := strings.IndexAny(string(b), "\x00\n"); i >= 0 {
			b = b[:i]
		}
		return string(b), true
	},
	user: func() string {
		b, err := os.ReadFile("/dev/user")
		if err != nil {
			return ""
		}
		return string(b)
	},
}

var errNoFactotum = sys.Error("factotum authentication not implemented")

func errstr(err error) string {
	if e, ok := err.(sys.Error); ok {
		return string(e)
	}
	if e, ok := err.(Error); ok {
		return string(e)
	}
	if s := sys.Errstr(); s != "" {
		return s
	}
	return err.Error()
}

type builder struct {
	factotum bool
}

func (b *builder) nsfile(r io.Reader) error {
	br := bufio.NewReader(r)
	for {
		l, err := br.ReadString('\n')
		if l != "" {
			if args := Unquoted(l); args != nil {
				if e := b.nsop(Expand(args, ops.getenv)); e != nil {
					return e
				}
			}
		}
		if err != nil {
			return nil
		}
	}
}

// Expand replaces $name in each argument by the value getenv gives; a
// name ends at '.', '/' or '$'. It returns nil, so that the line is
// skipped, when a variable is not set. As in Inferno, the character
// just after an expansion is not examined again.
func Expand(args []string, getenv func(string) (string, bool)) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		s := []rune(a)
		for i := 0; i < len(s); i++ {
			if s[i] != '$' {
				continue
			}
			j := i + 1
			for ; j < len(s); j++ {
				if c := s[j]; c == '.' || c == '/' || c == '$' {
					break
				}
			}
			if j > i+1 {
				v, ok := getenv(string(s[i+1 : j]))
				if !ok {
					return nil
				}
				vr := []rune(v)
				ns := append(append(append([]rune{}, s[:i]...), vr...), s[j:]...)
				s = ns
				i += len(vr)
			}
		}
		out = append(out, string(s))
	}
	return out
}

func (b *builder) nsop(argv []string) error {
	if len(argv) == 0 || strings.HasPrefix(argv[0], "#") {
		return nil
	}
	c := 0
	switch argv[0] {
	case ".":
		if len(argv) < 2 {
			return Error(".: needs a filename")
		}
		r, err := ops.open(argv[1])
		if err != nil {
			return Error(fmt.Sprintf("can't open %s for read %s", quote.Quote(argv[1]), errstr(err)))
		}
		defer r.Close()
		return b.nsfile(r)
	case "new":
		c = sys.NEWNS | sys.FORKENV
	case "clear":
		if err := clear(); err != nil {
			return Error(errstr(err))
		}
		return nil
	case "fork":
		c = sys.FORKNS
	case "nodev":
		c = sys.NODEVS
	case "bind":
		return b.bind(argv)
	case "mount":
		return b.mount(argv)
	case "unmount":
		return b.unmount(argv)
	case "import":
		return b.import9(argv)
	case "cd":
		if len(argv) != 2 {
			return Error("cd: must have one argument")
		}
		if err := ops.chdir(argv[1]); err != nil {
			return Error(errstr(err))
		}
		return nil
	default:
		return Error("invalid namespace command")
	}
	if err := ops.pctl(c); err != nil {
		return Error(errstr(err))
	}
	return nil
}

func clear() error {
	if err := ops.pctl(sys.FORKNS); err != nil {
		return err
	}
	if err := ops.bind("#/", "/", sys.MREPL); err != nil {
		return err
	}
	if err := ops.chdir("/"); err != nil {
		return err
	}
	return ops.pctl(sys.NEWNS)
}

// Mopts are the options of bind, mount, import and unmount.
type Mopts struct {
	Argv    []string
	Flags   int
	Alg     string
	Keyfile string
	Ignore  bool
	Use9    bool
	Doauth  bool
}

// ParseOpts parses the options of a bind, mount, import or unmount line;
// argv[0] is the operation.
func ParseOpts(argv []string) (*Mopts, error) {
	r := &Mopts{Alg: "none", Doauth: true}
	a := arg.New(argv)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'i':
			r.Ignore = true
		case 'a':
			r.Flags |= sys.MAFTER
		case 'b':
			r.Flags |= sys.MBEFORE
		case 'c':
			r.Flags |= sys.MCREATE
		case 'r':
			r.Flags |= sys.MREPL
		case 'k':
			if r.Keyfile = a.Arg(); r.Keyfile == "" {
				return nil, Error("mount: missing arg to -k option")
			}
		case 'C':
			if r.Alg = a.Arg(); r.Alg == "" {
				return nil, Error("mount: missing arg to -C option")
			}
		case '9':
			r.Use9 = true
		case 'A':
			r.Doauth = false
		default:
			return nil, Error(fmt.Sprintf("mount: bad option -%c", o))
		}
	}
	r.Argv = a.Argv()
	return r, nil
}

func (r *Mopts) ig(e string) error {
	if r.Ignore {
		return nil
	}
	return Error(e)
}

func (b *builder) bind(argv []string) error {
	r, err := ParseOpts(argv)
	if err != nil {
		return err
	}
	if len(r.Argv) < 2 {
		return Error("bind: too few args")
	}
	from, to := r.Argv[0], r.Argv[1]
	if err := ops.bind(from, to, r.Flags); err != nil {
		return r.ig(fmt.Sprintf("bind %s %s: %s", from, to, errstr(err)))
	}
	return nil
}

func (b *builder) mount(argv []string) error {
	r, err := ParseOpts(argv)
	if err != nil {
		return err
	}
	if len(r.Argv) < 2 {
		return r.ig("mount: too few args")
	}
	addr, dir := r.Argv[0], r.Argv[1]
	spec := ""
	if len(r.Argv) > 2 {
		spec = r.Argv[2]
	}
	dest := dial.Netmkaddr(addr, "net", "styx")
	fail := func(err error) error {
		if e, ok := err.(Error); ok {
			return r.ig(string(e))
		}
		return r.ig(fmt.Sprintf("mount %s %s: %s", quote.Quote(addr), quote.Quote(dir), errstr(err)))
	}
	if !r.Doauth || r.Use9 {
		if err := ops.mount(dest, dir, r.Flags, spec, r.Doauth && r.Use9); err != nil {
			return fail(err)
		}
		return nil
	}
	kd := "/usr/" + ops.user() + "/keyring/"
	cert := r.Keyfile
	if cert != "" {
		if cert[0] != '/' {
			cert = kd + cert
		}
		if _, err := ops.stat(cert); err != nil {
			return r.ig(fmt.Sprintf("cannot find certificate %s: %s", quote.Quote(cert), errstr(err)))
		}
	} else {
		cert = kd + addr
		if _, err := ops.stat(cert); err != nil {
			cert = kd + "default"
		}
	}
	return r.ig(fmt.Sprintf("cannot read certificate from %s: keyring authentication not implemented", quote.Quote(cert)))
}

func (b *builder) import9(argv []string) error {
	r, err := ParseOpts(argv)
	if err != nil {
		return err
	}
	if len(r.Argv) < 2 {
		return Error("import: too few args")
	}
	if !b.factotum {
		return r.ig("import: no factotum")
	}
	dest := dial.Netmkaddr(r.Argv[0], "net", "17007")
	return r.ig("import: " + dest + ": factotum authentication not implemented")
}

func (b *builder) unmount(argv []string) error {
	r, err := ParseOpts(argv)
	if err != nil {
		return err
	}
	var from, to string
	switch len(r.Argv) {
	case 1:
		to = r.Argv[0]
	case 2:
		from, to = r.Argv[0], r.Argv[1]
	default:
		return Error("unmount: takes 1 or 2 args")
	}
	if err := ops.unmount(from, to); err != nil {
		return r.ig("unmount: " + errstr(err))
	}
	return nil
}

// Unquoted splits s into words as Inferno's String->unquoted does:
// blanks separate words, single quotes quote, and a doubled quote within
// quotes stands for one. An unbalanced quote is closed at the end.
func Unquoted(s string) []string {
	var args []string
	var word []rune
	have, inquote := false, false
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case inquote:
			if c == '\'' {
				if i+1 < len(r) && r[i+1] == '\'' {
					word = append(word, '\'')
					i++
				} else {
					inquote = false
				}
				continue
			}
			word = append(word, c)
		case c == ' ' || c == '\t' || c == '\n':
			if have {
				args = append(args, string(word))
				word, have = nil, false
			}
		case c == '\'':
			inquote, have = true, true
		default:
			word, have = append(word, c), true
		}
	}
	if have {
		args = append(args, string(word))
	}
	return args
}
