# Purgatorio-Go: Design and Phased Roadmap

Purgatorio-Go is a clean-sheet rebuild of Inferno in Go. The kernel and the
hosted emulator are written in Go. Applications are Go compiled to
WebAssembly (`GOOS=wasip1 GOARCH=wasm`), which replaces Limbo and the Dis VM.

It has the same purpose and the same model as Purgatorio
(`github.com/ericvh/purgatorio`), the Rust version: everything is a file,
per-prog name spaces built with `bind` and `mount`, and one protocol, Styx
(9P2000). This document follows that project's plan section by section and
says where Go changes the design. The build order is the same:

1. Hosted emu on Linux and macOS.
2. Native kernel for aarch64, run under QEMU `virt`.
3. Graphics (draw, Tk, wm) last.

Standing decision carried over: every milestone is validated against
aarch64, not only the kernel phase. Each milestone's acceptance criteria
include native runs on Linux aarch64 and macOS arm64, the portable packages
building with no host OS, and (once it exists) a bare-metal aarch64 probe
running the milestone's portable subset in QEMU.

---

## 1. What we are rebuilding

| Inferno area | Location | Purgatorio-Go equivalent |
|---|---|---|
| Portable kernel: `Chan`, `Pgrp`, `Fgrp`, `Egrp`, qio, devices | `emu/port/*.c` | package `core` (portable) |
| Host glue: kprocs, host fs, sockets, tty | `emu/Linux`, `kproc-pthreads.c`, `devfs-posix.c` | `cmd/emu`, `cmd/emu/host` |
| Native kernels | `os/*` | `kernel/` (Phase K) |
| Dis VM: interpreter, JIT, GC, loader | `libinterp` | wazero, behind package `vm` |
| Builtin modules (`sys.m`, `draw.m`, ...) | `module/*.m` | WASI p1 + the `purgatorio` import module, plus `sdk/` |
| Styx (9P2000) | `include/styx.h`, `appl/lib/styx*.b` | package `styx` (portable) |
| Applications | `appl/cmd/*.b`, `appl/lib/*.b` | `appl/cmd/<name>` Go programs built to `.wasm` |
| Graphics | `libdraw`, `libmemdraw`, `libtk`, `devdraw.c` | Phase G |

Inferno concepts we keep: everything is a file; per-process name spaces
edited with `bind`, `mount` and `pctl`; one protocol; `file2chan`;
lightweight progs with CSP channels; dynamic modules; portable binaries (the
same `.wasm` runs on every host and on the kernel).

## 2. Design principles

1. **A portable core with a thin platform layer.** Name space, channel,
   device and Styx logic lives in portable packages (`abi`, `styx`,
   `platform`, `core`, `vm`) that never touch the host OS. The hosted emu is
   "the kernel on the hosted platform", as `emu/port` relates to `os/port`.
2. **Inferno semantics, not POSIX.** File descriptors, error strings, `Dir`
   and `Qid`, pctl flags and command behavior match Inferno. Inferno's man
   pages are the spec.
3. **Abstract the WASM engine.** Host functions are written once in package
   `vm` against a small `Memory` interface. Engines are adapters (`vm/wazero`).
4. **Test against real Inferno.** The Styx codec and file servers are checked
   against Inferno's emu, plan9port and the Linux v9fs client.
5. **Graphics stays out of the core.**
6. **aarch64 is first-class.** Decode wire formats with `encoding/binary`
   little-endian reads, never `unsafe` casts. Never assume a 4 KiB page
   (macOS arm64 is 16 KiB). Run the race detector on arm64 runners, where
   weak memory ordering shows bugs x86 hides.

### What Go changes

| Topic | Rust Purgatorio | Purgatorio-Go |
|---|---|---|
| Portable core | `no_std` + `alloc` crates built for `aarch64-unknown-none` | Packages that import no `os`, `net`, `syscall`, `os/*`, `path/filepath` and no third-party code. Enforced by `internal/portcheck` and by building them for `wasip1` (a target with no POSIX host) |
| Host-specific code | Only in `purg-emu/src/host/` | Only in `cmd/emu/host/`: no build constraints or `runtime.GOOS` elsewhere |
| Kprocs, locks, Rendez | A platform facade over threads or kernel threads | Goroutines, `sync.Mutex`, `sync.Cond`. The Go runtime is the scheduler on every target, hosted and bare metal, so the `platform` interface only covers clock, entropy, console and host identity |
| WASM engine | wasmtime (hosted), wasmi (kernel) | wazero, pure Go, on both. Its compiler (amd64, arm64) and interpreter modes are the two CI configurations, as wasmtime and wasmi are in Rust |
| Thread-per-prog | One OS thread per prog, with explicit stack sizes on macOS | One goroutine per prog. No stack sizing, no thread limits |
| Concurrency checking | `loom` models | `go test -race` on all three runners, plus stress tests. No model checker; keep lock-free code rare |
| Guest language | Rust for `wasm32-wasip1` | Go for `wasip1/wasm`, with `//go:wasmimport` for the `purgatorio` module and `//go:wasmexport` (Go 1.24+) for reactor modules |
| Guest size | Tens of KiB per command | About 2 MiB per command with the standard toolchain (the Go runtime and GC). See D-G1 |
| Bare-metal kernel | `#![no_std]` Rust | A Go runtime on bare metal. See D-G2 |
| Reproducible guests | Pinned rustflags, stripped symbols | `-trimpath -buildvcs=false -ldflags='-s -w -buildid='` and a pinned toolchain in `go.mod` |

## 3. Repository layout

One Go module, `github.com/ericvh/purgatorio-go`:

```
abi/            portable: ABI version, open/bind/pctl flags, Inferno error strings, errno map
styx/           portable: 9P2000 codec (all messages, Qid, Dir); later the server framework and client
platform/       portable: the Platform interface (clock, entropy, console, user, sysname)
core/           portable: Chan, Dev, devtab, Pgrp/mount table, Fgrp, Egrp, qio, Prog, devices
vm/             portable: WASI p1 and purgatorio host functions over a Memory interface
vm/wazero/      the wazero engine adapter
cmd/emu/        the hosted emu binary; cmd/emu/host/ is the only host-specific code
kernel/         Phase K: the bare-metal aarch64 kernel
sdk/            guest SDK: sys (purgatorio imports), later styx, kchan, dial, arg, bufio
appl/cmd/<n>/   commands -> root/bin/<n>.wasm
appl/test/<n>/  conformance guests -> root/bin/test/<n>.wasm
root/           the tree emu serves as #U
tools/build/    builds every guest (go run ./tools/build)
internal/portcheck/  enforces the portable-package rule
docs/
```

## 4. The emu core: progs, scheduler, channels

### 4.1 Objects

The objects mirror `emu/port/dat.h`, as in the Rust plan: `Chan` (reference
counted, closed through `Dev.Close` on last release), the `Dev` interface
(`Attach`, `Walk`, `Stat`, `Open`, `Create`, `Read`, `Write`, `Remove`,
`Wstat`, `Close`), `Pgrp` (mount table with union lists), `Fgrp`, `Egrp`,
`Prog`, `Queue` (qio) and `Rendez`.

### 4.2 Execution model

Each prog runs on its own goroutine. Blocking host calls block that
goroutine on a `Rendez` (a `sync.Cond` with an interrupt flag, so `kill`
and `ctl killgrp` work). This is the Rust plan's Model A, but goroutines are
cheap enough that thousands of progs cost nothing special, and the same code
runs on the bare-metal kernel because the kernel also has a Go runtime.

Killing a prog that is running guest code uses wazero's
`WithCloseOnContextDone`: each prog runs under a context, and `kill`
cancels it.

### 4.3 Channels

- **Inside one guest**, Go's own goroutines and channels work under wasip1,
  but the Go wasip1 runtime runs all of a guest's goroutines on one thread,
  and a host call that blocks stops all of them. Guests that need to wait on
  several things at once use `purgatorio.wait` (a poll over fds and kernel
  channels). To `/prog`, a guest is one prog, a known divergence from Dis.
- **Between progs**, channels are kernel objects carrying bytes plus
  transferred handles (`chan_new`, `chan_send`, `chan_recv`, `alt`), as in
  the Rust plan. `file2chan` and `spawn_prog` use them.

## 5. Name space, devices, Styx

Unchanged from the Rust plan (`docs/plan.md` Section 5 there): `namec`
follows `chan.c`, with lexical `..` cleanup (`core.Cleanname`); `bind`,
`mount` and `unmount` edit the `Pgrp`; `NODEVS` restricts device paths.
The device table and its phasing are the same: `#/`, `#c`, `#U`, `#e`, `#|`,
`#d`, `#p`, `#s`, `#M`, `#C`, `#I`, then TLS, cap, and the graphics devices
last.

Styx is 9P2000 only, version string `"9P2000"`. Package `styx` has the full
codec today; the server framework (used by `export` in the kernel and by
file servers in guests) and the client (used by `#M`) come in M3. The same
package is importable from guests, so a Go file server compiles to wasm
unchanged.

### 5.1 Debugging files of /prog/n

`#p` has all of Inferno's files. A guest is wasm, not Dis, so the
debugging files mean the closest thing:

- The engine reports every host call that may block (everything but
  the clock, random, args and environment calls). At that point it hands
  the prog the guest's call stack and its linear memory. While a prog is
  in such a call its state is `release`, as an Inferno prog in a system
  call is.
- `stack` lists the stack of the last reported call, top first, in
  Inferno's six fields: the frame number counted from the bottom (Inferno
  gives the frame pointer), the wasm function index (the pc), the code
  section offset of the frame's pc when the engine knows it (otherwise
  0), 0, 1 (compiled), and the module path. A host function's frame has
  the path `$` plus its module, as Inferno's built-in modules show
  `$Sys`. A prog running guest code is in state `ready` and its stack
  read fails with `thread must be stopped`, as in Inferno.
- `text` is the path of the module the prog runs. In Inferno it is
  unimplemented with mode 0000; here it has mode 0400.
- `exception` is the last trap, in Inferno's form `pc module error`: the
  function index of the trapping frame, the module name and wazero's trap
  message. A trap in a module loaded with `purgatorio.load` leaves the
  prog running with its exception set; a trap in the prog's own module
  ends it.
- `heap` opens read-write only, as in Inferno. Queries `addr.Fn` read the
  guest's linear memory with formats `W`, `B`, `V`, `R` and `P` (a 32-bit
  address, `nil` for 0). The formats that follow Dis heap objects (`L`,
  `A`, `C`, `M`, `c`, `I`) are refused with `bad process or channel
  control request`. Before any query, a read gives the memory itself: its
  size in bytes, its maximum in bytes (0 when the module sets none) and the
  wasm page size, then `memory`.
- `dbgctl` opens read-write only and is refused, like `heap`, for a group
  made `private`. A debugger holds a prog only at a reported host call,
  not between wasm instructions. `stop`, `unstop`, `start`, `cont` and
  `maim` work as in Inferno: `cont` attaches the debugger, runs the prog
  and replies with an error `child: stopped`, `child: exited` or `child:
  broken: ...`; between `cont`s an attached prog is held in state
  `debug`. Without breakpoints a `cont` runs to the end or to a `stop`.
  `step`, `toret` and `bpt` need instruction-level control the engine does
  not give and are refused with `bad process or channel control request`.
  Reads give `new pid` when the prog spawns and `exited` or `broken:
  error` when it ends; the blocking-state updates (`send`, `recv`, `alt`,
  `run`) are not produced.
- `ctl` takes Inferno's messages with Inferno's errors (`unknown control
  message "..."`, `wrong #args in control message "..."`). `killgrp`
  spares the writer. `restricted` is recorded and inherited by children;
  `exceptions` checks its argument and the leader but there is no
  exception propagation between guests.

### 5.2 Profiling, #P

`#P` has Inferno's layout and messages: `/prof/ctl` (`module`, `start`,
`startcp pid`, `stop`, `end`, `interval ms`), and per profiled module a
directory named by its id in eight hex digits with `name`, `path`,
`histogram` and `pctl`. Open modes and errors follow devprof.c.

Inferno samples the pc of the running Dis prog. The host cannot read a
running wasm guest's pc, so a module that starts while profiling is on,
and is wanted (it is in the `module` list, by name or path, or the list
is empty), is compiled a second time with a hook on every function entry
and exit. The hook keeps the function the guest is in, and the sampler
counts it every interval for each such guest that is not blocked in a
host call. Differences from Inferno:

- A pc is a wasm function index (imports first), not a Dis instruction,
  and each histogram pair is a function and its count.
- Only modules started after `start` are profiled; a running guest
  cannot be instrumented.
- `startcp pid` counts calls per function (not instructions) for
  modules started afterwards in that prog or its descendants.
- `startmp` (memory profiling) is refused with `bad arg in system call`:
  a guest's heap is its own.
- `name` is the module name used in wait messages (`md5sum`), as there
  is no Limbo module name.

`prof` (appl/cmd/prof) is Inferno's prof(1). A Go guest is linked
without a wasm name section, but its pclntab is in its data; prof finds
it and names each function index with its Go function, file and first
line. There are no per-line counts, so the line profile (`-l`, the
default) prints the function profile.
### 5.3 Serial ports, #t

The hosted emu's `#t` follows Inferno's deveia-posix.c and deveia-bsd.c
over termios: `eiaN`, `eiaNctl` and `eiaNstatus` per port, mode 0660,
owned by eve. The ctl letters are Inferno's (`b`, `l`, `s`, `p`, `x`,
`f`, `k`, `h`, `d`, `r`, `m`, `break`); others are ignored as there.
Settings outlive the last close. The status line is Inferno's: `opens n
ferr n oerr n baud n`. A read waits for input and a kill or interrupt
ends it.

The ports are the host devices given with `emu -eia dev,dev`, else
those in `$EMU_EIA`, else Inferno's: `/dev/ttyS0` to `/dev/ttyS7` on
Linux and the `/dev/cu.*` callout devices on macOS (Inferno asks IOKit
for the same list). As in Inferno, the list stops at the first device
that does not exist, and with none `#t` cannot be attached (`no free
devices`). The emu does not bind `#t`; as eia(3) says, `bind -a '#t'
/dev`.

## 6. WASM runtime and application ABI

### 6.1 Runtime

wazero (`github.com/tetratelabs/wazero`) is a pure-Go runtime with no cgo:

- **Compiler** mode generates native code on amd64 and arm64.
- **Interpreter** mode runs anywhere, with no executable memory.

The hosted emu uses the compiler by default and `emu -vm interpreter` on
request. Tests run every guest on both and require identical output, which
keeps the ABI independent of one execution mode. The kernel starts on the
interpreter (wazero's compiler needs `mmap` with execute permission, which a
bare-metal runtime must provide).

wazero's own WASI implementation is not used: package `vm` implements WASI
p1 natively over the Purgatorio name space, so the hosted emu and the
kernel share it.

### 6.2 ABI

WASI p1 where it expresses Inferno semantics exactly, plus the `purgatorio`
import module for the rest. See `docs/abi.md`. The planned `purgatorio`
surface is the same as the Rust project's:

```
open create stat fstat wstat fwstat dirread fd2path chdir dup pipe iounit
bind mount unmount export fversion fauth file2chan stream
errstr werrstr exits pctl spawn_prog
chan_new chan_close chan_send chan_recv alt wait
load call
```

### 6.3 Module loading

1. **Commands** are WASI command modules in `/bin`, one `_start` each.
2. **Libraries** (`bufio`, `arg`, `styxservers`, ...) are Go packages
   linked into each guest.
3. **Builtin modules**: `$Sys` is WASI plus `purgatorio`; `$Math` is Go's
   `math`; `$Keyring` is Go crypto in the guest; `$Draw` and `$Tk` are guest
   libraries over `/dev/draw` (Phase G).
4. **Runtime-loaded modules** (sh's `std`, `expr`, ...) are reactor modules
   built with `-buildmode=c-shared` and `//go:wasmexport`, loaded with
   `purgatorio.load` and called with byte-serialized arguments (M5).

### 6.4 Resource limits

Per-prog memory limits use wazero's memory limit pages; kill uses context
cancellation. Guest pointers are always bounds-checked through `Memory`.

## 7. Guest SDK and build pipeline

`sdk/` is imported by guests. `sdk/sys` has the `purgatorio` imports today
(`Errstr`, `Werrstr`, `Exits`). Planned: `Fd` with Inferno open modes,
`Dir`/`Qid`, `Bind`, `Mount`, `Pctl`, `SpawnProg`, kernel channels,
`file2chan`, a Styx server helper, `dial`/`announce`/`listen` over `/net`,
and ports of `arg`, `bufio`, `readdir`, `filepat`.

`go run ./tools/build` builds `appl/cmd/*` and `appl/test/*` with
`GOOS=wasip1 GOARCH=wasm`, reproducible flags, and appends the
`purgatorio.abi` custom section. Guests must be byte-identical on every
build host; CI compares hashes from all three runners.

## 8. Which programs to port, and in what order

The same waves as the Rust plan:

| Wave | Programs |
|---|---|
| W0 (M1) | `emuinit`, `echo`, `cat` |
| W1 (M2) | `sh` subset, `ls`, `pwd`, `mkdir`, `rm`, `cp`, `mv`, `date`, `sleep`, `wc`, `env`, `ps`, `kill` |
| W2 (M3) | `bind`, `mount`, `unmount`, `ns`, `memfs`, `ramfile`, `grep`, `tail`, `cmp`, `diff`, `du`, `tee`, `xd`, `md5sum` |
| W3 (M4) | `styxlisten`, `listen`, `dial`, `export`, `import`, `cs`, `os`, `netstat`, `mntgen` |
| W4 (M5) | full `sh` with loadable builtins, `tar`, `gzip`, auth tools |
| W5 (G) | `wm/wm`, `wm/sh`, `wm/edit` |

## 9. Testing

1. Unit tests (`go test ./...`) for every package; native Go fuzzing of the
   Styx decoder (`FuzzUnmarshal`).
2. Golden name space tests from `man/2/sys-bind` and real-emu behavior.
3. Styx conformance against plan9port `9p`, Linux v9fs and real Inferno emu.
4. App integration tests run guests in emu on both wazero modes and compare
   output and exit status with goldens.
5. ABI conformance guests (`appl/test/abi`) for every import, including
   error paths and bad pointers.
6. `go test -race` on all runners; stress tests on aarch64.

CI matrix: Linux x86_64 (`ubuntu-24.04`), Linux aarch64
(`ubuntu-24.04-arm`) and macOS arm64 (`macos-latest`), each running gofmt, vet,
the wasip1 build of the portable packages, the guest build, and the race
tests. A final job checks that guest hashes match across runners.

## 10. Sharing code between the emu and the kernel

The portable packages are shared unchanged. The kernel supplies its own
`platform.Platform` (generic timer, PL011, virtio-rng) and its own devices
(`#t`, virtio-blk, virtio-net). Networking: `#I` has a front end in `core`
with a `NetStack` interface; hosted uses `net`, the kernel a Go TCP/IP stack
(gVisor's `netstack` is the candidate) over virtio-net.

Hosted platform notes from the Rust plan still apply where they are about
semantics rather than Rust: on macOS's case-insensitive APFS, `#U` walk must
check exact name bytes; ignore `.DS_Store` and `._*`; qids come from
(`st_dev`, `st_ino`) and mtime.

## 11. Milestones and acceptance criteria

Every milestone also passes the aarch64 gate: hosted tests on the three
runners in both wazero modes, the portable packages building for wasip1 and
passing `portcheck`, the race tests on Linux aarch64, and (from M1 on, once
D-G2 is settled) the probe running the milestone's portable subset.

### M0: Bootstrap (done)

Module layout, `abi`, the full `styx` codec with round-trip, truncation,
known-bytes and fuzz tests, `core.Cleanname`, `vm` with the WASI p1 subset
Go's runtime needs and `purgatorio.{errstr,werrstr,exits}`, the wazero
adapter, `emu` running `root/bin/<cmd>.wasm`, the `echo` and `test/abi`
guests, reproducible guest builds, the portability check, and CI.

### M1: Hello from WASM in a name space

- `core`: `Chan`, `Dev`, devtab, `namec` with `#x` paths and a root bind
  (`/` = `#U`, `#c` at `/dev`); `Fgrp` with fds 0-2 on `/dev/cons`; `Prog`.
- Devices: `#/`, `#c` (`cons`, `null`, `user`, `sysname`, `time`, `pid`,
  `drivers`), `#U` read-only.
- `vm`: WASI p1 over the name space: preopens `/` and live `.`,
  `path_open` read-only, `fd_seek`, `fd_filestat_get`, `path_filestat_get`,
  `fd_readdir`; `purgatorio.{stat,fstat,dirread}`.
- emu loads `/bin/<cmd>.wasm` through the name space; `emuinit`, `cat`.
- **Accept:** `emu echo hello world` prints `hello world`;
  `emu cat /dev/sysname /dev/user`; `cat /nonexistent` prints
  `cat: /nonexistent: file does not exist` and exits non-zero;
  `echo hi | emu cat`; a bad pointer to `fd_read` gives EFAULT and errstr
  `bad address` without a host crash; `cat` uses plain `os.Open` and gets
  its error text from `sys.Errstr`; on macOS, `cat /LIB/...` fails while
  `cat /lib/...` works.

### M2: Usable shell, processes, env, pipes

`spawn_prog` with pctl flags, goroutine-per-prog, `Rendez`, wait status;
`#e`, `#|`, `#U` read/write; `bind`/`unmount` with unions and `MCREATE`;
the W1 apps including the `sh` subset; the Inferno boot sequence (`emuinit`
then `sh -l`). **Accept:** as the Rust plan's M2 (pipelines of three or
more stages, `/env`, unions, background jobs), on all runners.

### M3: Styx everywhere

`#M` client mux, `mount`, `export`, `#s` file2chan, kernel channels, `#p`,
`#d`, the rest of `#c`; the Styx server framework in `styx` and the SDK;
the W2 apps. **Accept:** memfs, `ns` output in Inferno's format, `kill` of a
prog blocked in read, a pipe loopback `export`/`mount` passing the golden
name space tests.

### M4: Networking and interop

`#I` on host sockets, `cs`, dial helpers, `styxlisten`, `import`, `os`
(`#C`). **Accept:** a Purgatorio-Go export is mountable from real Inferno,
plan9port and Linux v9fs; it mounts a real Inferno export.

### M5: Full shell, module loading, auth

Reactor modules and `load`/`call`, sh loadable builtins, full WASI p1 for
third-party binaries, `fauth` and the chosen auth scheme, TLS.
**Accept:** the `sh-std` and `sh-expr` man page examples run unchanged; an
unmodified Go or Rust WASI p1 CLI runs and sees the name space; an
authenticated mount between two emus works.

### Phase K: native aarch64 kernel under QEMU

K1 boot (DTB, PL011, GIC, timer, MMU, goroutines on bare metal), K2 the
portable core on metal booting to `sh` from an initrd, K3 devices
(virtio-blk and a file system, virtio-net, preemption), K4 SMP and
performance. Acceptance mirrors the Rust plan's Phase K.

### Phase G: Graphics

`#i` draw with a Go memdraw, fonts and a draw SDK, a guest-side toolkit,
`wm`. Acceptance mirrors the Rust plan's Phase G.

The emu shows the screen in a host window (`emu -g WxH`) or a browser
(`-web`, `-w addr`). The window backends are in `cmd/emu/host`, in pure
Go so cross builds need no C toolchain: an X11 client over
`jezek/xgb` on Linux (Wayland desktops run it under XWayland, since a
native Wayland client would need XKB's keymap compiler), and Cocoa
through `ebitengine/purego` on macOS, with AppKit on the main thread
and the emu on other goroutines. Input follows Inferno's emu: buttons
1 to 3, the wheel as buttons 4 and 5, keyboard(6) codes for the named
keys, Option- and Command-click as buttons 2 and 3 on the Mac. A
resized window resizes the screen as the Rust sibling does
(`platform.Resizer`): devdraw makes the screen image the new size,
keeping its pixels and windows (`memdraw.Image.Resize`), and gives
each client a refresh record for image 0 with the new rectangle; wm
waits on its refresh file and takes the new size. The window is in
the display's pixels on X11 and in points on macOS (each screen pixel
is 2×2 device pixels on a Retina display). Closing the window ends
the emu with status 0, as Inferno's does.

## 12. Open decisions

Carried over unchanged from the Rust plan: D1 license (Apache-2.0,
resolved), D2 WASI p1 plus our own module (resolved), D3 binaries in `/bin`
(resolved), D8 9P2000 only, D9 modern Ed25519 auth, D10 guest-side toolkit,
D11 behavior compatibility with Inferno, D12 Linux and macOS hosts
(resolved), D0 aarch64 validation every milestone (resolved).

New for Go:

| # | Decision | Options | Recommendation |
|---|---|---|---|
| D-G1 | Guest toolchain | Standard Go (`GOOS=wasip1`, about 2 MiB per command, full language and library) / TinyGo (tens to hundreds of KiB, partial reflection and library support) / both | Standard Go first, for fidelity and one toolchain. Measure load time and memory at M2; add TinyGo as an optional size-optimized build if a command's size matters (for example on the kernel's initrd) |
| D-G2 | Bare-metal Go runtime | **DECIDED: TamaGo** (`GOOS=tamago`, tamago-go built from source by make.bash, as its own `tamago` command does). | Its arm64 package gives exceptions, the MMU, the generic timer and GICv3; `kernel/board/virt` adds QEMU virt: RAM at 0x40000000, the PL011, PSCI power off. K1 boots under TCG and KVM; core, vm and wazero link into the kernel |
| D-G3 | Kernel WASM execution | wazero interpreter / wazero compiler with executable pages from the bare-metal runtime | Interpreter first; measure at K4 |
| D-G4 | Guest concurrency | Go goroutines inside one guest (single-threaded) plus `purgatorio.wait` / wasm threads | Goroutines plus `wait`. Revisit when Go's wasip1 port gains threads |
| D-G5 | Kernel network stack | gVisor netstack / a smaller Go stack / our own | gVisor netstack, behind the `NetStack` interface |

## 13. Risks

- **Guest size and startup.** Go's runtime makes each command megabytes and
  each start compile megabytes of wasm. Mitigations: wazero's compilation
  cache keyed by qid, TinyGo (D-G1).
- **Bare-metal Go.** The kernel depends on a Go runtime working on
  aarch64 bare metal (D-G2). This is the largest risk and should be
  tested early.
- **Blocking inside guests.** One blocking host call stops all of a guest's
  goroutines. Programs that serve and wait at once need `purgatorio.wait`.
- **`sh` fidelity**, **ABI churn** and **graphics scope**, as in the Rust
  plan.
