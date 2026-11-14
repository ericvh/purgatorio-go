// Command crypt encrypts standard input to standard output, or with -d
// decrypts it, as the Rust sibling's does. The key is Argon2id of the
// secret at cost -C m,t (default 19456 KiB, 2 passes), and the data is
// sealed with ChaCha20-Poly1305 in 64 KiB chunks, so a damaged, truncated
// or reordered file fails to decrypt (docs/auth.md).
//
//	crypt [-d] [-C m,t] (-k secret | -f secretfile)
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/authproto"
	"github.com/ericvh/purgatorio-go/sdk/pglogin"
)

func encrypt(secret []byte, m, t uint32, in io.Reader, out *bufio.Writer) {
	c, header, err := pglogin.EncryptStream(secret, m, t, rand.Reader)
	if err != nil {
		authproto.Fail(err.Error())
	}
	out.Write(header)
	cur := make([]byte, pglogin.CryptChunk)
	n, _ := io.ReadFull(in, cur)
	for {
		next := make([]byte, pglogin.CryptChunk)
		k := 0
		if n == pglogin.CryptChunk {
			k, _ = io.ReadFull(in, next)
		}
		b, err := c.SealChunk(cur[:n], k == 0)
		if err != nil {
			authproto.Fail(err.Error())
		}
		out.Write(b)
		if k == 0 {
			return
		}
		cur, n = next, k
	}
}

func chunk(in io.Reader) []byte {
	var l [4]byte
	switch k, _ := io.ReadFull(in, l[:]); k {
	case 0:
		return nil
	case 4:
	default:
		authproto.Fail(pglogin.ErrWrongCrypt.Error())
	}
	n := binary.LittleEndian.Uint32(l[:])
	if n > pglogin.CryptChunk+16 {
		authproto.Fail(pglogin.ErrWrongCrypt.Error())
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(in, ct); err != nil {
		authproto.Fail(pglogin.ErrWrongCrypt.Error())
	}
	return ct
}

func decrypt(secret []byte, in io.Reader, out *bufio.Writer) {
	header := make([]byte, pglogin.CryptHeader)
	if _, err := io.ReadFull(in, header); err != nil {
		authproto.Fail(pglogin.ErrNotCrypt.Error())
	}
	c, err := pglogin.DecryptStream(secret, header)
	if err != nil {
		authproto.Fail(err.Error())
	}
	cur := chunk(in)
	if cur == nil {
		authproto.Fail(pglogin.ErrWrongCrypt.Error())
	}
	for {
		next := chunk(in)
		p, err := c.OpenChunk(cur, next == nil)
		if err != nil {
			out.Flush()
			authproto.Fail(err.Error())
		}
		out.Write(p)
		if next == nil {
			return
		}
		cur = next
	}
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("crypt [-d] [-C m,t] (-k secret | -f secretfile)")
	decrypting := false
	var secret []byte
	m, t := uint32(pglogin.DefaultM), uint32(pglogin.DefaultT)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			decrypting = true
		case 'k':
			secret = []byte(a.EArg())
		case 'f':
			p := a.EArg()
			b, err := os.ReadFile(p)
			if err != nil {
				authproto.Fail(authproto.FileErr(p, err))
			}
			secret = []byte(strings.TrimRight(string(b), "\n"))
		case 'C':
			var err error
			if m, t, err = authproto.Cost(a.EArg()); err != nil {
				authproto.Fail(err.Error())
			}
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 || secret == nil {
		a.Usage()
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	in := bufio.NewReader(os.Stdin)
	if decrypting {
		decrypt(secret, in, out)
	} else {
		encrypt(secret, m, t, in, out)
	}
}
