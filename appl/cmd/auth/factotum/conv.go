package main

import (
	"encoding/binary"
	"errors"
	"runtime"
	"strconv"

	"github.com/ericvh/purgatorio-go/sdk/authproto"
)

// A conv is one rpc conversation running a Plan 9 protocol (p9any), as
// Inferno's factotum runs one: the protocol is a goroutine that reads
// and writes through the conv as if it were the peer, and each rpc
// request is handed to it and answered by it. Plan 9's rpc verbs follow:
// a read gives "ok <data>" for the caller to send to the peer, or
// "phase" when the protocol wants data instead; the caller then writes
// what it read from the peer, and "toosmall <n>" asks for at least n
// bytes. "done haveai" ends it, and "authinfo" gives the result.
type conv struct {
	reqs  chan *creq
	cur   *creq // the request the protocol has yet to answer
	attrs []authproto.Attr
	ai    *authinfo
	done  bool
	err   string
}

type creq struct {
	verb  string
	arg   []byte
	reply chan creply
}

type creply struct {
	data []byte
	err  error
}

// authinfo is what a protocol establishes: the users on each side and
// the shared secret.
type authinfo struct {
	cuid, suid, cap string
	secret          []byte
}

// pack is the authinfo as the authinfo rpc gives it: each field a
// 2-byte little-endian count and its bytes.
func (ai *authinfo) pack() []byte {
	var b []byte
	for _, f := range [][]byte{[]byte(ai.cuid), []byte(ai.suid), []byte(ai.cap), ai.secret} {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(f)))
		b = append(b, f...)
	}
	return b
}

// startConv runs proto in a new conversation.
func startConv(attrs []authproto.Attr, proto func(c *conv) string) *conv {
	c := &conv{reqs: make(chan *creq), attrs: attrs}
	go c.run(proto)
	return c
}

// do hands an rpc request to the conversation and waits for its answer.
func (c *conv) do(verb string, arg []byte) ([]byte, error) {
	r := &creq{verb: verb, arg: arg, reply: make(chan creply, 1)}
	c.reqs <- r
	a := <-r.reply
	return a.data, a.err
}

// stop ends the conversation; its goroutine exits when it next waits.
func (c *conv) stop() { close(c.reqs) }

func (c *conv) run(proto func(c *conv) string) {
	err := proto(c)
	if c.err == "" {
		c.err = err
	}
	c.done = true
	if c.err != "" {
		c.error(c.err)
	}
	c.finish()
}

func (r *creq) answer(s string)      { r.reply <- creply{data: []byte(s)} }
func (r *creq) answerData(b []byte)  { r.reply <- creply{data: append([]byte("ok "), b...)} }
func (r *creq) phase(s string)       { r.answer("phase " + s) }
func (r *creq) answerErr(err string) { r.reply <- creply{err: errors.New(err)} }

// rio waits for the next request the protocol must handle, answering
// attr and unknown verbs itself. A stopped conversation ends here.
func (c *conv) rio() *creq {
	for {
		r, ok := <-c.reqs
		if !ok {
			runtime.Goexit()
		}
		switch r.verb {
		case "start", "read", "write", "authinfo":
			return r
		case "attr":
			r.answer("ok " + attrtext(c.attrs))
		default:
			r.answer("error unknown rpc")
		}
	}
}

// finish answers requests once the protocol has ended.
func (c *conv) finish() {
	for {
		r := c.rio()
		switch r.verb {
		case "write":
			r.phase("protocol phase error")
		case "read":
			if c.err != "" {
				r.answer("error " + c.err)
			} else {
				c.doneReply(r)
			}
		case "authinfo":
			switch {
			case !c.done:
				r.answer("error authentication unfinished")
			case c.ai == nil:
				r.answer("error no authinfo available")
			default:
				r.answerData(c.ai.pack())
			}
		default:
			r.answer("error unexpected request")
		}
	}
}

func (c *conv) doneReply(r *creq) {
	if c.ai != nil {
		r.answer("done haveai")
	} else {
		r.answer("done")
	}
}

// ok answers the pending request, if any, with ok.
func (c *conv) ok() {
	if c.cur != nil {
		c.cur.answer("ok")
		c.cur = nil
	}
}

// toosmall asks the caller for at least n bytes.
func (c *conv) toosmall(n int) {
	if c.cur != nil {
		c.cur.answer("toosmall " + strconv.Itoa(n))
		c.cur = nil
	}
}

// error fails the pending request with "error s", as a failed read of
// rpc, as Inferno's factotum does.
func (c *conv) error(s string) {
	if c.cur != nil {
		c.cur.answerErr("error " + s)
		c.cur = nil
	}
}

// read gives the next data the caller writes, leaving its request
// pending.
func (c *conv) read() []byte {
	c.ok()
	for {
		r := c.rio()
		switch r.verb {
		case "write":
			c.cur = r
			if r.arg == nil {
				return []byte{}
			}
			return r.arg
		case "authinfo":
			r.answer("error authentication unfinished")
		default:
			r.phase("protocol phase error")
		}
	}
}

// readn reads until the caller gives at least n bytes in one write.
func (c *conv) readn(n int) []byte {
	for {
		b := c.read()
		if len(b) >= n {
			return b
		}
		c.toosmall(n)
	}
}

// write gives b to the caller's next read, for it to send to the peer.
func (c *conv) write(b []byte) {
	c.ok()
	for {
		r := c.rio()
		switch r.verb {
		case "read":
			r.answerData(b)
			return
		case "authinfo":
			r.answer("error authentication unfinished")
		default:
			r.phase("protocol phase error")
		}
	}
}

// finished records the protocol's result and ends the exchange.
func (c *conv) finished(ai *authinfo) {
	c.ai = ai
	c.ok()
	for {
		r := c.rio()
		switch r.verb {
		case "read", "write":
			c.doneReply(r)
			return
		default:
			r.phase("protocol phase error")
		}
	}
}
