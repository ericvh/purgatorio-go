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
  streams. Fd 3 will be preopened as `/` (the prog's root) and fd 4 as `.`,
  a live handle that always resolves to the prog's current dot.
- WASI calls return an errno. The host also records the exact Inferno error
  string (`file does not exist`, `permission denied`, ...) in the prog's
  errstr, and `purgatorio.errstr` returns it. `abi.Errno` maps strings to
  errnos; unknown strings map to EIO and keep their text.
- `purgatorio` calls return -1 on error and set errstr. Pointers are `u32`
  offsets into the guest's exported `memory`; strings are `(ptr, len)` UTF-8.

## `purgatorio` module, v0.1

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
| `bind` | `(new, nlen, old, olen, flags) -> 0` | MREPL, MBEFORE, MAFTER, MCREATE. |
| `unmount` | `(name, nlen, old, olen) -> 0` | nlen 0 removes everything on old. |
| `pctl` | `(flags, fds_ptr, n) -> pid` | fds are kept by NEWFD or closed by FORKFD. |
| `spawn_prog` | `(path, plen, argv, alen, flags, fdmap, n) -> pid` | argv is NUL-terminated strings; fdmap is n pairs of i32 (child, parent), used with NEWFD. |
| `exec` | `(path, plen, argv, alen) -> -1` | Replace the module in this prog; returns only on error. |
| `stream` | `(src, dst, bufsiz) -> n` | Sys->stream. |
| `sleep` | `(ms) -> 0` | Sys->sleep; a kill ends it. |
| `millisec` | `() -> ms` | Sys->millisec. |

| `detach` | `() -> pid` | Post this prog's wait message and carry on under a new pid, as a Limbo command whose init returns with spawned threads running. |
| `mount` | `(fd, afd, old, olen, flags, aname, alen) -> 0` | Sys->mount; afd -1 unless fauth was used. |
| `fauth` | `(fd, aname, alen) -> fd` | Start Tauth on the conversation. |
| `fversion` | `(fd, msize, ver, vlen, buf, blen) -> n` | Agreed version written to buf. |
| `export` | `(fd, dir, dlen, flags) -> 0` | Serve dir on fd; flags 1 (EXPASYNC) returns at once. |
| `file2chan` | `(dir, dlen, name, nlen, handles) -> 0` | Two i32 channel handles: reads, writes. |
| `chan_new` | `(cap) -> h` | A kernel channel; cap 0 is synchronous. |
| `chan_close` | `(h) -> 0` | Close and drop the handle. |
| `chan_send` | `(h, buf, n, caps, ncaps) -> 0` | Send bytes and channel handles, which move. |
| `chan_recv` | `(h, buf, blen, caps, maxcaps, ncaps_ptr) -> n` | Receive; closed and empty is `i/o on hungup channel`. |
| `alt` | `(ops, nops, block) -> index` | Each op is six i32: handle, kind (0 recv, 1 send), buf, len, caps, ncaps. A receive rewrites len (-1 for closed) and ncaps. |
| `wait` | `(ops, nops, timeout_ms) -> index` | Pairs (kind, id): 0 fd readable, 1 fd writable, 2 channel receivable, 3 sendable; -1 on timeout. |

file2chan messages on the channels:

    read request:  off[8] count[4] fid[4], cap: reply channel
    write request: off[8] fid[4] data..., cap: reply channel
    reply:         0 then the data (a read) or count[4] (a write),
                   or 1 then an error string

A request with no reply channel says the client closed that fid, as
Inferno sends a nil reply channel. `load` comes in M5.

A child's exit message goes to the parent's `/prog/<pid>/wait` when that
file is open at spawn time, as in Inferno: `pid "name":status`.

## WASI p1 status

All of WASI p1 is implemented over the name space: paths resolve through
the prog's binds and mounts, and every error records the Inferno string.
fd 3 is the `/` preopen and fd 4 the `.` preopen, which always means the
prog's current directory. The Go runtime makes paths absolute itself from
`PWD`, which the host sets to the prog's dot. Renames work within one
directory (wstat of the name) and are EXDEV across directories. Links,
symlinks and `proc_raise` are ENOTSUP; the socket calls are ENOTSOCK.
