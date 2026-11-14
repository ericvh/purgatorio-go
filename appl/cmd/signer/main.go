// Command signer is Inferno's signer(8) service: on its standard input
// and output it sends the signer's public key and Diffie-Hellman
// parameters, takes the caller's public key, and returns a certificate
// for it blinded with a random pad. The pad and a digest of the
// exchange are kept in /keydb/signed/owner for countersigner. It runs in
// /keydb and makes signerkey there, an elgamal key, if there is none.
package main

import (
	"crypto/rand"
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	pkmodlen = 512 // public key modulus
	dhmodlen = 512 // Diffie-Hellman modulus
)

func main() {
	sys.Pctl(sys.FORKNS, nil)
	if err := sys.Chdir("/keydb"); err != nil {
		os.Stderr.WriteString("signer: no key database\n")
		sys.Exits("fail:no keydb")
	}
	if err := sign(); err != "" {
		fmt.Fprintf(os.Stderr, "signer: %s\n", err)
		sys.Exits("fail:error")
	}
}

func sign() string {
	info := signerkey("signerkey")
	if info == nil {
		return "can't read key"
	}
	stdin, stdout := os.Stdin, os.Stdout

	// The public part to the client.
	mypkbuf := []byte(keyring.PKtoStr(keyring.SKtoPK(info.MySK)))
	keyring.SendMsg(stdout, mypkbuf)
	keyring.SendMsg(stdout, []byte(info.Alpha.IPtoB64()))
	keyring.SendMsg(stdout, []byte(info.P.IPtoB64()))

	// The client's public key.
	hispkbuf, err := keyring.GetMsg(stdin)
	if err != nil {
		return "caller hung up"
	}
	hispk := keyring.StrtoPK(string(hispkbuf))
	if hispk == nil {
		return "illegal caller PK"
	}

	// Hash, sign, and blind.
	cert := keyring.Sign(info.MySK, 0, keyring.SHA1(hispkbuf, nil, nil), "sha1")
	if !keyring.Verify(info.MyPK, cert, keyring.SHA1(hispkbuf, nil, nil)) {
		return "bad signer certificate"
	}
	certbuf := []byte(keyring.CerttoStr(cert))
	blind := make([]byte, len(certbuf))
	rand.Read(blind)
	for i := range blind {
		certbuf[i] ^= blind[i]
	}

	// Sum the keys and the blinded certificate.
	state := keyring.MD5(mypkbuf, nil, nil)
	keyring.MD5(hispkbuf, nil, state)
	digest := make([]byte, keyring.MD5dlen)
	keyring.MD5(certbuf, digest, state)

	// Keep the sum and the pad.
	file := "signed/" + hispk.Owner
	fd, err := sys.Create(file, sys.OWRITE, 0o600)
	if err != nil {
		return "can't create " + file + ": " + sys.Errstr()
	}
	f := os.NewFile(uintptr(fd), file)
	if keyring.SendMsg(f, blind) != nil || keyring.SendMsg(f, digest) != nil {
		e := sys.Errstr()
		f.Close()
		sys.Remove(file)
		return "can't write " + file + ": " + e
	}
	f.Close()

	keyring.SendMsg(stdout, certbuf)
	return ""
}

func signerkey(filename string) *keyring.Authinfo {
	if info, err := keyring.ReadAuthinfo(filename); err == nil {
		return info
	}
	sk := keyring.GenSK("elgamal", "*", pkmodlen)
	info := &keyring.Authinfo{MySK: sk, MyPK: keyring.SKtoPK(sk), SPK: keyring.SKtoPK(sk)}
	pkbuf := []byte(keyring.PKtoStr(info.MyPK))
	info.Cert = keyring.Sign(sk, 0, keyring.SHA1(pkbuf, nil, nil), "sha1")
	info.Alpha, info.P = keyring.DHparams(dhmodlen)
	if err := keyring.WriteAuthinfo(filename, info); err != nil {
		fmt.Fprintf(os.Stderr, "can't write signerkey file: %s\n", err)
		return nil
	}
	return info
}
