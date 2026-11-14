// Command logind is Inferno's inflogin service (logind(8)): run on a
// connection as standard input, it certifies a user who proves
// knowledge of the password kept in keyfs (/mnt/keys), with the
// signer's key in /keydb/signerkey. getauthinfo is the client.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/login"
	"github.com/ericvh/purgatorio-go/sdk/ssl"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const timelimit = 5 * 60 * 1000 // milliseconds

var (
	keydb  = "/mnt/keys"
	stderr *os.File
)

func main() {
	stderr = os.Stderr
	if len(os.Args) == 3 && os.Args[1] == "-T" {
		stalk(os.Args[2])
		return
	}
	if f, err := os.OpenFile("/dev/cons", os.O_WRONLY, 0); err == nil {
		stderr = f
	}
	c, err := ssl.Connect(os.Stdin)
	if err != nil {
		fatal("pushing ssl: " + err.Error())
	}
	// A time limit, so that a dead connection is recovered well before
	// TCP gives up on it.
	grp, _ := sys.Pctl(sys.NEWPGRP, nil)
	tpid := stalker(grp)
	if e := dologin(c); e != "" {
		fmt.Fprintf(stderr, "logind: %s\n", e)
		keyring.PutError(c.Dfd, e)
	}
	if tpid > 0 {
		kill(tpid, "kill")
	}
}

func dologin(c *dial.Connection) string {
	info, e := signerkey("/keydb/signerkey")
	if info == nil {
		return "can't read signer's own key: " + e
	}
	d := c.Dfd

	// The user name; acknowledge it.
	name, err := keyring.GetString(d)
	if err != nil {
		return err.Error()
	}
	keyring.PutString(d, name)

	ivec, err := keyring.GetByteArray(d)
	if err != nil {
		return "can't get initialization vector: " + err.Error()
	}
	if len(ivec) < 8 {
		return "can't get initialization vector: too short"
	}

	pw := getsecret(name)
	if pw == nil {
		return fmt.Sprintf("no password entry for %s: %s", name, sys.Errstr())
	}
	if len(pw) < keyring.SHA1dlen {
		return "bad password for " + name + ": not SHA1 hashed?"
	}
	userexp, ok := getexpiry(name)
	if !ok {
		return fmt.Sprintf("expiry time for %s: %s", name, sys.Errstr())
	}

	// Our Diffie-Hellman part, alpha**r0 mod p, sent encrypted with the
	// password.
	bits := info.P.Bits()
	r0 := keyring.Random(bits/4, bits)
	alphar0 := info.Alpha.Expmod(r0, info.P)
	key := login.RC4Key(pw, ivec)
	if err := ssl.Secret(c, key, key); err != nil {
		return "can't set ssl secret: " + err.Error()
	}
	if ssl.Ctl(c, "alg rc4") != nil {
		return "can't push alg rc4: " + sys.Errstr()
	}
	if keyring.PutString(d, alphar0.IPtoB64()) != nil {
		return "can't send (alpha**r0 mod p): " + sys.Errstr()
	}
	if ssl.Ctl(c, "alg clear") != nil {
		return "can't clear alg: " + sys.Errstr()
	}
	if keyring.PutString(d, info.Alpha.IPtoB64()) != nil || keyring.PutString(d, info.P.IPtoB64()) != nil {
		return "can't send alpha, p: " + sys.Errstr()
	}

	s, err := keyring.GetString(d)
	if err != nil {
		return "can't get alpha**r1 mod p:" + err.Error()
	}
	alphar1 := keyring.B64toIP(s)
	if alphar1 == nil {
		return "can't get alpha**r1 mod p:bad number"
	}

	// Digest with alpha**(r0*r1) mod p from here on.
	secret := alphar1.Expmod(r0, info.P).IPtoBytes()
	if err := ssl.Secret(c, secret, secret); err != nil {
		return "can't set digest secret: " + err.Error()
	}
	if ssl.Ctl(c, "alg sha1") != nil {
		return "can't push alg sha1: " + sys.Errstr()
	}

	if keyring.PutString(d, keyring.PKtoStr(keyring.SKtoPK(info.MySK))) != nil {
		return "can't send signer's public key: " + sys.Errstr()
	}
	s, err = keyring.GetString(d)
	if err != nil {
		return "client public key: " + err.Error()
	}
	hispk := keyring.StrtoPK(s)
	if hispk == nil || hispk.Owner != name {
		return "pk name doesn't match user name"
	}

	cert := keyring.Sign(info.MySK, userexp, keyring.SHA1([]byte(s), nil, nil), "sha1")
	if keyring.PutString(d, keyring.CerttoStr(cert)) != nil {
		return "can't send certificate: " + sys.Errstr()
	}
	return ""
}

func fatal(msg string) {
	fmt.Fprintf(stderr, "logind: %s\n", msg)
	sys.Exits("")
}

func signerkey(filename string) (*keyring.Authinfo, string) {
	info, err := keyring.ReadAuthinfo(filename)
	if err != nil {
		return nil, "readauthinfo " + err.Error()
	}
	if info.Cert.Exp != 0 && info.Cert.Exp < daytime.Now() {
		return nil, "signer key expired"
	}
	return info, ""
}

func getsecret(id string) []byte {
	b, err := os.ReadFile(keydb + "/" + id + "/secret")
	if err != nil {
		return nil
	}
	return b
}

func getexpiry(id string) (int, bool) {
	b, err := os.ReadFile(keydb + "/" + id + "/expire")
	if err != nil {
		return 0, false
	}
	switch s := string(b); s {
	case "never":
		return 0, true
	case "expired":
		sys.Werrstr(fmt.Sprintf("entry for %s expired", id))
		return 0, false
	default:
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		return n, true
	}
}

// stalker starts a prog that kills the prog group after the time
// limit; it returns its pid, or 0 if it could not be started.
func stalker(grp int) int {
	pid, err := sys.Spawn("/bin/logind.wasm", []string{"logind", "-T", strconv.Itoa(grp)}, 0, nil)
	if err != nil {
		return 0
	}
	return pid
}

func stalk(grp string) {
	if sys.Sleep(timelimit) != nil {
		return // killed: the login finished
	}
	fmt.Fprintf(stderr, "logind: login timed out\n")
	kill(atoi(grp), "killgrp")
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func kill(pid int, how string) {
	f, err := os.OpenFile("/prog/"+strconv.Itoa(pid)+"/ctl", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	f.Write([]byte(how))
	f.Close()
}
