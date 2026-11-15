// Command auth/aescbc encrypts (-e) or decrypts (-d) standard input to
// standard output under a password, in the file format of Plan 9's and
// Inferno's aescbc: a header line, a 16-byte IV, AES-CBC of 16 random
// bytes and the file, and an HMAC-SHA1 of that ciphertext. The key comes
// from -k, the file named by -f, or is read from /dev/cons. Decryption
// also takes the older secstore files with no header.
package main

import (
	"bufio"
	"bytes"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	checkpat  = "XXXXXXXXXXXXXXXX"
	checklen  = len(checkpat)
	bufsize   = 4096
	aesmaxkey = 32
	sha1dlen  = 20
	md5dlen   = 16
	aesbsize  = keyring.AESbsize

	v2hdr = "AES CBC SHA1  2\n"
)

var (
	bin  = bufio.NewReader(os.Stdin)
	bout = bufio.NewWriter(os.Stdout)
)

func main() {
	sys.Pctl(sys.FORKFD, nil)
	a := arg.New(os.Args)
	a.SetUsage("auth/aescbc -d [-k key] [-f keyfile] <file.aes >clear.txt\n  or: auth/aescbc -e [-k key] [-f keyfile] <clear.txt >file.aes")
	encrypt := -1
	keyfile, pass := "", ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd', 'e':
			if encrypt >= 0 {
				a.Usage()
			}
			encrypt = 0
			if o == 'e' {
				encrypt = 1
			}
		case 'f':
			keyfile = a.EArg()
		case 'k':
			pass = a.EArg()
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 || encrypt < 0 {
		a.Usage()
	}

	buf := make([]byte, bufsize+sha1dlen) // checklen <= sha1dlen
	var pwd []byte
	if keyfile != "" {
		fd, err := sys.Open(keyfile, sys.OREAD)
		if err != nil {
			fail("can't open "+quote.Quote(keyfile)+": "+sys.Errstr(), "keyfile")
		}
		f := os.NewFile(uintptr(fd), keyfile)
		n, _ := io.ReadFull(f, buf)
		f.Close()
		for n > 0 && buf[n-1] == '\n' {
			n--
		}
		if n <= 0 {
			fail("no key", "no key")
		}
		pwd = append([]byte(nil), buf[:n]...)
	} else {
		if pass == "" {
			pass = readpassword("password")
		}
		if pass == "" {
			fail("no key", "no key")
		}
		pwd = []byte(pass)
	}
	key := make([]byte, aesmaxkey)
	key2 := make([]byte, sha1dlen)
	state := keyring.SHA1([]byte("aescbc file"), nil, nil)
	keyring.SHA1(pwd, key2, state)
	clear(pwd)
	copy(key, key2[:md5dlen])
	nkey := md5dlen
	keyring.MD5(key[:nkey], key2, nil) // protect the key even if HMAC-SHA1 is broken
	key2 = key2[:md5dlen]

	if encrypt == 1 {
		write([]byte(v2hdr))
		genrandom(buf[:2*aesbsize]) // CBC is semantically secure if the IV is unpredictable
		aes := keyring.AESsetup(key[:nkey], buf[:aesbsize])
		keyring.AEScbc(aes, buf[aesbsize:2*aesbsize], keyring.Encrypt)
		write(buf[:2*aesbsize])
		state = keyring.HMAC_SHA1(buf[aesbsize:2*aesbsize], key2, nil, nil)
		for {
			n, err := io.ReadFull(bin, buf[:bufsize])
			if n > 0 {
				keyring.AEScbc(aes, buf[:n], keyring.Encrypt)
				write(buf[:n])
				state = keyring.HMAC_SHA1(buf[:n], key2, nil, state)
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if err != nil {
				fail("read error: "+sys.Errstr(), "read error")
			}
		}
		keyring.HMAC_SHA1(nil, key2, buf[:sha1dlen], state)
		write(buf[:sha1dlen])
	} else {
		read(buf[:aesbsize])
		if string(buf[:aesbsize]) == v2hdr {
			read(buf[:2*aesbsize]) // the IV and the random initial plaintext
			aes := keyring.AESsetup(key[:nkey], buf[:aesbsize])
			state = keyring.HMAC_SHA1(buf[aesbsize:2*aesbsize], key2, nil, nil)
			keyring.AEScbc(aes, buf[aesbsize:2*aesbsize], keyring.Decrypt)
			read(buf[:sha1dlen])
			for {
				n, _ := io.ReadFull(bin, buf[sha1dlen:sha1dlen+bufsize])
				if n <= 0 {
					break
				}
				state = keyring.HMAC_SHA1(buf[:n], key2, nil, state)
				keyring.AEScbc(aes, buf[:n], keyring.Decrypt)
				write(buf[:n])
				copy(buf, buf[n:n+sha1dlen]) // not yet decrypted
			}
			keyring.HMAC_SHA1(nil, key2, buf[sha1dlen:2*sha1dlen], state)
			if !bytes.Equal(buf[:sha1dlen], buf[sha1dlen:2*sha1dlen]) {
				fail("decrypted file failed to authenticate", "failed to authenticate")
			}
		} else { // compatibility with a past mistake: secstore files
			aes := keyring.AESsetup(key[:aesbsize], buf[:aesbsize])
			read(buf[:checklen])
			keyring.AEScbc(aes, buf[:checklen], keyring.Decrypt)
			for {
				n, _ := io.ReadFull(bin, buf[checklen:checklen+bufsize])
				if n <= 0 {
					break
				}
				keyring.AEScbc(aes, buf[checklen:checklen+n], keyring.Decrypt)
				write(buf[:n])
				copy(buf, buf[n:n+checklen])
			}
			if string(buf[:checklen]) != checkpat {
				fail("decrypted file failed to authenticate", "failed to authenticate")
			}
		}
	}
	bout.Flush()
}

func fail(s, why string) {
	bout.Flush()
	os.Stderr.WriteString("aescbc: " + s + "\n")
	sys.Exits("fail:" + why)
}

func read(buf []byte) {
	if n, _ := io.ReadFull(bin, buf); n != len(buf) {
		os.Stderr.WriteString("aescbc: unexpectedly short read\n")
		sys.Exits("fail:read error")
	}
}

func write(buf []byte) {
	if n, _ := bout.Write(buf); n != len(buf) {
		os.Stderr.WriteString("aescbc: write error: " + sys.Errstr() + "\n")
		sys.Exits("fail:write error")
	}
}

// readpassword reads a line from /dev/cons with echo off.
func readpassword(prompt string) string {
	cfd, err := sys.Open("/dev/cons", sys.ORDWR)
	if err != nil {
		return ""
	}
	cons := os.NewFile(uintptr(cfd), "/dev/cons")
	defer cons.Close()
	in := bufio.NewReader(cons)
	ctl, err := sys.Open("/dev/consctl", sys.OWRITE)
	if err != nil {
		os.Stderr.WriteString("aescbc: warning: cannot hide typed password\n")
	} else {
		c := os.NewFile(uintptr(ctl), "/dev/consctl")
		if _, err := c.Write([]byte("rawon")); err != nil {
			os.Stderr.WriteString("aescbc: warning: cannot hide typed password\n")
		}
		defer c.Close()
	}
	var s []rune
loop:
	for {
		cons.Write([]byte(prompt + ": "))
		s = s[:0]
		for {
			c, _, err := in.ReadRune()
			if err != nil {
				break loop
			}
			switch c {
			case '\n':
				break loop
			case '\b', 0o177:
				if len(s) > 0 {
					s = s[:len(s)-1]
				}
			case 'u' & 0o37:
				cons.Write([]byte("\n"))
				continue loop
			default:
				s = append(s, c)
			}
		}
	}
	cons.Write([]byte("\n"))
	return string(s)
}

func genrandom(b []byte) {
	fd, err := sys.Open("/dev/notquiterandom", sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("aescbc: can't open /dev/notquiterandom: " + sys.Errstr() + "\n")
		sys.Exits("fail:random")
	}
	f := os.NewFile(uintptr(fd), "/dev/notquiterandom")
	defer f.Close()
	if n, _ := io.ReadFull(f, b); n != len(b) {
		os.Stderr.WriteString("aescbc: can't read random numbers: " + sys.Errstr() + "\n")
		sys.Exits("fail:read random")
	}
}
