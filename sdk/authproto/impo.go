package authproto

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/ssl"
)

// Encryption after p9any for exportfs's import and 9export (-e), as
// Plan 9's import and exportfs set it up: each side sends 4 random
// bytes, both hash the client's 4 bytes, the first 8 bytes of the shared
// secret and the server's 4 bytes with SHA-1, and each direction's ssl
// secret is half the digest, written as hex and read back as base64
// (Inferno's import does the same).

// ImpoKey exchanges the random parts of the key over conn and returns
// the ssl secrets, in and out, for the client (import) or the server
// (9export).
func ImpoKey(conn io.ReadWriter, secret []byte, client bool) (in, out []byte, err error) {
	var key [16]byte
	mine, theirs := key[0:4], key[12:16]
	if !client {
		mine, theirs = theirs, mine
	}
	if _, err := rand.Read(mine); err != nil {
		return nil, nil, err
	}
	copy(key[4:12], secret)
	if _, err := conn.Write(mine); err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(conn, theirs); err != nil {
		return nil, nil, err
	}
	d := sha1.Sum(key[:])
	a, b := hexb64(d[0:10]), hexb64(d[10:20])
	if client {
		return a, b, nil
	}
	return b, a, nil
}

func hexb64(b []byte) []byte {
	s, _ := base64.StdEncoding.DecodeString(hex.EncodeToString(b))
	return s
}

// pushed keeps the ssl conversations' ctl files open.
var pushed []*dial.Connection

// PushSSL pushes ssl onto fd with the secrets and the algorithms alg
// ("rc4_256 sha1") and returns the file to use from then on.
func PushSSL(fd *os.File, in, out []byte, alg string) (*os.File, error) {
	c, err := ssl.Connect(fd)
	if err != nil {
		return nil, errors.New("can't connect ssl: " + err.Error())
	}
	if err := ssl.Secret(c, in, out); err != nil {
		return nil, errors.New("can't write secret: " + err.Error())
	}
	if err := ssl.Ctl(c, "alg "+alg); err != nil {
		return nil, errors.New("can't push algorithm " + alg + ": " + err.Error())
	}
	pushed = append(pushed, c)
	return c.Dfd, nil
}
