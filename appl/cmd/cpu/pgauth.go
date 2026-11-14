package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/rstyx"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// pgmain is cpu over pgauth's rstyx protocol (docs/rcmd.md), the Rust
// sibling's, unless the protocol named by -p, $authproto or
// /lib/keys/proto is keyring: then it returns false and Inferno's cpu
// runs, without -p in its arguments.
//
//	cpu [-AS] [-k keyfile] [-e alg] [-x dir] [-p proto] host [cmd [arg...]]
func pgmain(module string) bool {
	proto, rest := takeProto(os.Args)
	protos, err := authproto.Resolve(proto)
	if err != nil {
		fail(err.Error())
	}
	if protos[0] == authproto.Keyring {
		os.Args = rest
		return false
	}
	a := arg.New(os.Args)
	a.SetUsage(os.Args[0] + " [-AS] [-k keyfile] [-e alg] [-x dir] [-p proto] host [cmd [arg...]]")
	o := &rstyx.Opts{Module: module, Hybrid: protos[0] == authproto.Hybrid, NoAuth: protos[0] == authproto.None}
	alg := rstyx.Cipher
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'A':
			o.NoAuth = true
		case 'S':
			o.SrvAuth = true
		case 'k', 'f':
			o.Keyfile = a.EArg()
		case 'e', 'C':
			alg = a.EArg()
		case 'x':
			o.Export = a.EArg()
		case 'p':
			a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 || (o.NoAuth && o.SrvAuth) {
		a.Usage()
	}
	if err := rstyx.CheckAlg(alg, o.NoAuth); err != nil {
		fail(err.Error())
	}
	cmd := rstyx.Command(args[1:])
	if module == "/bin/cpu.wasm" {
		cmd = "auxi/cpuslave " + cmd
	}
	st, err := rstyx.Run(o, args[0], cmd)
	if err != nil {
		fail(err.Error())
	}
	if st != "" {
		sys.Exits(st)
	}
	return true
}

// takeProto finds -p's value in argv (before the first non-option) and
// returns argv without it.
func takeProto(argv []string) (string, []string) {
	out := []string{argv[0]}
	proto := ""
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" || len(a) < 2 || a[0] != '-' {
			return proto, append(out, argv[i:]...)
		}
		if a == "-p" && i+1 < len(argv) {
			proto = argv[i+1]
			i++
			continue
		}
		out = append(out, a)
		// Options that take an argument consume the next word.
		if len(a) == 2 && (a[1] == 'k' || a[1] == 'f' || a[1] == 'e' || a[1] == 'C' || a[1] == 'x' || a[1] == 'a') && i+1 < len(argv) {
			out = append(out, argv[i+1])
			i++
		}
	}
	return proto, out
}

func fail(e string) {
	os.Stderr.WriteString("cpu: " + e + "\n")
	sys.Exits("fail:errors")
}
