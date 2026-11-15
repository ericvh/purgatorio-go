package main

// The file server's loop: 9P from the mount and JSON-RPC from the MCP
// server, both at once, so that a call that takes long does not hold up
// the rest of the mount. The prog is single-threaded; sys.Wait on both
// descriptors takes the place of a worker per call.
//
// A read of a file whose answer is not yet known sends the request and
// leaves the read waiting; the server's answer, matched by its request
// id, ends it. Every other request is answered at once. Tflush of a
// waiting read ends it at once and, when no other read waits for that
// request, sends the server notifications/cancelled. With a timeout
// (mcpfs -t), a request not answered in time fails with "timed out" and
// is cancelled the same way. When the mount goes, the loop ends at once,
// answered requests or not.

import (
	"time"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

// etimedout is the error of a request that ran out of time.
const etimedout = "timed out"

// waiter is a read waiting for the server's answer.
type waiter struct {
	tag    uint16
	offset uint64
	count  uint32
}

// pending is a request the server has not yet answered.
type pending struct {
	call     *call
	path     uint64
	waiters  []waiter
	deadline time.Time // zero for none
}

type server struct {
	fs      *mcpfs
	srv     *styxservers.Server
	pending map[int64]*pending
}

func (fs *mcpfs) serve(srv *styxservers.Server) {
	s := &server{fs: fs, srv: srv, pending: map[int64]*pending{}}
	c := fs.client
	for _, v := range c.held {
		if err := s.mcpMessage(v); err != nil {
			return
		}
	}
	c.held = nil
	for {
		// What is already read needs no wait.
		for srv.Buffered() {
			if !s.request() {
				return
			}
		}
		for c.buffered() {
			if err := s.mcpLine(); err != nil {
				return
			}
		}
		wait := -1
		if d, ok := s.nextDeadline(); ok {
			wait = max(int(time.Until(d).Milliseconds())+1, 0)
		}
		i, err := sys.Wait([][2]int{{sys.WaitRead, 0}, {sys.WaitRead, c.from}}, wait)
		if err != nil {
			return
		}
		switch i {
		case 0:
			if !s.request() {
				return
			}
		case 1:
			if err := c.fill(); err != nil {
				s.hungup()
				return
			}
		}
		s.expire()
	}
}

func (s *server) nextDeadline() (time.Time, bool) {
	var d time.Time
	for _, p := range s.pending {
		if !p.deadline.IsZero() && (d.IsZero() || p.deadline.Before(d)) {
			d = p.deadline
		}
	}
	return d, !d.IsZero()
}

// expire fails the requests whose time is up.
func (s *server) expire() {
	now := time.Now()
	for id, p := range s.pending {
		if !p.deadline.IsZero() && !now.Before(p.deadline) {
			s.fs.client.cancel(id, etimedout)
			delete(s.pending, id)
			s.finish(id, p, nil, etimedout)
		}
	}
}

// hungup fails every waiting read: the server has gone.
func (s *server) hungup() {
	for id, p := range s.pending {
		delete(s.pending, id)
		s.finish(id, p, nil, "mcp server: hungup")
	}
}

// finish ends request id: it keeps its result on its call for later
// reads, and answers every read waiting for it.
func (s *server) finish(id int64, p *pending, res []byte, e string) {
	cl := p.call
	if cl.inflight == id {
		cl.inflight = 0
		cl.done, cl.res, cl.err = true, res, e
	}
	for _, w := range p.waiters {
		if e != "" {
			s.srv.Reply(&styx.Fcall{Type: styx.Rerror, Tag: w.tag, Ename: e})
			continue
		}
		s.srv.Reply(readAt(w.tag, w.offset, w.count, cl.end, res))
	}
}

// readAt answers a read of a call's result: an offset at or past where
// the writes ended counts from there.
func readAt(tag uint16, offset uint64, count uint32, end uint64, res []byte) *styx.Fcall {
	if offset >= end {
		offset -= end
	}
	return styxservers.ReadBytes(&styx.Fcall{Tag: tag, Offset: offset, Count: count}, res)
}

// mcpLine takes one line from the MCP server.
func (s *server) mcpLine() error {
	l, ok := s.fs.client.take()
	if !ok {
		return nil
	}
	v, ok := parse(l)
	if !ok {
		return nil
	}
	return s.mcpMessage(v)
}

// mcpMessage takes one message from the MCP server: the answer to a
// request, a request of its own (answered), or a notification.
func (s *server) mcpMessage(v mcp.Value) error {
	fs := s.fs
	if method, ok := mcp.Str(v, "method"); ok {
		if rid, ok := mcp.Get(v, "id"); ok {
			return fs.client.answer(v, rid)
		}
		switch method {
		case "notifications/tools/list_changed":
			return fs.toolsChanged()
		case "notifications/message", "notifications/progress":
			fs.note(v)
		}
		return nil
	}
	id, ok := mcp.Int(v, "id")
	if !ok {
		return nil
	}
	if done, err := fs.refreshed(id, v); done || err != nil {
		return err
	}
	p := s.pending[id]
	if p == nil {
		return nil
	}
	delete(s.pending, id)
	r, err := replyResult(v)
	if err != nil {
		s.finish(id, p, nil, err.Error())
	} else {
		s.finish(id, p, render(p.path, r), "")
	}
	return nil
}

// request reads one 9P request and answers it, or leaves it waiting for
// the server. It returns false when the mount has gone.
func (s *server) request() bool {
	srv, fs := s.srv, s.fs
	t, err := srv.Next()
	if err != nil {
		return false
	}
	switch t.Type {
	case styx.Topen:
		c := srv.GetFid(t.Fid)
		if c != nil && !c.IsOpen {
			ok := t.Mode&sys.ORCLOSE == 0
			if fs.takesWrites(c.Path) {
				ok = ok && t.Mode&3 != sys.OEXEC
			} else {
				ok = ok && t.Mode&3 == sys.OREAD && t.Mode&sys.OTRUNC == 0
			}
			if !ok {
				srv.Error(t, string(styxservers.Eperm))
				return true
			}
			if fs.takesWrites(c.Path) {
				c.Open(int(t.Mode&3), qid(c.Path))
				c.Data = &call{}
				srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: qid(c.Path), Iounit: srv.Iounit()})
				return true
			}
		}
		srv.Open(t)
	case styx.Tread:
		c, err := srv.CanRead(t)
		if c == nil {
			srv.Error(t, err.Error())
			return true
		}
		if c.Qtype&styx.QTDIR != 0 {
			srv.Read(t)
			return true
		}
		ti, f, isTool := fs.toolOf(c.Path)
		switch {
		case c.Path == pathServer:
			srv.Reply(styxservers.ReadBytes(t, fs.server))
		case c.Path == pathLog:
			srv.Reply(styxservers.ReadBytes(t, fs.log))
		case isTool && f == 1:
			srv.Reply(styxservers.ReadBytes(t, fs.tools[ti].schema))
		case fs.asks(c.Path):
			s.read(t, c)
		default:
			srv.Error(t, string(styxservers.Eperm))
		}
	case styx.Twrite:
		c, err := srv.CanWrite(t)
		if c == nil {
			srv.Error(t, err.Error())
			return true
		}
		if !fs.takesWrites(c.Path) {
			srv.Error(t, string(styxservers.Eperm))
			return true
		}
		cl, _ := c.Data.(*call)
		if cl == nil || cl.done {
			cl = &call{}
			c.Data = cl
		}
		cl.args = append(cl.args, t.Data...)
		cl.end = t.Offset + uint64(len(t.Data))
		srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
	case styx.Tflush:
		s.flush(t.Oldtag)
		srv.Reply(&styx.Fcall{Type: styx.Rflush, Tag: t.Tag})
	case styx.Tremove:
		if c := srv.GetFid(t.Fid); c != nil {
			srv.DelFid(c)
		}
		srv.Error(t, string(styxservers.Eperm))
	default:
		srv.Default(t)
	}
	return true
}

// read answers a read of a file that asks the server: from the result
// if it is known, or by waiting for the request, sent now if it has not
// been.
func (s *server) read(t *styx.Fcall, c *styxservers.Fid) {
	cl, _ := c.Data.(*call)
	if cl == nil {
		cl = &call{}
		c.Data = cl
	}
	if cl.done {
		if cl.err != "" {
			s.srv.Error(t, cl.err)
		} else {
			s.srv.Reply(readAt(t.Tag, t.Offset, t.Count, cl.end, cl.res))
		}
		return
	}
	if cl.inflight == 0 || s.pending[cl.inflight] == nil {
		method, params, err := s.fs.query(c.Path, cl.args)
		if err != nil {
			// Bad arguments: the read fails now, and so do later ones.
			cl.done, cl.err = true, err.Error()
			s.srv.Error(t, cl.err)
			return
		}
		id, err := s.fs.client.start(method, params)
		if err != nil {
			s.srv.Error(t, err.Error())
			return
		}
		cl.inflight = id
		p := &pending{call: cl, path: c.Path}
		if s.fs.timeout > 0 {
			p.deadline = time.Now().Add(time.Duration(s.fs.timeout) * time.Millisecond)
		}
		s.pending[id] = p
	}
	p := s.pending[cl.inflight]
	p.waiters = append(p.waiters, waiter{t.Tag, t.Offset, t.Count})
}

// flush ends the read with tag oldtag if it waits for the server. The
// request is cancelled when no other read waits for it, and a later read
// sends it again.
func (s *server) flush(oldtag uint16) {
	for id, p := range s.pending {
		for i, w := range p.waiters {
			if w.tag != oldtag {
				continue
			}
			p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
			if len(p.waiters) == 0 {
				s.fs.client.cancel(id, "the read was flushed")
				delete(s.pending, id)
				if p.call.inflight == id {
					p.call.inflight = 0
				}
			}
			return
		}
	}
}
