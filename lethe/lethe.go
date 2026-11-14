// Package lethe is Lethe, a stateless, authenticated protocol for reading
// static file trees, wire-compatible with the Rust Purgatorio's
// (docs/lethe.md here, and the Rust design it points to).
//
// The package holds what both ends need: frames and the requests and
// replies inside them (msg.go), the path rules (path.go), a server engine
// over a Tree (server.go), and the client's side (client.go). It is
// portable: the kernel's #λ device and the host server lethed share it.
package lethe

const (
	// Version is the fifth byte of every frame.
	Version = 1
	// Header is a frame's header: size[4] version[1] type[1] id[4].
	Header = 10
	// MaxFrame is the largest frame.
	MaxFrame = 65536
	// MaxCount is the largest count of a read or a directory read.
	MaxCount = 32768
	// MaxWalk is the most names one Twalk carries.
	MaxWalk = 64
	// NonceLen is an XChaCha20-Poly1305 nonce.
	NonceLen = 24
	// TagLen is a Poly1305 tag.
	TagLen = 16
	// EphLen is an X25519 public key.
	EphLen = 32
)

// None is the user a clear request runs as: Inferno's unauthenticated
// identity.
const None = "none"

// Rights a ticket can carry.
const (
	RightRead   = 1
	RightWrite  = 2
	RightStream = 4
	// RightServerProof is reserved: it asks for the server's proof in
	// Rauth (later).
	RightServerProof = 0x80
	// Grantable are the bits a server may grant.
	Grantable = RightRead | RightWrite | RightStream
)

// Outer frame types.
const (
	FTauth  = 1
	FRauth  = 2
	FTreq   = 3
	FRreq   = 4
	FRerror = 5
	// FTclear is a request with no ticket, not sealed: the clear mode.
	FTclear = 9
	// FRclear is a reply, not sealed.
	FRclear = 10
)

// Inner request and reply codes.
const (
	OpTstat    = 0x10
	OpRstat    = 0x11
	OpTread    = 0x12
	OpRread    = 0x13
	OpTreaddir = 0x14
	OpRreaddir = 0x15
	// OpTdigest and OpRdigest are reserved: the SHA-256 of a file.
	OpTdigest = 0x16
	OpRdigest = 0x17
	// OpTwalk walks several names from a directory in one request.
	OpTwalk  = 0x18
	OpRwalk  = 0x19
	OpRerror = 0x7f
)

func isWriteOp(c uint8) bool { return c >= 0x20 && c <= 0x2f }

// Errors Lethe adds to Inferno's.
const (
	EVersion       = "unknown lethe version"
	EMessage       = "bad lethe message"
	ETicket        = "bad ticket"
	ETicketKey     = "unknown ticket key"
	ETicketExpired = "ticket expired"
	ERequestExpiry = "request expired"
	EMac           = "bad record mac"
	// EAuthRequired answers a clear request to a server not started
	// with -A.
	EAuthRequired = "authentication required"
	// EAuthNotRequired answers a Tauth to a server started with -A that
	// trusts no key for the user.
	EAuthNotRequired = "authentication not required"
	EAuthFailed      = "authentication failed"
	ENoTrust         = "server trusts no keys"
	ERevoked         = "certificate revoked"
)

// Renew reports whether a client that gets e in the clear should
// authenticate again and resend.
func Renew(e string) bool {
	switch e {
	case ETicket, ETicketKey, ETicketExpired, ERequestExpiry:
		return true
	}
	return false
}

// Error is an error string, as Inferno's.
type Error string

func (e Error) Error() string { return string(e) }
