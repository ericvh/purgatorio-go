package lethe

import (
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/styx"
)

// Frames and the plaintexts inside them. Decoding is strict: a message
// must end exactly where its last field does, so each has one encoding,
// and Encode gives back the bytes Decode took.

// AuthContext starts the message a Tauth signature covers.
const AuthContext = "lethe1 auth\x00"

// FrameSize checks the size in a frame's first four bytes before
// anything else is read.
func FrameSize(size [4]byte) (int, error) {
	n := le.Uint32(size[:])
	if n < Header || n > MaxFrame {
		return 0, Error(EMessage)
	}
	return int(n), nil
}

// HeaderBytes is a frame's header, the associated data of what it seals.
func HeaderBytes(ty uint8, id uint32, size int) [Header]byte {
	var h [Header]byte
	le.PutUint32(h[0:], uint32(size))
	h[4] = Version
	h[5] = ty
	le.PutUint32(h[6:], id)
	return h
}

// FrameID is a frame's id, or 0 when it has no header.
func FrameID(b []byte) uint32 {
	if len(b) < Header {
		return 0
	}
	return le.Uint32(b[6:])
}

// Frame is an outer frame's body.
type Frame interface {
	Type() uint8
	put(w *writer)
}

// Tauth is the client's ephemeral key, what it asks for, who it is, and
// its signature over Message.
type Tauth struct {
	Eph       [EphLen]byte
	Rights    uint8
	Prefix    string
	Uname     string
	Kind      uint8 // 0 a public key, 1 a pgcert1 line
	Body      []byte
	Signature [64]byte
}

// Tauth2 is the hybrid exchange's Tauth: the client's ephemeral X25519
// key and ML-KEM-768 encapsulation key, what it asks for, who it is, and
// its hybrid signature over Message.
type Tauth2 struct {
	Eph       [EphLen]byte
	EK        [KEMKeyLen]byte
	Rights    uint8
	Prefix    string
	Uname     string
	Kind      uint8 // 0 a hybrid public key, 1 a pgcert2 line
	Body      []byte
	Signature []byte
}

// Rauth2 is the server's ephemeral key, the ML-KEM ciphertext and the
// sealed Grant.
type Rauth2 struct {
	Eph    [EphLen]byte
	CT     [KEMCtLen]byte
	Nonce  [NonceLen]byte
	Sealed []byte
}

// Rauth is the server's ephemeral key and the sealed Grant.
type Rauth struct {
	Eph    [EphLen]byte
	Nonce  [NonceLen]byte
	Sealed []byte
}

// Treq is a ticket and a sealed Request. With Ref set it is a Treqref:
// the ticket's reference (Tref) in place of the ticket, which is nil.
type Treq struct {
	Ticket []byte
	Ref    *[TrefLen]byte
	Nonce  [NonceLen]byte
	Sealed []byte
}

// Rreq is a sealed Reply.
type Rreq struct {
	Nonce  [NonceLen]byte
	Sealed []byte
}

// Rerror is an error in the clear.
type Rerror struct{ Ename string }

// Tclear is a request in the clear: an encoded Op, with no ticket, no
// time and no seal.
type Tclear struct{ Request []byte }

// Rclear is a reply in the clear: an encoded Reply.
type Rclear struct{ Reply []byte }

func (*Tauth) Type() uint8 { return FTauth }
func (*Rauth) Type() uint8 { return FRauth }
func (f *Treq) Type() uint8 {
	if f.Ref != nil {
		return FTreqref
	}
	return FTreq
}
func (*Rreq) Type() uint8   { return FRreq }
func (*Rerror) Type() uint8 { return FRerror }
func (*Tclear) Type() uint8 { return FTclear }
func (*Rclear) Type() uint8 { return FRclear }
func (*Tauth2) Type() uint8 { return FTauth2 }
func (*Rauth2) Type() uint8 { return FRauth2 }

// AuthContext2 starts the message a Tauth2 signature covers.
const AuthContext2 = "lethe2 auth\x00"

// Message is what the Tauth2 signature covers.
func (t *Tauth2) Message() []byte {
	w := &writer{}
	w.bytes([]byte(AuthContext2))
	w.bytes(t.Eph[:])
	w.bytes(t.EK[:])
	w.u8(t.Rights)
	w.str16(t.Prefix)
	w.str16(t.Uname)
	w.u8(t.Kind)
	w.data16(t.Body)
	return w.b
}

func (t *Tauth2) put(w *writer) {
	w.bytes(t.Eph[:])
	w.bytes(t.EK[:])
	w.u8(t.Rights)
	w.str16(t.Prefix)
	w.str16(t.Uname)
	w.u8(t.Kind)
	w.data16(t.Body)
	w.data16(t.Signature)
}

func (f *Rauth2) put(w *writer) {
	w.bytes(f.Eph[:])
	w.bytes(f.CT[:])
	w.bytes(f.Nonce[:])
	w.bytes(f.Sealed)
}

// Message is what the Tauth signature covers.
func (t *Tauth) Message() []byte {
	return AuthMessage(t.Eph[:], t.Rights, t.Prefix, t.Uname, t.Kind, t.Body)
}

// AuthMessage is the signed message of a Tauth with these fields.
func AuthMessage(eph []byte, rights uint8, prefix, uname string, kind uint8, body []byte) []byte {
	w := &writer{b: make([]byte, 0, len(AuthContext)+EphLen+8+len(prefix)+len(uname)+len(body))}
	w.bytes([]byte(AuthContext))
	w.bytes(eph)
	w.u8(rights)
	w.str16(prefix)
	w.str16(uname)
	w.u8(kind)
	w.data16(body)
	return w.b
}

func (t *Tauth) put(w *writer) {
	w.bytes(t.Eph[:])
	w.u8(t.Rights)
	w.str16(t.Prefix)
	w.str16(t.Uname)
	w.u8(t.Kind)
	w.data16(t.Body)
	w.bytes(t.Signature[:])
}
func (f *Rauth) put(w *writer) { w.bytes(f.Eph[:]); w.bytes(f.Nonce[:]); w.bytes(f.Sealed) }
func (f *Treq) put(w *writer) {
	if f.Ref != nil {
		w.bytes(f.Ref[:])
	} else {
		w.data16(f.Ticket)
	}
	w.bytes(f.Nonce[:])
	w.bytes(f.Sealed)
}
func (f *Rreq) put(w *writer)   { w.bytes(f.Nonce[:]); w.bytes(f.Sealed) }
func (f *Rerror) put(w *writer) { w.str16(f.Ename) }
func (f *Tclear) put(w *writer) { w.bytes(f.Request) }
func (f *Rclear) put(w *writer) { w.bytes(f.Reply) }

// Encode returns the bytes of the frame f with id.
func Encode(id uint32, f Frame) []byte {
	w := &writer{b: make([]byte, Header, 64)}
	f.put(w)
	h := HeaderBytes(f.Type(), id, len(w.b))
	copy(w.b, h[:])
	return w.b
}

func sealed(r *reader) []byte {
	s := r.rest()
	if len(s) < TagLen {
		r.err = true
	}
	return s
}

// Decode decodes one whole frame.
func Decode(b []byte) (uint32, Frame, error) {
	r := &reader{b: b}
	size := r.u32()
	if r.err || int(size) != len(b) || size > MaxFrame {
		return 0, nil, Error(EMessage)
	}
	if r.u8() != Version {
		return 0, nil, Error(EVersion)
	}
	ty := r.u8()
	id := r.u32()
	if r.err {
		return 0, nil, Error(EMessage)
	}
	var f Frame
	switch ty {
	case FTauth:
		t := &Tauth{}
		r.array(t.Eph[:])
		t.Rights = r.u8()
		t.Prefix = r.str16()
		t.Uname = r.str16()
		t.Kind = r.u8()
		t.Body = r.data16()
		r.array(t.Signature[:])
		f = t
	case FRauth:
		t := &Rauth{}
		r.array(t.Eph[:])
		r.array(t.Nonce[:])
		t.Sealed = sealed(r)
		f = t
	case FTreq:
		t := &Treq{Ticket: r.data16()}
		r.array(t.Nonce[:])
		t.Sealed = sealed(r)
		f = t
	case FTreqref:
		t := &Treq{Ref: new([TrefLen]byte)}
		r.array(t.Ref[:])
		r.array(t.Nonce[:])
		t.Sealed = sealed(r)
		f = t
	case FRreq:
		t := &Rreq{}
		r.array(t.Nonce[:])
		t.Sealed = sealed(r)
		f = t
	case FRerror:
		f = &Rerror{Ename: r.str16()}
	case FTauth2:
		t := &Tauth2{}
		r.array(t.Eph[:])
		r.array(t.EK[:])
		t.Rights = r.u8()
		t.Prefix = r.str16()
		t.Uname = r.str16()
		t.Kind = r.u8()
		t.Body = r.data16()
		t.Signature = r.data16()
		f = t
	case FRauth2:
		t := &Rauth2{}
		r.array(t.Eph[:])
		r.array(t.CT[:])
		r.array(t.Nonce[:])
		t.Sealed = sealed(r)
		f = t
	case FTclear:
		f = &Tclear{Request: r.rest()}
	case FRclear:
		f = &Rclear{Reply: r.rest()}
	default:
		return 0, nil, Error(EMessage)
	}
	if !r.done() {
		return 0, nil, Error(EMessage)
	}
	return id, f, nil
}

// ErrorFrame is a clear Rerror frame.
func ErrorFrame(id uint32, ename string) []byte { return Encode(id, &Rerror{Ename: ename}) }

// Grant is what Rauth seals: the server's clock, the ticket's expiry,
// what was granted and the ticket.
type Grant struct {
	Now     uint64
	Expires uint64
	Rights  uint8
	Prefix  string
	Ticket  []byte
}

func (g *Grant) Encode() []byte {
	w := &writer{}
	w.u64(g.Now)
	w.u64(g.Expires)
	w.u8(g.Rights)
	w.str16(g.Prefix)
	w.data16(g.Ticket)
	return w.b
}

func DecodeGrant(b []byte) (*Grant, error) {
	r := &reader{b: b}
	g := &Grant{Now: r.u64(), Expires: r.u64(), Rights: r.u8(), Prefix: r.str16(), Ticket: r.data16()}
	if !r.done() {
		return nil, Error(EMessage)
	}
	return g, nil
}

// Op is an operation: inside a Treq, or the body of a Tclear.
type Op interface {
	// Path is the path the op is about.
	Path() string
	// Name is the op's name, for logs.
	Name() string
	put(w *writer)
}

// Stat asks for an entry.
type Stat struct{ P string }

// Read asks for at most Count bytes at Offset. With Cond the client
// holds data for version Have.
type Read struct {
	P      string
	Offset uint64
	Count  uint32
	Cond   bool
	Have   uint32
}

// Readdir asks for a page of packed entries after Cookie.
type Readdir struct {
	P      string
	Cookie []byte
	Count  uint32
}

// Walk walks Names in turn from the directory P (at most MaxWalk).
type Walk struct {
	P     string
	Names []string
}

func (o *Stat) Path() string    { return o.P }
func (o *Read) Path() string    { return o.P }
func (o *Readdir) Path() string { return o.P }
func (o *Walk) Path() string    { return o.P }
func (*Stat) Name() string      { return "stat" }
func (*Read) Name() string      { return "read" }
func (*Readdir) Name() string   { return "readdir" }
func (*Walk) Name() string      { return "walk" }

func (o *Stat) put(w *writer) { w.u8(OpTstat); w.str16(o.P) }
func (o *Read) put(w *writer) {
	w.u8(OpTread)
	w.str16(o.P)
	w.u64(o.Offset)
	w.u32(o.Count)
	if o.Cond {
		w.u8(1)
		w.u32(o.Have)
	} else {
		w.u8(0)
		w.u32(0)
	}
}
func (o *Readdir) put(w *writer) { w.u8(OpTreaddir); w.str16(o.P); w.data16(o.Cookie); w.u32(o.Count) }
func (o *Walk) put(w *writer) {
	w.u8(OpTwalk)
	w.str16(o.P)
	w.u16(uint16(len(o.Names)))
	for _, n := range o.Names {
		w.str16(n)
	}
}

// Undecoded is a request that does not decode: malformed (Code 0), a
// reserved write op, or another reserved or unknown op.
type Undecoded struct {
	Write bool
	Code  uint8
	Bad   bool
}

func (u *Undecoded) Error() string { return EMessage }

// EncodeOp is op u8 fields: the body of a Tclear, and a Treq's
// plaintext after its time.
func EncodeOp(o Op) []byte {
	w := &writer{}
	o.put(w)
	return w.b
}

// DecodeOp decodes what EncodeOp made, strictly. Its error is an
// *Undecoded.
func DecodeOp(b []byte) (Op, error) { return getOp(&reader{b: b}) }

func getOp(r *reader) (Op, error) {
	bad := &Undecoded{Bad: true}
	code := r.u8()
	if r.err {
		return nil, bad
	}
	var o Op
	switch code {
	case OpTstat:
		o = &Stat{P: r.str16()}
	case OpTread:
		x := &Read{P: r.str16(), Offset: r.u64(), Count: r.u32()}
		cond, vers := r.u8(), r.u32()
		switch {
		case cond == 0 && vers == 0:
		case cond == 1:
			x.Cond, x.Have = true, vers
		default:
			return nil, bad
		}
		o = x
	case OpTreaddir:
		o = &Readdir{P: r.str16(), Cookie: r.data16(), Count: r.u32()}
	case OpTwalk:
		x := &Walk{P: r.str16()}
		n := int(r.u16())
		if n > MaxWalk {
			return nil, bad
		}
		x.Names = make([]string, 0, n)
		for i := 0; i < n; i++ {
			x.Names = append(x.Names, r.str16())
		}
		o = x
	default:
		if isWriteOp(code) {
			return nil, &Undecoded{Write: true, Code: code}
		}
		return nil, &Undecoded{Code: code}
	}
	if !r.done() {
		return nil, bad
	}
	return o, nil
}

// Request is the plaintext of a Treq.
type Request struct {
	// Time is the client's estimate of the server's clock, in seconds.
	Time uint64
	Op   Op
}

func (q *Request) Encode() []byte {
	w := &writer{}
	w.u64(q.Time)
	q.Op.put(w)
	return w.b
}

// DecodeRequest decodes a request. The time is returned (with ok) even
// for a request whose op does not decode, so the server can check it
// first.
func DecodeRequest(b []byte) (t uint64, ok bool, op Op, err error) {
	r := &reader{b: b}
	t = r.u64()
	if r.err {
		return 0, false, nil, &Undecoded{Bad: true}
	}
	op, err = getOp(r)
	return t, true, op, err
}

// Reply is the plaintext of an Rreq, and the body of an Rclear.
type Reply interface{ putReply(w *writer) error }

// Rstat is a stat entry.
type Rstat struct{ Dir styx.Dir }

// Rread is data, or that the client's version is current.
type Rread struct {
	Qid       styx.Qid
	Unchanged bool
	Data      []byte
}

// Rreaddir is packed Styx stat entries and the cookie that resumes.
type Rreaddir struct {
	Qid     styx.Qid
	Cookie  []byte
	Entries []byte
}

// Rwalk is the qid of each name walked. When every name was walked
// Error is empty and Stat is the last one's entry; otherwise Error says
// why the next name could not be walked, and Stat is nil.
type Rwalk struct {
	Qids  []styx.Qid
	Error string
	Stat  *styx.Dir
}

// RError is the answer about a file: an Inferno error string.
type RError struct{ Ename string }

func putQid(w *writer, q styx.Qid) { w.u8(q.Type); w.u32(q.Vers); w.u64(q.Path) }
func getQid(r *reader) styx.Qid {
	return styx.Qid{Type: r.u8(), Vers: r.u32(), Path: r.u64()}
}

// dirFits reports whether d packs: its strings fit their fields.
func dirFits(d *styx.Dir) bool {
	for _, s := range []string{d.Name, d.UID, d.GID, d.MUID} {
		if len(s) > 0xffff {
			return false
		}
	}
	return StatFixLen+len(d.Name)+len(d.UID)+len(d.GID)+len(d.MUID) <= 0xffff+2
}

func (x *Rstat) putReply(w *writer) error {
	if !dirFits(&x.Dir) {
		return Error(EMessage)
	}
	w.u8(OpRstat)
	w.b = styx.MarshalDir(w.b, &x.Dir)
	return nil
}

func (x *Rread) putReply(w *writer) error {
	w.u8(OpRread)
	putQid(w, x.Qid)
	if x.Unchanged {
		w.u8(1)
	} else {
		w.u8(0)
	}
	w.data32(x.Data)
	return nil
}

func (x *Rreaddir) putReply(w *writer) error {
	w.u8(OpRreaddir)
	putQid(w, x.Qid)
	w.data16(x.Cookie)
	w.data32(x.Entries)
	return nil
}

func (x *Rwalk) putReply(w *writer) error {
	w.u8(OpRwalk)
	w.u16(uint16(len(x.Qids)))
	for _, q := range x.Qids {
		putQid(w, q)
	}
	w.str16(x.Error)
	switch {
	case x.Stat != nil && x.Error == "":
		if !dirFits(x.Stat) {
			return Error(EMessage)
		}
		w.b = styx.MarshalDir(w.b, x.Stat)
	case x.Stat == nil && x.Error != "":
	default:
		return Error(EMessage)
	}
	return nil
}

func (x *RError) putReply(w *writer) error { w.u8(OpRerror); w.str16(x.Ename); return nil }

// EncodeReply encodes x; one that cannot be encoded goes out as the
// error "bad lethe message".
func EncodeReply(x Reply) []byte {
	w := &writer{}
	if x.putReply(w) != nil {
		return EncodeReply(&RError{Ename: EMessage})
	}
	return w.b
}

// Dirs unpacks packed stat entries.
func Dirs(b []byte) ([]styx.Dir, error) {
	var out []styx.Dir
	for len(b) > 0 {
		if len(b) < 2 {
			return nil, Error(EMessage)
		}
		n := int(le.Uint16(b)) + 2
		if n > len(b) {
			return nil, Error(EMessage)
		}
		d, err := unpackDir(b[:n])
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
		b = b[n:]
	}
	return out, nil
}

// unpackDir is styx.UnmarshalDir with UTF-8 checked, as the Rust codec
// checks its strings.
func unpackDir(b []byte) (*styx.Dir, error) {
	d, err := styx.UnmarshalDir(b)
	if err != nil {
		return nil, Error(EMessage)
	}
	for _, s := range []string{d.Name, d.UID, d.GID, d.MUID} {
		if !utf8.ValidString(s) {
			return nil, Error(EMessage)
		}
	}
	return d, nil
}

// DecodeReply decodes a reply strictly.
func DecodeReply(b []byte) (Reply, error) {
	r := &reader{b: b}
	bad := Error(EMessage)
	var x Reply
	switch r.u8() {
	case OpRstat:
		d, err := unpackDir(r.rest())
		if err != nil {
			return nil, bad
		}
		x = &Rstat{Dir: *d}
	case OpRread:
		y := &Rread{Qid: getQid(r)}
		switch r.u8() {
		case 0:
		case 1:
			y.Unchanged = true
		default:
			return nil, bad
		}
		y.Data = r.data32()
		if len(y.Data) > MaxCount || (y.Unchanged && len(y.Data) != 0) {
			return nil, bad
		}
		x = y
	case OpRreaddir:
		y := &Rreaddir{Qid: getQid(r), Cookie: r.data16(), Entries: r.data32()}
		if r.err {
			return nil, bad
		}
		if _, err := Dirs(y.Entries); err != nil {
			return nil, bad
		}
		x = y
	case OpRwalk:
		n := int(r.u16())
		if n > MaxWalk {
			return nil, bad
		}
		y := &Rwalk{Qids: make([]styx.Qid, 0, n)}
		for i := 0; i < n; i++ {
			y.Qids = append(y.Qids, getQid(r))
		}
		y.Error = r.str16()
		if r.err {
			return nil, bad
		}
		rest := r.rest()
		if y.Error == "" {
			d, err := unpackDir(rest)
			if err != nil {
				return nil, bad
			}
			y.Stat = d
		} else if len(rest) != 0 {
			return nil, bad
		}
		x = y
	case OpRerror:
		x = &RError{Ename: r.str16()}
	default:
		return nil, bad
	}
	if !r.done() {
		return nil, bad
	}
	return x, nil
}

// StatFixLen is the size of a packed entry with empty strings, its own
// size field included.
const StatFixLen = 2 + 2 + 4 + 13 + 4 + 4 + 4 + 8 + 4*2
