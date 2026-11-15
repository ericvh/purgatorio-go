package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// pgmain is the Rust sibling's import, unless the protocol named by -p,
// $authproto or /lib/keys/proto is keyring: then it returns false and
// Inferno's import (Plan 9's exportfs protocol) runs, without -p. It
// dials addr and mounts the Styx server there, authenticating with
// pgauth's handshake (hybrid or classical) unless -A; -S requires the
// server to prove it is the host addr names, as mount -S does. -9 tree
// is for a Plan 9 exportfs (or 9export): unless -A, import first
// authenticates with p9any through factotum, as Plan 9's import does,
// then names the tree and starts 9P in the clear.
//
//	import [-AS] [-k keyfile] [-9 tree] [-p proto] addr mtpt
func pgmain() bool {
	proto, rest := "", []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-p" && i+1 < len(os.Args) {
			proto = os.Args[i+1]
			i++
			continue
		}
		rest = append(rest, os.Args[i])
	}
	protos, err := authproto.Resolve(proto)
	if err != nil {
		fail("usage", err.Error())
	}
	if protos[0] == authproto.Keyring {
		os.Args = rest
		return false
	}
	a := arg.New(rest)
	a.SetUsage("import [-AS] [-k keyfile] [-9 tree] [-p proto] addr mtpt")
	noauth, srvauth := protos[0] == authproto.None, false
	keyfile, tree := "", ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'A':
			noauth = true
		case 'S':
			srvauth = true
		case 'k':
			keyfile = a.EArg()
		case '9':
			tree = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 2 || (noauth || tree != "") && srvauth {
		a.Usage()
	}
	addr := dial.Netmkaddr(args[0], "net", "styx")
	c, err := dial.Dial(addr, "")
	if err != nil {
		fail("dial failed", "can't dial "+addr+": "+err.Error())
	}
	fd := int(c.Dfd.Fd())
	if tree != "" {
		if !noauth {
			fac, err := authproto.OpenFactotum()
			if err != nil {
				fail("factotum", "can't open "+authproto.Factotum+"/rpc: "+err.Error())
			}
			if _, err := authproto.Proxy(c.Dfd, fac, "proto=p9any role=client"); err != nil {
				fail("auth", "can't authenticate import: "+err.Error())
			}
			fac.Close()
		}
		c.Dfd.Write([]byte(tree))
		buf := make([]byte, 128)
		n, _ := c.Dfd.Read(buf)
		if string(buf[:n]) != "OK" {
			fail("import", "bad remote tree: "+string(buf[:n]))
		}
	}
	if noauth || tree != "" {
		if err := sys.Mount(fd, -1, args[1], sys.MREPL, ""); err != nil {
			fail("mount failed", "mount failed: "+sys.Errstr())
		}
		return true
	}
	hybrid := protos[0] == authproto.Hybrid
	var afd *os.File
	if keyfile == "" && authproto.HaveFactotum() {
		afd, err = authproto.AuthenticateAgent(fd, "", hybrid)
	} else if k, cert, kerr := authproto.Key(keyfile, hybrid); kerr != nil {
		err = kerr
	} else {
		afd, err = authproto.Authenticate(fd, "", hybrid, k, cert)
	}
	if err != nil {
		fail("auth", "authentication failed: "+err.Error())
	}
	flags := sys.MREPL
	if srvauth {
		if err := authproto.CheckHost(afd, hybrid, authproto.Host(addr), authproto.Now()); err != nil {
			fail("auth", err.Error())
		}
		flags |= sys.MSRVAUTH
	}
	if err := sys.Mount(fd, int(afd.Fd()), args[1], flags, ""); err != nil {
		fail("mount failed", "mount failed: "+sys.Errstr())
	}
	return true
}
