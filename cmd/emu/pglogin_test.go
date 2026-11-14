package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestPasswordTools runs the password tools over the loopback
// (docs/auth.md, "Password scheme"): auth/keyfs and auth/changelogin, the
// signer under listen with auth/getauthinfo in both login versions (a
// wrong password and a refused version included), auth/passwd through
// auth/keysrv, auth/secstore, crypt and netkey.
func TestPasswordTools(t *testing.T) {
	kd := filepath.Join(root, "keydbpl")
	os.MkdirAll(kd, 0o755)
	t.Cleanup(func() { os.RemoveAll(kd); os.RemoveAll(filepath.Join(root, "mnt", "pgkeys")) })
	// Both wazero modes run in the same directory: start each afresh.
	script := (`k=/keydbpl
rm -rf $k/keys $k/alice $k/calice $k/alice2 $k/x $k/ss $k/c $k/back
echo master | auth/keyfs -C 64,1 -m /mnt/pgkeys $k/keys
auth/createsignerkey -f $k/signer s > $k/signers
auth/createsignerkey -p classical -f $k/csigner c > $k/csigners
echo secret1 | auth/changelogin -m /mnt/pgkeys -C 64,1 alice
cat /mnt/pgkeys/alice/status
listen 'tcp!127.0.0.1!0' auth/signer -m /mnt/pgkeys -f $k/signer > /dev/null >[2] /dev/null </dev/null
P1=tcp!`+"`"+`{cat /net/tcp/0/local}
listen 'tcp!127.0.0.1!0' auth/keysrv -m /mnt/pgkeys -f $k/signer > /dev/null >[2] /dev/null </dev/null
P2=tcp!`+"`"+`{cat /net/tcp/1/local}
listen -p classical 'tcp!127.0.0.1!0' auth/signer -m /mnt/pgkeys -f $k/csigner > /dev/null >[2] /dev/null </dev/null
P3=tcp!`+"`"+`{cat /net/tcp/2/local}
echo secret1 | auth/getauthinfo -s $P1 -u alice $k/alice
auth/verify -s $k/signers $k/alice
echo wrong | auth/getauthinfo -s $P1 -u alice $k/x
echo secret1 | auth/getauthinfo -p classical -s $P1 -u alice $k/x
echo secret1 | auth/getauthinfo -s $P3 -u alice $k/x
echo secret1 | auth/getauthinfo -p classical -s $P3 -u alice $k/calice
auth/verify -s $k/csigners $k/calice
{echo secret1; echo secret2; echo secret2} | auth/passwd -s $P2 -u alice
echo secret1 | auth/getauthinfo -s $P1 -u alice $k/x
echo secret2 | auth/getauthinfo -s $P1 -u alice $k/alice2
echo new password $status
echo pw | auth/secstore -C 64,1 -s $k/ss -p $k/signers
auth/secstore -s $k/ss
echo pw | auth/secstore -s $k/ss -G signers > $k/back
cmp $k/back $k/signers
echo secstore round trip $status
echo nope | auth/secstore -s $k/ss -G signers
echo hello world | crypt -C 64,1 -k s > $k/c
crypt -d -k s < $k/c
crypt -d -k x < $k/c
{echo password; echo 12345} | netkey
`)
	// Into a file, not a pipe: the servers left running hold their
	// standard error open.
	out, _ := emu(t, "", "sh", "-c", "{"+script+"} > /keydbpl/out >[2=1]; sed '"+maskIDs+"' /keydbpl/out")
	want := `^ok
name alice
key PQ
expires HEX
signer PQ trusted
auth/getauthinfo: authentication failed
auth/getauthinfo: hybrid authentication required
auth/getauthinfo: classical authentication required
name alice
key HEX
expires HEX
signer HEX trusted
auth/getauthinfo: authentication failed
new password 
signers
secstore round trip 
auth/secstore: signers: wrong password or damaged file
hello world
crypt: wrong secret or damaged file
HEX
$`
	if !regexp.MustCompile(want).MatchString(out) {
		t.Errorf("got:\n%s", out)
	}
}
