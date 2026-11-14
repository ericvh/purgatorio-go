package lethe

import (
	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// Tree is what a server serves. Paths are valid Lethe paths; errors are
// Inferno strings. Permission checks are the server's, from the modes
// and owners the tree reports.
type Tree interface {
	Stat(path string) (styx.Dir, error)
	// Read reads path at off into buf; it returns the file's qid at the
	// time of the read and the count.
	Read(path string, off uint64, buf []byte) (styx.Qid, int, error)
	// List returns the directory's qid and at most limit of its entries
	// whose names sort after after (as bytes), in that order.
	List(path string, after []byte, limit int) (styx.Qid, []styx.Dir, error)
}

// Config is a server's settings.
type Config struct {
	// Lifetime is how long a ticket lasts, in seconds.
	Lifetime uint64
	// Window is how far a request's time may be from the server's.
	Window uint64
	// Allow are the rights this server grants.
	Allow uint8
}

// DefaultConfig is lethed's: an hour's tickets, a minute's window, reads.
func DefaultConfig() Config { return Config{Lifetime: 3600, Window: 60, Allow: RightRead} }

// Env is what the server needs from its host for one frame.
type Env struct {
	// Now is seconds since 1970 UTC.
	Now    uint64
	Random func([]byte)
	// Trust gives the keys trusted for a user name, read when a Tauth
	// comes in.
	Trust func(uname string) Trust
}

// Event is what happened, for a log.
type Event struct {
	// Kind is "issued", "answered" or "refused".
	Kind   string
	Uname  string
	Rights uint8
	Prefix string
	Op     string
	Path   string
	// Error is the error answered, if any.
	Error string
}

// String is the event as lethed -v logs it.
func (e Event) String() string {
	switch e.Kind {
	case "issued":
		return "ticket for " + e.Uname + " rights " + itoa(int(e.Rights)) + " prefix " + e.Prefix
	case "answered":
		s := e.Uname + " " + e.Op + " " + e.Path
		if e.Error != "" {
			s += ": " + e.Error
		}
		return s
	}
	return "refused: " + e.Error
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// Server answers frames. It keeps nothing between them.
type Server struct {
	Config Config
	// Clear is whether clear requests are answered (lethed -A). Off,
	// they are refused with "authentication required".
	Clear bool
	// Tickets issues and opens tickets; nil serves no client that
	// authenticates.
	Tickets Tickets
}

// Tickets is the authenticated half of a server: the Tauth exchange and
// the opening of sealed requests. Its methods answer with whole frames;
// Answer gives them the reply to an opened request.
type Tickets interface {
	// Auth answers a Tauth frame (raw is the whole frame) or refuses it.
	Auth(s *Server, env *Env, id uint32, raw []byte, f Frame) ([]byte, Event, error)
	// Request opens a Treq, has s answer it, and seals the reply; or
	// refuses it in the clear.
	Request(s *Server, tree Tree, env *Env, id uint32, raw []byte, f *Treq) ([]byte, Event, error)
}

// Who a request is answered for.
type Who struct {
	Uname  string
	Rights uint8
	Prefix string
	// Owner is whether the user can own a file. A clear request never
	// does: it gets the other bits whatever the owner is called.
	Owner bool
}

func (w *Who) allowed(d *styx.Dir, bit uint32) bool {
	if w.Owner {
		return Allowed(w.Uname, d, bit)
	}
	return d.Mode&bit != 0
}

// Allowed reports whether user may access d for bit (4 read, 1 search):
// the owner's bits for its owner, else the other bits.
func Allowed(user string, d *styx.Dir, bit uint32) bool {
	bits := d.Mode
	if d.UID == user {
		bits = d.Mode >> 6
	}
	return bits&bit != 0
}

func errstr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Handle answers one whole frame.
func (s *Server) Handle(tree Tree, env *Env, raw []byte) ([]byte, Event) {
	id := FrameID(raw)
	refuse := func(e string) ([]byte, Event) {
		return ErrorFrame(id, e), Event{Kind: "refused", Error: e}
	}
	_, f, err := Decode(raw)
	if err != nil {
		return refuse(err.Error())
	}
	switch f := f.(type) {
	case *Tauth:
		if s.Tickets == nil {
			return refuse(s.noTickets())
		}
		r, ev, err := s.Tickets.Auth(s, env, id, raw, f)
		if err != nil {
			return refuse(err.Error())
		}
		return r, ev
	case *Treq:
		if s.Tickets == nil {
			return refuse(ETicketKey)
		}
		r, ev, err := s.Tickets.Request(s, tree, env, id, raw, f)
		if err != nil {
			return refuse(err.Error())
		}
		return r, ev
	case *Tclear:
		// The clear mode is asked for on both ends: a server that was
		// not told to allow it says so and reads nothing.
		if !s.Clear {
			return refuse(EAuthRequired)
		}
		return s.clearRequest(tree, id, f.Request)
	}
	return refuse(EMessage)
}

// noTickets is the refusal of a Tauth by a server that issues no
// tickets: as one that trusts no key.
func (s *Server) noTickets() string {
	if s.Clear {
		return EAuthNotRequired
	}
	return ENoTrust
}

func (s *Server) clearRequest(tree Tree, id uint32, req []byte) ([]byte, Event) {
	who := &Who{Uname: None, Rights: s.Config.Allow & RightRead, Prefix: "/"}
	op, err := DecodeOp(req)
	reply, name, p, e := s.Decoded(tree, who, op, err)
	return Encode(id, &Rclear{Reply: reply}), Event{Kind: "answered", Uname: None, Op: name, Path: p, Error: e}
}

// Decoded is the encoded reply to a request that decoded (op) or did not
// (err, an *Undecoded), with the op's name, path and error for the log.
func (s *Server) Decoded(tree Tree, who *Who, op Op, err error) (reply []byte, name, path, ename string) {
	if err != nil {
		u, _ := err.(*Undecoded)
		switch {
		case u != nil && u.Write:
			name, ename = "write", abi.Eperm
		case u != nil && !u.Bad:
			name, ename = "unknown", abi.Enotimpl
		default:
			name, ename = "bad", EMessage
		}
		return EncodeReply(&RError{Ename: ename}), name, "", ename
	}
	r, aerr := s.answer(tree, who, op)
	if aerr != nil {
		ename = aerr.Error()
		return EncodeReply(&RError{Ename: ename}), op.Name(), op.Path(), ename
	}
	return r, op.Name(), op.Path(), ""
}

// reach checks the requester's rights and prefix and the tree's
// permissions for p: search on every directory above it.
func reach(tree Tree, who *Who, p string) (styx.Dir, error) {
	if !ValidPath(p) {
		return styx.Dir{}, Error(abi.Efilename)
	}
	if who.Rights&RightRead == 0 || !Within(p, who.Prefix) {
		return styx.Dir{}, Error(abi.Eperm)
	}
	for _, a := range Ancestors(p) {
		d, err := tree.Stat(a)
		if err != nil {
			return styx.Dir{}, err
		}
		if d.Mode&styx.DMDIR == 0 {
			return styx.Dir{}, Error(abi.Enotdir)
		}
		if !who.allowed(&d, 1) {
			return styx.Dir{}, Error(abi.Eperm)
		}
	}
	return tree.Stat(p)
}

// walk is the encoded Rwalk for walking names from the directory path,
// whose entry is d and which the requester can reach.
func walk(tree Tree, who *Who, path string, d styx.Dir, names []string) []byte {
	qids := make([]styx.Qid, 0, len(names))
	cur, p := d, path
	for _, name := range names {
		step := func() (styx.Dir, error) {
			if cur.Mode&styx.DMDIR == 0 {
				return styx.Dir{}, Error(abi.Enotdir)
			}
			if !who.allowed(&cur, 1) {
				return styx.Dir{}, Error(abi.Eperm)
			}
			next := Join(p, name)
			if !ValidElem(name) || !ValidPath(next) {
				return styx.Dir{}, Error(abi.Efilename)
			}
			return tree.Stat(next)
		}
		next, err := step()
		if err != nil {
			return EncodeReply(&Rwalk{Qids: qids, Error: err.Error()})
		}
		p = Join(p, name)
		qids = append(qids, next.Qid)
		cur = next
	}
	return EncodeReply(&Rwalk{Qids: qids, Stat: &cur})
}

func (s *Server) answer(tree Tree, who *Who, op Op) ([]byte, error) {
	d, err := reach(tree, who, op.Path())
	if err != nil {
		return nil, err
	}
	switch o := op.(type) {
	case *Stat:
		return EncodeReply(&Rstat{Dir: d}), nil
	case *Walk:
		return walk(tree, who, o.P, d, o.Names), nil
	case *Read:
		if d.Mode&styx.DMDIR != 0 {
			return nil, Error(abi.Eisdir)
		}
		if !who.allowed(&d, 4) {
			return nil, Error(abi.Eperm)
		}
		if o.Cond && o.Have == d.Qid.Vers {
			return EncodeReply(&Rread{Qid: d.Qid, Unchanged: true}), nil
		}
		buf := make([]byte, min(o.Count, MaxCount))
		q, n, err := tree.Read(o.P, o.Offset, buf)
		if err != nil {
			return nil, err
		}
		return EncodeReply(&Rread{Qid: q, Data: buf[:min(n, len(buf))]}), nil
	case *Readdir:
		if d.Mode&styx.DMDIR == 0 {
			return nil, Error(abi.Enotdir)
		}
		if !who.allowed(&d, 4) {
			return nil, Error(abi.Eperm)
		}
		max := int(min(o.Count, MaxCount))
		// No entry packs smaller than StatFixLen, so one more than this
		// many cannot fit: a full list always overflows the page, and a
		// shorter one is the end of the directory.
		q, list, err := tree.List(o.P, o.Cookie, max/StatFixLen+1)
		if err != nil {
			return nil, err
		}
		var data []byte
		var last []byte
		more := false
		for i := range list {
			e := &list[i]
			if !dirFits(e) {
				return nil, Error(EMessage)
			}
			n := StatFixLen + len(e.Name) + len(e.UID) + len(e.GID) + len(e.MUID)
			if len(data)+n > max {
				more = true
				break
			}
			data = styx.MarshalDir(data, e)
			last = []byte(e.Name)
		}
		if more && len(data) == 0 {
			return nil, Error(abi.Eshortstat)
		}
		if !more {
			last = nil
		}
		return EncodeReply(&Rreaddir{Qid: q, Cookie: last, Entries: data}), nil
	}
	return nil, Error(EMessage)
}

// Trust is what a server trusts for one user, read when a Tauth comes
// in: keys held directly, trusted signers, and revocations.
type Trust struct {
	Keys    [][32]byte
	Signers [][32]byte
	Revoked [][32]byte
	// Damaged is set when the revocation list did not parse.
	Damaged bool
}

// RequiresAuth reports whether anyone could authenticate: some key or
// signer is trusted.
func (t *Trust) RequiresAuth() bool { return len(t.Keys) > 0 || len(t.Signers) > 0 }
