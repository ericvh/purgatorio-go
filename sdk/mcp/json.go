// Package mcp holds what mcpd and mcpfs share: a small JSON reader and
// writer (RFC 8259) that keeps object members in order, keeps integers
// exact and never writes a raw newline, which MCP's stdio transport
// forbids inside a message; and base64. encoding/json does neither of the
// first two, and escapes <, > and &, which the Rust sibling's mcpd does
// not; its replies must be byte-compatible.
package mcp

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxDepth bounds nesting, so hostile input cannot exhaust the stack.
const maxDepth = 128

// A Value is nil (null), bool, int64, float64, string, []Value or Obj.
type Value = any

// Member is one member of an object.
type Member struct {
	K string
	V Value
}

// Obj is an object, members in order.
type Obj []Member

// O builds an object from alternating keys and values.
func O(kv ...any) Obj {
	o := make(Obj, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		o = append(o, Member{kv[i].(string), norm(kv[i+1])})
	}
	return o
}

// norm turns Go integers into int64.
func norm(v any) Value {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		if x > 1<<63-1 {
			return float64(x)
		}
		return int64(x)
	case []Obj:
		a := make([]Value, len(x))
		for i, o := range x {
			a[i] = o
		}
		return a
	}
	return v
}

// Get is member k of an object, or nil and false.
func Get(v Value, k string) (Value, bool) {
	o, ok := v.(Obj)
	if !ok {
		return nil, false
	}
	for _, m := range o {
		if m.K == k {
			return m.V, true
		}
	}
	return nil, false
}

// Has says whether object v has member k.
func Has(v Value, k string) bool { _, ok := Get(v, k); return ok }

// Str is member k as a string.
func Str(v Value, k string) (string, bool) {
	x, _ := Get(v, k)
	s, ok := x.(string)
	return s, ok
}

// Bool is member k as a boolean.
func Bool(v Value, k string) (bool, bool) {
	x, _ := Get(v, k)
	b, ok := x.(bool)
	return b, ok
}

// Int is member k as a whole number.
func Int(v Value, k string) (int64, bool) {
	x, _ := Get(v, k)
	return AsInt(x)
}

// AsInt is v as a whole number, including a float with no fraction.
func AsInt(v Value) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		if x == float64(int64(x)) && x < 9e15 && x > -9e15 {
			return int64(x), true
		}
	}
	return 0, false
}

// Set replaces or appends member k.
func (o *Obj) Set(k string, v any) {
	for i := range *o {
		if (*o)[i].K == k {
			(*o)[i].V = norm(v)
			return
		}
	}
	*o = append(*o, Member{k, norm(v)})
}

// TypeName is the JSON type of v, for error messages.
func TypeName(v Value) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int64:
		return "integer"
	case float64:
		return "number"
	case string:
		return "string"
	case []Value:
		return "array"
	case Obj:
		return "object"
	}
	return "unknown"
}

// Equal compares two values as JSON values of the same kind.
func Equal(a, b Value) bool { return Encode(a) == Encode(b) && TypeName(a) == TypeName(b) }

// Encode is the compact, single-line encoding.
func Encode(v Value) string {
	var b strings.Builder
	write(&b, v)
	return b.String()
}

func write(b *strings.Builder, v Value) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		if x != x || x > 1.7976931348623157e308 || x < -1.7976931348623157e308 {
			b.WriteString("null")
		} else {
			b.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
		}
	case string:
		writeStr(b, x)
	case []Value:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	case Obj:
		b.WriteByte('{')
		for i, m := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeStr(b, m.K)
			b.WriteByte(':')
			write(b, m.V)
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

const hexd = "0123456789abcdef"

func writeStr(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		// Other controls, DEL, and the two line separators some
		// readers treat as newlines.
		case c < 0x20 || c == 0x7f || c == 0x2028 || c == 0x2029:
			b.WriteString(`\u`)
			b.WriteByte(hexd[c>>12&15])
			b.WriteByte(hexd[c>>8&15])
			b.WriteByte(hexd[c>>4&15])
			b.WriteByte(hexd[c&15])
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
}

// Parse parses one JSON text; anything but white space after it is an
// error. The text must be valid UTF-8.
func Parse(src string) (Value, error) {
	p := &parser{s: src}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.s) {
		return nil, p.err("unexpected text after the value")
	}
	return v, nil
}

// Error is a parse error.
type Error string

func (e Error) Error() string { return string(e) }

type parser struct {
	s   string
	pos int
}

func (p *parser) err(msg string) error {
	return Error(msg + " at byte " + strconv.Itoa(p.pos))
}

func (p *parser) peek() int {
	if p.pos < len(p.s) {
		return int(p.s[p.pos])
	}
	return -1
}

func (p *parser) space() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) literal(w string, v Value) (Value, error) {
	if strings.HasPrefix(p.s[p.pos:], w) {
		p.pos += len(w)
		return v, nil
	}
	return nil, p.err("bad literal")
}

func (p *parser) value(depth int) (Value, error) {
	if depth > maxDepth {
		return nil, p.err("nested too deeply")
	}
	switch c := p.peek(); {
	case c < 0:
		return nil, p.err("unexpected end of text")
	case c == 'n':
		return p.literal("null", nil)
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return s, nil
	case c == '[':
		p.pos++
		items := []Value{}
		p.space()
		if p.peek() == ']' {
			p.pos++
			return items, nil
		}
		for {
			p.space()
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
			p.space()
			switch p.peek() {
			case ',':
				p.pos++
			case ']':
				p.pos++
				return items, nil
			default:
				return nil, p.err("expected , or ]")
			}
		}
	case c == '{':
		p.pos++
		o := Obj{}
		p.space()
		if p.peek() == '}' {
			p.pos++
			return o, nil
		}
		for {
			p.space()
			if p.peek() != '"' {
				return nil, p.err("expected a member name")
			}
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			p.space()
			if p.peek() != ':' {
				return nil, p.err("expected :")
			}
			p.pos++
			p.space()
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			// A repeated name keeps its last value.
			o.Set(k, v)
			p.space()
			switch p.peek() {
			case ',':
				p.pos++
			case '}':
				p.pos++
				return o, nil
			default:
				return nil, p.err("expected , or }")
			}
		}
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	}
	return nil, p.err("unexpected character")
}

func (p *parser) digits() int {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
	}
	return p.pos - start
}

func (p *parser) number() (Value, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	is := p.pos
	n := p.digits()
	if n == 0 || n > 1 && p.s[is] == '0' {
		return nil, p.err("bad number")
	}
	float := false
	if p.peek() == '.' {
		float = true
		p.pos++
		if p.digits() == 0 {
			return nil, p.err("bad number")
		}
	}
	if c := p.peek(); c == 'e' || c == 'E' {
		float = true
		p.pos++
		if c := p.peek(); c == '+' || c == '-' {
			p.pos++
		}
		if p.digits() == 0 {
			return nil, p.err("bad number")
		}
	}
	text := p.s[start:p.pos]
	if !float {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, nil
		}
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, p.err("number out of range")
	}
	return f, nil
}

func (p *parser) hex4() (rune, error) {
	if p.pos+4 > len(p.s) {
		return 0, p.err(`bad \u escape`)
	}
	var r rune
	for _, c := range []byte(p.s[p.pos : p.pos+4]) {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, p.err(`bad \u escape`)
		}
		r = r<<4 | rune(d)
	}
	p.pos += 4
	return r, nil
}

func (p *parser) str() (string, error) {
	p.pos++
	var b strings.Builder
	for {
		start := p.pos
		for p.pos < len(p.s) {
			c := p.s[p.pos]
			if c == '"' || c == '\\' || c < 0x20 {
				break
			}
			p.pos++
		}
		b.WriteString(p.s[start:p.pos])
		switch c := p.peek(); {
		case c < 0:
			return "", p.err("unterminated string")
		case c == '"':
			p.pos++
			return b.String(), nil
		case c == '\\':
			p.pos++
			if p.pos >= len(p.s) {
				return "", p.err("unterminated string")
			}
			e := p.s[p.pos]
			p.pos++
			switch e {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'b':
				b.WriteByte(8)
			case 'f':
				b.WriteByte(12)
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				hi, err := p.hex4()
				if err != nil {
					return "", err
				}
				cp := hi
				if hi >= 0xd800 && hi < 0xdc00 {
					if !strings.HasPrefix(p.s[p.pos:], `\u`) {
						return "", p.err("unpaired surrogate")
					}
					p.pos += 2
					lo, err := p.hex4()
					if err != nil {
						return "", err
					}
					if lo < 0xdc00 || lo >= 0xe000 {
						return "", p.err("unpaired surrogate")
					}
					cp = 0x10000 + (hi-0xd800)<<10 + (lo - 0xdc00)
				} else if hi >= 0xdc00 && hi < 0xe000 {
					return "", p.err("unpaired surrogate")
				}
				b.WriteRune(cp)
			default:
				return "", p.err("bad escape")
			}
		default:
			return "", p.err("control character in a string")
		}
	}
}

// ValidUTF8Prefix is how many leading bytes of b are whole UTF-8
// characters, and whether the rest is only an incomplete character (true)
// rather than invalid bytes.
func ValidUTF8Prefix(b []byte) (int, bool) {
	i := 0
	for i < len(b) {
		r, n := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && n <= 1 {
			return i, !utf8.FullRune(b[i:])
		}
		i += n
	}
	return i, false
}

// Lossy is b as text, with each invalid sequence as U+FFFD, as Rust's
// String::from_utf8_lossy makes it.
func Lossy(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var s strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 {
			// Rust replaces a maximal prefix of a valid sequence with
			// one U+FFFD; Go's decoder consumes one byte at a time.
			n = maximalPrefix(b)
			s.WriteRune(utf8.RuneError)
			b = b[n:]
			continue
		}
		s.WriteString(string(b[:n]))
		b = b[n:]
	}
	return s.String()
}

// maximalPrefix is the length of the longest prefix of b that starts a
// valid UTF-8 sequence but does not complete one (at least 1).
func maximalPrefix(b []byte) int {
	c := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		need, lo = 2, 0xa0
	case c >= 0xe1 && c <= 0xec || c == 0xee || c == 0xef:
		need = 2
	case c == 0xed:
		need, hi = 2, 0x9f
	case c == 0xf0:
		need, lo = 3, 0x90
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	case c == 0xf4:
		need, hi = 3, 0x8f
	default:
		return 1
	}
	i := 1
	for ; i <= need && i < len(b); i++ {
		if b[i] < lo || b[i] > hi {
			break
		}
		lo, hi = 0x80, 0xbf
	}
	return i
}
