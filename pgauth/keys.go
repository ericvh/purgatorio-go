package pgauth

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// Hex is lowercase hex, as every text format here uses.
func Hex(b []byte) string {
	out := make([]byte, 2*len(b))
	for i, c := range b {
		out[2*i] = hexDigits[c>>4]
		out[2*i+1] = hexDigits[c&15]
	}
	return string(out)
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// HexDecode decodes lowercase hex of any even length.
func HexDecode(s string) ([]byte, bool) {
	if len(s)%2 != 0 {
		return nil, false
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		h, ok1 := hexDigit(s[2*i])
		l, ok2 := hexDigit(s[2*i+1])
		if !ok1 || !ok2 {
			return nil, false
		}
		out[i] = h<<4 | l
	}
	return out, true
}

func hexArray(s string, n int) ([]byte, bool) {
	if len(s) != 2*n {
		return nil, false
	}
	return HexDecode(s)
}

// lines splits text at newlines, as Rust's split on b'\n' does: a final
// newline gives a last empty line.
func lines(text []byte) [][]byte { return bytes.Split(text, []byte{'\n'}) }

func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// ParseList parses a key list: one key ID per line (64 hex digits, or
// "pq:" and 64 hex digits for a hybrid key), optionally a space or tab
// and a comment. Blank lines and lines starting with # are ignored. It
// fails when a line is malformed: a revocation list that does not parse
// must refuse everything.
func ParseList(text []byte) ([]KeyID, bool) {
	keys := []KeyID{}
	for _, line := range lines(text) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		blank := true
		for _, c := range line {
			if !asciiSpace(c) {
				blank = false
				break
			}
		}
		if blank || (len(line) > 0 && line[0] == '#') {
			continue
		}
		n := 64
		if bytes.HasPrefix(line, []byte(HybridPrefix)) {
			n += len(HybridPrefix)
		}
		key, rest := line[:min(len(line), n)], line[min(len(line), n):]
		if len(rest) > 0 && rest[0] != ' ' && rest[0] != '\t' {
			return nil, false
		}
		id, ok := ParseKeyID(string(key))
		if !ok {
			return nil, false
		}
		keys = append(keys, id)
	}
	return keys, true
}

// ParseListLenient is ParseList that skips malformed lines, for trust
// lists, where a bad line must grant nothing and cancel nothing.
func ParseListLenient(text []byte) []KeyID {
	keys := []KeyID{}
	for _, line := range lines(text) {
		if k, ok := ParseList(line); ok && len(k) > 0 {
			keys = append(keys, k[0])
		}
	}
	return keys
}

// ListLine is one line of a key list, with its newline.
func ListLine(id KeyID, comment string) string {
	s := id.String()
	if comment != "" {
		s += " " + comment
	}
	return s + "\n"
}

// Contains reports whether id is in list.
func Contains(list []KeyID, id KeyID) bool {
	for _, k := range list {
		if k == id {
			return true
		}
	}
	return false
}

// AnyName is the name in a named key list that matches every name.
const AnyName = "*"

// Named is one line of a named key list (/lib/keys/users,
// /lib/keys/servers): a key and the name it is trusted for.
type Named struct {
	Name string
	ID   KeyID
}

// ParseNamed parses a named key list: "<name> <key> [comment]" per line.
// Blank lines, lines starting with # and malformed lines are skipped.
func ParseNamed(text []byte) []Named {
	var out []Named
	for _, line := range lines(text) {
		if !utf8.Valid(line) {
			continue
		}
		s := strings.TrimSpace(string(line))
		if s == "" || s[0] == '#' {
			continue
		}
		w := strings.FieldsFunc(s, func(r rune) bool {
			return r < 0x80 && asciiSpace(byte(r))
		})
		if len(w) < 2 {
			continue
		}
		if id, ok := ParseKeyID(w[1]); ok {
			out = append(out, Named{Name: w[0], ID: id})
		}
	}
	return out
}

// NameMatches reports whether pattern, a name from a named key list,
// covers name: it is AnyName, or name with ASCII case ignored.
func NameMatches(pattern, name string) bool {
	return pattern == AnyName || asciiEqualFold(pattern, name)
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// KeysFor returns the keys of list trusted for name, or with all set,
// every key in it.
func KeysFor(list []Named, name string, all bool) []KeyID {
	var keys []KeyID
	for _, n := range list {
		if (all || NameMatches(n.Name, name)) && !Contains(keys, n.ID) {
			keys = append(keys, n.ID)
		}
	}
	return keys
}

// NamedLine is one line of a named key list.
func NamedLine(name string, id KeyID) string { return name + " " + id.String() + "\n" }

// AuthInfo is what getauthinfo and mkauthinfo write, at
// /usr/<user>/keyring/<keyname>: the user's key, the signer that
// certified it, and the certificate.
//
//	key <seed in hex>
//	signer <the signer's key ID>
//	pgcert1 ... (or pgcert2 ...)
//
// A classical authinfo is the Rust sibling's format exactly. A hybrid one
// has a 64-byte seed, a "pq:" signer and a pgcert2 certificate.
type AuthInfo struct {
	Key    *PrivateKey
	Signer KeyID
	Cert   *Cert
}

// Encode writes the authinfo.
func (a *AuthInfo) Encode() string {
	return "key " + Hex(a.Key.seed) + "\nsigner " + a.Signer.String() + "\n" + a.Cert.Encode() + "\n"
}

// ParseAuthInfo reads an authinfo strictly.
func ParseAuthInfo(text []byte) (*AuthInfo, error) {
	ls := lines(text)
	if len(ls) < 3 {
		return nil, EBadInfo
	}
	seedHex, ok := bytes.CutPrefix(ls[0], []byte("key "))
	if !ok {
		return nil, EBadInfo
	}
	seed, ok := HexDecode(string(seedHex))
	if !ok {
		return nil, EBadInfo
	}
	key, err := NewPrivateKey(seed)
	if err != nil {
		return nil, EBadInfo
	}
	sig, ok := bytes.CutPrefix(ls[1], []byte("signer "))
	if !ok {
		return nil, EBadInfo
	}
	signer, ok := ParseKeyID(string(sig))
	if !ok || signer.Hybrid != key.Hybrid() {
		return nil, EBadInfo
	}
	cert, err := ParseCert(ls[2])
	if err != nil {
		return nil, EBadInfo
	}
	for _, l := range ls[3:] {
		if len(l) != 0 {
			return nil, EBadInfo
		}
	}
	if !cert.Key.Equal(key.Public()) {
		return nil, EBadInfo
	}
	return &AuthInfo{Key: key, Signer: signer, Cert: cert}, nil
}

// FindCert finds a certificate in text: the whole text, or a line of it
// (an authinfo, for instance).
func FindCert(text []byte) (*Cert, bool) {
	for _, l := range lines(text) {
		if bytes.HasPrefix(l, []byte("pgcert")) {
			c, err := ParseCert(l)
			return c, err == nil
		}
	}
	return nil, false
}
