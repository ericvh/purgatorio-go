// Command lethe attaches the Lethe tree served at addr (docs/lethe.md)
// on mtpt, in the style of mount:
//
//	lethe [-abc] [-A] [-k keyfile] [-t ms] [-u user] addr mtpt [path]
//
// path is the subtree to attach (default /); the ticket is limited to
// it. The key is the authinfo keyfile if given, else
// /usr/user/keyring/default, /usr/user/key, then the test key.
//
// -A asks for no authentication and no encryption, as mount -A does:
// requests go in the clear to a server started with -A, which answers
// them as the user none. It takes no key and no user.
//
// -t ms sets how long the device trusts what it learnt about a file
// before asking the server again; the default is 1000, and 0 asks every
// time.
//
// The connection is made by #λ, which keeps the key to renew its ticket
// and dials again on its own when a connection fails.
package main

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const usage = "lethe [-abc] [-A] [-k keyfile] [-t ms] [-u user] addr mtpt [path]"

func fail(msg string) {
	os.Stderr.WriteString("lethe: " + msg + "\n")
	sys.Exits("fail:" + msg)
}

func user() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil {
		return ""
	}
	return string(b)
}

// key finds the key to authenticate with: its seed, and a certificate
// line if it came from an authinfo.
func key(keyfile, user string) ([]byte, string) {
	if keyfile != "" {
		p := keyfile
		if !strings.Contains(p, "/") && !strings.HasPrefix(p, "#") {
			p = "/usr/" + user + "/keyring/" + p
		}
		seed, cert, err := readAuthinfo(p)
		if err != nil {
			fail(err.Error())
		}
		return seed, cert
	}
	safe := user != "" && user != "." && user != ".." && !strings.Contains(user, "/")
	if safe {
		if seed, cert, err := readAuthinfo("/usr/" + user + "/keyring/default"); err == nil {
			return seed, cert
		}
	}
	var paths []string
	if safe {
		paths = append(paths, "/usr/"+user+"/key")
	}
	paths = append(paths, "/lib/test/auth.key")
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
			return b, ""
		}
	}
	fail("no key")
	return nil, ""
}

// readAuthinfo reads a pgcert authinfo file: a seed line and a
// certificate line.
func readAuthinfo(p string) ([]byte, string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "", sys.Error(p + ": " + sys.Errstr())
	}
	return parseAuthinfo(p, string(b))
}

// address is addr as tcp!numeric-host!port, through /net/cs when it
// answers.
func address(addr string) string {
	addr = dial.Netmkaddr(addr, "tcp", "")
	if f, err := os.OpenFile("/net/cs", os.O_RDWR, 0); err == nil {
		defer f.Close()
		if _, err := f.Write([]byte(addr)); err == nil {
			f.Seek(0, 0)
			buf := make([]byte, 256)
			for {
				n, err := f.Read(buf)
				if n <= 0 || err != nil {
					break
				}
				clone, a, ok := strings.Cut(string(buf[:n]), " ")
				if ok && strings.HasSuffix(clone, "/tcp/clone") {
					return "tcp!" + a
				}
			}
		}
	}
	if strings.HasPrefix(addr, "tcp!") {
		return addr
	}
	fail("only tcp is supported")
	return ""
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage(usage)
	flags := sys.MREPL
	create := 0
	var keyfile, who string
	noauth, haveUser := false, false
	cache := -1
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			flags = sys.MAFTER
		case 'b':
			flags = sys.MBEFORE
		case 'c':
			create = sys.MCREATE
		case 'A':
			noauth = true
		case 'k':
			keyfile = a.EArg()
		case 'u':
			who, haveUser = a.EArg(), true
		case 't':
			n, err := strconv.Atoi(a.EArg())
			if err != nil || n < 0 {
				a.Usage()
			}
			cache = n
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	// -A is a mode of its own: a key or a user with it is a mistake.
	if len(rest) < 2 || len(rest) > 3 || (noauth && (keyfile != "" || haveUser)) {
		a.Usage()
	}
	var s strings.Builder
	s.WriteString("dial " + address(rest[0]) + "\n")
	if noauth {
		s.WriteString("noauth\n")
	} else {
		if !haveUser {
			who = user()
		}
		seed, cert := key(keyfile, who)
		s.WriteString("user " + who + "\nkey " + hex.EncodeToString(seed) + "\n")
		if cert != "" {
			s.WriteString("cert " + cert + "\n")
		}
	}
	if len(rest) == 3 {
		s.WriteString("path " + rest[2] + "\n")
	}
	if cache >= 0 {
		s.WriteString("cache " + strconv.Itoa(cache) + "\n")
	}
	f, err := os.OpenFile("#λ/clone", os.O_RDWR, 0)
	if err != nil {
		fail("#λ/clone: " + sys.Errstr())
	}
	if _, err := f.Write([]byte(s.String())); err != nil {
		fail(sys.Errstr())
	}
	f.Seek(0, 0)
	buf := make([]byte, 32)
	n, err := f.Read(buf)
	if err != nil {
		fail(sys.Errstr())
	}
	src := "#λ" + strings.TrimSpace(string(buf[:n]))
	if err := sys.Bind(src, rest[1], flags|create); err != nil {
		fail("cannot bind " + src + " on " + rest[1] + ": " + sys.Errstr())
	}
	f.Close()
}

// parseAuthinfo parses an authinfo's text: "key <seed hex>", "signer
// <key hex>" and a certificate line. The certificate is checked by the
// device and the server, not here.
func parseAuthinfo(p, text string) ([]byte, string, error) {
	lines := strings.Split(text, "\n")
	bad := sys.Error(p + ": bad authinfo")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "key ") || !strings.HasPrefix(lines[1], "signer ") {
		return nil, "", bad
	}
	seed, err := hex.DecodeString(lines[0][4:])
	if err != nil || len(seed) != 32 || lines[2] == "" {
		return nil, "", bad
	}
	for _, l := range lines[3:] {
		if l != "" {
			return nil, "", bad
		}
	}
	return seed, lines[2], nil
}
