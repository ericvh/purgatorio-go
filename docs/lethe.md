# Lethe

Lethe is a stateless protocol for reading static file trees. It was
designed for the Rust Purgatorio, and its design document is the
specification: `docs/lethe.md` in github.com/ericvh/purgatorio (the
first cut is its Section 14, the clear mode its Section 17). This file
says what the Go version implements and where it lives. The Go version
is wire-compatible with the Rust one: each end talks to the other.

In short: a server keeps no state. Each request names an absolute path
and carries everything needed to answer it; a reply carries the file's
qid, whose version a client caches by. A lost connection loses nothing,
and any replica can answer any request.

## What is implemented

| piece | where | status |
|---|---|---|
| the codec: frames, requests, replies, paths | `lethe/` | done |
| the server engine over a `lethe.Tree` | `lethe/server.go` | done |
| the clear mode: `Tclear`, `Rclear`, `lethed -A`, `lethe -A`, `noauth` in `#λ` | `lethe`, `lethed`, `#λ`, `lethe` | done |
| `lethed`, the host server | `cmd/lethed`, `cmd/emu/host/lethed.go` | done (clear mode) |
| `#λ`, the client device, in the emu and the kernel | `core/devlethe.go` | done (clear mode) |
| `lethe`, the attach command | `appl/cmd/lethe` | done |
| tickets, the `Tauth` exchange, sealed requests | | next |
| UDP, streaming, writes, `Tdigest`, server proofs | | not planned yet (later in the Rust design too) |

All of `stat`, `read` (with `cond`), `readdir` and `Twalk` work. The
transport is TCP.

## The pieces

- **`lethe`** is a portable package (no `os`, no `net`), so the kernel
  and the host share it. It holds the frame and message codec, the path
  rules, the server engine (`Server.Handle`: one frame in, one frame
  out) and the client's half of the clear mode. Decoding is strict, as
  in Rust: a frame must end exactly where its last field does. The tests
  in `lethe/msg_test.go` are the Rust codec's byte vectors.
- **`lethed`** is a binary of its own, `cmd/lethed`. Its host side
  (`cmd/emu/host/lethed.go`) is next to `#U`, whose host semantics it
  shares: Inferno error strings, owner names, qids from inodes, names
  that are case-exact on macOS and hidden entries left out. A file's
  qid version comes from its modification time, nanoseconds included,
  and its size. Symbolic links are followed only when they stay inside
  the root; a link out is answered as a name that does not exist.
  Resolved paths and sorted directory listings are kept for a second.
- **`#λ`** (`core/devlethe.go`) is in the device table of both the emu
  and the kernel. It dials through the platform's IP stack, the one
  `#I` uses. It keeps the Rust device's caches: an attribute cache that
  trusts entries and qids for `cache` milliseconds (1000 by default),
  and a data cache of 32 KiB blocks by path and version, at most 4 MiB
  a connection, least recently used out first. A sequential reader gets
  four blocks of readahead on one connection. Connections are pooled; a
  request whose connection fails is sent again on a new one, up to nine
  times with a growing pause, so a server that restarts costs a reader a
  delay and not an error. A connection lives as long as a file of it is
  held: the device's table holds connections weakly.
- **`lethe`** (`appl/cmd/lethe`) resolves the address with `/net/cs`,
  writes the settings to `#λ/clone`, and binds `#λn`.

## Using it

```
lethed -A -a 127.0.0.1:5640 /some/tree         # on the host
lethe -A tcp!127.0.0.1!5640 /n/remote           # in the emu
lethe -A tcp!10.0.2.2!5640 /n/remote            # in the kernel under QEMU
```

```
lethed [-v] [-A] [-a addr] [-u owner] [-t ticketkey] [-e lifetime] [-w window]
       [-k keyfile ...] [-U userdir] [-s signers] [-r revoked] root
lethe [-abc] [-A] [-k keyfile] [-t ms] [-u user] addr mtpt [path]
```

The options are the Rust ones. Until tickets land, `lethed` serves only
with `-A`, and `lethe` without `-A` fails when it authenticates. The
clear mode gives up confidentiality, integrity and identity: everyone is
`none` and gets the other bits of each file. Use it only on trusted
links (a loopback, a host and its own VM).

`#λ/clone` takes the Rust device's settings:

```
dial tcp!host!port
noauth
path /lib        (optional: the subtree)
cache 1000       (optional: the attribute lifetime in ms)
```

## Tests

- `go test ./lethe`: the codec against the Rust byte vectors, and the
  engine over a tree in memory (clear requests, refusals, walks).
- `go test ./cmd/emu -run Lethe`: a `lethed` in the test and the emu as
  the client, on both wazero modes: reads, readahead, listings of 300
  names, subtrees, errors, a server that goes away and comes back.
- `TestLetheRust` in the same file checks the two implementations
  against each other: the Go client reads from the Rust `lethed`, and
  the Rust client (on wasmtime and wasmi) from the Go `lethed`. It runs
  when `PURG_RUST_BIN` names a directory with the Rust `lethed` and
  `emu` (`cargo build -p purg-emu --bins`) and `PURG_RUST_ROOT` the
  Rust root (`cargo xtask build-root`).
- `root/lib/test/sh/lethe.sh`: the device's and the command's errors
  with no server, in the emu and on the kernel.
- `go run ./tools/kernel -net`: the kernel reads a tree from a `lethed`
  on the host over QEMU user networking.
