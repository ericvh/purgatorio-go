// Package authproto is the guest side of pgauth's mount handshake
// (docs/auth.md): which protocol a command uses, the keys a user proves
// with, and the client's half of versions 3 and 4 over an auth fd.
package authproto

import (
	"errors"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The protocols a command may be told to use.
const (
	Hybrid    = "hybrid"    // version 4, the default
	Classical = "classical" // version 3, the Rust sibling's
	Keyring   = "keyring"   // Inferno's keyring and ssl
	None      = "none"      // no authentication (as -A)
)

// Default is the protocol when nothing names one.
const Default = Hybrid

// SettingFile is the system-wide setting.
const SettingFile = "/lib/keys/proto"

// EBadProto is the error for a name that is not a protocol.
var EBadProto = errors.New("unknown authentication protocol")

// Parse splits a setting ("hybrid", "hybrid classical", "hybrid,classical")
// and checks each name. keyring and none stand alone.
func Parse(s string) ([]string, error) {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
	if len(f) == 0 {
		return nil, EBadProto
	}
	for _, p := range f {
		switch p {
		case Hybrid, Classical:
		case Keyring, None:
			if len(f) > 1 {
				return nil, EBadProto
			}
		default:
			return nil, EBadProto
		}
	}
	return f, nil
}

// Resolve is the protocols a command uses: flag when it is set, else the
// environment variable authproto, else the first line of
// /lib/keys/proto, else hybrid.
func Resolve(flag string) ([]string, error) {
	if flag != "" {
		return Parse(flag)
	}
	if e := os.Getenv("authproto"); e != "" {
		return Parse(e)
	}
	if b, err := os.ReadFile(SettingFile); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		if strings.TrimSpace(line) != "" {
			return Parse(line)
		}
	}
	return []string{Default}, nil
}

// ExportFlags is the sys.Export flags for the protocols a server takes;
// 0 for keyring and none, where the kernel asks for no proof.
func ExportFlags(protos []string) int {
	f := 0
	for _, p := range protos {
		switch p {
		case Hybrid:
			f |= sys.EXPHYBRID
		case Classical:
			f |= sys.EXPCLASSICAL
		}
	}
	return f
}

// User is the prog's user.
func User() string {
	b, _ := os.ReadFile("/dev/user")
	return strings.TrimSpace(string(b))
}

// Test seeds: the public test keys, which no stock system trusts.
const (
	TestKey       = "/lib/test/auth.key"  // classical, the Rust sibling's
	TestHybridKey = "/lib/test/auth.hkey" // hybrid
)

// ReadKey reads an authinfo or a seed file.
func ReadKey(file string) (*pgauth.PrivateKey, *pgauth.Cert, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	if a, err := pgauth.ParseAuthInfo(b); err == nil {
		return a.Key, a.Cert, nil
	}
	k, err := pgauth.NewPrivateKey(b)
	if err != nil {
		return nil, nil, errors.New(file + ": " + err.Error())
	}
	return k, nil, nil
}

// Key is the key the user proves with, of the family asked for: keyfile
// when given (an authinfo or a seed; a name without a slash is under
// /usr/<user>/keyring), else the first of /usr/<user>/keyring/default,
// /usr/<user>/key and the test key that is of that family.
func Key(keyfile string, hybrid bool) (*pgauth.PrivateKey, *pgauth.Cert, error) {
	u := User()
	if keyfile != "" {
		if !strings.Contains(keyfile, "/") {
			keyfile = "/usr/" + u + "/keyring/" + keyfile
		}
		k, c, err := ReadKey(keyfile)
		if err != nil {
			return nil, nil, err
		}
		if k.Hybrid() != hybrid {
			return nil, nil, errors.New(keyfile + ": " + pgauth.EWrongAlg.Error())
		}
		return k, c, nil
	}
	test := TestKey
	if hybrid {
		test = TestHybridKey
	}
	for _, f := range []string{"/usr/" + u + "/keyring/default", "/usr/" + u + "/key", test} {
		if k, c, err := ReadKey(f); err == nil && k.Hybrid() == hybrid {
			return k, c, nil
		}
	}
	return nil, nil, pgauth.ENoKey
}

// Authenticate runs the client's half of the handshake on the
// conversation fd: Fauth, the offer, the proof. It returns the auth fd
// to mount with. hybrid selects version 4, else version 3; neither falls
// back to the other.
func Authenticate(fd int, aname string, hybrid bool, k *pgauth.PrivateKey, c *pgauth.Cert) (*os.File, error) {
	afd, err := sys.Fauth(fd, aname)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(afd), "auth")
	n := pgauth.Signed3Size
	if hybrid {
		n = pgauth.Signed4Size
	}
	signed := make([]byte, n)
	if _, err := f.ReadAt(signed, 0); err != nil {
		f.Close()
		return nil, sysErr(err)
	}
	proof, err := pgauth.MakeProof(k, c, signed, User(), aname)
	if err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt(proof, 0); err != nil {
		f.Close()
		return nil, sysErr(err)
	}
	return f, nil
}

// ServerProof reads the server's proof from an auth fd after an accepted
// version 3 or 4 proof. The kernel fetches it from the server once and
// gives every reader the same bytes.
func ServerProof(afd *os.File, hybrid bool) ([]byte, error) {
	off := pgauth.ServerProofOffset(3)
	if hybrid {
		off = pgauth.ServerProofOffset(4)
	}
	var p []byte
	buf := make([]byte, 8192)
	for len(p) <= pgauth.MaxProof4 {
		n, err := afd.ReadAt(buf, off+int64(len(p)))
		p = append(p, buf[:n]...)
		if n == 0 || err != nil {
			break
		}
	}
	return p, nil
}

// CheckHost checks who the server's proof says it is, for a client that
// dialled host (empty for none), against /lib/keys/servers,
// /lib/keys/serversigners (or /lib/keys/signers) and /lib/keys/revoked.
// The kernel checks the signature over the session when the mount asks
// for MSRVAUTH, on the same bytes.
func CheckHost(afd *os.File, hybrid bool, host string, now uint64) error {
	proof, _ := ServerProof(afd, hybrid)
	pol := &pgauth.Policy{Now: now}
	servers, _ := os.ReadFile("/lib/keys/servers")
	named := pgauth.ParseNamed(servers)
	if host == "" {
		pol.Keys = pgauth.KeysFor(named, "", true)
	} else {
		pol.Keys = pgauth.KeysFor(named, host, false)
	}
	if b, err := os.ReadFile("/lib/keys/serversigners"); err == nil {
		pol.Signers = pgauth.ParseListLenient(b)
	} else if _, serr := os.Stat("/lib/keys/serversigners"); serr == nil {
		pol.Signers = nil // there but unreadable: trust no signer for servers
	} else {
		b, _ := os.ReadFile("/lib/keys/signers")
		pol.Signers = pgauth.ParseListLenient(b)
	}
	if b, err := os.ReadFile("/lib/keys/revoked"); err == nil {
		l, ok := pgauth.ParseList(b)
		pol.Revoked, pol.Damaged = l, !ok
	} else if _, serr := os.Stat("/lib/keys/revoked"); serr == nil {
		pol.Damaged = true
	}
	return pgauth.CheckServerIdentity(pol, proof, hybrid, host)
}

// Host is the host field of a dial string as written: fs.example in
// tcp!fs.example!styx or fs.example!styx. Empty for anything else.
func Host(addr string) string {
	f := strings.Split(addr, "!")
	switch len(f) {
	case 3:
		return f[1]
	case 2:
		return f[0]
	}
	return ""
}

func sysErr(err error) error {
	if e := sys.Errstr(); e != "" {
		return sys.Error(e)
	}
	return err
}
