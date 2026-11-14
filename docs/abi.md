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

| Call | Signature | Meaning |
|---|---|---|
| `errstr` | `(buf, blen i32) -> n i32` | Copy the prog's error string into `buf`. |
| `werrstr` | `(ptr, len i32)` | Set the prog's error string (at most 128 bytes). |
| `exits` | `(status, slen i32)` | End the prog with an Inferno status; empty means success. |

The rest of the planned surface (`open`, `create`, `stat`, `bind`, `mount`,
`pctl`, `spawn_prog`, channels, `file2chan`, `load`) is listed in
`docs/plan.md` Section 6 and lands milestone by milestone.

## WASI p1 status

Every WASI p1 function is registered so any guest links. Implemented today:
`args_*`, `environ_*`, `clock_res_get`, `clock_time_get`, `fd_read` (fd 0),
`fd_write` (fds 1, 2), `fd_close`, `fd_fdstat_get`, `fd_fdstat_set_flags`,
`fd_prestat_get` (no preopens yet), `poll_oneoff` (clocks; stdio is always
ready), `proc_exit`, `sched_yield`, `random_get`. The rest return ENOSYS.
