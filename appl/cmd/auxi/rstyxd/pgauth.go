package main

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/rstyx"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The pgauth rstyx server (docs/rcmd.md), the Rust sibling's design: one
// prog owns the connection's write side and waits on pipes only; itself
// run with --pump copies the connection into a pipe, with --feed passes
// standard input to the command (reporting how much, which is the
// client's credit), and with --session mounts the client and runs the
// command.
//
//	auxi/rstyxd [-A] [-e] [-k keyfile] [-p proto]

const (
	self     = "/bin/auxi/rstyxd.wasm"
	drainMs  = 300
	lingerMs = 2000
	fdAck    = 5
	fdStyx   = 5
	fdStatus = 6
	client   = "/n/client"
)

// pgmain runs the pgauth server unless the protocol is keyring, which is
// Inferno's: it reports whether it did.
func pgmain() bool {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case rstyx.Pump:
			rstyx.RunPump()
			return true
		case "--feed":
			feed()
			return true
		case "--session":
			session(os.Args[2:])
			return true
		}
	}
	a := arg.New(os.Args)
	a.SetUsage("auxi/rstyxd [-A] [-e] [-k keyfile] [-p proto]")
	noauth, keyfile, proto := false, "", ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'A':
			noauth = true
		case 'e':
		case 'k', 'f':
			keyfile = a.EArg()
		case 'p':
			proto = a.EArg()
		default:
			a.Usage()
		}
	}
	protos, err := authproto.Resolve(proto)
	if err != nil {
		fail(err.Error())
	}
	switch protos[0] {
	case authproto.Keyring:
		os.Args = os.Args[:1]
		return false
	case authproto.None:
		noauth = true
	}
	var accept pgauth.Accept
	if !noauth {
		if accept, err = authproto.Accept(protos); err != nil {
			fail(err.Error())
		}
	}
	serve(noauth, accept, keyfile)
	return true
}

type fdr int

func (f fdr) Read(b []byte) (int, error) {
	r := rstyx.ReadSome(int(f), len(b))
	if len(r) == 0 {
		return 0, rstyx.ErrHungup
	}
	return copy(b, r), nil
}

// opening runs the exchange: it returns the EXEC flags, the command, the
// link and whether the session is hybrid, or false when the client was
// refused (it has the reason).
func opening(noauth bool, accept pgauth.Accept, keyfile string) (byte, string, *rstyx.Link, bool, bool) {
	refuse := func(why string) { rstyx.WriteAll(0, rstyx.Frame(rstyx.Err, []byte(why))) }
	k, d, err := rstyx.ReadFrame(fdr(0))
	if err != nil || k != rstyx.Hello {
		refuse(rstyx.ErrProto.Error())
		return 0, "", nil, false, false
	}
	magic, uname, _ := strings.Cut(string(d), " ")
	hybrid := magic == rstyx.Magic3
	if !hybrid && magic != rstyx.Magic2 {
		refuse(rstyx.ErrProto.Error())
		return 0, "", nil, false, false
	}
	var srv *pgauth.Server
	var pol *pgauth.Policy
	if !noauth {
		if hybrid && accept&pgauth.AcceptHybrid == 0 {
			refuse(pgauth.EClassicalRequired.Error())
			return 0, "", nil, false, false
		}
		if !hybrid && accept&pgauth.AcceptClassical == 0 {
			refuse(pgauth.EHybridRequired.Error())
			return 0, "", nil, false, false
		}
		pol = authproto.ClientTrust(uname, authproto.Now(), accept)
		if !pol.RequiresAuth() {
			refuse(pgauth.ENoTrust.Error())
			return 0, "", nil, false, false
		}
		srv = pgauth.NewServer(hybrid)
	}
	var chal []byte
	if srv != nil {
		chal = srv.Offer()
	}
	if rstyx.WriteAll(0, rstyx.Frame(rstyx.Chal, chal)) != nil {
		return 0, "", nil, false, false
	}
	k, proof, err := rstyx.ReadFrame(fdr(0))
	if err != nil || k != rstyx.Proof {
		refuse(rstyx.ErrProto.Error())
		return 0, "", nil, false, false
	}
	link := rstyx.ServerLink(nil)
	var mine []byte
	if srv != nil {
		if len(proof) == 0 {
			refuse(pgauth.ERequired.Error())
			return 0, "", nil, false, false
		}
		if err := pgauth.Check(pol, srv.Offer(), uname, rstyx.Aname, proof); err != nil {
			refuse(err.Error())
			return 0, "", nil, false, false
		}
		keys, err := srv.Keys(uname, rstyx.Aname, proof)
		if err != nil {
			refuse(pgauth.ENoSession.Error())
			return 0, "", nil, false, false
		}
		link = rstyx.ServerLink(keys)
		mine = serverProof(keys, keyfile)
	}
	if rstyx.WriteAll(0, rstyx.Frame(rstyx.OK, mine)) != nil {
		return 0, "", nil, false, false
	}
	k, d, err = link.Read(fdr(0))
	if err != nil || k != rstyx.Exec || len(d) == 0 {
		return 0, "", nil, false, false
	}
	return d[0], string(d[1:]), link, hybrid, true
}

// serverProof is this side's proof for the OK frame: from keyfile, else
// the guest factotum, else the user's own keys of the session's family.
func serverProof(keys *pgauth.Keys, keyfile string) []byte {
	hybrid := keys.Version == 4
	if keyfile != "" {
		k, c, err := authproto.ReadKey(keyfile)
		if err != nil || k.Hybrid() != hybrid {
			return nil
		}
		p, _ := pgauth.MakeServerProof(k, c, keys)
		return p
	}
	if authproto.HaveFactotum() {
		alg := "alg=classical"
		if hybrid {
			alg = "alg=hybrid"
		}
		if p, err := authproto.FactotumRPC("proto=ed25519 role=server "+alg, keys.Session()); err == nil {
			return p
		}
	}
	k, c, err := authproto.Key("", hybrid)
	if err != nil {
		return nil
	}
	p, _ := pgauth.MakeServerProof(k, c, keys)
	return p
}

type stream struct {
	fd     int
	kind   byte
	credit int
}

func serve(noauth bool, accept pgauth.Accept, keyfile string) {
	flags, cmd, link, hybrid, ok := opening(noauth, accept, keyfile)
	if !ok {
		return
	}
	children := rstyx.NewChildren()
	np, _ := sys.Pipe()
	netpump, err := sys.Spawn(self, []string{"rstyxd", rstyx.Pump}, sys.NEWFD, [][2]int{{0, 0}, {1, np[1]}, {2, 2}})
	if err != nil {
		return
	}
	sys.Close(np[1])
	fp, _ := sys.Pipe()
	ip, _ := sys.Pipe()
	ap, _ := sys.Pipe()
	feeder, err := sys.Spawn(self, []string{"rstyxd", "--feed"}, sys.NEWFD,
		[][2]int{{0, fp[0]}, {1, ip[1]}, {2, 2}, {fdAck, ap[1]}})
	if err != nil {
		return
	}
	sys.Close(fp[0])
	sys.Close(ip[1])
	sys.Close(ap[1])
	op, _ := sys.Pipe()
	ep, _ := sys.Pipe()
	sp, _ := sys.Pipe()
	tp, _ := sys.Pipe()
	proto := "classical"
	if hybrid {
		proto = "hybrid"
	}
	sess, err := sys.Spawn(self, []string{"rstyxd", "--session", strconv.Itoa(int(flags)), proto, keyfile, cmd},
		sys.NEWFD|sys.FORKNS|sys.FORKENV|sys.NEWPGRP,
		[][2]int{{0, ip[0]}, {1, op[1]}, {2, ep[1]}, {fdStyx, sp[1]}, {fdStatus, tp[1]}})
	if err != nil {
		return
	}
	for _, fd := range []int{ip[0], op[1], ep[1], sp[1], tp[1]} {
		sys.Close(fd)
	}
	ctl, _ := sys.Open("/prog/"+strconv.Itoa(sess)+"/ctl", sys.OWRITE)
	status, have := mux(link, np[0], fp[1], ap[0], sp[0], tp[0],
		[]*stream{{op[0], rstyx.Out, rstyx.Window}, {ep[0], rstyx.Errs, rstyx.Window}})
	sent := false
	if have {
		if w, err := link.Wire(rstyx.Status, []byte(status)); err == nil {
			sent = rstyx.WriteAll(0, w) == nil
		}
	}
	if ctl >= 0 {
		rstyx.WriteAll(ctl, []byte("killgrp"))
	}
	rstyx.Kill(sess)
	rstyx.Kill(feeder)
	for sent {
		if _, ok, _ := rstyx.WaitReadable([]int{np[0]}, lingerMs); !ok || len(rstyx.ReadSome(np[0], rstyx.Max)) == 0 {
			break
		}
	}
	rstyx.Kill(netpump)
	children.Reap(3, 2000)
}

func mux(link *rstyx.Link, netR, feed, ack, styx, st int, streams []*stream) (string, bool) {
	var status string
	have := false
	send := func(k byte, d []byte) bool {
		w, err := link.Wire(k, d)
		return err == nil && rstyx.WriteAll(0, w) == nil
	}
	const (
		srcNet = iota
		srcStyx
		srcAck
		srcStatus
		srcStream
	)
	for {
		if have && streams[0].fd < 0 && streams[1].fd < 0 {
			return status, true
		}
		fds := []int{netR}
		srcs := []int{srcNet}
		if styx >= 0 {
			fds, srcs = append(fds, styx), append(srcs, srcStyx)
		}
		if ack >= 0 {
			fds, srcs = append(fds, ack), append(srcs, srcAck)
		}
		if !have {
			fds, srcs = append(fds, st), append(srcs, srcStatus)
		}
		for i, s := range streams {
			if s.fd >= 0 && s.credit > 0 {
				fds, srcs = append(fds, s.fd), append(srcs, srcStream+i)
			}
		}
		idle := have
		for _, s := range streams {
			if s.fd >= 0 && s.credit == 0 {
				idle = false
			}
		}
		ms := -1
		if idle {
			ms = drainMs
		}
		i, ok, err := rstyx.WaitReadable(fds, ms)
		if err != nil {
			return "", false
		}
		if !ok {
			if have {
				return status, true
			}
			continue
		}
		switch src := srcs[i]; {
		case src == srcNet:
			b := rstyx.ReadSome(netR, rstyx.Max)
			if len(b) == 0 {
				return "", false
			}
			link.Push(b)
			for {
				k, d, got, err := link.Take()
				if err != nil {
					return "", false
				}
				if !got {
					break
				}
				switch k {
				case rstyx.Styx:
					if styx >= 0 && rstyx.WriteAll(styx, d) != nil {
						styx = -1
					}
				case rstyx.In:
					if len(d) == 0 {
						if feed >= 0 {
							sys.Close(feed)
						}
						feed = -1
					} else if feed >= 0 && rstyx.WriteAll(feed, d) != nil {
						feed = -1
					}
				case rstyx.Win:
					s, n, ok := rstyx.ParseWin(d)
					if !ok || (s != rstyx.Out && s != rstyx.Errs) {
						return "", false
					}
					j := 0
					if s == rstyx.Errs {
						j = 1
					}
					streams[j].credit += n
				default:
					return "", false
				}
			}
		case src == srcStyx:
			b := rstyx.ReadSome(styx, rstyx.StyxChunk)
			if len(b) == 0 {
				styx = -1
			} else if !send(rstyx.Styx, b) {
				return "", false
			}
		case src == srcAck:
			b := rstyx.ReadSome(ack, 4)
			if len(b) != 4 {
				ack = -1
				continue
			}
			if !send(rstyx.Win, rstyx.WinData(rstyx.In, int(binary.LittleEndian.Uint32(b)))) {
				return "", false
			}
		case src == srcStatus:
			b := rstyx.ReadSome(st, rstyx.Max)
			have = true
			if len(b) > 0 && b[0] == 's' {
				status = string(b[1:])
			} else {
				status = "auxi/rstyxd: session lost"
			}
		default:
			s := streams[src-srcStream]
			b := rstyx.ReadSome(s.fd, min(s.credit, rstyx.Chunk))
			if len(b) == 0 {
				s.fd = -1
			} else {
				s.credit -= len(b)
				if !send(s.kind, b) {
					return "", false
				}
			}
		}
	}
}

// feed copies fd 0 to fd 1 and, after each piece, writes its length to
// fdAck: a command that does not read its input blocks this prog only.
func feed() {
	buf := make([]byte, rstyx.Chunk)
	for {
		n, err := os.Stdin.Read(buf)
		if n <= 0 || err != nil {
			return
		}
		if rstyx.WriteAll(1, buf[:n]) != nil || rstyx.WriteAll(fdAck, binary.LittleEndian.AppendUint32(nil, uint32(n))) != nil {
			return
		}
	}
}

func mkdir(p string) {
	if _, err := sys.Stat(p); err != nil {
		if fd, err := sys.Create(p, sys.OREAD, sys.DMDIR|0o777); err == nil {
			sys.Close(fd)
		}
	}
}

// command mounts the client's namespace (the 9P conversation on fdStyx)
// and runs cmd under sh -c, returning its exit status.
func command(flags int, proto, keyfile, cmd string) (string, error) {
	mkdir("/n")
	mkdir(client)
	if flags&rstyx.NoAuth != 0 {
		if _, err := sys.Mount(fdStyx, -1, client, sys.MREPL, ""); err != nil {
			return "", sys.Error("mount " + client + ": " + sys.Errstr())
		}
	} else {
		// The client's export asks this side for a proof, made from the
		// same key the OK frame's was.
		hybrid := proto == "hybrid"
		var afd *os.File
		var err error
		if keyfile == "" && authproto.HaveFactotum() {
			afd, err = authproto.AuthenticateAgent(fdStyx, "", hybrid)
		} else if k, c, kerr := authproto.Key(keyfile, hybrid); kerr != nil {
			err = kerr
		} else {
			afd, err = authproto.Authenticate(fdStyx, "", hybrid, k, c)
		}
		if err != nil {
			return "", sys.Error("mount " + client + ": " + err.Error())
		}
		if _, err := sys.Mount(fdStyx, int(afd.Fd()), client, sys.MREPL, ""); err != nil {
			return "", sys.Error("mount " + client + ": " + sys.Errstr())
		}
	}
	// The wait file must be open before the spawn to hear of the exit.
	me, _ := sys.Pctl(0, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
	if err != nil {
		return "", sys.Error("no wait file: " + sys.Errstr())
	}
	pid, err := sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", cmd}, sys.NEWFD, [][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		return "", sys.Error("sh: " + sys.Errstr())
	}
	return waitFor(wfd, pid), nil
}

// waitFor is the exit status of child pid, from the wait file wfd.
func waitFor(wfd, pid int) string {
	for {
		b := rstyx.ReadSome(wfd, 512)
		if len(b) == 0 {
			return "wait failed"
		}
		id, rest, _ := strings.Cut(string(b), " ")
		if id == strconv.Itoa(pid) {
			if i := strings.Index(rest, "\":"); i >= 0 {
				return rest[i+2:]
			}
			return ""
		}
	}
}

// session is --session flags proto keyfile cmd: run the command with fds
// 0 to 2 as given, write "s" and its status to fdStatus, and stay until
// killed, so that its process group can always be named.
func session(args []string) {
	if len(args) != 4 {
		fail("usage: auxi/rstyxd --session flags proto keyfile cmd")
	}
	flags, _ := strconv.Atoi(args[0])
	status, err := command(flags, args[1], args[2], args[3])
	if err != nil {
		os.Stderr.WriteString("auxi/rstyxd: " + err.Error() + "\n")
		status = "fail:" + err.Error()
	}
	for _, fd := range []int{0, 1, 2, fdStyx} {
		sys.Close(fd)
	}
	rstyx.WriteAll(fdStatus, []byte("s"+status))
	rstyx.ReadSome(fdStatus, 1)
}
