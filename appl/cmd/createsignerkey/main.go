// Command createsignerkey makes the signer's key (Inferno
// createsignerkey(8)): a self-signed key for name-of-owner and
// Diffie-Hellman parameters, written as an Authinfo to /keydb/signerkey
// or keyfile.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	skexpire = 0    // the signer key never expires
	pkmodlen = 1024 // public key modulus
	dhmodlen = 1024 // Diffie-Hellman modulus
)

var algs = []string{"rsa", "elgamal"} // the first is the default

func main() {
	a := arg.New(os.Args)
	a.SetUsage("createsignerkey [-a algorithm] [-f keyfile] [-e ddmmyyyy] [-b size-in-bits] name-of-owner")
	alg := algs[0]
	filename := "/keydb/signerkey"
	expire := skexpire
	bits := pkmodlen
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'a':
			alg = a.Arg()
			if alg == "" {
				a.Usage()
			}
			known := false
			for _, x := range algs {
				known = known || x == alg
			}
			if !known {
				fatal("unknown algorithm: " + alg)
			}
		case 'f', 'k':
			filename = a.EArg()
		case 'e':
			var err string
			if expire, err = checkdate(a.EArg()); err != "" {
				fatal(err)
			}
		case 'b':
			bits, _ = strconv.Atoi(a.EArg())
			if bits < 32 || bits > 4096 {
				fatal("modulus must be in the range of 32 to 4096 bits")
			}
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	owner := args[0]

	// A local key, self-signed.
	sk := keyring.GenSK(alg, owner, bits)
	if sk == nil {
		fatal(fmt.Sprintf("algorithm %s not configured in system", alg))
	}
	info := &keyring.Authinfo{MySK: sk, MyPK: keyring.SKtoPK(sk), SPK: keyring.SKtoPK(sk)}
	pkbuf := []byte(keyring.PKtoStr(info.MyPK))
	info.Cert = keyring.Sign(sk, expire, keyring.SHA1(pkbuf, nil, nil), "sha1")
	info.Alpha, info.P = keyring.DHparams(dhmodlen)
	if err := keyring.WriteAuthinfo(filename, info); err != nil {
		fatal(fmt.Sprintf("can't write signerkey file %s: %s", filename, err))
	}
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "createsignerkey: %s\n", s)
	sys.Exits("fail:error")
}

// checkdate turns ddmmyyyy into the last second of that day.
func checkdate(word string) (int, string) {
	if len(word) != 8 {
		return 0, "!date must be in form ddmmyyyy"
	}
	now := daytime.Now()
	tm := daytime.Local(now)
	tm.Sec, tm.Min, tm.Hour = 59, 59, 24
	tm.Mday, _ = strconv.Atoi(word[0:2])
	if tm.Mday > 31 || tm.Mday < 1 {
		return 0, "!bad day of month"
	}
	mon, _ := strconv.Atoi(word[2:4])
	tm.Mon = mon - 1
	if tm.Mon > 11 || tm.Mday < 0 {
		return 0, "!bad month"
	}
	year, _ := strconv.Atoi(word[4:8])
	tm.Year = year - 1900
	if tm.Year < 70 {
		return 0, "!bad year"
	}
	t := daytime.Tm2epoch(tm)
	if t < now {
		return 0, "!expiration date must be in the future"
	}
	return t, ""
}
