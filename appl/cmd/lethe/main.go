// Command lethe attaches the Lethe tree served at addr (docs/lethe.md)
// on mtpt, in the style of mount:
//
//	lethe [-abc] [-A] [-p proto] [-k keyfile] [-t ms] [-u user] addr mtpt [path]
//
// path is the subtree to attach (default /); the ticket is limited to
// it.
//
// -p names the ticket exchange: hybrid (X25519 with ML-KEM-768 and
// hybrid Ed25519 + ML-DSA-65 signatures) or classical (the Rust
// sibling's, Ed25519 and X25519). Without -p it is $authproto, then the
// first line of /lib/keys/proto, then hybrid (docs/auth.md, "Choosing
// algorithms"); of several names the first is used. The other exchange
// is never tried.
//
// The key is keyfile (an authinfo or a seed) if given, else
// /usr/user/keyring/default, /usr/user/key, then the test key
// (/lib/test/auth.key, or /lib/test/auth.hkey for hybrid). It must be
// of the exchange's kind.
//
// -A asks for no authentication and no encryption, as mount -A does:
// requests go in the clear to a server started with -A, which answers
// them as the user none. It takes no key, no user and no protocol.
//
// -t ms sets how long the device trusts what it learnt about a file
// before asking the server again; the default is 1000, and 0 asks every
// time.
//
// The connection is made by #λ, which keeps the key to renew its ticket
// and dials again on its own when a connection fails.
package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const usage = "lethe [-abc] [-A] [-p proto] [-k keyfile] [-t ms] [-u user] addr mtpt [path]"

func fail(msg string) {
	os.Stderr.WriteString("lethe: " + msg + "\n")
	sys.Exits("fail:" + msg)
}

// proto is the exchange to use: the first of what -p, $authproto or
// /lib/keys/proto names (authproto.Resolve), hybrid or classical.
func proto(flag string) string {
	ps, err := authproto.Resolve(flag)
	if err != nil {
		fail(err.Error())
	}
	if ps[0] != authproto.Hybrid && ps[0] != authproto.Classical {
		fail("lethe does not take " + ps[0] + ": use hybrid, classical or -A")
	}
	return ps[0]
}

// key finds the key to authenticate with, and its certificate if it came
// from an authinfo: keyfile, else /usr/user/keyring/default,
// /usr/user/key, then the test key of the exchange's family.
func key(keyfile, user string, hybrid bool) (*pgauth.PrivateKey, *pgauth.Cert) {
	if keyfile != "" {
		p := keyfile
		if !strings.Contains(p, "/") && !strings.HasPrefix(p, "#") {
			p = "/usr/" + user + "/keyring/" + p
		}
		k, c, err := authproto.ReadKey(p)
		if err != nil {
			if _, serr := os.Stat(p); serr != nil {
				fail(p + ": " + sys.Errstr())
			}
			fail(err.Error())
		}
		return k, c
	}
	var paths []string
	if user != "" && user != "." && user != ".." && !strings.Contains(user, "/") {
		paths = append(paths, "/usr/"+user+"/keyring/default", "/usr/"+user+"/key")
	}
	if hybrid {
		paths = append(paths, authproto.TestHybridKey)
	} else {
		paths = append(paths, authproto.TestKey)
	}
	for _, p := range paths {
		if k, c, err := authproto.ReadKey(p); err == nil && k.Hybrid() == hybrid {
			return k, c
		}
	}
	fail(string(pgauth.ENoKey))
	return nil, nil
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
	var keyfile, who, pflag string
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
		case 'p':
			pflag = a.EArg()
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
	// -A is a mode of its own: a key, a user or a protocol with it is a
	// mistake.
	if len(rest) < 2 || len(rest) > 3 || (noauth && (keyfile != "" || haveUser || pflag != "")) {
		a.Usage()
	}
	var s strings.Builder
	s.WriteString("dial " + address(rest[0]) + "\n")
	if noauth {
		s.WriteString("noauth\n")
	} else {
		p := proto(pflag)
		if !haveUser {
			who = authproto.User()
		}
		k, cert := key(keyfile, who, p == "hybrid")
		if k.Hybrid() != (p == "hybrid") {
			fail(string(pgauth.EWrongAlg))
		}
		s.WriteString("user " + who + "\nkey " + pgauth.Hex(k.Seed()) + "\nproto " + p + "\n")
		if cert != nil {
			s.WriteString("cert " + cert.Encode() + "\n")
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
