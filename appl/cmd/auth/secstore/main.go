// Command auth/secstore keeps files encrypted under a password. The
// store is a directory (default /usr/<user>/secstore), which may be an
// authenticated import of a server's. Each file is sealed with
// ChaCha20-Poly1305 under an Argon2id key from the password (one line on
// standard input), with its own salt, in the Rust sibling's format.
// -g decrypts a stored file into the current directory, -G to standard
// output, -p encrypts a local file into the store under its base name,
// and -r removes one. With no option it lists the store.
//
//	auth/secstore [-s dir] [-C m,t] [-g file | -G file | -p file | -r file]
package main

import (
	"os"
	"sort"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func base(name string) string {
	b := name[strings.LastIndexByte(name, '/')+1:]
	if b == "" || b == "." || b == ".." {
		authproto.Fail("bad file name " + name)
	}
	return b
}

func open(dir, name string) []byte {
	path := dir + "/" + base(name)
	blob, err := os.ReadFile(path)
	if err != nil {
		authproto.Fail(authproto.FileErr(path, err))
	}
	pw, err := authproto.Password("password")
	if err != nil {
		authproto.Fail(err.Error())
	}
	data, _, _, err := pglogin.Open([]byte(pw), blob)
	if err != nil {
		authproto.Fail(name + ": " + err.Error())
	}
	return data
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("auth/secstore [-s dir] [-C m,t] [-g file | -G file | -p file | -r file]")
	dir := "/usr/" + authproto.User() + "/secstore"
	m, t := uint32(pglogin.DefaultM), uint32(pglogin.DefaultT)
	op, file := 'l', ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			dir = a.EArg()
		case 'C':
			var err error
			if m, t, err = authproto.Cost(a.EArg()); err != nil {
				authproto.Fail(err.Error())
			}
		case 'g', 'G', 'p', 'r':
			op, file = o, a.EArg()
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	switch op {
	case 'l':
		ents, err := os.ReadDir(dir)
		if err != nil {
			authproto.Fail(authproto.FileErr(dir, err))
		}
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			os.Stdout.WriteString(n + "\n")
		}
	case 'g':
		b := base(file)
		if err := os.WriteFile(b, open(dir, file), 0o600); err != nil {
			authproto.Fail(authproto.FileErr(b, err))
		}
	case 'G':
		os.Stdout.Write(open(dir, file))
	case 'p':
		data, err := os.ReadFile(file)
		if err != nil {
			authproto.Fail(authproto.FileErr(file, err))
		}
		pw, err := authproto.Password("password")
		if err != nil {
			authproto.Fail(err.Error())
		}
		blob, err := pglogin.Seal([]byte(pw), m, t, data)
		if err != nil {
			authproto.Fail(err.Error())
		}
		if _, err := sys.Stat(dir); err != nil {
			authproto.MkdirsFor(dir + "/x")
		}
		if err := authproto.WriteSecret(dir+"/"+base(file), blob); err != nil {
			authproto.Fail(err.Error())
		}
	case 'r':
		path := dir + "/" + base(file)
		if err := sys.Remove(path); err != nil {
			authproto.Fail(path + ": " + sys.Errstr())
		}
	}
}
