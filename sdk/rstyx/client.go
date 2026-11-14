package rstyx

import (
	"errors"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Opts is how a client connects.
type Opts struct {
	Module  string // the calling command's module, run again as each pump
	NoAuth  bool   // -A: prove nothing and speak in the clear
	SrvAuth bool   // -S: the server must prove it is the host dialled
	Hybrid  bool   // pgrstyx3 rather than pgrstyx2
	Keyfile string // -k
	Export  string // -x: the directory served as /n/client
}

// Cipher is the one algorithm -e and -C take.
const Cipher = "chacha20poly1305"

// CheckAlg checks an algorithm named with -e or -C: none (or clear)
// goes only with -A.
func CheckAlg(alg string, noauth bool) error {
	switch {
	case alg == Cipher:
		return nil
	case (alg == "none" || alg == "clear") && noauth:
		return nil
	case alg == "none" || alg == "clear":
		return errors.New("an authenticated connection is always encrypted; use -A for none")
	}
	return errors.New("unknown algorithm " + alg)
}

// SignedProof makes the client's proof over signed: with the guest
// factotum when no key file is named and one is mounted, else with the
// key in keyfile or the user's own.
func SignedProof(signed []byte, hybrid bool, user, aname, keyfile string) ([]byte, error) {
	if keyfile == "" && authproto.HaveFactotum() {
		alg := "alg=classical"
		if hybrid {
			alg = "alg=hybrid"
		}
		return authproto.FactotumRPC("proto=ed25519 role=client "+alg+" user="+authproto.Quote(user)+" aname="+aname, signed)
	}
	k, c, err := authproto.Key(keyfile, hybrid)
	if err != nil {
		return nil, err
	}
	return pgauth.MakeProof(k, c, signed, user, aname)
}

func hello(net int, o *Opts, host, cmd string) (*Link, bool, error) {
	user := authproto.User()
	magic := Magic2
	if o.Hybrid {
		magic = Magic3
	}
	if err := WriteAll(net, Frame(Hello, []byte(magic+" "+user))); err != nil {
		return nil, false, err
	}
	k, chal, err := ReadFrame(fdReader(net))
	if err != nil {
		return nil, false, err
	}
	switch k {
	case Chal:
	case Err:
		return nil, false, errors.New(string(chal))
	default:
		return nil, false, ErrProto
	}
	var proof []byte
	var keys *pgauth.Keys
	if !o.NoAuth {
		if len(chal) == 0 || (!o.Hybrid && len(chal) != pgauth.Offer3Size) {
			return nil, false, pgauth.ENoSession
		}
		cl, err := pgauth.NewClient(chal, o.Hybrid)
		if err != nil {
			return nil, false, err
		}
		if proof, err = SignedProof(cl.Signed(), o.Hybrid, user, Aname, o.Keyfile); err != nil {
			return nil, false, err
		}
		if keys, err = cl.Keys(user, Aname, proof); err != nil {
			return nil, false, pgauth.ENoSession
		}
	}
	if err := WriteAll(net, Frame(Proof, proof)); err != nil {
		return nil, false, err
	}
	k, srv, err := ReadFrame(fdReader(net))
	if err != nil {
		return nil, false, err
	}
	switch k {
	case OK:
	case Err:
		return nil, false, errors.New(string(srv))
	default:
		return nil, false, ErrProto
	}
	perr := error(pgauth.EServerNone)
	if keys != nil {
		perr = pgauth.CheckServerHost(authproto.ServerTrust(host, authproto.Now()), keys, srv, host)
	}
	if o.SrvAuth && perr != nil {
		return nil, false, perr
	}
	proved := perr == nil && host != ""
	link := ClientLink(keys)
	flags := byte(0)
	if o.NoAuth || proved {
		flags = NoAuth
	}
	w, err := link.Wire(Exec, append([]byte{flags}, cmd...))
	if err != nil {
		return nil, false, err
	}
	return link, proved, WriteAll(net, w)
}

type fdReader int

func (f fdReader) Read(b []byte) (int, error) {
	r := ReadSome(int(f), len(b))
	if len(r) == 0 {
		return 0, ErrHungup
	}
	return copy(b, r), nil
}

// Run runs cmd at host and returns its exit status.
func Run(o *Opts, host, cmd string) (string, error) {
	if len(cmd) >= Max {
		return "", errors.New("command too long")
	}
	addr := MkAddr(host)
	conn, err := dial.Dial(addr, "")
	if err != nil {
		return "", errors.New("dial " + addr + " failed: " + err.Error())
	}
	defer conn.Dfd.Close()
	net := int(conn.Dfd.Fd())
	link, proved, err := hello(net, o, authproto.Host(addr), cmd)
	if err != nil {
		return "", err
	}
	children := NewChildren()
	np, err := sys.Pipe()
	if err != nil {
		return "", err
	}
	pump := []string{"pump", Pump}
	netpump, err := sys.Spawn(o.Module, pump, sys.NEWFD, [][2]int{{0, net}, {1, np[1]}, {2, 2}})
	if err != nil {
		return "", errors.New(sys.Errstr())
	}
	sys.Close(np[1])
	ip, _ := sys.Pipe()
	inpump, err := sys.Spawn(o.Module, pump, sys.NEWFD, [][2]int{{0, 0}, {1, ip[1]}, {2, 2}})
	if err != nil {
		return "", errors.New(sys.Errstr())
	}
	sys.Close(ip[1])
	ep, _ := sys.Pipe()
	argv := []string{"export"}
	switch {
	case o.NoAuth || proved:
		argv = append(argv, "-A")
	case o.Hybrid:
		argv = append(argv, "-p", "hybrid")
	default:
		argv = append(argv, "-p", "classical")
	}
	dir := o.Export
	if dir == "" {
		dir = "/"
	}
	argv = append(argv, dir)
	if _, err := sys.Spawn("/bin/export.wasm", argv, sys.NEWFD, [][2]int{{0, ep[1]}}); err != nil {
		return "", errors.New(sys.Errstr())
	}
	sys.Close(ep[1])
	st, err := mux(net, link, np[0], ip[0], ep[0])
	Kill(netpump)
	Kill(inpump)
	children.Reap(3, 2000)
	return st, err
}

func mux(net int, link *Link, netR, inR, exp int) (string, error) {
	credit := Window
	sending := true
	send := func(k byte, d []byte) {
		b, err := link.Wire(k, d)
		if err != nil {
			sending = false
			return
		}
		if sending && WriteAll(net, b) != nil {
			sending = false
		}
	}
	for {
		fds := []int{netR}
		expAt, inAt := -1, -1
		if exp >= 0 && sending {
			fds = append(fds, exp)
			expAt = len(fds) - 1
		}
		if inR >= 0 && sending && credit > 0 {
			fds = append(fds, inR)
			inAt = len(fds) - 1
		}
		which, ok, err := WaitReadable(fds, -1)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		switch which {
		case 0:
			b := ReadSome(netR, Max)
			if len(b) == 0 {
				return "", ErrHungup
			}
			link.Push(b)
			for {
				k, d, got, err := link.Take()
				if err != nil {
					return "", err
				}
				if !got {
					break
				}
				switch k {
				case Styx:
					if exp >= 0 && WriteAll(exp, d) != nil {
						exp = -1
					}
				case Out, Errs:
					fd := 1
					if k == Errs {
						fd = 2
					}
					WriteAll(fd, d)
					send(Win, WinData(k, len(d)))
				case Win:
					s, n, ok := ParseWin(d)
					if !ok || s != In {
						return "", ErrProto
					}
					credit += n
				case Status:
					return string(d), nil
				default:
					return "", ErrProto
				}
			}
		case expAt:
			b := ReadSome(exp, StyxChunk)
			if len(b) == 0 {
				exp = -1
			} else {
				send(Styx, b)
			}
		case inAt:
			b := ReadSome(inR, min(credit, Chunk))
			credit -= len(b)
			send(In, b)
			if len(b) == 0 {
				inR = -1
			}
		}
	}
}
