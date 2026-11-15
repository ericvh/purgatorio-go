package authproto

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The client side of factotum's Plan 9 protocols, as Inferno's Factotum
// module (appl/lib/factotum.b): Proxy relays a conversation between a
// peer and factotum's rpc file, doing what factotum says.

// AuthRpcMax is the largest rpc message.
const AuthRpcMax = 4096

// Authinfo is what factotum establishes: the users on each side, a
// capability (on a server) and the shared secret.
type Authinfo struct {
	Cuid, Suid, Cap string
	Secret          []byte
}

// UnpackAuthinfo reads the reply to the authinfo rpc.
func UnpackAuthinfo(b []byte) (*Authinfo, bool) {
	var f [4][]byte
	for i := range f {
		if len(b) < 2 {
			return nil, false
		}
		n := int(binary.LittleEndian.Uint16(b))
		if len(b)-2 < n {
			return nil, false
		}
		f[i], b = append([]byte(nil), b[2:2+n]...), b[2+n:]
	}
	return &Authinfo{Cuid: string(f[0]), Suid: string(f[1]), Cap: string(f[2]), Secret: f[3]}, true
}

// OpenFactotum opens factotum's rpc file.
func OpenFactotum() (*os.File, error) {
	fd, err := sys.Open(Factotum+"/rpc", sys.ORDWR)
	if err != nil {
		return nil, sys.Error(sys.Errstr())
	}
	return os.NewFile(uintptr(fd), "rpc"), nil
}

// rpc does one rpc: the reply's verb and data, and for a reply that is
// not ok, the error as Inferno's rpc sets it.
func rpc(afd io.ReadWriter, verb string, a []byte) (string, []byte, string) {
	buf := append(append([]byte(verb), ' '), a...)
	if len(buf) > AuthRpcMax {
		return "toobig", nil, "rpc too big"
	}
	if n, err := afd.Write(buf); err != nil || n != len(buf) {
		if err == nil {
			return "rpc failure", nil, "rpc short write"
		}
		return "rpc failure", nil, sysErr(err).Error()
	}
	r := make([]byte, AuthRpcMax)
	n, err := afd.Read(r)
	if err != nil && err != io.EOF {
		return "rpc failure", nil, sysErr(err).Error()
	}
	r = r[:n]
	t, data := string(r), []byte(nil)
	if i := strings.IndexByte(t, ' '); i >= 0 {
		t, data = t[:i], r[i+1:]
	}
	switch t {
	case "ok":
		return t, data, ""
	case "error":
		if len(data) == 0 {
			return t, data, "unspecified rpc error"
		}
		return t, data, string(data)
	case "needkey":
		return t, data, "needkey " + string(data)
	case "badkey":
		f := strings.Split(string(data), "\n")
		if len(f) < 2 {
			return t, data, "badkey " + Quote(string(data))
		}
		return t, data, "badkey " + Quote(f[1])
	case "phase":
		return t, data, "phase error: " + Quote(string(data))
	case "done", "protocol", "toosmall", "internal":
		return t, data, ""
	}
	return "rpc failure", data, "malformed rpc response: " + Quote(t)
}

// Proxy authenticates over conn as factotum (open on afd) directs, with
// the protocol and keys params names ("proto=p9any role=client"), and
// returns what it established. The errors are Inferno's.
func Proxy(conn, afd io.ReadWriter, params string) (*Authinfo, error) {
	if afd == nil {
		return nil, errors.New("no authentication fd")
	}
	if o, _, e := rpc(afd, "start", []byte(params)); o != "ok" {
		return nil, errors.New("proxy start: " + e)
	}
	for {
		o, a, e := rpc(afd, "read", nil)
		switch o {
		case "done":
			if string(a) != "haveai" {
				return &Authinfo{}, nil // succeeded, but nothing to say
			}
			o, a, e := rpc(afd, "authinfo", nil)
			if o != "ok" {
				return nil, errors.New(e)
			}
			ai, ok := UnpackAuthinfo(a)
			if !ok {
				return nil, errors.New("bad auth info from factotum")
			}
			return ai, nil
		case "ok":
			if n, err := conn.Write(a); n != len(a) {
				return nil, errors.New("proxy write fd: " + errText(err))
			}
		case "phase":
			buf := make([]byte, AuthRpcMax)
			n := 0
			for {
				o, a, e = rpc(afd, "write", buf[:n])
				if o != "toosmall" {
					break
				}
				c, _ := strconv.Atoi(string(a))
				if c > AuthRpcMax || c <= n {
					break
				}
				m, err := conn.Read(buf[n:c])
				if m <= 0 {
					if err == nil || err == io.EOF {
						return nil, errors.New("proxy short read")
					}
					return nil, errors.New(errText(err))
				}
				n += m
			}
			if o != "ok" {
				return nil, errors.New("proxy rpc write: " + e)
			}
		default:
			return nil, errors.New("proxy rpc: " + e)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return sys.Errstr()
	}
	return sysErr(err).Error()
}
