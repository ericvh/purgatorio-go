package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/core"
)

// TestModulePolicy signs modules in the system with auth/sign, with a
// hybrid and an Ed25519 key, and runs them under auth/modpolicy
// (docs/signing.md): require takes the hybrid signature and refuses an
// unsigned module and, unless told classical, the Ed25519 one; warn runs
// an unsigned module after a warning; /prog/<pid>/module shows the
// policy and the signer; a prog under require cannot lower its policy or
// spawn an unsigned module.
func TestModulePolicy(t *testing.T) {
	kd := filepath.Join(root, "modpg")
	for _, d := range []string{"keys", "bin"} {
		if err := os.MkdirAll(filepath.Join(kd, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(kd) })
	script := `k=/modpg
auth/createsignerkey -f $k/dev dev > $k/keys/modules
auth/createsignerkey -p classical -f $k/old old >> $k/keys/modules
cp /bin/echo.wasm /bin/test/pgauth.wasm $k/bin
auth/sign -k $k/dev $k/bin/echo.wasm
auth/sign -k $k/dev $k/bin/pgauth.wasm
cp /bin/echo.wasm $k/old.wasm
auth/sign -k $k/old $k/old.wasm
cp /bin/echo.wasm $k/plain.wasm
bind -b $k/keys /lib/keys
auth/modpolicy require $k/bin/echo.wasm signed
auth/modpolicy require $k/plain.wasm unsigned
echo status $status
auth/modpolicy warn $k/plain.wasm warned
auth/modpolicy require $k/old.wasm classical
echo status $status
auth/modpolicy require,classical $k/old.wasm classical taken
auth/modpolicy require $k/bin/pgauth.wasm module
`
	out, _ := emu(t, "", "sh", "-c", "{"+script+"} >[2=1] | sed '"+maskIDs+"'")
	want := `^signed
auth/modpolicy: /modpg/plain.wasm: module not signed
status /modpg/plain.wasm: module not signed
/modpg/plain.wasm: warning: module not signed
warned
auth/modpolicy: /modpg/old.wasm: hybrid signature required
status /modpg/old.wasm: hybrid signature required
classical taken
policy require
module HEX PQ /modpg/bin/pgauth.wasm
lower: permission denied
spawn: /bin/ls.wasm: module not signed
$`
	if !regexp.MustCompile(want).MatchString(out) {
		t.Errorf("got:\n%s", out)
	}
}

// TestModulesFlag is emu --modules: the first prog is checked under the
// policy, pinned to the boot namespace, which trusts no module signer.
func TestModulesFlag(t *testing.T) {
	for _, c := range []struct {
		level core.ModLevel
		out   string
		err   string
	}{
		{core.ModWarn, "/bin/emuinit.wasm: warning: module not signed\n", ""},
		{core.ModRequire, "", "/bin/emuinit.wasm: module not signed"},
	} {
		for _, m := range testModes() {
			var out lockedBuffer
			h := &host.Host{In: strings.NewReader(""), Out: &out}
			sys, err := host.Boot(h, root, getEngines(t)[m])
			if err != nil {
				t.Fatal(err)
			}
			if err := sys.Init.SetModulePolicy(c.level, false); err != nil {
				t.Fatal(err)
			}
			st, err := runWatched(t, sys, m, &out, []string{"/bin/test/pgauth"})
			sys.Init.Exit(st)
			got := ""
			if err != nil {
				got = core.Errstr(err)
			}
			if got != c.err || !strings.HasPrefix(out.String(), c.out) {
				t.Errorf("%s %v: got %q %q, want %q %q", m, c.level, out.String(), got, c.out, c.err)
			}
		}
	}
}

// TestCertChain makes a certificate chain in the system: a signer
// certifies an intermediate key, which certifies the key that signs a
// module (auth/mkauthinfo -f with an authinfo, auth/getpk -c for its
// certificate). The module verifies as trusted through the chain; without
// the intermediate's certificate its signer is untrusted.
func TestCertChain(t *testing.T) {
	kd := filepath.Join(root, "certchain")
	if err := os.MkdirAll(kd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(kd) })
	script := `d=/certchain
auth/createsignerkey -f $d/sk rootsigner > $d/modules
auth/mkauthinfo -f $d/sk mid $d/mid.info
auth/mkauthinfo -f $d/mid.info leaf $d/leaf.info
auth/getpk -c $d/mid.info > $d/mid.cert
auth/getpk -c $d/sk
echo status $status
cp /bin/echo.wasm $d/e.wasm
cp /bin/echo.wasm $d/g.wasm
auth/sign -k $d/leaf.info -c $d/mid.cert $d/e.wasm
auth/sign -k $d/leaf.info $d/g.wasm
auth/verify -m -s $d/modules $d/e.wasm
echo status $status
auth/verify -m -s $d/modules $d/g.wasm
echo status $status
`
	out, _ := emu(t, "", "sh", "-c", "{"+script+"} >[2=1] | sed '"+maskIDs+"'")
	want := `^auth/getpk: /certchain/sk: no certificate
status errors
digest HEX
abi [0-9.]+
signer PQ trusted leaf mid
status 
digest HEX
abi [0-9.]+
signer PQ untrusted leaf
auth/verify: untrusted signer
status untrusted signer
$`
	if !regexp.MustCompile(want).MatchString(out) {
		t.Errorf("got:\n%s", out)
	}
}
