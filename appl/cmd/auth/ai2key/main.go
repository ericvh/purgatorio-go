// Command auth/ai2key prints each Inferno authinfo file as a factotum
// key for the infauth protocol (Inferno ai2key(8)): one line of
// attributes holding the keys, the certificate and the Diffie-Hellman
// parameters, tagged with the dom, server and service the file name
// suggests.
//
// Inferno's ai2key adds the -t tag's leading space again for every file
// after the first; this one adds it once.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("ai2key [-t 'attr=value attr=value ...'] keyfile ...")
	tag := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 't':
			tag = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) == 0 {
		a.Usage()
	}
	if tag != "" {
		tag = " " + tag
	}
	now := daytime.Now()
	for _, keyfile := range args {
		ai, err := keyring.ReadAuthinfo(keyfile)
		if err != nil {
			os.Stderr.WriteString("ai2key: cannot read " + keyfile + ": " + err.Error() + "\n")
			sys.Exits("fail:error")
		}
		exp := uint32(ai.Cert.Exp)
		if exp != 0 && int(exp) <= now {
			fmt.Fprintf(os.Stderr, "ai2key: %s: certificate expired -- key ignored\n", keyfile)
			continue
		}
		expires := ""
		if exp != 0 {
			expires = fmt.Sprintf(" expires=%d", exp)
		}
		ha := ai.Cert.HA
		if ha == "sha" {
			ha = "sha1"
		}
		fmt.Printf("key proto=infauth%s %s sigalg=%s-%s user=%s signer=%s pk=%s !sk=%s spk=%s cert=%s dh-alpha=%s dh-p=%s%s\n",
			tag, locations(filename(keyfile)), ai.Cert.SA.Name, ha, quote.Quote(ai.MyPK.Owner), quote.Quote(ai.SPK.Owner),
			dnl(keyring.PKtoStr(ai.MyPK)), dnl(keyring.SKtoStr(ai.MySK)), dnl(keyring.PKtoStr(ai.SPK)),
			dnl(keyring.CerttoStr(ai.Cert)), ai.Alpha.IPtoStr(16), ai.P.IPtoStr(16), expires)
	}
}

func filename(s string) string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == '/' })
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// locations guesses the dom, server and service attributes from the
// file name.
func locations(file string) string {
	if file == "default" {
		return "dom=* server=*"
	}
	f := strings.FieldsFunc(file, func(r rune) bool { return r == '!' })
	switch len(f) {
	case 2:
		return server(f[1])
	case 3: // ignore the network
		return server(f[1]) + " service=" + quote.Quote(f[2])
	}
	return server(file)
}

func server(name string) string {
	if len(strings.FieldsFunc(name, func(r rune) bool { return r == '.' })) > 1 {
		return "dom=" + quote.Quote(name) + " server=" + quote.Quote(name)
	}
	return "server=" + quote.Quote(name)
}

// dnl turns newlines into ^ and drops trailing ones.
func dnl(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\n", "^"), "^")
}
