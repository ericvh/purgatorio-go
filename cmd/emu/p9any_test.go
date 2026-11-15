package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestP9any has 9export and import authenticate each other with p9any
// and p9sk1 through two factotums, each holding its user's key, with
// tickets from auth/authsrv under listen in the emu, whose key file has
// both users' keys. A server with the wrong key fails with Plan 9's
// error, a client with the wrong key with Inferno's.
func TestP9any(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(filepath.Join(root, "mnt", "factotum")) })

	// Each server runs in its own name space with its own factotum.
	server := func(pw, flags, log string) string {
		return `sh -c {load std; pctl forkns; auth/factotum -a tcp!$A
echo 'key proto=p9sk1 dom=pg user=glenda ''!password=` + pw + `''' > /mnt/factotum/ctl
listen -A 'tcp!127.0.0.1!0' 9export -a ` + flags + ` >[2] ` + log + `}
`
	}
	script := `load std
d=/tmp/p9xtest
rm -rf $d
mkdir -p $d/tree/sub $d/n $d/m
echo hello > $d/tree/sub/f
echo 'key proto=p9sk1 dom=pg user=glenda ''!password=server pw''' > $d/keys
echo 'key proto=p9sk1 dom=pg user=bootes ''!password=client pw''' >> $d/keys
listen -A 'tcp!127.0.0.1!0' auth/authsrv -k $d/keys
A=` + "`" + `{cat /net/tcp/0/local}
` + server("server pw", "", "/dev/null") +
		server("wrong", "", "/tmp/p9xtest/badlog") +
		server("server pw", "-e 'rc4_256 sha1'", "/dev/null") + `P=tcp!` + "`" + `{cat /net/tcp/1/local}
B=tcp!` + "`" + `{cat /net/tcp/2/local}
E=tcp!` + "`" + `{cat /net/tcp/3/local}
auth/factotum -a tcp!$A
echo 'key proto=p9sk1 dom=pg user=bootes ''!password=client pw''' > /mnt/factotum/ctl
cat /mnt/factotum/ctl
import -9 $d/tree $P $d/m
cat $d/m/sub/f
echo new > $d/m/sub/f
cat $d/tree/sub/f
unmount $d/m
import -c -p keyring $P $d/tree $d/n
cat $d/n/sub/f
echo made > $d/n/g
cat $d/tree/g
unmount $d/n
import -p keyring -e 'rc4_256 sha1' $E $d/tree $d/n
cat $d/n/g
echo crypt > $d/n/g
cat $d/tree/g
unmount $d/n
echo wrong server key
import -9 $d/tree $B $d/m
echo $status
cat $d/badlog
echo wrong client key
echo 'delkey proto=p9sk1' > /mnt/factotum/ctl
echo 'key proto=p9sk1 dom=pg user=bootes !password=wrong' > /mnt/factotum/ctl
import -9 $d/tree $P $d/m
import -p keyring $P $d/tree $d/n
echo $status
rm -rf $d
`
	dmRun(t, script, `key proto=p9sk1 dom=pg user=bootes !password?
hello
new
new
made
made
crypt
wrong server key
import: can't authenticate import: proxy short read
auth
9export: can't authenticate 9export: proxy rpc write: error p9sk1: key mismatch with auth server
wrong client key
import: can't authenticate import: proxy rpc write: error p9sk1: getasticket: failed: wrong key?
import: can't authenticate import: proxy rpc write: error p9sk1: getasticket: failed: wrong key?
auth
`)
}
