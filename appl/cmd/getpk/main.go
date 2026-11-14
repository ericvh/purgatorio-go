// Command getpk prints the public key in each key file (Inferno
// getpk(8)): its SHA-1 hash in hex, or with -a the key itself; -s takes
// the signer's key instead of the owner's, and -u adds the owner's name.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("usage: getpk [-asu] file...")
	var aflag, sflag, uflag bool
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			sflag = true
		case 'a':
			aflag = true
		case 'u':
			uflag = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	multi := len(args) > 1
	for _, f := range args {
		info, err := keyring.ReadAuthinfo(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "getpk: cannot read %s: %s\n", f, err)
			continue
		}
		pk := info.MyPK
		if sflag {
			pk = info.SPK
		}
		s := keyring.PKtoStr(pk)
		if !aflag {
			d := make([]byte, keyring.SHA1dlen)
			keyring.SHA1([]byte(s), d, nil)
			s = fmt.Sprintf("%X", d)
		}
		if multi {
			s = f + ": " + s
		}
		if uflag {
			s += " " + pk.Owner
		}
		fmt.Printf("%s\n", s)
	}
}
