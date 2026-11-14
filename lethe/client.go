package lethe

import "io"

// The client's side. A client told to use the clear mode sends Tclear
// requests: no ticket, no keys, nothing sealed. The two modes never mix:
// a sealed request's reply must be sealed and a clear request's clear, so
// neither side can be talked into the other mode by what comes back.

// Failure is why a request got no answer.
type Failure struct {
	// Kind is FailClear for an error in the clear (Renew says whether a
	// new ticket helps), FailSealed for the server's answer about the
	// file, FailBad for a reply that did not open or decode.
	Kind int
	Text string
}

const (
	FailClear = iota
	FailSealed
	FailBad
)

func (f *Failure) Error() string { return f.Text }

// ClearFrame is a Tclear for op.
func ClearFrame(id uint32, op Op) []byte { return Encode(id, &Tclear{Request: EncodeOp(op)}) }

// ClearReply opens the reply to the clear request with id. A sealed
// reply is refused: a clear client holds no key to open one. The error
// is a *Failure.
func ClearReply(id uint32, raw []byte) (Reply, error) {
	rid, f, err := Decode(raw)
	if err != nil {
		return nil, &Failure{FailBad, err.Error()}
	}
	if rid != id {
		return nil, &Failure{FailBad, EMessage}
	}
	switch f := f.(type) {
	case *Rclear:
		r, err := DecodeReply(f.Reply)
		if err != nil {
			return nil, &Failure{FailBad, err.Error()}
		}
		if e, ok := r.(*RError); ok {
			return nil, &Failure{FailSealed, e.Ename}
		}
		return r, nil
	case *Rerror:
		return nil, &Failure{FailClear, f.Ename}
	}
	return nil, &Failure{FailBad, EMessage}
}

// ReadFrame reads one frame from r, checking its size before the body.
func ReadFrame(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n, err := FrameSize(size)
	if err != nil {
		return nil, err
	}
	b := make([]byte, n)
	copy(b, size[:])
	if _, err := io.ReadFull(r, b[4:]); err != nil {
		return nil, err
	}
	return b, nil
}
