// Command export serves the name space below dir as Styx on a connection
// (Inferno export(4)): the file named by connection, or standard input.
// With -a it returns at once and the export goes on in the background.
//
// The export authenticates its clients in the kernel with the protocol
// named by -p, $authproto or /lib/keys/proto (docs/auth.md): hybrid (the
// default), classical, or both ("hybrid,classical"); it then trusts the
// keys under /lib/keys in its own namespace and lets nobody in when it
// trusts none. keyring and none (or -A) ask for no proof, as Inferno's
// export, which leaves keyring authentication to the listener. -e
// refuses conversations left in the clear, and -k keyfile is the
// authinfo or seed the server proves itself with.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func usage() {
	os.Stderr.WriteString("Usage: export [-aAe] [-k keyfile] [-p proto] dir [connection]\n")
	sys.Exits("fail:usage")
}

func main() {
	argv := os.Args[1:]
	flag := sys.EXPWAIT
	noauth, encrypt := false, false
	keyfile, proto := "", ""
	for ; len(argv) > 0 && len(argv[0]) > 1 && argv[0][0] == '-'; argv = argv[1:] {
		if argv[0] == "--" {
			argv = argv[1:]
			break
		}
		opts := argv[0][1:]
		for i := 0; i < len(opts); i++ {
			switch opts[i] {
			case 'a':
				flag = sys.EXPASYNC
			case 'A':
				noauth = true
			case 'e':
				encrypt = true
			case 'k', 'p':
				v := opts[i+1:]
				if v == "" {
					if len(argv) < 2 {
						usage()
					}
					argv = argv[1:]
					v = argv[0]
				}
				if opts[i] == 'k' {
					keyfile = v
				} else {
					proto = v
				}
				i = len(opts)
			default:
				usage()
			}
		}
	}
	if len(argv) < 1 || len(argv) > 2 {
		usage()
	}
	auth := 0
	if !noauth {
		protos, err := authproto.Resolve(proto)
		if err != nil {
			os.Stderr.WriteString("export: " + err.Error() + "\n")
			sys.Exits("fail:usage")
		}
		auth = authproto.ExportFlags(protos)
	}
	if auth == 0 && (encrypt || keyfile != "") {
		os.Stderr.WriteString("export: -e and -k need hybrid or classical authentication\n")
		sys.Exits("fail:usage")
	}
	if encrypt {
		auth |= sys.EXPENCRYPT
	}
	var key []byte
	if keyfile != "" {
		var err error
		if key, err = os.ReadFile(keyfile); err != nil {
			os.Stderr.WriteString("export: can't read " + keyfile + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:open")
		}
	}
	fd := 0
	if len(argv) == 2 {
		var err error
		if fd, err = sys.Open(argv[1], sys.ORDWR); err != nil {
			os.Stderr.WriteString("export: can't open " + argv[1] + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:open")
		}
	}
	var err error
	if key != nil {
		err = sys.ExportKey(fd, argv[0], flag|auth, key)
	} else {
		err = sys.Export(fd, argv[0], flag|auth)
	}
	if err != nil {
		os.Stderr.WriteString("export: can't export: " + sys.Errstr() + "\n")
		sys.Exits("fail:export")
	}
}
