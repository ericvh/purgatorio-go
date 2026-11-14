package mcp

import (
	"encoding/base64"
	"errors"
)

// Base64 is standard base64 with padding (RFC 4648).
func Base64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// Unbase64 decodes standard base64. White space is skipped and padding
// may be left off.
func Unbase64(text string) ([]byte, error) {
	out := make([]byte, 0, len(text)/4*3)
	var acc, bits uint32
	pad := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		var v byte
		switch {
		case c >= 'A' && c <= 'Z':
			v = c - 'A'
		case c >= 'a' && c <= 'z':
			v = c - 'a' + 26
		case c >= '0' && c <= '9':
			v = c - '0' + 52
		case c == '+':
			v = 62
		case c == '/':
			v = 63
		case c == '=':
			pad++
			continue
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		default:
			return nil, errors.New("bad base64 character")
		}
		if pad > 0 {
			return nil, errors.New("base64 data after padding")
		}
		acc = acc<<6 | uint32(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
			acc &= 1<<bits - 1
		}
	}
	if bits >= 6 || acc != 0 || pad > 2 {
		return nil, errors.New("truncated base64")
	}
	return out, nil
}
