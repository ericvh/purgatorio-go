// Package libsec has the ciphers and digests Inferno's libsec provides
// that Go's standard library does not, or not with Inferno's semantics:
// MD4, IDEA, and RC4 that can skip and back up. The ssl device (#D) and
// the guest Keyring share them. It also has ChaCha20-Poly1305, which Go
// keeps inside crypto/tls, for pgauth's records, and XChaCha20-Poly1305
// for Lethe. It is portable: the kernel reuses it.
package libsec
