package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/keyring"
)

// authKeys writes a signer key and keys it has certified to root/keydb,
// made natively with small parameters so that the guests do no slow key
// generation, and removes them when the test ends.
func authKeys(t *testing.T) {
	t.Helper()
	kd := filepath.Join(root, "keydb")
	mk := filepath.Join(root, "mnt", "keys")
	for _, d := range []string{kd, mk} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(kd); os.Remove(mk) })

	alpha, p := keyring.DHparams(256)
	signer := keyring.GenSK("elgamal", "signer", 256)
	other := keyring.GenSK("elgamal", "other", 256)
	certify := func(by *keyring.SK, owner string) *keyring.Authinfo {
		sk := keyring.GenSKfromPK(keyring.SKtoPK(by), owner)
		if by.Owner == owner {
			sk = by
		}
		pk := keyring.SKtoPK(sk)
		cert := keyring.Sign(by, 0, keyring.SHA1([]byte(keyring.PKtoStr(pk)), nil, nil), "sha1")
		return &keyring.Authinfo{MySK: sk, MyPK: pk, Cert: cert, SPK: keyring.SKtoPK(by), Alpha: alpha, P: p}
	}
	files := map[string][]byte{
		"signerkey": keyring.MarshalAuthinfo(certify(signer, "signer")),
		"alice":     keyring.MarshalAuthinfo(certify(signer, "alice")),
		"bob":       keyring.MarshalAuthinfo(certify(signer, "bob")),
		"mallory":   keyring.MarshalAuthinfo(certify(other, "mallory")),
		"nvram":     []byte("keyfs password"),
		"keys":      nil,
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(kd, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuthMount(t *testing.T) {
	authKeys(t)
	ok := func(alg string) string {
		return "remote username is bob\nread \"hello from the root\\n\" <nil>\n"
	}
	cases := []struct {
		name string
		args []string
		out  string
	}{
		{"none", []string{"none"}, "server: client is alice\n" + ok("none")},
		{"rc4_128 sha1", []string{"rc4_128 sha1"}, "server: client is alice\n" + ok("")},
		{"listed", []string{"md5", "none", "md5"}, "server: client is alice\n" + ok("")},
		{"des", []string{"descbc/md5"}, "server: client is alice\n" + ok("")},
		{"unsupported", []string{"rc4_128 sha1", "sha1"},
			"server: unsupported client algorithm: rc4_128\nremote username is bob\nmount failed\nserver status \"fail:auth\"\n"},
	}
	for _, c := range cases {
		args := append([]string{"/bin/test/auth", "mount", "/keydb/alice", "/keydb/bob"}, c.args...)
		out, st := emu(t, "", args...)
		if out != c.out || st != "" {
			t.Errorf("%s: got %q %q, want %q", c.name, out, st, c.out)
		}
	}

	// A key from another signer is refused at both ends.
	out, _ := emu(t, "", "/bin/test/auth", "mount", "/keydb/mallory", "/keydb/bob", "none")
	want := "server: pk doesn't match certificate\nauthentication failed: pk doesn't match certificate\nserver status \"fail:auth\"\n"
	if out != want {
		t.Errorf("foreign signer: got %q, want %q", out, want)
	}
}

func TestAuthLogin(t *testing.T) {
	authKeys(t)
	out, st := emu(t, "", "/bin/test/auth", "login", "carol", "carolspassword", "wrongpassword")
	want := `keyfs ""
changelogin "new account\nsecret: \nconfirm: \nexpires [DDMMYYYY/permanent, return = DATE]: change written\n" ""
expire "never" <nil>
secret is the password's hash: true
certificate for carol signed by signer expires 0
certificate verifies: true; logind status ""
`
	if !strings.HasPrefix(out, want) || st != "" {
		t.Fatalf("got %q %q\nwant prefix %q", out, st, want)
	}
	if rest := out[len(want):]; rest != "login failed; logind status \"\"\nlogind complained: true\n" {
		t.Errorf("wrong password: %q", rest)
	}

	// The key logind certified authenticates with one the signer made.
	out, _ = emu(t, "", "/bin/test/auth", "mount", "/keydb/carol.login", "/keydb/bob", "rc4_256 sha1")
	if want := "server: client is carol\nremote username is bob\n"; !strings.HasPrefix(out, want) {
		t.Errorf("mount with the login key: %q", out)
	}
}

func TestGetpk(t *testing.T) {
	authKeys(t)
	ai, err := keyring.ReadAuthinfoFrom(mustOpen(t, filepath.Join(root, "keydb", "alice"))), error(nil)
	if ai == nil || err != nil {
		t.Fatal("read alice")
	}
	d := make([]byte, keyring.SHA1dlen)
	keyring.SHA1([]byte(keyring.PKtoStr(ai.MyPK)), d, nil)
	cases := []struct {
		args    []string
		out, st string
	}{
		{[]string{"getpk", "/keydb/alice"}, fmt.Sprintf("%X\n", d), ""},
		{[]string{"getpk", "-a", "-u", "/keydb/alice"}, keyring.PKtoStr(ai.MyPK) + " alice\n", ""},
		{[]string{"getpk", "-s", "-a", "/keydb/alice"}, keyring.PKtoStr(ai.SPK) + "\n", ""},
		{[]string{"getpk", "/keydb/nonexistent"}, "getpk: cannot read /keydb/nonexistent: file does not exist\n", ""},
		{[]string{"getpk", "/keydb/nvram"}, "getpk: cannot read /keydb/nvram: /keydb/nvram: input or format error\n", ""},
		{[]string{"createsignerkey"}, "usage: createsignerkey [-a algorithm] [-f keyfile] [-e ddmmyyyy] [-b size-in-bits] name-of-owner\n", "fail:usage"},
		{[]string{"createsignerkey", "-a", "dsa", "x"}, "createsignerkey: unknown algorithm: dsa\n", "fail:error"},
	}
	for _, c := range cases {
		out, st := emu(t, "", c.args...)
		if out != c.out || st != c.st {
			t.Errorf("%v: got %q %q, want %q %q", c.args, out, st, c.out, c.st)
		}
	}
}

func mustOpen(t *testing.T, name string) *os.File {
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
