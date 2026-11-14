package keyring

import (
	"bytes"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// ReadAuthinfo is Keyring's readauthinfo: the signer's public key, the
// certificate, the secret key, alpha and p, each a message as SendMsg
// writes it.
func ReadAuthinfo(filename string) (*Authinfo, error) {
	fd, err := sys.Open(filename, sys.OREAD)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filename)
	defer f.Close()
	ai := ReadAuthinfoFrom(f)
	if ai == nil {
		return nil, Error(quote(filename) + ": " + MSG)
	}
	return ai, nil
}

// ReadAuthinfoFrom reads an Authinfo from r; nil if it is malformed.
func ReadAuthinfoFrom(r io.Reader) *Authinfo {
	var msgs [5]string
	for i := range msgs {
		b, err := getmsg(r, Maxmsg)
		if err != nil {
			return nil
		}
		msgs[i] = string(b)
	}
	ai := &Authinfo{SPK: StrtoPK(msgs[0]), Cert: StrtoCert(msgs[1]), MySK: StrtoSK(msgs[2])}
	if ai.SPK == nil || ai.Cert == nil || ai.MySK == nil {
		return nil
	}
	ai.MyPK = SKtoPK(ai.MySK)
	ai.Alpha = StrtoIP(msgs[3], 64)
	ai.P = StrtoIP(msgs[4], 64)
	if ai.Alpha == nil || ai.P == nil {
		return nil
	}
	return ai
}

// WriteAuthinfo is Keyring's writeauthinfo. It opens an existing file
// with truncation, so as to keep its permissions and to work on a
// file2chan file, and creates it mode 0600 otherwise.
func WriteAuthinfo(filename string, info *Authinfo) error {
	if info == nil || info.Alpha == nil || info.P == nil || info.SPK == nil || info.MySK == nil || info.Cert == nil {
		return Error("nil reference")
	}
	fd, err := sys.Open(filename, sys.OTRUNC|sys.OWRITE)
	if err != nil {
		fd, err = sys.Create(filename, sys.OWRITE, 0o600)
	}
	if err != nil {
		fd, err = sys.Open(filename, sys.OWRITE)
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), filename)
	defer f.Close()
	if err := WriteAuthinfoTo(f, info); err != nil {
		return sys.Error(sys.Errstr())
	}
	return nil
}

// WriteAuthinfoTo writes info's messages to w.
func WriteAuthinfoTo(w io.Writer, info *Authinfo) error {
	for _, s := range []string{PKtoStr(info.SPK), CerttoStr(info.Cert), SKtoStr(info.MySK),
		bigtobase64(info.Alpha.v), bigtobase64(info.P.v)} {
		if err := SendMsg(w, []byte(s)); err != nil {
			return err
		}
	}
	return nil
}

// MarshalAuthinfo returns the file form of info.
func MarshalAuthinfo(info *Authinfo) []byte {
	var b bytes.Buffer
	WriteAuthinfoTo(&b, info)
	return b.Bytes()
}
