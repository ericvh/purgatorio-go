package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestFactotum loads an authinfo into auth/factotum and uses it without
// any prog reading the key again (docs/auth.md, "Factotum"): mount makes
// its proof through it, the export behind styxlisten makes its server
// proof through it (-S), and auth/sign signs a module through it.
func TestFactotum(t *testing.T) {
	kd := filepath.Join(root, "factpg")
	os.MkdirAll(filepath.Join(kd, "keys"), 0o755)
	t.Cleanup(func() { os.RemoveAll(kd); os.RemoveAll(filepath.Join(root, "mnt", "factotum")) })
	script := (`k=/factpg
auth/createsignerkey -f $k/signer s > $k/keys/signers
auth/mkauthinfo -f $k/signer -e never ` + "`" + `{cat /dev/user} $k/alice
echo '*' ` + "`" + `{auth/getpk $k/alice} > $k/keys/servers
cp $k/keys/signers $k/keys/modules
auth/factotum $k/alice
rm $k/alice
sed 's/user=.* cert=.* !seed/user=U cert=CERT !seed/' /mnt/factotum/ctl
bind -b $k/keys /lib/keys
styxlisten 'tcp!127.0.0.1!0' export /lib/test/keys > /dev/null >[2] /dev/null < /dev/null
mount -S tcp!` + "`" + `{cat /net/tcp/0/local} /n/remote
ls /n/remote
auth/sign -o $k/echo.wasm /bin/echo.wasm
auth/verify -m -s $k/keys/modules $k/echo.wasm
`)
	out, _ := emu(t, "", "sh", "-c", "{"+script+"} > /factpg/out >[2=1]; sed '"+maskIDs+"' /factpg/out")
	// ed25519 is masked as HEX too.
	want := `^key proto=HEX alg=hybrid user=U cert=CERT !seed\?
/n/remote/servers
/n/remote/users
digest HEX
abi 0.2
signer PQ trusted [^ ]+
$`
	if !regexp.MustCompile(want).MatchString(out) {
		t.Errorf("got:\n%s", out)
	}
}
