// Package login is Inferno's Login module (appl/lib/login.b), the
// client of the inflogin service (logind): it proves knowledge of the
// user's password to the signer with an encrypted key exchange and gets
// back the signer's certificate of a new key, an Authinfo.
package login

import (
	"crypto/rand"
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/ssl"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func errorf(format string, a ...any) error { return sys.Error(fmt.Sprintf(format, a...)) }

// Login dials the signer at dest ($SIGNER by default) and logs in as id.
func Login(id, password, dest string) (*keyring.Authinfo, error) {
	if dest == "" {
		dest = "$SIGNER"
	}
	dest = dial.Netmkaddr(dest, "net", "inflogin")
	lc, err := dial.Dial(dest, "")
	if err != nil {
		return nil, errorf("can't contact login service: %s: %s", dest, err)
	}
	defer lc.Cfd.Close()
	defer lc.Dfd.Close()
	return Conn(lc.Dfd, id, password)
}

// PasswordKey is the digest of a password the signer keeps, as
// changelogin stores it in keyfs: SHA-1 of the password.
func PasswordKey(password string) []byte {
	d := make([]byte, keyring.SHA1dlen)
	keyring.SHA1([]byte(password), d, nil)
	return d
}

// RC4Key derives the key for the encrypted exchange from the password
// digest and the client's initialisation vector.
func RC4Key(pw, ivec []byte) []byte {
	k := make([]byte, 8)
	for i := range k {
		k[i] = pw[i] ^ pw[8+i]
	}
	for i := 0; i < 4; i++ {
		k[i] ^= pw[16+i]
	}
	for i := range k {
		k[i] ^= ivec[i]
	}
	return k
}

// Conn runs the login protocol on an open connection to logind.
func Conn(fd *os.File, id, password string) (*keyring.Authinfo, error) {
	c, err := ssl.Connect(fd)
	if err != nil {
		return nil, sys.Error("can't push ssl: " + err.Error())
	}
	defer c.Cfd.Close()
	defer c.Dfd.Close()
	d := c.Dfd

	// user->CA name; CA->user ack
	if keyring.PutString(d, id) != nil {
		return nil, errorf("can't send user name: %s", sys.Errstr())
	}
	s, err := keyring.GetString(d)
	if err != nil {
		return nil, sys.Error("remote: " + err.Error())
	}
	if s != id {
		return nil, sys.Error("unexpected reply from signer: " + s)
	}

	// user->CA ivec
	ivec := make([]byte, 8)
	rand.Read(ivec)
	if keyring.PutByteArray(d, ivec) != nil {
		return nil, errorf("can't send initialization vector: %s", sys.Errstr())
	}

	// Start encrypting with the password.
	pw := RC4Key(PasswordKey(password), ivec)
	if err := ssl.Secret(c, pw, pw); err != nil {
		return nil, sys.Error("can't set secret: " + err.Error())
	}
	if ssl.Ctl(c, "alg rc4") != nil {
		return nil, errorf("can't push alg rc4: %s", sys.Errstr())
	}

	// CA->user key(alpha**r0 mod p)
	if s, err = keyring.GetString(d); err != nil {
		if err.Error() == "failure" {
			return nil, sys.Error("name or secret incorrect (alpha**r0 mod p)")
		}
		return nil, sys.Error("remote:" + err.Error())
	}

	// Stop encrypting.
	if ssl.Ctl(c, "alg clear") != nil {
		return nil, errorf("can't push alg clear: %s", sys.Errstr())
	}
	alphar0 := keyring.B64toIP(s)

	// CA->user alpha, p
	info := &keyring.Authinfo{}
	if s, err = keyring.GetString(d); err != nil {
		if err.Error() == "failure" {
			return nil, sys.Error("name or secret incorrect (alpha)")
		}
		return nil, sys.Error("remote: " + err.Error())
	}
	info.Alpha = keyring.B64toIP(s)
	if s, err = keyring.GetString(d); err != nil {
		if err.Error() == "failure" {
			return nil, sys.Error("name or secret incorrect (p)")
		}
		return nil, sys.Error("remote: " + err.Error())
	}
	info.P = keyring.B64toIP(s)
	if alphar0 == nil || info.Alpha == nil || info.P == nil {
		return nil, sys.Error("bogus diffie hellman constants")
	}
	bits, abits := info.P.Bits(), info.Alpha.Bits()
	if abits > bits || abits < 2 {
		return nil, sys.Error("bogus diffie hellman constants")
	}

	// user->CA alpha**r1 mod p
	r1 := keyring.Random(bits/4, bits)
	alphar1 := info.Alpha.Expmod(r1, info.P)
	if keyring.PutString(d, alphar1.IPtoB64()) != nil {
		return nil, errorf("can't send (alpha**r1 mod p): %s", sys.Errstr())
	}

	// Digest with alpha**(r0*r1) mod p from here on.
	secret := alphar0.Expmod(r1, info.P).IPtoBytes()
	if err := ssl.Secret(c, secret, secret); err != nil {
		return nil, sys.Error("can't set digesting: " + err.Error())
	}
	if ssl.Ctl(c, "alg sha1") != nil {
		return nil, errorf("can't push alg sha1: %s", sys.Errstr())
	}

	// CA->user CA's public key
	if s, err = keyring.GetString(d); err != nil {
		return nil, sys.Error("can't get signer's public key: " + err.Error())
	}
	info.SPK = keyring.StrtoPK(s)
	if info.SPK == nil {
		return nil, sys.Error("can't get signer's public key: bad public key")
	}

	// A new key pair; user->CA its public key; CA->user the certificate.
	info.MySK = keyring.GenSKfromPK(info.SPK, id)
	info.MyPK = keyring.SKtoPK(info.MySK)
	if keyring.PutString(d, keyring.PKtoStr(info.MyPK)) != nil {
		return nil, errorf("can't send your public: %s", sys.Errstr())
	}
	if s, err = keyring.GetString(d); err != nil {
		return nil, sys.Error("can't get certificate: " + err.Error())
	}
	info.Cert = keyring.StrtoCert(s)
	if info.Cert == nil {
		return nil, sys.Error("can't get certificate: bad certificate")
	}
	return info, nil
}
