package keyring

import (
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Message sizes, as keyring.c's Maxmsg and the error limit of getbuf.
const (
	Maxmsg = 4096
	maxerr = 64
)

// MSG is the error for a malformed or short message.
const MSG = "input or format error"

// Error is an error string from the protocol or the peer.
type Error string

func (e Error) Error() string { return string(e) }

// writeall is kwrite: one write of b.
func writeall(w io.Writer, b []byte) (int, error) { return w.Write(b) }

// nreadn reads n bytes unless the reader ends or fails first.
func nreadn(r io.Reader, b []byte) (int, error) {
	t := 0
	for t < len(b) {
		m, err := r.Read(b[t:])
		if m <= 0 {
			if t == 0 {
				if err == nil {
					err = io.EOF
				}
				return m, err
			}
			break
		}
		t += m
	}
	return t, nil
}

// SendMsg is Keyring's sendmsg: a header of four decimal digits and a
// newline, then the data, in two writes.
func SendMsg(w io.Writer, buf []byte) error {
	if _, err := writeall(w, fmt.Appendf(nil, "%4.4d\n", len(buf))); err != nil {
		return err
	}
	n, err := writeall(w, buf)
	if err != nil {
		return err
	}
	if n <= 0 && len(buf) > 0 {
		return Error(MSG)
	}
	return nil
}

// SendErrMsg is Keyring's senderrmsg: '!' and three digits, then the
// error text.
func SendErrMsg(w io.Writer, s string) error { return senderr(w, s, false) }

func senderr(w io.Writer, s string, remote bool) error {
	m := 0
	if remote {
		m = len("remote: ")
	}
	if _, err := writeall(w, fmt.Appendf(nil, "!%3.3d\n", len(s)+m)); err != nil {
		return err
	}
	if remote {
		writeall(w, []byte("remote: "))
	}
	_, err := writeall(w, []byte(s))
	return err
}

// errRemote marks an error message sent by the peer.
type errRemote struct{ s string }

func (e *errRemote) Error() string { return e.s }

// getmsg reads one message of fewer than max bytes. An error message from
// the peer comes back as *errRemote.
func getmsg(r io.Reader, max int) ([]byte, error) {
	var num [5]byte
	if n, _ := nreadn(r, num[:]); n != 5 {
		return nil, getmsgerr(n)
	}
	digits := num[:4]
	isErr := num[0] == '!'
	if isErr {
		digits = num[1:4]
	}
	l := strtoul(string(digits))
	if l < 0 || l >= max {
		return nil, Error(MSG)
	}
	buf := make([]byte, l)
	if n, _ := nreadn(r, buf); n != l {
		return nil, getmsgerr(n)
	}
	if isErr {
		return nil, &errRemote{string(buf)}
	}
	return buf, nil
}

func getmsgerr(n int) error {
	if n > 0 {
		return Error(MSG)
	}
	return Error("hungup")
}

// strtoul is the C function on a short field: leading blanks, then digits.
func strtoul(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	n, _ := strconv.Atoi(s[i:j])
	return n
}

// GetMsg is Keyring's getmsg: one message, or the peer's error message
// as the error.
func GetMsg(r io.Reader) ([]byte, error) {
	b, err := getmsg(r, Maxmsg)
	var re *errRemote
	if errors.As(err, &re) {
		return nil, Error(re.s)
	}
	return b, err
}

// getbuf reads one delimited message: a 0 byte then data, or 0xff then
// an error string.
func getbuf(r io.Reader) ([]byte, error) {
	buf := make([]byte, Maxmsg)
	n, err := r.Read(buf)
	if n <= 0 {
		_ = err
		return nil, Error("hungup")
	}
	buf = buf[:n]
	switch buf[0] {
	case 0:
		return buf[1:], nil
	case 0xff:
	default:
		// A wrong password garbles the messages devssl decrypts.
		return nil, Error("failure")
	}
	e := buf[1:]
	if len(e) < 1 {
		return nil, Error("unknown")
	}
	if len(e) >= maxerr {
		e = e[:maxerr-1]
	}
	return nil, Error(e)
}

// GetString reads a message from a delimited connection (ssl).
func GetString(r io.Reader) (string, error) {
	b, err := getbuf(r)
	return string(b), err
}

// GetByteArray reads a message from a delimited connection.
func GetByteArray(r io.Reader) ([]byte, error) { return getbuf(r) }

func putbuf(w io.Writer, kind byte, b []byte) error {
	if len(b) >= Maxmsg {
		b = b[:Maxmsg-1]
	}
	_, err := writeall(w, append([]byte{kind}, b...))
	return err
}

// PutString writes a message on a delimited connection.
func PutString(w io.Writer, s string) error { return putbuf(w, 0, []byte(s)) }

// PutByteArray writes a message on a delimited connection.
func PutByteArray(w io.Writer, a []byte) error { return putbuf(w, 0, a) }

// PutError writes an error message on a delimited connection.
func PutError(w io.Writer, s string) error { return putbuf(w, 0xff, []byte(s)) }
