# Guest ABI

Guests are WebAssembly modules built with the standard Go toolchain for
`GOOS=wasip1 GOARCH=wasm`. Each one imports from two modules and nothing
else; the emu refuses a module that imports from any other.

1. `wasi_snapshot_preview1` (WASI p1), implemented natively by package `vm`
   over the Purgatorio name space. There is no host WASI layer: wazero's own
   WASI is not used. The Go standard library (`os`, `fmt`, `time`) works on
   top of it.
2. `purgatorio`, for what WASI p1 cannot express exactly: Inferno error
   strings, exit status strings, name spaces, Inferno open modes, full `Dir`
   and `Qid`, pipes, progs, Styx plumbing. Guests reach it through
   `sdk/sys` (`//go:wasmimport purgatorio ...`).

Non-standard functions never go in the WASI namespace.

## Version

`abi.Version` is written into each guest as the custom section
`purgatorio.abi` by `go run ./tools/build`. Bump it and update this file
whenever the ABI changes.

## Conventions

- Fds are one table, shared by both modules. Fds 0, 1 and 2 are the standard
  streams. Fd 3 is preopened as `/` (the prog's root) and fd 4 as `.`,
  a live handle that always resolves to the prog's current dot.
- WASI calls return an errno. The host also records the exact Inferno error
  string (`file does not exist`, `permission denied`, ...) in the prog's
  errstr, and `purgatorio.errstr` returns it. `abi.Errno` maps strings to
  errnos; unknown strings map to EIO and keep their text.
- `purgatorio` calls return -1 on error and set errstr. Pointers are `u32`
  offsets into the guest's exported `memory`; strings are `(ptr, len)` UTF-8.

## `purgatorio` module, v0.3

Strings are `(ptr, len)`; fds are the prog's fds. A call that returns a
`Dir` or a string writes nothing and returns the length it needs when the
buffer is too small, so the caller can retry with a bigger one.

| Call | Signature | Meaning |
|---|---|---|
| `errstr` | `(buf, blen) -> n` | Copy the prog's error string. |
| `werrstr` | `(ptr, len)` | Set the error string (at most 128 bytes). |
| `exits` | `(status, slen)` | End the prog; empty status is success. |
| `open` | `(path, plen, mode) -> fd` | Sys->open with Inferno modes. |
| `create` | `(path, plen, mode, perm) -> fd` | Sys->create; DMDIR in perm for a directory. |
| `remove` | `(path, plen) -> 0` | Sys->remove. |
| `chdir` | `(path, plen) -> 0` | Change the prog's dot. `sdk/sys.Chdir` also updates the Go runtime's cwd. |
| `stat` | `(path, plen, buf, blen) -> n` | Packed 9P Dir. |
| `fstat` | `(fd, buf, blen) -> n` | Packed 9P Dir. |
| `wstat` | `(path, plen, dir, dlen) -> 0` | Packed Dir; null fields unchanged. |
| `fwstat` | `(fd, dir, dlen) -> 0` | As wstat. |
| `dirread` | `(fd, buf, blen) -> n` | Whole packed Dirs; 0 at the end. |
| `fd2path` | `(fd, buf, blen) -> n` | The name the fd was opened by. |
| `dup` | `(old, new) -> fd` | new = -1 takes the lowest free fd. |
| `pipe` | `(fds_ptr) -> 0` | Two i32 fds on a new `#|` pipe. |
| `iounit` | `(fd) -> n` | Largest atomic I/O, 0 for none. |
| `seek` | `(fd, off i64, whence, res_ptr) -> 0` | New offset written at res_ptr as i64. |
| `bind` | `(new, nlen, old, olen, flags) -> id` | MREPL, MBEFORE, MAFTER, MCREATE. Returns the mount id (v0.3, below). |
| `unmount` | `(name, nlen, old, olen) -> 0` | nlen 0 removes everything on old. |
| `pctl` | `(flags, fds_ptr, n) -> pid` | fds are kept by NEWFD or closed by FORKFD. |
| `spawn_prog` | `(path, plen, argv, alen, flags, fdmap, n) -> pid` | argv is NUL-terminated strings; fdmap is n pairs of i32 (child, parent), used with NEWFD. |
| `exec` | `(path, plen, argv, alen) -> -1` | Replace the module in this prog; returns only on error. |
| `stream` | `(src, dst, bufsiz) -> n` | Sys->stream. |
| `sleep` | `(ms) -> 0` | Sys->sleep; a kill ends it. |
| `millisec` | `() -> ms` | Sys->millisec. |
| `detach` | `() -> pid` | Post this prog's wait message and carry on under a new pid, as a Limbo command whose init returns with spawned threads running. |
| `mount` | `(fd, afd, old, olen, flags, aname, alen) -> id` | Sys->mount; afd -1 unless fauth was used. Returns the mount id, as bind does. `MCACHE` (`0x10`) reads plain files ahead (v0.3, below). `MSRVAUTH` (`0x40000000`) requires the server's proof (below). |
| `fauth` | `(fd, aname, alen) -> fd` | Start Tauth on the conversation. |
| `fversion` | `(fd, msize, ver, vlen, buf, blen) -> n` | Agreed version written to buf. |
| `export` | `(fd, dir, dlen, flags) -> 0` | Serve dir on fd; flags 1 (EXPASYNC) returns at once; 2, 4 and 8 authenticate (below); 0x40000000 (EXPRDONLY) serves read-only. |
| `export_key` | `(fd, dir, dlen, flags, key, klen) -> 0` | `export` whose server proves itself with key, an authinfo or a seed (`export -k`). |
| `file2chan` | `(dir, dlen, name, nlen, handles) -> 0` | Two i32 channel handles: reads, writes. |
| `chan_new` | `(cap) -> h` | A kernel channel; cap 0 is synchronous. |
| `chan_close` | `(h) -> 0` | Close and drop the handle. |
| `chan_send` | `(h, buf, n, caps, ncaps) -> 0` | Send bytes and channel handles, which move. |
| `chan_recv` | `(h, buf, blen, caps, maxcaps, ncaps_ptr) -> n` | Receive; closed and empty is `i/o on hungup channel`. |
| `alt` | `(ops, nops, block) -> index` | Each op is six i32: handle, kind (0 recv, 1 send), buf, len, caps, ncaps. A receive rewrites len (-1 for closed) and ncaps. |
| `wait` | `(ops, nops, timeout_ms) -> index` | Pairs (kind, id): 0 fd readable, 1 fd writable, 2 channel receivable, 3 sendable; -1 on timeout. |
| `srv_iph2a` | `(name, nlen, buf, blen) -> n` | Inferno's Srv->iph2a, through the platform's IP stack: the addresses, separated by NUL bytes. ndb/cs uses it. |
| `srv_ipa2h` | `(addr, alen, buf, blen) -> n` | Srv->ipa2h: the names, separated by NUL bytes. |
| `srv_ipn2p` | `(net, nlen, svc, slen, buf, blen) -> n` | Srv->ipn2p: the port number, in decimal. |

### Mount ids (v0.3)

`bind` and `mount` return the mount id of the new mount table entry, as
sys-bind(2) says: a positive integer, unique among the entries of every
name space, the same sequence `ns` orders by. Before v0.3 they returned 0.
Both values are success to a caller that checks for -1, and the SDK has
always treated any result of 0 or more as success, so guests built for
v0.2 keep working. `sdk/sys.Bind` and `sdk/sys.Mount` return `(int,
error)`, as Limbo's `Sys->bind` and `Sys->mount` return an int.

### `MCACHE` mounts (v0.3)

`mount` with `MCACHE` (`0x10`, Inferno's value) says the tree is
disk-like: its plain files hold fixed bytes at each offset. On such a
mount a read of a plain file smaller than the iounit asks the server for
a whole iounit, and the bytes left over serve the reads that follow, so a
sequential reader sends one `Tread` per iounit instead of one per read.
Directories, append-only and exclusive files and auth files are never
read ahead. The block kept for a file is dropped by a write, a wstat, a
remove or a truncating open through the mount, by an open or stat that
shows a new qid version, and when the file is closed. Changes made by
other clients with no new qid version are not seen until then: that is
what `MCACHE` asks for. Without `MCACHE` each read is one `Tread`, as in
Inferno's `devmnt`; on a pipe, a stream or a control file a read sent
for more would take data the program did not ask for. Inferno's hosted
emu accepts `MCACHE` and ignores it. `bind` refuses it (`bad arg in
system call`), as it refuses any flag but the order and `MCREATE`.
Older guests never set the bit, so they are not affected.

### Authentication (v0.2)

pgauth's handshake runs in the kernel (docs/auth.md). No WASI call
changes.

- `export` flags: `EXPHYBRID` (2) takes version 4 proofs, `EXPCLASSICAL`
  (4) versions 1 to 3, both take either; with neither the export asks
  for no proof, as before. `EXPENCRYPT` (8) refuses version 1 and 2
  proofs and attaches without a proof (`encryption required`). An
  authenticating export reads its trust files in its own namespace and,
  trusting none, refuses `Tauth` and `Tattach` with `server trusts no
  keys`.
- An auth fd from `fauth`: a read of 96 bytes at offset 0 returns the
  server's classical offer followed by the client's ephemeral X25519
  key; a read of 2368 bytes, the hybrid offer, the client's X25519 key
  and the ML-KEM ciphertext. The kernel makes the ephemeral keys once
  per auth fd and keeps the secrets. A server that offers no hybrid key
  fails the 2368-byte read with `no hybrid key offered`; one that offers
  no session key fails the 96-byte read with `no session key offered`.
  Any other read is passed to the server (a bare challenge).
- A write at offset 0 of a version 3 or 4 proof over those bytes is sent
  in as many messages as it takes; after its reply the conversation is
  records each way.
- After it, a read at offset 64 (version 3) or 1248 (version 4) returns
  the server's proof, fetched once.
- `mount` with `MSRVAUTH` and an auth fd checks that proof against the
  mounter's `/lib/keys/servers`, `/lib/keys/serversigners` (or
  `/lib/keys/signers`) and `/lib/keys/revoked`: `server did not
  authenticate` or `server authentication failed`.

file2chan messages on the channels:

    read request:  off[8] count[4] fid[4], cap: reply channel
    write request: off[8] fid[4] data..., cap: reply channel
    reply:         0 then the data (a read) or count[4] (a write),
                   or 1 then an error string

A request with no reply channel says the client closed that fid, as
Inferno sends a nil reply channel.

## Loadable modules

The counterpart of Limbo's `load`. A module is a reactor built with
`-buildmode=c-shared` (tools/build builds `appl/lib/<name>` into
`root/bin/lib/<name>.wasm`) that imports sdk/module and registers its
functions in `init`. It is instantiated in the caller's prog, sharing its
fds and name space, and called with bytes in and bytes out.

| Call | Signature | Meaning |
|---|---|---|
| `load` | `(path, plen) -> h` | Load a module; one without sdk/module's exports is `not a loadable module`. |
| `call` | `(h, name, nlen, in, ilen, out, olen) -> n` | Call a function; when n > olen nothing is written and `call_result` fetches it. An error from the function is the errstr. |
| `call_result` | `(out, olen) -> n` | The result a too-short `call` left. |
| `unload` | `(h) -> 0` | Release the module. |

A module exports `purgatorio_alloc(n) -> ptr` and
`purgatorio_call(name, nlen, in, ilen) -> ptr<<32|len`, whose result's first
byte is 0 before the result or 1 before an error string.

A child's exit message goes to the parent's `/prog/<pid>/wait` when that
file is open at spawn time, as in Inferno: `pid "name":status`.

## Prog domains: `#pdomain`

Not Inferno: the Rust sibling's extension, which the limits below use.
`#p` with the attach spec `domain` is a view of the prog table limited to
one prog domain. No host call is added and the ABI version is unchanged.

- Attaching `#pdomain` founds a domain for the attaching prog's process
  group: every prog then in that group joins it, and every prog started
  from a member afterwards is a member too, whatever its own process
  group. A prog never leaves a domain.
- The view lists only members. Any other pid does not exist in it:
  walking to it, and so `ls`, `kill` or a write to its `ctl`, fails with
  `file does not exist`. `killgrp` written to a member's `ctl` kills the
  members of that group, not the rest of it.
- Domains nest. A member that attaches `#pdomain` founds a domain inside
  its own; the outer view still shows its members.
- `#pdomain=N` is the view of domain `N` (the process group that founded
  it) without joining it.
- `#p` with no spec is the whole table. Any other spec fails with `bad
  arg in system call`.
- `NODEVS` forbids `#p` with any spec, so a sandbox that binds
  `#pdomain` on `/prog` and then gives up devices cannot get the whole
  table back.
- Each domain has its own `#s` specs. `#s` attached with a spec finds the
  directory an earlier attach with that spec made only if both progs'
  innermost domain is the same (no domain counts as one). A directory
  already bound into a name space stays usable through that binding.

## Dropping single devices: `nodev`

Not Inferno: the Rust sibling's extension, which `mcpd` uses. Writing
`nodev <chars>` to `/prog/<pid>/ctl` forbids that prog's name space to
attach the devices named by `<chars>` (their device characters, such as
`¤` or `¤s`), whether or not `NODEVS` is set. Copies of the name space
made afterwards (`FORKNS`, `NEWNS`) keep the list, and there is no way
to take a device back. An attach of a dropped device fails with
`mount/attach disallowed`. It is how `mcpd` keeps a session from `#¤`,
which `NODEVS` allows. It is a `ctl` message, so the ABI version is
unchanged.

## Read-only `export`

`export(fd, dir, dlen, flags)` also takes `EXPRDONLY` (`0x40000000`, the
Rust sibling's `RDONLY`). A read-only export serves walks, stats and
reads, and answers `file system read only` (exportfs's error) to an open
for writing, with `OTRUNC` or with `ORCLOSE`, and to create, write,
remove and wstat. The `export` command sets it with `-r`. This is how
`mcpd` shows `/bin` and `/lib` to a session without letting it change
them. A new flag bit, not a new call, so the ABI version is unchanged.

## Memory and prog limits (K4)

A prog's wasm memory can be limited, and so can a domain's. Inferno has
no direct analogue (its pools are system-wide). The limits are `ctl`
messages and a file, not host calls, and what a guest sees when one is
reached is ordinary wasm behavior, so the ABI version is unchanged.

- Writing `memlimit N` to `/prog/<pid>/ctl` limits the wasm memory the
  prog holds to `N` bytes: the linear memories of its command and of the
  modules it has loaded, and their tables at 8 bytes an element. `N` may
  end in `k`, `m` or `g` (2^10, 2^20, 2^30). The limit applies to growth
  from then on, and every prog the prog starts afterwards gets the same
  limit for itself.
- `domain memlimit N` limits the memory all members of the prog's
  innermost domain hold between them, and `domain proglimit N` the
  number of members. A spawn that would pass the prog limit fails with
  `no free procs`; spawns into a domain with a prog limit are admitted
  one at a time. A prog in no domain gets `bad arg in system call`, as
  does a malformed message. A prog is held to its own limit and to the
  limit of every domain it is in.
- A limit can only be lowered. Writing a higher one fails with
  `permission denied` and changes nothing.
- `/prog/<pid>/mem` reads `<bytes held> <limit>`, with `-` for no limit.

Past a limit, `memory.grow` returns -1. The Go runtime then fails with
`fatal error: out of memory` on the prog's stderr and exit status `2`.
A module (a command, or one `load` loads) whose initial memory and
tables are already past a limit fails to instantiate with `out of
memory: virtual memory`. vm/wazero enforces the limits in both wazero
modes, and so in the kernel, through wazero's memory allocator hook: each
instance's linear memory is a buffer whose growth `vm.Meter` charges to
the prog.

Not counted: table growth (wazero has no hook for it; Go guests never
grow their table), the host's compiled code and goroutine stacks, and
what the prog's files hold in the kernel (memfs, `#e`). A domain's prog
limit bounds the first two.

## WASI p1 status

All of WASI p1 is implemented over the name space: paths resolve through
the prog's binds and mounts, and every error records the Inferno string.
fd 3 is the `/` preopen and fd 4 the `.` preopen, which always means the
prog's current directory. The Go runtime makes paths absolute itself from
`PWD`, which the host sets to the prog's dot. Renames work within one
directory (wstat of the name) and are EXDEV across directories. Links,
symlinks and `proc_raise` are ENOTSUP; the socket calls are ENOTSOCK.
