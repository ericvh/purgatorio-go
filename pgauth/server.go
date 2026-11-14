package pgauth

// The server's proof of a version 3 or 4 handshake (docs/auth.md,
// "Server authentication"):
//
//	server proof = kind u8 || signature || body
//	message      = context || transcript[32] || bind[32] || kind u8 || body
//
// with context "pgauth3 server\x00" (version 3, the Rust sibling's) or
// "pgauth4 server\x00" (version 4, a hybrid key or a pgcert2 line and a
// hybrid signature). On a mount it is read from the auth fid after the
// offer: at offset Offer3Size for version 3, Offer4Size for version 4.

const (
	server3Context = "pgauth3 server\x00"
	server4Context = "pgauth4 server\x00"

	// SessionSize is what an agent is given to make a server proof: the
	// session's transcript, then its bound value (Keys.Session).
	SessionSize = 64
)

// ServerProofOffset is where the server's proof is read on the auth fid
// of a session of the given version.
func ServerProofOffset(version int) int64 {
	if version == 4 {
		return Offer4Size
	}
	return Offer3Size
}

// ServerMessage is what a server proof signs, from the session's 64
// bytes (Keys.Session); hybrid selects the version 4 context.
func ServerMessage(session []byte, hybrid bool, kind byte, body []byte) []byte {
	ctx := server3Context
	if hybrid {
		ctx = server4Context
	}
	m := make([]byte, 0, len(ctx)+len(session)+1+len(body))
	m = append(m, ctx...)
	m = append(m, session...)
	m = append(m, kind)
	return append(m, body...)
}

// AssembleServerProof builds a server proof from its parts.
func AssembleServerProof(kind byte, sig, body []byte) []byte {
	p := make([]byte, 0, 1+len(sig)+len(body))
	p = append(p, kind)
	p = append(p, sig...)
	return append(p, body...)
}

// MakeServerProof is the server's proof for keys, signed with k and
// carrying cert when given. The key must match the session: hybrid for
// version 4, classical for version 3.
func MakeServerProof(k *PrivateKey, cert *Cert, keys *Keys) ([]byte, error) {
	if k.Hybrid() != (keys.Version == 4) {
		return nil, EWrongAlg
	}
	kind, body := ProofBody(cert, k.Public())
	sig := k.Sign(ServerMessage(keys.Session(), k.Hybrid(), kind, body))
	return AssembleServerProof(kind, sig, body), nil
}

type serverProof struct {
	kind byte
	sig  []byte
	body []byte
}

func parseServerProof(p []byte, hybrid bool) (*serverProof, bool) {
	max, slen := MaxProof3, SignatureSize
	if hybrid {
		max, slen = MaxProof4, HybridSignatureSize
	}
	if len(p) > max || len(p) < 2+slen {
		return nil, false
	}
	return &serverProof{kind: p[0], sig: p[1 : 1+slen], body: p[1+slen:]}, true
}

// ServerKey is the server's public key in a proof that CheckServer has
// accepted.
func ServerKey(proof []byte, hybrid bool) (PublicKey, bool) {
	p, ok := parseServerProof(proof, hybrid)
	if !ok {
		return nil, false
	}
	switch p.kind {
	case KindKey:
		k := PublicKey(p.body)
		return k, k.Valid() && k.Hybrid() == hybrid
	case KindCert:
		c, err := ParseCert(p.body)
		if err != nil || c.Hybrid() != hybrid {
			return nil, false
		}
		return c.Key, true
	}
	return nil, false
}

// CheckServer checks the server's proof for the session keys against
// what the client trusts as a server: pol.Keys are server keys it holds
// directly, pol.Signers the signers whose certificates it takes. The
// certificate's name is not checked (CheckServerIdentity does that). An
// empty proof is EServerNone; anything else that fails is EServerFailed.
func CheckServer(pol *Policy, keys *Keys, proof []byte) error {
	if len(proof) == 0 {
		return EServerNone
	}
	hybrid := keys.Version == 4
	p, ok := parseServerProof(proof, hybrid)
	if !ok {
		return EServerFailed
	}
	msg := ServerMessage(keys.Session(), hybrid, p.kind, p.body)
	switch p.kind {
	case KindKey:
		k := PublicKey(p.body)
		if !k.Valid() || k.Hybrid() != hybrid || !Contains(pol.Keys, k.ID()) || pol.revoked(k) || !k.Verify(msg, p.sig) {
			return EServerFailed
		}
		return nil
	case KindCert:
		c, err := ParseCert(p.body)
		if err != nil || c.Hybrid() != hybrid || !c.Key.Verify(msg, p.sig) {
			return EServerFailed
		}
		if CheckCert(pol, c, "", false) != nil {
			return EServerFailed
		}
		return nil
	}
	return EServerFailed
}

// CheckServerIdentity checks who the server in proof is, for a client
// that dialled host (empty: no host, as for a mount of a descriptor):
// everything CheckServerHost checks except the signature over the
// session. pol.Keys are the server keys trusted for host. A certificate
// passes when its key is one of them, or when its name is host (ASCII
// case ignored) and a trusted signer made it; one for another name fails
// with ENameMismatch.
func CheckServerIdentity(pol *Policy, proof []byte, hybrid bool, host string) error {
	if len(proof) == 0 {
		return EServerNone
	}
	p, ok := parseServerProof(proof, hybrid)
	if !ok {
		return EServerFailed
	}
	switch p.kind {
	case KindKey:
		k := PublicKey(p.body)
		if !k.Valid() || k.Hybrid() != hybrid || !Contains(pol.Keys, k.ID()) || pol.revokedOrDamaged(k) {
			return EServerFailed
		}
		return nil
	case KindCert:
		c, err := ParseCert(p.body)
		if err != nil || c.Hybrid() != hybrid {
			return EServerFailed
		}
		if host != "" && !Contains(pol.Keys, c.Key.ID()) && !asciiEqualFold(host, c.Name) {
			return ENameMismatch
		}
		if CheckCert(pol, c, "", false) != nil {
			return EServerFailed
		}
		return nil
	}
	return EServerFailed
}

// CheckServerHost is CheckServer for a client that dialled host: the
// proof must verify for the session, and CheckServerIdentity must pass.
func CheckServerHost(pol *Policy, keys *Keys, proof []byte, host string) error {
	if err := CheckServer(pol, keys, proof); err != nil {
		return err
	}
	return CheckServerIdentity(pol, proof, keys.Version == 4, host)
}
