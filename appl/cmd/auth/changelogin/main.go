// Command auth/changelogin, on the signer's machine, adds user to
// auth/keyfs (default /mnt/keys) or changes its password. The password
// is one line on standard input. The verifier stored is Argon2id at cost
// -C m,t (default 19456 KiB, 2 passes). expires is seconds since 1970 or
// never; a new user gets a year. Inferno's changelogin is /bin/changelogin.
//
//	auth/changelogin [-m mntpt] [-C m,t] [-e expires] user
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const year = 365 * 24 * 60 * 60

func put(path, value string) {
	fd, err := sys.Open(path, sys.OWRITE)
	if err != nil {
		authproto.Fail(path + ": " + sys.Errstr())
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if _, err := f.Write([]byte(value)); err != nil {
		authproto.Fail(authproto.FileErr(path, err))
	}
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/changelogin [-m mntpt] [-C m,t] [-e expires] user")
	mtpt := authproto.Keys
	m, t := uint32(pglogin.DefaultM), uint32(pglogin.DefaultT)
	expires, haveExp := uint64(0), false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mtpt = a.EArg()
		case 'C':
			var err error
			if m, t, err = authproto.Cost(a.EArg()); err != nil {
				authproto.Fail(err.Error())
			}
		case 'e':
			s := a.EArg()
			var ok bool
			if expires, ok = authproto.Expiry(s); !ok {
				authproto.Fail("bad expiry " + s)
			}
			haveExp = true
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) != 1 {
		a.Usage()
	}
	user := rest[0]
	if !pgauth.ValidName(user) {
		authproto.Fail("bad user name " + user)
	}
	pw, err := authproto.Password("password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	if pw == "" {
		authproto.Fail("empty password")
	}
	dir := mtpt + "/" + user
	_, serr := sys.Stat(dir)
	isnew := serr != nil
	if isnew {
		fd, err := sys.Create(dir, sys.OREAD, sys.DMDIR|0o700)
		if err != nil {
			authproto.Fail(dir + ": " + sys.Errstr())
		}
		os.NewFile(uintptr(fd), dir).Close()
	}
	kdf, err := pglogin.NewKdf(m, t)
	if err != nil {
		authproto.Fail(err.Error())
	}
	v, err := pglogin.NewVerifier([]byte(pw), kdf)
	if err != nil {
		authproto.Fail(err.Error())
	}
	put(dir+"/secret", v.Encode())
	if !haveExp && isnew {
		expires, haveExp = authproto.Now()+year, true
	}
	if haveExp {
		put(dir+"/expire", authproto.ShowExpiry(expires))
	}
	put(dir+"/status", "ok")
}
