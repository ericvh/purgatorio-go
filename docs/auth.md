# Authentication

Purgatorio-Go has three ways to authenticate a mount or an export:

| choice | what it is | for |
|---|---|---|
| `hybrid` (default) | Ed25519 + ML-DSA-65 keys, `pgcert2` certificates, the version 4 handshake (X25519 + ML-KEM-768), then encrypted records | Go peers (this port's own formats) |
| `classical` | Ed25519 keys, `pgcert1` certificates, the version 3 handshake (X25519), then encrypted records | Rust peers (byte-compatible with the Rust sibling's `purg-auth`) |
| `keyring` | Inferno's keyring: station-to-station over the raw connection, then `ssl` (`#D`) | Inferno peers |

`hybrid` and `classical` are this system's own protocols, taken from the
Rust sibling (its `docs/auth.md` is the reference for every classical
format) with a post-quantum layer added. `keyring` is Inferno's
`keyring(2)`, `auth(2)` and `ssl(3)` (`sdk/keyring`, `sdk/auth`,
`core/devssl.go`) and is unchanged.

The classical formats interoperate with the Rust sibling, and the cross
tests (`TestRcmdRust`, `TestLetheRust`, CI's `rust-interop` job) check
them. The hybrid formats were designed here, before the Rust sibling had
any. It has since defined hybrid formats of its own that do not match
these: a Go hybrid peer and a Rust hybrid peer do not understand each
other ("The Rust sibling's hybrid formats" below). Converging them is an
open item.

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

The Rust sibling writes a hybrid key in a list differently
(`mldsa65:` and an id hashed another way) and does not read `pq:`
lines: a revocation list with them refuses everything there. Keep lists
shared with Rust systems classical.

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

### A conversation that went to records

Once a conversation has switched to records it stays that way. A mount
on it without its auth fd fails with `conversation is encrypted`, and so
does any later attempt to start it again in the clear after its session
has ended (the mount driver remembers, on the file, that it went to
records), rather than sending a `Tversion` the server would read as a
broken record. Dial again for a new conversation.

### Errors

The Rust sibling's errors keep their wording: `authentication failed`,
`certificate expired`, `certificate revoked`, `untrusted signer`,
`certificate name mismatch`, `no session key offered`, `server did not
authenticate`, `server authentication failed`, `encryption required`,
`conversation is encrypted`, `server trusts no keys`. The hybrid layer
adds:

| error | meaning |
|---|---|
| `no hybrid key offered` | a hybrid client found an offer without an ML-KEM key (a classical server, such as a Rust one built before its version 4, or an offer cut short on the way) |
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
- **Known answers.** `pgauth/testdata/acvp` is a subset of NIST's ACVP
  vectors for ML-KEM-768 (key generation from seeds, deterministic
  encapsulation, encapsulation key checks) and ML-DSA-65 (key
  generation, verification with a context), checked through the calls
  `pgauth` makes; Go's standard library runs the full sets in its own
  tests.
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

## The Rust sibling's hybrid formats

The Rust sibling has since added a hybrid layer of its own (its
`docs/auth.md`, "Hybrid key exchange (version 4)" and "Post-quantum
signatures"). It uses the same primitives (X25519 with ML-KEM-768,
Ed25519 with ML-DSA-65, the same 1984-byte public keys, 3373-byte
signatures and `pghybrid1` prefix) but different formats:

| | here | the Rust sibling |
|---|---|---|
| version 4 proof | `0x04 \|\| len u32 \|\| kind \|\| signature[3373] \|\| C \|\| ct \|\| body`, kinds 0 and 1 hybrid only | `0x04 \|\| kind \|\| signature[64 or 3373] \|\| C \|\| ct \|\| body`, kinds 0 and 1 Ed25519, 2 and 3 hybrid; version 4 with an Ed25519 key is its default |
| a hybrid key in a list | `pq:` and `SHA-256("pgkey2" 0x00 \|\| key)` | `mldsa65:` and `SHA-256("pghybrid1 key" 0x00 \|\| key)` |
| `pgcert2` | base64url key, signers and signatures | `mldsa65:` and hex, whole signer keys, at most 4 signatures |
| module signatures | section version 2, kinds 2 and 3 over `pgmodsig2` | section version 1, kinds 2 and 3 over `pgmodsig1` |
| AOT images | hybrid entries over `pgaotimg2` | Ed25519 entries only |
| downgrade | no fallback: a client uses exactly the protocol it is told | every current server marks its challenge, and a client falls back to version 3 only from a server without the mark |
| Lethe | `Tauth2` as in docs/lethe.md | its own `Tauth2` (docs/lethe.md, "The Rust sibling's hybrid exchange") |

So a Go peer and a Rust peer interoperate only under `classical`
(version 3, `pgcert1`, Ed25519 module signatures, classical Lethe
tickets). Converging the two hybrid layers is an open item; when it is
done, the cross tests should cover the hybrid formats too.

## Password scheme

The signer checks a password without the password, or anything an
attacker can test a guess against, crossing the network. As in the Rust
sibling, the password is hardened with **Argon2id** into a verifier `w`
(`argon2id <m> <t> <p> <salt> <w>`, default 19456 KiB and 2 passes,
`-C m,t` to change it), which `auth/keyfs` keeps sealed under the master
password; and **SPAKE2** over the Ed25519 group, with `w` as its
password, gives both ends a key only when they used the same `w`. The
code is `sdk/pglogin` (SPAKE2 over `filippo.io/edwards25519`, Argon2id
from `golang.org/x/crypto/argon2`; both pure Go, built for wasip1 and
`GOOS=tamago`).

Two versions of the login protocol:

```
pglogin1 (classical: the Rust sibling's, byte for byte)
C -> S  "pglogin1 <op> <name>"
S -> C  "argon2id <m> <t> <p> <salt hex>"
S -> C  SPAKE2 message B (33 bytes)
C -> S  SPAKE2 message A (33 bytes)
C -> S  seal(c2s, request)
S -> C  seal(s2c, reply)   or   "error <reason>" in the clear

pglogin2 (hybrid, the default)
C -> S  "pglogin2 <op> <name>"
S -> C  "argon2id <m> <t> <p> <salt hex>"
S -> C  SPAKE2 message B
S -> C  ML-KEM-768 encapsulation key (1184 bytes, ephemeral)
C -> S  SPAKE2 message A
C -> S  ML-KEM-768 ciphertext (1088 bytes)
C -> S  seal(c2s, request)
S -> C  seal(s2c, reply)   or   "error <reason>" in the clear
```

SPAKE2 is python-spake2's construction (the Rust `spake2` crate): the
identities are `A = <name>`, `B = "pgsigner"`, and its key is SHA-256
over the hashes of `w` and both identities, both messages and the shared
point. pglogin1 derives one ChaCha20-Poly1305 key per direction with
HKDF-SHA256 from that key, salted with SHA-256 of the first two frames
(labels `pglogin1 c2s`, `pglogin1 s2c`). pglogin2 uses the SPAKE2 key
followed by the ML-KEM shared secret as the HKDF input, salted with the
hash of the first two frames, the encapsulation key and the ciphertext
(labels `pglogin2 c2s`, `pglogin2 s2c`). Nonces count from 0.

**Why a hybrid login.** SPAKE2 rests on discrete logarithms. Someone who
records a pglogin1 exchange and later has a quantum computer can take
discrete logs of the two messages and then test password guesses
offline against the sealed request: the password, and every certificate
the session carried, fall to a dictionary. With the ML-KEM secret mixed
into the keys, the sealed request cannot be opened without it, so the
recording gives nothing to test a guess against while ML-KEM-768 holds;
and it changes nothing about the classical security, which still rests
on SPAKE2. An active attacker gets one online guess per connection in
either version. The verifier `w` stays password-equivalent, as in the
Rust sibling; an augmented PAKE (OPAQUE) is still an open question there.

**Choosing.** The client tools (`auth/getauthinfo`, `auth/passwd`) use
one version, from `-p`, `$authproto`, `/lib/keys/proto` or `hybrid`; the
servers (`auth/signer`, `auth/keysrv`) take the versions their `-p` or
setting names, `hybrid` by default, or both with `hybrid,classical`. A
server sent the other version answers `error hybrid authentication
required` or `error classical authentication required` in the clear;
nothing falls back. The key a signer certifies is of the signer's
family (a hybrid signer takes only hybrid keys: `key of the wrong
algorithm` otherwise), so `auth/getauthinfo -p classical` against a
classical signer is the whole Rust flow: an Ed25519 key, pglogin1 and a
`pgcert1`.

Sealed files (keyfs's database, secstore's files: `pgseal1`) and crypt's
stream (`pgcrypt1`) are the Rust sibling's formats. They are password
encryption, which has no key exchange to harden; their 256-bit keys
need no change.

Compatibility: `sdk/pglogin/testdata` was written by the Rust
`purg-auth` with a deterministic random source (`gen.rs.txt`). Go makes
the same SPAKE2 messages and key, the same verifier and stand-in
verifier for an unknown user, the same sealed file and crypt stream, the
same netkey answer, and replays a whole pglogin1 exchange frame for
frame from both ends.

## Commands and the kernel

How each part takes the choice:

- **`mount`** resolves the protocol as above. With `hybrid` or
  `classical` it runs `fauth`, reads the offer, signs the proof with
  the key in `-k keyfile` (an authinfo or a seed; a name without a slash
  is under `/usr/<user>/keyring`) or else the first of
  `/usr/<user>/keyring/default`, `/usr/<user>/key` and the public test
  key of that family, writes it, and mounts with the auth fd. `-S`
  requires the server's proof for the host of the dial string (the
  guest checks who it is, the kernel checks the signature over the
  session, on the same bytes; `MSRVAUTH`). With `keyring` it is
  Inferno's mount: keyring authentication before `Tversion`, then
  `ssl`. A `{command}` is never authenticated, as in Inferno, and runs
  with `authproto=none`, so an export it starts asks for no proof
  either: `mount {export dir} /n/x` works as it always did.
- **`export`** with `hybrid` or `classical` (or both) asks the kernel to
  authenticate: its export then trusts the files under `/lib/keys` and
  `/usr/<user>/key.pub` in the exporter's own namespace (as the Rust
  sibling's), lets nobody in when it trusts nothing (`server trusts no
  keys`), and after a version 3 or 4 proof seals every reply. `-k
  keyfile` is the server's key for its proof (else the exporting user's
  own, from the same files `mount` uses), `-e` refuses version 1 and 2
  proofs, and `-A`, `keyring` and `none` ask for no proof, which is
  Inferno's export: with `keyring` the listener has authenticated the
  connection already.
- **`styxlisten`** with `hybrid` or `classical` lets the kernel's export
  of each client authenticate it, with `-k` as the server's key; with
  `keyring` it authenticates each client with Inferno's protocol as
  before. The server command runs on a pipe with `authproto=none`.
- **`listen`** runs non-Styx services too, so it does not authenticate
  itself: it passes the protocol to each command as `authproto` (or
  `none` with `-A`, except under `keyring`, whose `-A` is Inferno's and
  concerns only `listen`'s own authentication), and an `export` or
  `auxi/rstyxd` it runs authenticates that way.
- **`rcmd`, `cpu`, `auxi/rstyxd` and `import`** speak pgauth's protocols
  under `hybrid` and `classical` and Inferno's under `keyring`
  (`docs/rcmd.md`).
- The **kernel's own calls** (`sys.Export` from `cpu`, `rcmd`,
  `iostats`, `trfs`) ask for no proof unless their flags say so, as in
  Inferno.

Differences from the Rust sibling:

- `#¤` here is Inferno's capability device (`devcap.c`), not a key
  agent, and the kernel's `mount` with no auth fd does not authenticate:
  the `mount` command does it through `fauth`, with a key file or the
  guest factotum.
- An export without `-p` and without `authproto` authenticates with
  `hybrid`, whose formats no Rust peer speaks (the Rust sibling's
  hybrid formats are its own, above): a Rust peer needs `-p classical`
  (or `hybrid,classical`) on the Go side, and a Go client of a Rust
  server needs `-p classical`.

## Tools

The Rust sibling's tools, at the same paths (`appl/cmd/auth/<name>`
installs as `/bin/auth/<name>.wasm`), with the same flags and output.
Inferno's keyring tools of the same names (`createsignerkey`, `getpk`,
`getauthinfo`, `keyfs`, `signer`, `changelogin`, `logind`) stay at
`/bin/<name>`, so the two never clash: `createsignerkey` is Inferno's,
`auth/createsignerkey` this system's. The one Rust tool outside `auth/`
whose name Inferno's takes is `getauthinfo`; here it is
`auth/getauthinfo`. `crypt` and `netkey` keep their Rust paths.
Inferno's `auth/aescbc` (the aescbc file format of secstore files) and
`auth/ai2key` (an Inferno authinfo file as a factotum key) are under
`auth/` too, as in Inferno.

| tool | role |
|---|---|
| `auth/createsignerkey [-f keyfile] [-p proto] [name]` | Makes a signer key (default `/keydb/signerkey`): hybrid, or Ed25519 with `-p classical`. Prints its key-list line, ready for `/lib/keys/signers`. |
| `auth/getpk [-c] file ...` | Prints the key ID of a seed, a `*.pub` file, an authinfo or a certificate: 64 hex digits, or `pq:` and the fingerprint. `-c` prints the certificate line of an authinfo or a certificate instead. |
| `auth/mkauthinfo [-f signerkey] [-e expires] user keyfile` | Makes a key for `user` of the signer's family, certifies it (`pgcert2` for a hybrid signer, `pgcert1` for an Ed25519 one) and writes the authinfo. `signerkey` may be an authinfo, whose key then certifies the new one: a chain (`docs/signing.md`, "Trust"). |
| `auth/countersigner [-f key] file` | Adds a signature of the certificate's family and prints the file with the new certificate. |
| `auth/verify [-m] [-p proto] [-s signers] [-r revoked] [-u name] [-t time] file` | Checks a certificate as a server would, or with `-m` a module as the loader would; `-p hybrid,classical` lets classical module signatures count. |
| `auth/sign [-k keyfile] [-p proto] [-c certfile]... [-o out] module` | Signs a module with a seed or an authinfo, or the user's own key of the family `-p` names. |
| `auth/keyfs [-m mntpt] [-C m,t] [keyfile]` | Serves the key database (default `/keydb/keys` at `/mnt/keys`), sealed under an Argon2id key from the master password (standard input). Each user is a directory with `secret`, `expire` and `status`. |
| `auth/changelogin [-m mntpt] [-C m,t] [-e expires] user` | Reads a password and writes the user's verifier in keyfs. |
| `auth/signer [-f signerkey] [-m mntpt] [-e days] [-p proto]` | The certificate server, for one conversation on fd 0 (under `listen`). |
| `auth/getauthinfo [-s signer] [-u user] [-p proto] keyname` | Reads the password, makes a key, gets it certified by the signer and writes the authinfo. Inferno's is `/bin/getauthinfo`. |
| `auth/keysrv [-f signerkey] [-m mntpt] [-p proto]` | The password-change server, for one conversation on fd 0. |
| `auth/authsrv [-k keyfile]` | Plan 9's ticket service for `p9sk1` (authsrv(6)), for one conversation on fd 0, from the keys in `/lib/keys/p9sk1` ("p9any and p9sk1"). |
| `auth/passwd [-s server] [-u user] [-p proto]` | Changes the password through `auth/keysrv`; the new verifier is made on the client. |
| `auth/secstore [-s dir] [-C m,t] [-g file \| -G file \| -p file \| -r file]` | Files sealed under a password in a store directory. |
| `crypt [-d] [-C m,t] (-k secret \| -f secretfile)` | Encrypts or decrypts standard input in 64 KiB chunks. |
| `auth/factotum [-m mntpt] [-a authaddr] [-s authinfo] [authinfo ...]` | The agent (see "Factotum"). |
| `netkey` | Answers challenges with HMAC-SHA256 under the password's hash, in place of the DES SecureNet calculator. |

The tools make hybrid keys unless told `-p classical`, and read and
write the Rust formats: an Ed25519 key signs exactly as the Rust tools do
(`cmd/emu/authtools_test.go` signs a module with a key the Rust code
made and gets its bytes).

## Factotum

`auth/factotum [-m mntpt] [-a authaddr] [-s authinfo] [authinfo ...]`
is the Rust sibling's agent, with Inferno's `p9any`: Plan 9's `ctl` and
`rpc` files at `/mnt/factotum`. Keys are written to `ctl` as attribute
lists; a name starting with `!` is secret and never read back.

```
key proto=ed25519 [user=<name>] [role=<role>] !seed=<hex> [cert='<certificate>']
key proto=pass server=<addr> user=<name> !password=<password>
key proto=p9sk1 dom=<authdom> user=<name> !password=<password>
key proto=p9sk1 dom=<authdom> user=<name> !hex=<14 hex digits>
delkey proto=<proto> [attr=value ...]
```

A seed of 32 bytes is an Ed25519 key and one of 64 a hybrid key; `ctl`
shows which with `alg=classical` or `alg=hybrid`, and a `start` names
`alg` to pick the key for the handshake it runs. Roles (`client`,
`server`, `modsign`) and the choice of key are the Rust sibling's: a key
kept for a role serves only it, one without a role serves every role,
and a `start` takes the first key kept for its role, else the first with
none. Factotum builds every message it signs, so it signs nothing but:

- `role=client` (the default): `write` the 32, 96 or 2368 bytes read
  from an auth fd, `read` gives `ok <proof>`, version 2, 3 or 4;
- `role=server`: `write` the session's transcript and bound value (64
  bytes), `read` gives the server's proof, version 3 or 4 by the key;
- `role=modsign`: `write` a module's digest and ABI version, `read` gives
  a `purgatorio.sig` entry.

A reply longer than one read (a hybrid proof with a certificate is about
14 KiB) comes in as many reads as it takes; the mount driver keeps
reading while reads are full, so one large read gets all of it.

Who uses it, when no key file is named:

- `mount` makes its proof through it (`authproto.AuthenticateAgent`);
- the kernel's export asks it, in the exporter's namespace, for its
  server proof before it looks for the exporting user's key files, so a
  server's key loaded with `auth/factotum -s` never has to be readable by
  the exporting prog;
- `auth/sign` signs through it (`role=modsign`).

As in the Rust sibling, whoever can open factotum's `rpc` can make
proofs with its keys: a user's factotum belongs in that user's namespace.

### p9any and p9sk1

`start proto=p9any role=client` or `role=server` runs Plan 9's `p9any`
and `p9sk1` with `p9sk1` keys, as Inferno's factotum does
(`appl/cmd/auth/factotum/p9any.go`), for 9export and import
(`docs/rcmd.md`). The conversation uses Plan 9's rpc verbs: a `read`
gives `ok <data>` to send to the peer, or `phase` when factotum wants
the peer's next message, which the caller then `write`s (`toosmall <n>`
asks for at least n bytes); `done haveai` ends it, and `authinfo` gives
the users and the shared secret. `authproto.Proxy` is the caller's side,
as Inferno's `Factotum->proxy`. An error is a failed read of `rpc`
(`error <message>`), as in Inferno, so Inferno's messages reach the
command: `can't authenticate import: proxy rpc write: error p9sk1:
getasticket: failed: wrong key?`.

`p9sk1` is the shared-key protocol of Plan 9's authsrv(6). The server
sends a ticket request naming its user and domain; the client asks the
authentication server for tickets, one sealed with its own DES key and
one with the server's, and passes the server's on with an authenticator;
each side then proves it holds the ticket's key. `sdk/auth9` has the
messages, Plan 9's DES encryption and `passtokey`, as Inferno's Auth9.

- The client dials the authentication server at the address `-a` gives
  (Plan 9's factotum option), else the `auth` cs gives for the domain,
  else `$auth`, at the `ticket` service (`tcp!*!567`). As in Inferno, it
  always asks the server; it does not make tickets itself when it holds
  the server's key, as Plan 9's factotum can.
- Inferno's `p9any` is a client only; the server role here is written from authsrv(6),
  with Plan 9's errors (`no valid keys`, `p9sk1: key mismatch with auth
  server`, `p9sk1: client lies through his teeth`). It offers
  `v.2 p9sk1@dom` for each `p9sk1` key.
- The client checks the ticket's challenge as well as its number, so a
  wrong key always gives `wrong key?` rather than, once in 256 tries,
  failing later at the server.
- `auth/authsrv [-k keyfile]` is the authentication server's ticket
  service, for one conversation on fd 0. It answers ticket requests as
  authsrv(6) describes (`auth9.ServeTickets`), from the keys in
  `keyfile`, by default `/lib/keys/p9sk1`. Run it under `listen` on the
  `ticket` service (`tcp=ticket port=567` in `/lib/ndb/local`):

  ```
  listen -A 'tcp!*!ticket' auth/authsrv
  ```

  The key file holds factotum's `p9sk1` key lines, one a user, the same
  lines a user writes to factotum's `ctl`:

  ```
  # /lib/keys/p9sk1
  key proto=p9sk1 dom=<authdom> user=<name> !password=<password>
  key proto=p9sk1 dom=<authdom> user=<name> !hex=<14 hex digits>
  ```

  Blank lines and lines starting with `#` are skipped, as are keys of
  other protocols; a line it cannot take stops it with the line's
  number. A request is answered from the keys of its domain. A host gets
  tickets for itself only (Plan 9's `speaksfor` is not kept); an
  unknown user or a host asking for another gets tickets sealed with
  random keys, so the reply tells it nothing. Plan 9's password changes
  and challenge boxes are not served. The file holds every user's key,
  so only the host owner should be able to read it.
  `cmd/emu/p9any_test.go` runs it.
- The capability (`cap`) a Plan 9 server returns, and `p9sk2`, are left
  out.

## Status

- Landed: `pgauth` (keys, lists, both certificate formats, authinfo,
  proofs of versions 1 to 4, both exchanges, the record layer, the
  server proof) and `pgauth/modsig` (module digests and signatures,
  signed image files), with cross tests against files written by the Rust
  sibling's `purg-auth` (`pgauth/testdata/rust`, classical formats): Go parses them and
  makes the same bytes from the same seeds. The kernel's export and #M
  speak versions 1 to 4 with records and the server's proof; `mount`,
  `export`, `styxlisten` and `listen` take `-p`, `authproto` and
  `/lib/keys/proto` (`cmd/emu/pgauth_test.go`: every pairing, the
  refused downgrades, a server that trusts nothing, `-S`, and a relay
  that sees no name or data once encrypted).
- The tools above, and the module policy (`docs/signing.md`).
- The password tools and both login versions (`cmd/emu/pglogin_test.go`).
- The guest factotum (`cmd/emu/factotum_test.go`, and unit tests of its
  roles in `appl/cmd/auth/factotum`).
- Factotum's `p9any` and `p9sk1`, both roles, used by 9export and both
  imports (`cmd/emu/p9any_test.go`).
- `rcmd`, `cpu`, `auxi/rstyxd` and `import` over pgauth (`docs/rcmd.md`),
  with Inferno's protocols kept under `keyring`.
- A conversation that went to records is never restarted in the clear.

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
