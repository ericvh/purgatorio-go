package lethe

import "crypto/sha256"

// Tickets (the Rust ticket.rs): a session's master secret, the user and
// what they may do, sealed under a key only the servers hold.
//
//	ticket = keyid u32 || nonce[24] || XChaCha20-Poly1305(ticket key, aad, plain)
//	aad    = "lethe1 ticket" 0x00 || keyid
//	plain  = master[32] issued u64 expires u64 rights u8 uname s[2] prefix s[2]
//	keyid  = first 4 bytes of SHA-256("lethe1 ticket key" 0x00 || ticket key), LE

const (
	ticketAAD   = "lethe1 ticket\x00"
	ticketKeyID = "lethe1 ticket key\x00"
	// MaxTicket bounds a ticket a server will try to open.
	MaxTicket = 4 + NonceLen + TagLen + 32 + 17 + 2 + 255 + 2 + MaxPath
)

// Ticket is what a ticket holds.
type Ticket struct {
	Master  [32]byte
	Issued  uint64
	Expires uint64
	Rights  uint8
	Uname   string
	Prefix  string
}

func (t *Ticket) encode() []byte {
	w := &writer{}
	w.bytes(t.Master[:])
	w.u64(t.Issued)
	w.u64(t.Expires)
	w.u8(t.Rights)
	w.str16(t.Uname)
	w.str16(t.Prefix)
	return w.b
}

func decodeTicket(b []byte) (*Ticket, error) {
	r := &reader{b: b}
	t := &Ticket{}
	r.array(t.Master[:])
	t.Issued, t.Expires, t.Rights = r.u64(), r.u64(), r.u8()
	t.Uname, t.Prefix = r.str16(), r.str16()
	if !r.done() {
		return nil, Error(ETicket)
	}
	return t, nil
}

// TicketKey is a server's ticket key and its id.
type TicketKey struct {
	key [32]byte
	ID  uint32
}

// NewTicketKey makes the key and its id.
func NewTicketKey(key [32]byte) TicketKey {
	h := sha256.New()
	h.Write([]byte(ticketKeyID))
	h.Write(key[:])
	d := h.Sum(nil)
	return TicketKey{key: key, ID: le.Uint32(d)}
}

// Seal seals t with nonce (random).
func (k TicketKey) Seal(t *Ticket, nonce *[NonceLen]byte) []byte {
	id := le.AppendUint32(nil, k.ID)
	aad := append([]byte(ticketAAD), id...)
	out := append(id, nonce[:]...)
	return append(out, Seal(&k.key, nonce, aad, t.encode())...)
}

// Open opens a ticket sealed under k.
func (k TicketKey) Open(ticket []byte) (*Ticket, error) {
	if len(ticket) > MaxTicket || len(ticket) < 4+NonceLen+TagLen {
		return nil, Error(ETicket)
	}
	aad := append([]byte(ticketAAD), ticket[:4]...)
	nonce := [NonceLen]byte(ticket[4 : 4+NonceLen])
	plain, err := Open(&k.key, &nonce, aad, ticket[4+NonceLen:])
	if err != nil {
		return nil, Error(ETicket)
	}
	return decodeTicket(plain)
}

// OpenTicket opens a ticket with whichever of keys sealed it: "unknown
// ticket key" when none did, "bad ticket" when it was changed.
func OpenTicket(keys []TicketKey, ticket []byte) (*Ticket, error) {
	if len(ticket) < 4 {
		return nil, Error(ETicket)
	}
	id := le.Uint32(ticket)
	for _, k := range keys {
		if k.ID == id {
			return k.Open(ticket)
		}
	}
	return nil, Error(ETicketKey)
}
