# Lethe

Lethe is a stateless protocol for reading static file trees. It was
designed for the Rust Purgatorio, and its design document is the
specification: `docs/lethe.md` in github.com/ericvh/purgatorio (the
first cut is its Section 14, tickets its Section 6, the clear mode its
Section 17). This file says what the Go version implements, where it
lives, and the one thing it adds: a hybrid post-quantum ticket
exchange, described in full below. The Go version is wire-compatible
with the Rust one in the clear mode and with classical tickets: each end
talks to the other.

In short: a server keeps no state. Each request names an absolute path
and carries everything needed to answer it; a reply carries the file's
qid, whose version a client caches by. A client authenticates once and
gets a ticket, sealed under a key only the servers hold; every request
carries the ticket and is sealed with the ticket's session keys. A lost
connection loses nothing, and any replica holding the ticket key can
answer any request.

## What is implemented

| piece | where | status |
|---|---|---|
| the codec: frames, requests, replies, paths | `lethe/` | done |
| the server engine over a `lethe.Tree` | `lethe/server.go` | done |
| tickets, the key schedule, sealed requests and replies | `lethe/ticket.go`, `crypto.go`, `tickets.go`, `session.go` | done |
| the classical ticket exchange (`Tauth`, `Rauth`), the Rust one | `lethe` | done |
| the hybrid ticket exchange (`Tauth2`, `Rauth2`), the default | `lethe` | done (Go only) |
| ticket narrowing: rights and prefix | `lethe`, `lethed`, `lethe` | done |
| the clear mode: `Tclear`, `Rclear`, `lethed -A`, `lethe -A`, `noauth` in `#λ` | `lethe`, `lethed`, `#λ`, `lethe` | done |
| `lethed`, the host server | `cmd/lethed`, `cmd/emu/host/lethed*.go` | done |
| `#λ`, the client device, in the emu and the kernel | `core/devlethe.go` | done |
| `lethe`, the attach command | `appl/cmd/lethe` | done |
| XChaCha20-Poly1305 | `libsec/xchacha.go` | done |
| ticket key rotation (several `-t`) | | not yet (the Rust first cut takes one key too) |
| UDP, streaming, writes, `Tdigest`, server proofs | | later, as in the Rust design |

All of `stat`, `read` (with `cond`), `readdir` and `Twalk` work. The
transport is TCP.

## Choosing the exchange

Lethe follows the system-wide setting of `docs/auth.md` ("Choosing
algorithms"):

- `hybrid` is the default: X25519 together with ML-KEM-768 for the
  session's keys, and hybrid Ed25519 + ML-DSA-65 signatures from hybrid
  keys and `pgcert2` certificates.
- `classical` is the Rust sibling's exchange exactly: X25519 alone, an
  Ed25519 signature from a classical key or a `pgcert1` certificate. It
  is an ordinary choice, and the one to use with a Rust peer.

`lethed -p proto` names what the server takes: `hybrid`, `classical`, or
both (`hybrid,classical`); without `-p`, `$authproto`, then `hybrid`.
`lethe -p proto` names what the client uses; without it, `$authproto`,
then the first line of `/lib/keys/proto`, then `hybrid`. A client uses
exactly one exchange, the first named, with a key of that kind.

Neither end falls back:

| client | server | error |
|---|---|---|
| `lethe -p classical` | `lethed` (hybrid only) | `hybrid authentication required` |
| `lethe` (hybrid) | `lethed -p classical` | `classical authentication required` |
| `lethe` (hybrid) | the Rust `lethed` | `no hybrid key offered` |
| a Rust `lethe` | `lethed` (hybrid only) | `hybrid authentication required` |
| `lethe` (hybrid), classical key | | `key of the wrong algorithm` |

A ticket remembers which exchange issued it (below), so a server that
takes only hybrid tickets refuses a classical ticket made by a replica
that shares its ticket key, with `hybrid authentication required`, and a
classical-only server refuses a hybrid ticket with `classical
authentication required`. The clear mode (`-A`) is separate and
unchanged.

## The hybrid ticket exchange

It replaces the Rust `Tauth`/`Rauth` pair (Section 6 of the Rust design)
and nothing else: the ticket, `Treq`, `Rreq`, the replay window, the
authorization rules and the errors are the same. It uses two outer frame
types the Rust design keeps in reserve, inside version 1: no new
version byte, and a Rust server answers them `bad lethe message`.

```
type 11  Tauth2 = eph_c[32] ek[1184] rights u8 prefix s[2] uname s[2]
                  kind u8 body s[2] signature s[2]
type 12  Rauth2 = eph_s[32] ct[1088] nonce[24] sealed_s2c(grant)
grant           = now u64 expires u64 rights u8 prefix s[2] ticket s[2]   (as Rust)
```

- `eph_c` is a fresh X25519 key and `ek` a fresh ML-KEM-768
  encapsulation key, both the client's. Unlike the mount's version 4
  (where the server offers the encapsulation key), Lethe has no server
  challenge, so the client, which speaks first, makes the key pair and
  the server encapsulates to it: still one round trip.
- `kind` and `body` are a hybrid public key (0, 1984 bytes) or a
  `pgcert2` line (1), as in a version 4 mount proof (`docs/auth.md`).
- The signature is the body key's hybrid signature (3373 bytes: Ed25519,
  then ML-DSA-65, both over `"pghybrid1" 0x00 || M`, `docs/auth.md`) over

  ```
  M = "lethe2 auth" 0x00 || eph_c || ek || rights u8 || prefix s[2]
      || uname s[2] || kind u8 || body s[2]
  ```

  a context string of its own, so it never verifies as a mount proof, a
  certificate, a module signature or a classical Lethe `Tauth`.
- The server checks it with the trust of an export (keys held directly,
  trusted signers, revocations; a damaged revocation list refuses
  everyone), makes `eph_s`, computes `ss_x = X25519(eph_s, eph_c)` (all
  zeros refused) and `(ss_k, ct) = Encaps(ek)`, and derives

  ```
  transcript = M || eph_s || ct
  prk        = HKDF-Extract(salt = SHA-256(transcript), ikm = ss_k || ss_x)
  master     = HKDF-Expand(prk, "lethe2 master", 32)
  c2s        = HKDF-Expand(master, "lethe1 c2s", 32)
  s2c        = HKDF-Expand(master, "lethe1 s2c", 32)
  ```

  ML-KEM's secret goes first, as in TLS's `X25519MLKEM768` and the
  mount's version 4. The salt covers both public keys of both exchanges
  and the ciphertext. The keys stay secret as long as either X25519 or
  ML-KEM-768 does.
- `grant` is sealed under `s2c` with XChaCha20-Poly1305, a random
  24-byte nonce, and as associated data the frame header (10 bytes),
  `eph_s` and `ct`.
- The ticket is the Rust ticket (`keyid || nonce || XChaCha20-Poly1305
  (ticket key, "lethe1 ticket" 0x00 || keyid, master || issued ||
  expires || rights || uname || prefix)`), with bit 6 (0x40) of its
  rights set: "issued by the hybrid exchange". The bit is never granted
  to the client (the grant's rights do not have it), and a Rust server
  that opens such a ticket ignores it. The ticket key is symmetric
  (256-bit XChaCha20), so it needs no post-quantum change.

Sizes: a hybrid `Tauth2` with a public key is about 6.5 KiB, with a
`pgcert2` certificate about 14.5 KiB (within one 64 KiB frame); `Rauth2`
is about 1.3 KiB plus the ticket. Requests and replies are the same
size in both exchanges, and a ticket is renewed once an hour by default,
so the cost is per ticket, not per request.

### Adopting it in Rust

Nothing here changes the Rust repository. For the Rust Lethe to speak
the hybrid exchange it would need:

1. The hybrid keys, signatures and `pgcert2` of `docs/auth.md` in
   `purg-auth` (the same list as for the mount's version 4), and
   ML-KEM-768 (`ml-kem` from RustCrypto is `no_std`).
2. In `purg-lethe`: `kind::TAUTH2 = 11` and `kind::RAUTH2 = 12` taken out
   of the reserved range, their codec (the layout above, strict as the
   rest), `crypto::master2` and the sealing of `Rauth2`, and the server
   and client halves, beside the classical ones.
3. `rights::TICKET_HYBRID = 0x40`, set in tickets the hybrid exchange
   issues, and a server setting for which exchanges it takes (`lethed
   -p`), refusing the other with the errors above.
4. In `#λ` and `lethe`: a `proto` setting line and `-p`, with no
   fallback.
5. Cross tests against this implementation: `cmd/emu/lethe_test.go`
   already runs the Rust binaries against the Go ones when
   `PURG_RUST_BIN` is set, and would check the hybrid exchange the same
   way.

## The pieces

- **`lethe`** is a portable package (no `os`, no `net`), so the kernel
  and the host share it. It holds the codec, the path rules, the server
  engine (`Server.Handle`: one frame in, one frame out), the ticket layer
  (`TicketServer`) and the client's side (`AuthFrame`, `Session`,
  `Client`, and the clear requests). Decoding is strict, as in Rust: a
  frame must end exactly where its last field does. The codec tests are
  the Rust codec's byte vectors. Keys, certificates, trust and the hybrid
  signatures come from `pgauth`; X25519, ML-KEM-768, HKDF and SHA-256
  from Go's standard library; XChaCha20-Poly1305 from `libsec`.
- **`lethed`** is a binary of its own, `cmd/lethed`. Its host side
  (`cmd/emu/host/lethed*.go`) is next to `#U`, whose host semantics it
  shares: Inferno error strings, owner names, qids from inodes, names
  that are case-exact on macOS and hidden entries left out. A file's
  qid version comes from its modification time, nanoseconds included,
  and its size. Symbolic links are followed only when they stay inside
  the root; a link out is answered as a name that does not exist.
  Resolved paths and sorted directory listings are kept for a second.
  The trust files are read at every `Tauth`, so an edit takes effect on
  the next ticket.
- **`#λ`** (`core/devlethe.go`) is in the device table of both the emu
  and the kernel. It dials through the platform's IP stack, the one
  `#I` uses, and keeps the key to renew its ticket. It keeps the Rust
  device's caches: an attribute cache that trusts entries and qids for
  `cache` milliseconds (1000 by default), and a data cache of 32 KiB
  blocks by path and version, at most 4 MiB a connection, least recently
  used out first. A sequential reader gets four blocks of readahead on
  one connection. Connections are pooled; a request whose connection
  fails is sent again on a new one, up to nine times with a growing
  pause, so a server that restarts costs a reader a delay and not an
  error. A ticket the server refuses is renewed and the request resent.
  A connection lives as long as a file of it is held.
- **`lethe`** (`appl/cmd/lethe`) resolves the address with `/net/cs`,
  finds the key, writes the settings to `#λ/clone`, and binds `#λn`.

## Using it

```
lethed [-v] [-A] [-p proto] [-a addr] [-u owner] [-t ticketkey] [-e lifetime]
       [-w window] [-k keyfile ...] [-U userdir] [-s signers] [-r revoked] root
lethe [-abc] [-A] [-p proto] [-k keyfile] [-t ms] [-u user] addr mtpt [path]
```

The options are the Rust ones, with `-p` added. Public key files (`-k`,
`-U dir/<user>/key.pub`) hold 32 bytes (classical) or 1984 (hybrid);
the signer and revocation lists are `docs/auth.md`'s. The test keys are
`/lib/test/auth.key` (the Rust test key, classical) and
`/lib/test/auth.hkey` (hybrid), with their `.pub` files; nothing
trusts them unless told to.

```
lethed -k root/lib/test/auth.hkey.pub /some/tree      # hybrid
lethe tcp!127.0.0.1!5640 /n/remote

lethed -p classical -k root/lib/test/auth.key.pub /some/tree
lethe -p classical tcp!127.0.0.1!5640 /n/remote         # or a Rust lethe

lethed -A /some/tree                                    # the clear mode
lethe -A tcp!10.0.2.2!5640 /n/remote                    # from the kernel under QEMU
```

The clear mode gives up confidentiality, integrity and identity:
everyone is `none` and gets the other bits of each file. Use it only on
trusted links (a loopback, a host and its own VM).

`#λ/clone` takes the Rust device's settings, and `proto`:

```
dial tcp!host!port
user glenda
key <seed hex>            32 bytes classical, 64 hybrid
cert <pgcert line>        optional
proto hybrid              optional: hybrid (the default) or classical
path /lib                 optional: the subtree, also the ticket's prefix
cache 1000                optional: the attribute lifetime in ms
```

or `noauth` in place of `user`, `key`, `cert` and `proto`.

## Tests

- `go test ./lethe`: the codec against the Rust byte vectors; the
  engine over a tree in memory, every test of the sealed mode run with
  both exchanges (requests, sealed file errors, certificates and every
  refusal, narrowing, renewal, the replay window, tampering, replicas,
  reserved ops), the clear mode, and the rules that keep the exchanges
  and the modes apart. `go test -fuzz FuzzHandle ./lethe` feeds the
  engine arbitrary frames (the Rust fuzz target's job).
- `go test ./libsec`: XChaCha20-Poly1305 against
  draft-irtf-cfrg-xchacha's vectors.
- `go test ./cmd/emu -run Lethe`: a `lethed` in the test and the emu as
  the client, on both wazero modes: hybrid and classical tickets and
  each refusal, reads, readahead, listings of 300 names, subtrees,
  errors, a server that goes away and comes back.
- `TestLetheRust` in the same file checks the two implementations
  against each other, in the clear and with classical tickets: the Go
  client reads from the Rust `lethed`, and the Rust client (on wasmtime
  and wasmi) from the Go `lethed`; a hybrid Go client gets `no hybrid
  key offered` from the Rust `lethed`, and a Rust client `hybrid
  authentication required` from a hybrid-only Go `lethed`. It runs when
  `PURG_RUST_BIN` names a directory with the Rust `lethed` and `emu`
  (`cargo build -p purg-emu --bins`) and `PURG_RUST_ROOT` the Rust root
  (`cargo xtask build-root`).
- `root/lib/test/sh/lethe.sh`: the device's and the command's errors
  with no server, in the emu and on the kernel.
- `go run ./tools/kernel -net`: the kernel reads a tree from a `lethed`
  on the host over QEMU user networking, in the clear and with hybrid
  tickets.
