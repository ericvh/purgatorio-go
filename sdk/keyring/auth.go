package keyring

import (
	"errors"
	"io"
	"math/big"
	"os"
	"strconv"
	"time"
)

const maxbuf = 4096

// SetID changes the prog's user name when authentication asks for it, as
// Inferno's setid; it writes /dev/user, which only the host owner may do.
var SetID = func(name string) { _ = os.WriteFile("/dev/user", []byte(name), 0) }

// Auth is Keyring's auth, the station-to-station protocol of
// keyring-auth(2): both ends prove they hold a key certified by the
// same signer and agree a Diffie-Hellman secret. It returns the peer's
// name and the secret. With setid, the prog's user becomes the peer's
// name, or nobody on failure.
func Auth(rw io.ReadWriter, info *Authinfo, setid bool) (string, []byte, error) {
	id, secret, err := auth(rw, info)
	if setid {
		if err != nil {
			SetID("nobody")
		} else {
			SetID(id)
		}
	}
	return id, secret, err
}

func auth(fd io.ReadWriter, info *Authinfo) (string, []byte, error) {
	var (
		err      string // the reason for failure
		fromPeer bool   // err came from reading the peer (Inferno's err == buf)
		id       string
		secret   []byte
		done     bool
	)
	fail := func(e string) { err = e }
	readerr := func(e error) {
		err, fromPeer = e.Error(), true
		var re *errRemote
		if errors.As(e, &re) {
			err = re.s
		}
	}

	func() {
		if SendMsg(fd, []byte("1")) != nil {
			fail(MSG)
			return
		}
		buf, e := getmsg(fd, maxbuf-1)
		if e != nil {
			readerr(e)
			return
		}
		if v, _ := strconv.Atoi(string(buf)); v != 1 || len(buf) > 4 {
			fail("incompatible authentication protocol")
			return
		}
		switch {
		case info == nil:
			fail("no authentication information")
			return
		case info.P == nil:
			fail("missing diffie hellman mod")
			return
		case info.Alpha == nil:
			fail("missing diffie hellman base")
			return
		case info.MySK == nil:
			fail("bad sk arg")
			return
		case info.MyPK == nil:
			fail("bad pk arg")
			return
		case info.Cert == nil:
			fail("bad certificate arg")
			return
		case info.SPK == nil:
			fail("bad signer key arg")
			return
		}
		p, alpha := info.P.v, info.Alpha.v
		if p.Sign() < 0 {
			fail("-ve modulus")
			return
		}
		r0 := mprand(p.BitLen())
		alphar0 := new(big.Int).Exp(alpha, r0, p)

		// alpha**r0 mod p, my certificate and my public key.
		if SendMsg(fd, []byte(bigtobase64(alphar0))) != nil ||
			SendMsg(fd, []byte(CerttoStr(info.Cert))) != nil ||
			SendMsg(fd, []byte(PKtoStr(info.MyPK))) != nil {
			fail(MSG)
			return
		}

		// alpha**r1 mod p, his certificate and his public key.
		if buf, e = getmsg(fd, maxbuf-1); e != nil {
			readerr(e)
			return
		}
		a1 := B64toIP(string(buf))
		if a1 == nil || p.Cmp(a1.v) <= 0 {
			fail("implausible parameter value")
			return
		}
		alphar1 := a1.v
		if alphar0.Cmp(alphar1) == 0 {
			fail("possible replay attack")
			return
		}
		if buf, e = getmsg(fd, maxbuf-1); e != nil {
			readerr(e)
			return
		}
		hiscert := StrtoCert(string(buf))
		if hiscert == nil {
			fail("bad certificate syntax")
			return
		}
		if buf, e = getmsg(fd, maxbuf-1); e != nil {
			readerr(e)
			return
		}
		hispk := StrtoPK(string(buf))
		if hispk == nil {
			fail("bad public key")
			return
		}
		if !verifydata(info.SPK, hiscert, buf) {
			fail("pk doesn't match certificate")
			return
		}
		if hiscert.Exp != 0 && int64(hiscert.Exp) <= time.Now().Unix() {
			fail("certificate expired")
			return
		}

		// Sign alpha**r0 and alpha**r1 and send it.
		alphacert := signdata(info.MySK, "sha1", 0, []byte(bigtobase64(alphar0)+bigtobase64(alphar1)))
		if SendMsg(fd, []byte(CerttoStr(alphacert))) != nil {
			fail(MSG)
			return
		}

		// His signature of alpha**r1 and alpha**r0.
		if buf, e = getmsg(fd, maxbuf-1); e != nil {
			readerr(e)
			return
		}
		alphacert = StrtoCert(string(buf))
		if alphacert == nil {
			fail("alpha**r1 doesn't match certificate")
			return
		}
		if !verifydata(hispk, alphacert, []byte(bigtobase64(alphar1)+bigtobase64(alphar0))) {
			fail("bad certificate")
			return
		}

		// Authenticated, with the common secret alpha**(r0*r1).
		id = hispk.Owner
		secret = mptobe(new(big.Int).Exp(alphar1, r0, p))
		done = true
	}()

	if !done {
		if fromPeer {
			senderr(fd, "missing your authentication data", true)
		} else {
			senderr(fd, err, true)
		}
	} else {
		SendMsg(fd, []byte("OK"))
	}

	if !fromPeer {
		for {
			buf, e := getmsg(fd, maxbuf-1)
			if e != nil {
				if done || err == "" {
					var re *errRemote
					if errors.As(e, &re) && len(re.s) > 1 {
						err = re.s
					} else {
						err = MSG
					}
				}
				done = false
				break
			}
			if string(buf) == "OK" {
				break
			}
		}
	}
	if !done {
		if err == "" {
			err = MSG
		}
		return "", nil, Error(err)
	}
	return id, secret, nil
}
