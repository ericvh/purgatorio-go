package authproto

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// What the key tools share (appl/cmd/auth).

// The default files of the tools, as in the Rust sibling.
const (
	SignerKey = "/keydb/signerkey"
	Signers   = "/lib/keys/signers"
	Revoked   = "/lib/keys/revoked"
	Modules   = "/lib/keys/modules"
)

// Fail prints "argv0: msg" on standard error and exits with fail:msg.
func Fail(msg string) {
	os.Stderr.WriteString(os.Args[0] + ": " + msg + "\n")
	sys.Exits("fail:" + msg)
}

// FileErr is "path: err" with the Inferno error string when there is one.
func FileErr(path string, err error) string {
	if e := sys.Errstr(); e != "" {
		return path + ": " + e
	}
	return path + ": " + err.Error()
}

// Now is the time in seconds since 1970.
func Now() uint64 { return uint64(time.Now().Unix()) }

// Expiry parses an expiry: seconds since 1970, or never (0).
func Expiry(s string) (uint64, bool) {
	if s == "never" {
		return 0, true
	}
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// ShowExpiry is an expiry as the tools print it.
func ShowExpiry(e uint64) string {
	if e == 0 {
		return "never"
	}
	return strconv.FormatUint(e, 10)
}

// MkdirsFor makes the directories above path.
func MkdirsFor(path string) {
	i := strings.LastIndexByte(path, '/')
	if i <= 0 {
		return
	}
	dir := ""
	for _, part := range strings.Split(path[:i], "/") {
		if part == "" {
			continue
		}
		dir += "/" + part
		if _, err := sys.Stat(dir); err != nil {
			if fd, err := sys.Create(dir, sys.OREAD, sys.DMDIR|0o775); err == nil {
				os.NewFile(uintptr(fd), dir).Close()
			}
		}
	}
}

// WriteSecret writes data to a file only its owner can read.
func WriteSecret(path string, data []byte) error {
	fd, err := sys.Create(path, sys.OWRITE|sys.OTRUNC, 0o600)
	if err != nil {
		return sys.Error(path + ": " + sys.Errstr())
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return sys.Error(FileErr(path, err))
	}
	return nil
}

// KeyfilePath is a key file's path: name itself when it has a slash,
// else under /usr/<user>/keyring.
func KeyfilePath(name, user string) string {
	if strings.Contains(name, "/") || strings.HasPrefix(name, "#") {
		return name
	}
	return "/usr/" + user + "/keyring/" + name
}

// Hybrid reports whether a key tool told proto (a -p value, or empty for
// the default resolution) makes hybrid keys. keyring and none are not
// key families.
func HybridFor(proto string) (bool, error) {
	ps, err := Resolve(proto)
	if err != nil {
		return false, err
	}
	switch ps[0] {
	case Hybrid:
		return true, nil
	case Classical:
		return false, nil
	}
	return false, EBadProto
}
