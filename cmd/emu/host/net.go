package host

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
)

// Net is the host's IP stack: dual-stack IPv6 sockets, as Inferno's
// ipif6-posix.c. Sockets are non-blocking and waited on through Go's
// poller, so a kill or an interrupt can end a blocked call.
type Net struct{}

func (h *Host) Net() platform.NetStack { return Net{} }

type sock struct {
	fd   int
	f    *os.File
	kind int
}

func (Net) Socket(kind int) (platform.Socket, error) {
	typ := unix.SOCK_STREAM
	if kind == platform.UDP {
		typ = unix.SOCK_DGRAM
	}
	fd, err := unix.Socket(unix.AF_INET6, typ, 0)
	if err != nil {
		return nil, soerr(err)
	}
	unix.CloseOnExec(fd)
	_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0)
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	nosigpipe(fd)
	return newsock(fd, kind)
}

func newsock(fd, kind int) (*sock, error) {
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, soerr(err)
	}
	return &sock{fd: fd, f: os.NewFile(uintptr(fd), "socket"), kind: kind}, nil
}

func sa6(addr [16]byte, port uint16) *unix.SockaddrInet6 {
	return &unix.SockaddrInet6{Addr: addr, Port: int(port)}
}

func fromsa(sa unix.Sockaddr) ([16]byte, uint16) {
	switch a := sa.(type) {
	case *unix.SockaddrInet6:
		return a.Addr, uint16(a.Port)
	case *unix.SockaddrInet4:
		var b [16]byte
		b[10], b[11] = 0xff, 0xff
		copy(b[12:], a.Addr[:])
		return b, uint16(a.Port)
	}
	return [16]byte{}, 0
}

func (s *sock) Bind(addr [16]byte, port uint16, restricted bool) error {
	if addr == ([16]byte{}) {
		addr = [16]byte{} // in6addr_any
	}
	if restricted {
		for p := 600; p < 1024; p++ {
			if unix.Bind(s.fd, sa6(addr, uint16(p))) == nil {
				return nil
			}
		}
	}
	return soerr(unix.Bind(s.fd, sa6(addr, port)))
}

func (s *sock) Listen() error { return soerr(unix.Listen(s.fd, 256)) }

// wait runs op through the poller until it stops returning EAGAIN or stop
// is closed.
func (s *sock) wait(stop <-chan struct{}, write bool, op func(fd int) error) error {
	rc, err := s.f.SyscallConn()
	if err != nil {
		return soerr(err)
	}
	done := make(chan struct{})
	finished := make(chan bool)
	go func() {
		select {
		case <-stop:
			_ = s.f.SetDeadline(time.Now())
			<-done
			finished <- true
		case <-done:
			finished <- false
		}
	}()
	var opErr error
	f := func(fd uintptr) bool {
		opErr = op(int(fd))
		return opErr != unix.EAGAIN && opErr != unix.EWOULDBLOCK && opErr != unix.EINPROGRESS
	}
	if write {
		err = rc.Write(f)
	} else {
		err = rc.Read(f)
	}
	close(done)
	if <-finished {
		_ = s.f.SetDeadline(time.Time{})
		return platform.ErrInterrupted
	}
	if err != nil {
		return soerr(err)
	}
	return soerr(opErr)
}

func (s *sock) Accept(stop <-chan struct{}) (platform.Socket, [16]byte, uint16, error) {
	var nfd int
	var sa unix.Sockaddr
	err := s.wait(stop, false, func(fd int) error {
		var e error
		nfd, sa, e = unix.Accept(fd)
		return e
	})
	if err != nil {
		return nil, [16]byte{}, 0, err
	}
	unix.CloseOnExec(nfd)
	nosigpipe(nfd)
	ns, err := newsock(nfd, s.kind)
	if err != nil {
		return nil, [16]byte{}, 0, err
	}
	a, p := fromsa(sa)
	return ns, a, p, nil
}

func (s *sock) Connect(stop <-chan struct{}, addr [16]byte, port uint16) error {
	err := unix.Connect(s.fd, sa6(addr, port))
	if err == nil {
		return nil
	}
	if err != unix.EINPROGRESS {
		return soerr(err)
	}
	// Wait for the connection with poll(2), a slice at a time so that
	// stop is seen.
	for {
		select {
		case <-stop:
			return platform.ErrInterrupted
		default:
		}
		fds := []unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLOUT}}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return soerr(err)
		}
		if n == 0 {
			continue
		}
		e, err := unix.GetsockoptInt(s.fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if err != nil {
			return soerr(err)
		}
		if e != 0 {
			return soerr(syscall.Errno(e))
		}
		return nil
	}
}

func (s *sock) Recv(stop <-chan struct{}, buf, hdr []byte) (int, error) {
	var n int
	var from unix.Sockaddr
	err := s.wait(stop, false, func(fd int) error {
		var e error
		if hdr == nil {
			n, e = unix.Read(fd, buf)
		} else {
			n, _, _, from, e = unix.Recvmsg(fd, buf, nil, 0)
		}
		return e
	})
	if err != nil {
		return 0, err
	}
	if n < 0 {
		n = 0
	}
	if hdr != nil {
		ra, rp := fromsa(from)
		la, lp := s.LocalAddr()
		puthdr(hdr, ra, rp, la, lp)
	}
	return n, nil
}

// puthdr fills an Inferno udp header: raddr, laddr, [ifcaddr], rport,
// lport, by its length (ip.h).
func puthdr(h []byte, ra [16]byte, rp uint16, la [16]byte, lp uint16) {
	clear(h)
	switch len(h) {
	case 2*4 + 4:
		copy(h[0:], ra[12:])
		copy(h[4:], la[12:])
		h[8], h[9], h[10], h[11] = byte(rp>>8), byte(rp), byte(lp>>8), byte(lp)
	case 2*16 + 4:
		copy(h[0:], ra[:])
		copy(h[16:], la[:])
		h[32], h[33], h[34], h[35] = byte(rp>>8), byte(rp), byte(lp>>8), byte(lp)
	default:
		copy(h[0:], ra[:])
		copy(h[16:], la[:])
		copy(h[32:], la[:])
		h[48], h[49], h[50], h[51] = byte(rp>>8), byte(rp), byte(lp>>8), byte(lp)
	}
}

func gethdr(h []byte) ([16]byte, uint16) {
	var a [16]byte
	switch len(h) {
	case 2*4 + 4:
		a[10], a[11] = 0xff, 0xff
		copy(a[12:], h[0:4])
		return a, uint16(h[8])<<8 | uint16(h[9])
	case 2*16 + 4:
		copy(a[:], h[0:16])
		return a, uint16(h[32])<<8 | uint16(h[33])
	}
	copy(a[:], h[0:16])
	return a, uint16(h[48])<<8 | uint16(h[49])
}

func (s *sock) Send(stop <-chan struct{}, buf, hdr []byte) (int, error) {
	var n int
	err := s.wait(stop, true, func(fd int) error {
		var e error
		if hdr == nil {
			n, e = unix.Write(fd, buf)
		} else {
			a, p := gethdr(hdr)
			e = unix.Sendto(fd, buf, 0, sa6(a, p))
			n = len(buf)
		}
		return e
	})
	return n, err
}

func (s *sock) LocalAddr() ([16]byte, uint16) {
	sa, err := unix.Getsockname(s.fd)
	if err != nil {
		return [16]byte{}, 0
	}
	return fromsa(sa)
}

func (s *sock) Hangup() error {
	_ = unix.SetsockoptLinger(s.fd, unix.SOL_SOCKET, unix.SO_LINGER, &unix.Linger{Onoff: 1, Linger: 0})
	return s.Close()
}

func (s *sock) Close() error { return s.f.Close() }

func (s *sock) Keepalive(ms int) error {
	return soerr(unix.SetsockoptInt(s.fd, unix.SOL_SOCKET, unix.SO_KEEPALIVE, 1))
}

func (Net) LookupHost(host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil {
		return nil, core.Error("host lookup failed: " + host)
	}
	return addrs, nil
}

func (Net) LookupAddr(addr string) ([]string, error) {
	names, err := net.DefaultResolver.LookupAddr(context.Background(), addr)
	if err != nil {
		return nil, core.Error("address lookup failed: " + addr)
	}
	return names, nil
}

func (Net) LookupPort(network, service string) (int, error) {
	if n, err := strconv.Atoi(service); err == nil {
		return n, nil
	}
	p, err := net.DefaultResolver.LookupPort(context.Background(), network, service)
	if err != nil {
		return 0, core.Error("unknown service " + service)
	}
	return p, nil
}

// soerr turns a host socket error into Inferno's wording.
func soerr(err error) error {
	if err == nil {
		return nil
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		switch en {
		case unix.ECONNREFUSED:
			return core.Error(abi.Econrefused)
		case unix.ETIMEDOUT:
			return core.Error(abi.Etimedout)
		case unix.ENETUNREACH, unix.EHOSTUNREACH:
			return core.Error(abi.Enetunreach)
		case unix.EADDRINUSE:
			return core.Error("address in use")
		case unix.EPIPE, unix.ECONNRESET:
			return core.Error(abi.Ehungup)
		case unix.EACCES, unix.EPERM:
			return core.Error(abi.Eperm)
		}
		return core.Error(en.Error())
	}
	if errors.Is(err, os.ErrClosed) {
		return core.Error(abi.Ehungup)
	}
	return core.Error(err.Error())
}
