# Authentication

Purgatorio-Go has three ways to authenticate a mount or an export:

| choice | what it is | for |
|---|---|---|
| `hybrid` (default) | Ed25519 + ML-DSA-65 keys, `pgcert2` certificates, the version 4 handshake (X25519 + ML-KEM-768), then encrypted records | Go peers |
| `classical` | Ed25519 keys, `pgcert1` certificates, the version 3 handshake (X25519), then encrypted records | Rust peers (byte-compatible with the Rust sibling's `purg-auth`) |
| `keyring` | Inferno's keyring: station-to-station over the raw connection, then `ssl` (`#D`) | Inferno peers |

`hybrid` and `classical` are this system's own protocols, taken from the
Rust sibling (its `docs/auth.md` is the reference for every classical
format) with a post-quantum layer added. `keyring` is Inferno's
`keyring(2)`, `auth(2)` and `ssl(3)` (`sdk/keyring`, `sdk/auth`,
`core/devssl.go`) and is unchanged.

The formats and the handshake are in package `pgauth` (portable: the
kernel uses it) and module signatures in `pgauth/modsig`
(`docs/signing.md`).

## Choosing algorithms

The default is `hybrid`. `classical` and `keyring` are ordinary,
supported choices, not exceptions. A program uses what it is told, in
this order:

1. the `-p proto` flag of `mount`, `import`, `export`, `styxlisten`,
   `listen`, `rcmd` and `cpu`, and of the key and signing tools under
   `/bin/auth`;
2. the environment variable `authproto`;
3. the first line of `/lib/keys/proto`, the system-wide setting;
4. `hybrid`.

The value is one of `hybrid`, `classical` or `keyring`. A server (an
export or a listener) may name `hybrid` and `classical` together,
separated by a space or a comma: it then accepts either handshake. A
client always uses exactly one, the first named.

The rule is that **neither end falls back** to a choice the user did not
make:

- a `hybrid` client of a server that offers only the classical exchange
  fails with `no hybrid key offered`, instead of continuing with version 3;
- a server that takes only `hybrid` refuses a version 1, 2 or 3 proof
  with `hybrid authentication required`;
- a server that takes only `classical` offers no ML-KEM key, and refuses
  a version 4 proof with `classical authentication required`;
- a `keyring` client and a `hybrid` or `classical` server (or the other
  way round) do not understand each other and the mount fails; nothing
  retries with the other protocol.

The key tools make hybrid keys unless told `-p classical`. The module
policy takes hybrid signatures, and classical ones only when the policy
says so (`docs/signing.md`).

## Keys

| key | seed file | public key | signature |
|---|---|---|---|
| classical | 32 bytes, the Ed25519 seed | 32 bytes | Ed25519, 64 bytes |
| hybrid | 64 bytes: the Ed25519 seed, then the ML-DSA-65 seed | 1984 bytes: Ed25519 (32), then ML-DSA-65 (1952) | 3373 bytes: Ed25519 (64), then ML-DSA-65 (3309) |

A seed file's length says which kind it is. Seed files are mode 0600. A
public key file (`*.pub`, `/usr/<user>/key.pub`) holds the raw public
key, 32 or 1984 bytes.

A hybrid signature over a message `M` is the Ed25519 signature and the
ML-DSA-65 signature (FIPS 204, empty context, deterministic) of

```
M' = "pghybrid1" 0x00 || M
```

It verifies only when both halves do. The prefix means the Ed25519 half
cut out of a hybrid signature is never a classical signature over a
message a classical verifier would take, and every message a hybrid key
signs also begins with a hybrid-only context string (`pgcert2`, `pgauth4`,
`pgmodsig2`, `pgaotimg2`).

### Key lists

Text, one key per line, optionally followed by a space or tab and a
comment; blank lines and lines starting with `#` are ignored. A classical
key is its 64 hex digits, as in the Rust sibling. A hybrid key is
written by its **fingerprint**:

```
pq:<64 hex digits>      fingerprint = SHA-256("pgkey2" 0x00 || public key[1984])
```

so a line stays short and every list keeps one shape. Proofs and
certificates carry whole public keys; a verifier hashes the key it was
given and looks the fingerprint up.

The lists are those of the Rust sibling, read at the same paths in the
same namespaces: `/lib/keys/signers`, `/lib/keys/revoked`,
`/lib/keys/users` and `/lib/keys/servers` (named lists:
`<name> <key> [comment]`), `/lib/keys/serversigners`,
`/lib/keys/modules` and `/lib/keys/aot`. A damaged revocation list
refuses every certificate. Revoking a hybrid key's Ed25519 half (its
first 32 bytes, as a classical line) revokes the hybrid key too.

A list holding `pq:` lines is not readable by the Rust sibling's strict
parser: a revocation list with hybrid lines refuses everything there.
Keep lists meant for Rust systems classical.

### Certificates

```
pgcert1 <name> <key> <expires> <signer>:<signature> [...]     classical, hex
pgcert2 <name> <key> <expires> <signer>:<signature> [...]     hybrid, base64url
```

`pgcert1` is the Rust sibling's format exactly. `pgcert2` has the same
fields: the name (1 to 255 bytes of UTF-8 with no white space, control
characters, `/` or `:`, not `.` or `..`), the user's hybrid public key,
the expiry (decimal seconds since 1970 UTC, `0` for never) and one to 8
signatures by distinct hybrid signers. The key, each signer's public key
and each signature are unpadded base64url (RFC 4648, Section 5), which
keeps the line a third shorter than hex. Each signer signs

```
"pgcert2" 0x00 || len(name) u8 || name || key[1984] || expires u64 LE
```

with a hybrid signature, so both algorithms must verify for every
signature that counts. Parsing is strict: the line must be exactly what
the encoder writes. A `pgcert2` certificate certifies only a hybrid key,
and only hybrid signers sign it; a `pgcert1` certificate is classical
throughout.

### Authinfo

What `getauthinfo` and `auth/mkauthinfo` write, at
`/usr/<user>/keyring/<keyname>` (default `default`), mode 0600:

```
key <seed in hex>                64 hex digits (classical) or 128 (hybrid)
signer <the signer's key>        64 hex digits, or pq:<fingerprint>
pgcert1 ... | pgcert2 ...
```

The classical form is the Rust sibling's. Inferno's keyring files live
in the same directory with their own format; the first line tells them
apart.

## Mount handshake

The client sends `Tauth`, reads the server's offer from the auth fid,
and writes a proof. Versions 1 to 3 are the Rust sibling's and are
described in its `docs/auth.md`; in short:

```
v1  signature[64] || key[32]                           over the raw challenge
v2  0x02 || kind || signature[64] || body
v3  0x03 || kind || signature[64] || C[32] || body     offer = challenge[32] || S[32]
```

Version 4 is the hybrid handshake.

### Version 4

```
C -> S  Tversion, Tauth afid uname aname               (clear)
C -> S  Tread afid 0 1248
S -> C  offer4 = challenge[32] || S[32] || ek[1184]
        S: the server's ephemeral X25519 key
        ek: its ephemeral ML-KEM-768 encapsulation key
        (the client makes c, C = X25519(c, base), and (ss_k, ct) = Encaps(ek))
C -> S  Twrite afid 0.. proof4                          (one or more writes)
S -> C  Rwrite (or Rerror)                              last clear reply
        records from here on, each way
C -> S  Tattach fid afid uname aname, ...
```

```
signed4  = offer4[1248] || C[32] || ct[1088]                    2368 bytes
message4 = "pgauth4" 0x00 || signed4 || len(uname) u16 LE || uname
                           || len(aname) u16 LE || aname || kind u8 || body
proof4   = 0x04 || len u32 LE || kind u8 || signature[3373] || C[32]
                || ct[1088] || body
```

`len` is the length of the whole proof. `kind` and `body` are a hybrid
public key (0) or a `pgcert2` line (1). The signature is the body key's
hybrid signature over `message4`, so it binds the challenge, both X25519
keys, the encapsulation key, the ciphertext, both names and the client's
identity.

The ML-KEM roles are reversed from TLS (the server makes the
encapsulation key and the client the ciphertext) so that the exchange
needs no extra round trip: the offer is read before the proof exactly as
in version 3, and both ephemeral secrets still live for one exchange.

A proof with a certificate is about 14 KiB, more than one 8 KiB Styx
write carries, so the client writes it in pieces at increasing offsets.
The server takes the bytes in order (a write at any other offset is
`authentication failed`), knows the end from `len`, and checks the proof
when the last piece arrives; the reply to that write is the result, and
the records start after it in both directions, as in version 3.

Keys:

```
ss_x   = X25519(s, C) = X25519(c, S)                all zeros refused
ss_k   = ML-KEM-768 shared secret
prk    = HKDF-Extract(salt = SHA-256(message4), ikm = ss_k || ss_x)
c2s    = HKDF-Expand(prk, "pgauth4 c2s", 32)
s2c    = HKDF-Expand(prk, "pgauth4 s2c", 32)
bind   = HKDF-Expand(prk, "pgauth4 server", 32)
```

The secret concatenation puts ML-KEM's secret first, as TLS's
`X25519MLKEM768` does, and like X-Wing it hashes the public values of
both exchanges into the derivation (through the salt, which covers the
offer, `C` and `ct`). The session keys stay secret as long as either
X25519 or ML-KEM-768 does.

A server that takes both handshakes offers the 1248 bytes; their first
64 are a version 3 offer, so a classical client that reads 64 bytes
proceeds as before. A classical-only server offers 64 bytes.

### Record layer

Unchanged from the Rust sibling, for both versions:

```
record = size[4] || ciphertext || tag[16]       ChaCha20-Poly1305 (RFC 8439)
nonce  = seq u64 LE || 0[4]
aad    = size[4]
```

The keys are 256 bits, which Grover's algorithm leaves at 128 bits of
security, so the cipher needs no post-quantum change. Go's standard
library has ChaCha20-Poly1305 only inside `crypto/tls`, so `libsec`
implements it (tested against RFC 8439 and against records the Rust
sibling sealed). Each direction carries at most 2^48 records; errors are
`bad record mac`, `record too long`, `record too short` and `record
sequence exhausted`.

### Server proof

After an accepted proof the client may read the server's proof from the
auth fid, sealed: at offset 64 for version 3 (the Rust format), at offset
1248 for version 4, in as many reads as it takes (an empty read is the
end).

```
server proof = kind u8 || signature || body
message      = context || transcript[32] || bind[32] || kind u8 || body
context      = "pgauth3 server" 0x00   (v3: Ed25519, key or pgcert1)
               "pgauth4 server" 0x00   (v4: hybrid, key or pgcert2)
transcript   = SHA-256(message3 or message4)
```

What the client trusts as a server, and the per-host check, are the Rust
sibling's (`/lib/keys/servers`, `/lib/keys/serversigners`, the host as
dialled).

### Errors

The Rust sibling's errors keep their wording: `authentication failed`,
`certificate expired`, `certificate revoked`, `untrusted signer`,
`certificate name mismatch`, `no session key offered`, `server did not
authenticate`, `server authentication failed`, `encryption required`,
`conversation is encrypted`, `server trusts no keys`. The hybrid layer
adds:

| error | meaning |
|---|---|
| `no hybrid key offered` | a hybrid client found an offer without an ML-KEM key (a classical or Rust server, or an offer cut short on the way) |
| `hybrid authentication required` | a server that takes only `hybrid` got a version 1, 2 or 3 proof |
| `classical authentication required` | a server that takes only `classical` got a version 4 proof |
| `hybrid signature required` | a module policy that takes only hybrid signatures found only classical ones (`docs/signing.md`) |

## Why these algorithms

- **ML-KEM-768 and ML-DSA-65** are the NIST post-quantum standards
  (FIPS 203 and FIPS 204) at security category 3, about AES-192. The
  768 and 65 parameter sets are what TLS (`X25519MLKEM768`) and most
  hybrid deployments chose: category 1 leaves little margin against
  cryptanalytic progress on lattices, and category 5 costs about half
  again in size for no requirement we have.
- **Hybrid**, not post-quantum alone: the lattice schemes are young.
  Every key exchange here combines X25519 with ML-KEM and every
  signature is Ed25519 and ML-DSA, so a break of either half alone, a
  classical break of the lattice scheme or a quantum break of the curve,
  leaves the whole secure.
- **Go's standard library only** (`crypto/mlkem`, `crypto/mldsa`,
  `crypto/ed25519`, `crypto/ecdh`, `crypto/hkdf`, `crypto/sha256`), plus
  ChaCha20-Poly1305 in `libsec`. All of them build for `wasip1` (guests)
  and `GOOS=tamago` (the kernel); no third-party code is added.
- **The record layer stays** ChaCha20-Poly1305 with 256-bit keys, the
  Rust sibling's, so one layer serves both versions.

## Sizes and costs

Measured on an Apple Silicon host under Linux (`go test -bench .
./pgauth`), native Go, with other work running:

| item | classical | hybrid |
|---|---|---|
| seed file | 32 B | 64 B |
| public key | 32 B | 1984 B |
| signature | 64 B | 3373 B |
| key list line | 64 + comment | 67 + comment |
| certificate, one signer | 283 B | 9817 B |
| offer | 64 B | 1248 B |
| proof, key / certificate | 130 B / 381 B | 6483 B / 14307 B |
| server proof, key | 97 B | 5358 B |
| module signature section, one key | 116 B | 5378 B |
| handshake (both sides, certificate check) | 0.30 ms | 2.2 ms |

The record layer seals and opens an 8 KiB record in about 50 µs
together (165 MB/s), the same for both versions. Under wazero a guest
runs these several times slower; a mount pays the handshake once per
connection.

## Adopting version 4 in Rust

Nothing here changes the Rust repository. To speak version 4, the Rust
sibling would need:

1. ML-KEM-768 and ML-DSA-65 in `purg-auth` (`ml-kem` and `ml-dsa` from
   RustCrypto are `no_std`), with ML-DSA's deterministic signing so that
   module signatures stay reproducible.
2. The hybrid key, signature, fingerprint and `pgcert2` formats above,
   checked against vectors from this repository (`pgauth` pins its
   encodings with known-answer tests).
3. In `proof`, `session` and `server`: the version 4 proof, the offer of
   1248 bytes (its first 64 stay the version 3 offer, so old Rust clients
   keep working), the derivation, and the server proof at offset 1248.
4. In `purg-core`'s export: accepting a proof in several writes, since
   Rust's 64 KiB msize would carry it in one but an 8 KiB Go client
   writes it in pieces.
5. `purgatorio.sig` version 2 with entry kinds 2 and 3, and `pgaotimg2`
   in image files (`docs/signing.md`).
6. A setting like `authproto` and the no-fallback rules.

## Status

- Landed: `pgauth` (keys, lists, both certificate formats, authinfo,
  proofs of versions 1 to 4, both exchanges, the record layer, the
  server proof) and `pgauth/modsig` (module digests and signatures,
  signed image files), with cross tests against files written by the Rust
  sibling's `purg-auth` (`pgauth/testdata/rust`): Go parses them and
  makes the same bytes from the same seeds.
- Next: the kernel's mount and export speaking versions 3 and 4, the
  `-p` flag and the `authproto` setting, the tools under `/bin/auth`, and
  the module policy.

## Using `pgauth`

```go
k, _ := pgauth.GenerateKey(true)              // hybrid; false for Ed25519
k, _ = pgauth.NewPrivateKey(seedFileBytes)    // 32 or 64 bytes
sig := k.Sign(msg); ok := k.Public().Verify(msg, sig)
id := k.Public().ID()                         // for key lists: id.String()

c, _ := pgauth.NewCert("alice", k.Public(), 0) // pgcert2 for a hybrid key
c.Sign(signer); line := c.Encode(); c, _ = pgauth.ParseCert(line)

srv := pgauth.NewServer(true)                 // offer: srv.Offer()
cl, _ := pgauth.NewClient(srv.Offer(), true)  // or ENoHybrid
proof, _ := pgauth.MakeProof(k, c, cl.Signed(), uname, aname)
err := pgauth.Check(policy, srv.Offer(), uname, aname, proof)
keys, _ := srv.Keys(uname, aname, proof)      // and cl.Keys(...)
seal, open := keys.Client()                   // records
```

For a stream protocol of its own (Lethe), `NewServer`/`NewClient`,
`MakeProof`, `Check` and `Keys` are the whole exchange; frame the offer
and the proof however the protocol frames messages.
