// Command auth/verify checks the certificate in file (a certificate or an
// authinfo) as a server would, against a signer list (default
// /lib/keys/signers) and a revocation list (default /lib/keys/revoked) at
// time (default now). It prints the certificate's name, key, expiry and
// each signer with its standing, and fails with the error a server would
// give (docs/auth.md).
//
// With -m it checks a module's signatures (docs/signing.md) against the
// module signer list (default /lib/keys/modules) and the revocation
// list. It prints the module's digest and ABI version and, for each
// signature, the signing key, its standing and the names in its
// certificates, and fails with the error the loader would give under
// require. Classical (Ed25519) signatures count only when -p names
// classical ("-p hybrid,classical"), as under a policy that takes them.
//
//	auth/verify [-m] [-p proto] [-s signers] [-r revoked] [-u name] [-t time] file
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/verify [-m] [-p proto] [-s signers] [-r revoked] [-u name] [-t time] file")
	module := false
	signers, revoked, name, proto := "", authproto.Revoked, "", ""
	checkName := false
	now := authproto.Now()
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			module = true
		case 'p':
			proto = a.EArg()
		case 's':
			signers = a.EArg()
		case 'r':
			revoked = a.EArg()
		case 'u':
			name, checkName = a.EArg(), true
		case 't':
			s := a.EArg()
			t, ok := authproto.Expiry(s)
			if !ok {
				authproto.Fail("bad expiry " + s)
			}
			now = t
		default:
			a.Usage()
		}
	}
	rest := a.Argv()
	if len(rest) != 1 {
		a.Usage()
	}
	file := rest[0]
	text, err := os.ReadFile(file)
	if err != nil {
		authproto.Fail(authproto.FileErr(file, err))
	}
	if signers == "" {
		signers = authproto.Signers
		if module {
			signers = authproto.Modules
		}
	}
	sb, _ := os.ReadFile(signers)
	anchors := pgauth.ParseListLenient(sb)
	var rl []pgauth.KeyID
	damaged := false
	if rb, err := os.ReadFile(revoked); err == nil {
		var ok bool
		rl, ok = pgauth.ParseList(rb)
		damaged = !ok
	}
	if module {
		classical := false
		if proto != "" {
			ps, err := authproto.Parse(proto)
			if err != nil {
				authproto.Fail(err.Error())
			}
			for _, p := range ps {
				classical = classical || p == authproto.Classical
			}
		}
		verifyModule(file, text, &modsig.Trust{Anchors: anchors, Revoked: rl, Damaged: damaged, Now: now, Classical: classical})
		return
	}
	c, ok := pgauth.FindCert(text)
	if !ok {
		authproto.Fail(file + ": bad certificate")
	}
	pol := &pgauth.Policy{Signers: anchors, Revoked: rl, Damaged: damaged, Now: now}
	out := "name " + c.Name + "\nkey " + c.Key.ID().String() + "\nexpires " + authproto.ShowExpiry(c.Expires) + "\n"
	for _, s := range c.Sigs {
		id := s.Signer.ID()
		standing := "untrusted"
		switch {
		case !c.SignedBy(id):
			standing = "bad"
		case damaged || pgauth.Contains(rl, id):
			standing = "revoked"
		case pgauth.Contains(anchors, id):
			standing = "trusted"
		}
		out += "signer " + id.String() + " " + standing + "\n"
	}
	os.Stdout.WriteString(out)
	if err := pgauth.CheckCert(pol, c, name, checkName); err != nil {
		authproto.Fail(err.Error())
	}
}

func verifyModule(file string, wasm []byte, t *modsig.Trust) {
	info, err := modsig.Inspect(wasm)
	if err != nil {
		authproto.Fail(file + ": " + err.Error())
	}
	out := "digest " + pgauth.Hex(info.Digest[:]) + "\n"
	if len(info.ABI) == 0 {
		out += "abi -\n"
	} else {
		out += "abi " + string(info.ABI) + "\n"
	}
	if entries, err := modsig.Entries(info); err == nil {
		for _, e := range entries {
			msg, ok := modsig.Message(info.Digest, info.ABI, e.Hybrid())
			if !ok {
				break
			}
			line := "signer " + e.Key.ID().String() + " " + modsig.EntryStanding(e, msg, t).String()
			var names []string
			for _, c := range e.Certs {
				names = append(names, c.Name)
			}
			if len(names) > 0 {
				line += " " + strings.Join(names, " ")
			}
			out += line + "\n"
		}
	}
	os.Stdout.WriteString(out)
	if _, err := modsig.Check(info, t); err != nil {
		authproto.Fail(err.Error())
	}
}
