package authproto

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Factotum is where auth/factotum serves its files.
const Factotum = "/mnt/factotum"

// HaveFactotum reports whether a guest factotum is mounted.
func HaveFactotum() bool {
	_, err := sys.Stat(Factotum + "/rpc")
	return err == nil
}

// FactotumRPC runs one rpc conversation: start with attrs, write data,
// read the result. "needkey" and "error" replies become errors.
func FactotumRPC(attrs string, data []byte) ([]byte, error) {
	f, err := os.OpenFile(Factotum+"/rpc", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	do := func(req []byte) ([]byte, error) {
		if _, err := f.Write(req); err != nil {
			return nil, sysErr(err)
		}
		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return nil, sysErr(err)
		}
		r := buf[:n]
		switch {
		case string(r) == "ok":
			return nil, nil
		case strings.HasPrefix(string(r), "ok "):
			return r[3:], nil
		case strings.HasPrefix(string(r), "needkey"):
			return nil, errors.New("factotum: " + string(r))
		case strings.HasPrefix(string(r), "error "):
			return nil, errors.New("factotum: " + string(r[6:]))
		}
		return nil, errors.New("factotum: bad reply")
	}
	if _, err := do([]byte("start " + attrs)); err != nil {
		return nil, err
	}
	if _, err := do(append([]byte("write "), data...)); err != nil {
		return nil, err
	}
	out, err := do([]byte("read"))
	return append([]byte(nil), out...), err
}

func algAttr(hybrid bool) string {
	if hybrid {
		return "alg=hybrid"
	}
	return "alg=classical"
}

// AuthenticateAgent is Authenticate with the proof from the guest
// factotum, which holds the key so this prog need not read it.
func AuthenticateAgent(fd int, aname string, hybrid bool) (*os.File, error) {
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
	attrs := "proto=ed25519 role=client " + algAttr(hybrid) + " user=" + Quote(User()) + " aname=" + Quote(aname)
	proof, err := FactotumRPC(attrs, signed)
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

// SignModuleAgent asks the guest factotum for a module signature entry
// (role=modsign) over digest and the ABI version.
func SignModuleAgent(digest [32]byte, abi []byte, hybrid bool) ([]byte, error) {
	return FactotumRPC("proto=ed25519 role=modsign "+algAttr(hybrid), append(digest[:], abi...))
}
