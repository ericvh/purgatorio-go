// Package auth is Inferno's Auth module (appl/lib/auth.b): mutual
// authentication of a connection with keyring.Auth, then agreement on
// the ssl algorithms to push onto it.
//
// A client such as mount -k keyfile -C alg reads its key with
// keyring.ReadAuthinfo and calls Client on the connection before
// handing the returned fd to sys.Mount; a server such as export or
// styxlisten calls Server and serves the returned fd.
package auth

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/ssl"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Init is a no-op kept for Inferno's interface.
func Init() error { return nil }

func errorf(format string, a ...any) error { return sys.Error(fmt.Sprintf(format, a...)) }

// Server authenticates the client on fd with ai and takes the client's
// ssl algorithms, which must all be in algs unless algs is nil. It
// returns the fd to use, with ssl pushed unless the algorithm is none,
// and the client's name. With setid the prog takes the client's name.
func Server(algs []string, ai *keyring.Authinfo, fd *os.File, setid bool) (*os.File, string, error) {
	id, secret, err := keyring.Auth(fd, ai, setid)
	if err != nil {
		if ai == nil && err.Error() == "no authentication information" {
			err = sys.Error("no server certificate")
		}
		return nil, "", err
	}
	buf, err := keyring.GetMsg(fd)
	if err != nil || len(buf) == 0 {
		why := ""
		if err != nil {
			why = err.Error()
		}
		return nil, "", errorf("can't read client ssl algorithm: %s", why)
	}
	var alg []string
	for _, calg := range strings.FieldsFunc(string(buf), func(r rune) bool { return r == ' ' || r == '/' }) {
		if algs != nil && !contains(algs, calg) {
			return nil, "", sys.Error("unsupported client algorithm: " + calg)
		}
		alg = append(alg, calg)
	}
	a := strings.Join(alg, " ")
	if a == "" || a == "none" {
		return fd, id, nil
	}
	c, err := pushssl(fd, secret, secret, a)
	if err != nil {
		return nil, "", sys.Error("push ssl: " + err.Error())
	}
	return c, id, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Client authenticates with the server on fd and asks for ssl with alg
// (none when empty). It returns the fd to use and the server's name.
func Client(alg string, ai *keyring.Authinfo, fd *os.File) (*os.File, string, error) {
	if alg == "" {
		alg = "none"
	}
	id, secret, err := keyring.Auth(fd, ai, false)
	if err != nil {
		return nil, "", err
	}
	if err := keyring.SendMsg(fd, []byte(alg)); err != nil {
		return nil, "", errorf("can't send ssl algorithm: %s", sys.Errstr())
	}
	if alg == "none" {
		return fd, id, nil
	}
	c, err := pushssl(fd, secret, secret, alg)
	if err != nil {
		return nil, "", sys.Error("push ssl: " + err.Error())
	}
	return c, id, nil
}

// Auth is Client with the key named by keyspec when ai is nil; it also
// returns the Authinfo used.
func Auth(ai *keyring.Authinfo, keyspec, alg string, fd *os.File) (*os.File, *keyring.Authinfo, string, error) {
	if ai == nil && keyspec != "" {
		var err error
		if ai, err = Key(keyspec); err != nil {
			return nil, nil, "", errorf("can't obtain key: %s", err)
		}
	}
	c, id, err := Client(alg, ai, fd)
	return c, ai, id, err
}

// pushssl pushes ssl onto fd with the secrets and turns on alg.
func pushssl(fd *os.File, secretin, secretout []byte, alg string) (*os.File, error) {
	c, err := ssl.Connect(fd)
	if err != nil {
		return nil, sys.Error("can't connect ssl: " + err.Error())
	}
	if err := ssl.Secret(c, secretin, secretout); err != nil {
		return nil, sys.Error("can't write secret: " + err.Error())
	}
	err = ssl.Ctl(c, "alg "+alg)
	// The data fd keeps the conversation; holding ctl open would keep
	// the connection up after the caller closes it.
	c.Cfd.Close()
	if err != nil {
		c.Dfd.Close()
		return nil, errorf("can't push algorithm %s: %s", alg, err)
	}
	return c.Dfd, nil
}

// Key reads the Authinfo keyspec names; see Keyfile.
func Key(keyspec string) (*keyring.Authinfo, error) {
	f := Keyfile(keyspec)
	if f == "" {
		return nil, sys.Error(sys.Errstr())
	}
	return keyring.ReadAuthinfo(f)
}

// Keyfile finds the key file for keyspec, attribute=value pairs: key
// names a file outright (relative to the user's keyring directory);
// otherwise server, service and dom find the closest match among
// [net!]addr[!svc] in /usr/user/keyring, then default.
func Keyfile(keyspec string) string {
	al := parseattr(keyspec)
	if k := get(al, "key"); k != "" {
		if k[0] == '/' || strings.HasPrefix(k, "./") || strings.HasPrefix(k, "../") {
			return k
		}
		return keydir() + k
	}
	net := "net"
	svc := get(al, "service")
	addr := get(al, "server")
	if f := strings.FieldsFunc(addr, func(r rune) bool { return r == '!' }); len(f) > 1 {
		net, addr = f[0], f[1]
	}
	keyname := "default"
	if addr != "" {
		keyname = addr
	}
	kd := keydir()
	if dom := get(al, "dom"); dom != "" {
		if exists(kd + dom) {
			return kd + dom
		}
	}
	if keyname == "default" {
		return kd + "default"
	}
	l := []string{net + "!"}
	if net == "net" {
		l = []string{"net!", "tcp!"}
	}
	if svc != "" {
		for _, n := range l {
			if f := kd + n + keyname + "!" + svc; exists(f) {
				return f
			}
		}
	}
	for _, n := range l {
		if f := kd + n + keyname; exists(f) {
			return f
		}
	}
	if exists(kd + keyname) {
		return kd + keyname
	}
	if exists(kd + "default") {
		return kd + "default"
	}
	return kd + keyname
}

func keydir() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil || len(b) == 0 {
		sys.Werrstr("can't read /dev/user")
		return ""
	}
	return "/usr/" + string(b) + "/keyring/"
}

func exists(f string) bool {
	_, err := sys.Stat(f)
	return err == nil
}

type attr struct {
	val   bool
	name  string
	value string
}

func parseattr(s string) []attr {
	var al []attr
	for _, n := range strings.Fields(s) {
		a := attr{name: n}
		if i := strings.IndexByte(n, '='); i >= 0 {
			a = attr{val: true, name: n[:i], value: n[i+1:]}
		}
		if a.name == "" {
			continue
		}
		al = append(al, a)
	}
	return al
}

func get(al []attr, n string) string {
	for _, a := range al {
		if a.name == n && a.val {
			return a.value
		}
	}
	return ""
}
