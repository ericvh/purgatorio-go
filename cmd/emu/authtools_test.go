package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestAuthTools runs the key tools under /bin/auth (docs/auth.md): a
// hybrid and a classical signer, authinfo files they certify, getpk,
// verify with its errors, countersigner, and module signing, including
// files written by the Rust sibling's purg-auth (pgauth/testdata/rust):
// Go's verify takes its authinfo, and auth/sign with its key makes the
// module it signed, byte for byte.
// maskIDs is a sed script that hides key IDs and digests, which are
// random from run to run.
const maskIDs = "s/pq:[0-9a-f]*/PQ/g; s/[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*/HEX/g"

func TestAuthTools(t *testing.T) {
	kd := filepath.Join(root, "keydbpg")
	if err := os.MkdirAll(kd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(kd) })
	rust := filepath.Join("..", "..", "pgauth", "testdata", "rust")
	for _, f := range []string{"authinfo", "signers", "signer.seed", "module.wasm", "module.key.wasm"} {
		b, err := os.ReadFile(filepath.Join(rust, f))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(kd, "rust."+f), b, 0o644)
	}
	script := `k=/keydbpg
auth/createsignerkey -f $k/signer mysigner > $k/signers
auth/createsignerkey -p classical -f $k/csigner old > $k/csigners
cat $k/signers $k/csigners
auth/mkauthinfo -f $k/signer -e never alice $k/alice
auth/mkauthinfo -f $k/csigner -e 1 bob $k/bob
auth/verify -s $k/signers $k/alice
echo status $status
auth/verify -s $k/signers -u bob $k/alice
echo status $status
auth/verify -s $k/csigners $k/bob
echo status $status
auth/verify -s $k/csigners $k/alice
echo status $status
auth/getpk $k/alice $k/signer
auth/createsignerkey -f $k/other other > $k/others
auth/countersigner -f $k/other $k/alice > $k/alice2
auth/verify -s $k/others $k/alice2
echo status $status
auth/countersigner -f $k/csigner $k/alice > /dev/null
echo status $status
echo rust:
auth/verify -s $k/rust.signers -t 1700000000 $k/rust.authinfo
echo status $status
auth/sign -k $k/rust.signer.seed -o $k/signed.wasm $k/rust.module.wasm
auth/verify -m -s $k/rust.signers $k/signed.wasm
echo status $status
auth/verify -m -p hybrid,classical -s $k/rust.signers $k/signed.wasm
echo status $status
auth/sign -k $k/signer -o $k/hsigned.wasm $k/rust.module.wasm
auth/verify -m -s $k/signers $k/hsigned.wasm
echo status $status
auth/verify -m -s $k/csigners $k/hsigned.wasm
echo status $status
`
	// Keys are random, so IDs are masked: both wazero modes must print
	// the same.
	out, st := emu(t, "", "sh", "-c", "{"+script+"} >[2=1] | sed '"+maskIDs+"'")
	if st != "" {
		t.Errorf("status %q", st)
	}
	id, cid, digest := "PQ", "HEX", "HEX"
	want := "^" + id + " mysigner\n" + cid + " old\n" +
		"name alice\nkey " + id + "\nexpires never\nsigner " + id + " trusted\nstatus \n" +
		"name alice\nkey " + id + "\nexpires never\nsigner " + id + " trusted\n" +
		"auth/verify: certificate name mismatch\nstatus certificate name mismatch\n" +
		"name bob\nkey " + cid + "\nexpires 1\nsigner " + cid + " trusted\n" +
		"auth/verify: certificate expired\nstatus certificate expired\n" +
		"name alice\nkey " + id + "\nexpires never\nsigner " + id + " untrusted\n" +
		"auth/verify: untrusted signer\nstatus untrusted signer\n" +
		id + " /keydbpg/alice\n" + id + " /keydbpg/signer\n" +
		"name alice\nkey " + id + "\nexpires never\nsigner " + id + " untrusted\nsigner " + id + " trusted\nstatus \n" +
		"auth/countersigner: key of the wrong algorithm\nstatus key of the wrong algorithm\n" +
		"rust:\n" +
		"name alice\nkey HEX\nexpires HEX\n" +
		"signer HEX trusted\nstatus \n" +
		"digest " + digest + "\nabi 0.9\nsigner HEX classical\n" +
		"auth/verify: hybrid signature required\nstatus hybrid signature required\n" +
		"digest " + digest + "\nabi 0.9\nsigner HEX trusted\nstatus \n" +
		"digest " + digest + "\nabi 0.9\nsigner " + id + " trusted\nstatus \n" +
		"digest " + digest + "\nabi 0.9\nsigner " + id + " untrusted\n" +
		"auth/verify: untrusted signer\nstatus untrusted signer\n$"
	if !regexp.MustCompile(want).MatchString(out) {
		t.Errorf("got:\n%s", out)
	}
	signed, _ := os.ReadFile(filepath.Join(kd, "signed.wasm"))
	rsigned, _ := os.ReadFile(filepath.Join(kd, "rust.module.key.wasm"))
	if !bytes.Equal(signed, rsigned) {
		t.Error("auth/sign's module differs from the one the Rust sibling signed")
	}
}
