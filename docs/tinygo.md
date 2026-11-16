# TinyGo guests: an experiment

Decision D-G1 (docs/plan.md, Section 12) left TinyGo as an optional,
size-optimized build of the guests. This is the measurement: what we
gain and lose if guests are built with TinyGo instead of standard Go.
Nothing here changes the default. Standard Go guests are built as
before and are byte for byte the same.

Summary:

- 214 of 227 guests build. Eleven fail because TinyGo's linker step
  runs out of memory on ML-DSA, one needs a 64-bit `int`, one uses Go
  assembly.
- Guests are 9 times smaller (2.6 MiB to 272 KiB at the median), use
  about a tenth of the memory, start 3 to 6 times faster and compile
  5 to 10 times faster. Computing is no faster, and `sh` is 16 to 22%
  slower in the interpreter.
- 106 of 159 acceptance tests pass on a TinyGo root, 125 with standard
  Go modules for the 13 guests that do not build, and 142 with a
  ten-line fix for the largest problem: wasi-libc opens absolute paths
  relative to the current directory.
- The kernel boots and runs the sh corpus from a TinyGo initrd of 2.1 MB
  (18.8 MB with standard Go) in a tenth of the heap.

Recommendation: keep standard Go as the toolchain. TinyGo is worth
offering for the kernel's initrd once the path and stack problems are
fixed. Do not switch. The reasons are at the end.

## How to repeat it

```sh
go run ./tools/build                         # standard Go guests, root/bin
go run ./tools/build -tinygo                 # TinyGo guests, root-tinygo/bin
go run ./tools/build -tinygo -o root-mix -fill   # standard Go modules where TinyGo fails

mkdir -p root-tinygo/tmp root-tinygo/mnt
go run ./cmd/emu -r root-tinygo sh
PURG_EMU_ROOT=root-tinygo go test ./cmd/emu  # the acceptance tests on that root
go run ./tools/bench -root root-mix          # the suite of docs/perf.md
go run ./tools/initrd -root root-tinygo      # the kernel's root
go run ./tools/kernel -kvm -corpus -root root-tinygo
go run ./tools/bench -root root-mix -vm '' -kernel -kvm
```

`tools/build -tinygo` copies `root` without `bin`, `tmp` and `mnt` to
the root named by `-o` and builds every guest into it with
`tinygo build -target=wasip1`. A guest that fails is skipped and its
error goes to `<root>/tinygo-failed.txt`. `-flags` sets the TinyGo
flags, `-j` the number of builds at once, `-keep` skips guests already
there, `-only` builds one directory.

Be careful with `-j`: each of the eleven ML-DSA guests makes `wasm-opt` use
more than 12 GiB before it fails. Run the full build under a memory
limit (`ulimit -v`), or expect the kernel's OOM killer.

Everything was measured on 2026-11-16 on one Apple Silicon host (Asahi
Linux 6.14, aarch64, 8 cores, 16 GiB, 16 KiB pages), Go 1.27.4, TinyGo
0.44.0 (LLVM 22.1.4), at `next` b7e419d. The host is shared; the load
average was 1 to 3 during the timings.

## Options

Sizes of `sh.wasm` in bytes, and what each option did:

| Flags | `sh` | `echo` | Notes |
|---|---|---|---|
| (TinyGo defaults) | 888,584 | 385,502 | includes DWARF |
| `-opt=z -no-debug` | 330,004 | 66,163 | **used for everything below** |
| `-opt=s -no-debug` | 365,889 | 67,315 | |
| `-opt=2 -no-debug` | 467,392 | 78,726 | no faster: see below |
| `-opt=z -no-debug -gc=conservative` | 328,097 | 65,234 | slower |
| `-opt=z -no-debug -gc=leaking` | 276,949 | 50,396 | never frees; not usable for servers |
| `-opt=z -no-debug -scheduler=none` | 148,145 | 15,875 | no goroutines |
| `-scheduler=tasks` | - | - | does not build for wasm |

- **`-no-debug`** removes DWARF, which is more than half of a default
  build. The function names stay, so traps still print a stack.
- **`-opt=z` against `-opt=2`.** `-opt=2` is 40% larger and no faster
  in the benchmark suite: `sh-builtin-10k` took 0.30 s and 2.52 s
  (compiler, interpreter) against 0.29 to 0.34 s and 2.55 to 2.58 s.
  That is inside the noise.
- **`-gc`.** The default for wasip1 is `precise`. `conservative` made
  `sh-builtin-10k` slower in the interpreter (3.2 s with `-opt=z`,
  4.3 s with `-opt=2`, against 2.55 s).
- **`-scheduler`.** The default, `asyncify`, is the only one that works.
  `none` halves the code, but any guest that reaches `time.Sleep` or a
  timer fails to build (`attempted to start a goroutine without a
  scheduler`): of five guests tried, `sh`, `cat` and `echo` built, and
  `ls` and `memfs` did not. `sh` without a scheduler was no faster
  (2.69 s against 2.77 s in the interpreter), so Asyncify costs size,
  not speed, here.

## What builds

`tools/build` builds 227 guests: 208 commands, 18 tests and one
loadable module. With TinyGo 214 build and 13 fail, for three reasons:

| Cause | Guests |
|---|---|
| `wasm-opt` runs out of memory (11) | `auth/countersigner`, `auth/factotum`, `auth/mkauthinfo`, `auth/sign`, `auth/signer`, `auxi/rstyxd`, `cpu`, `import`, `mount`, `rcmd`, `test/pgauth` |
| `int` is 32 bits (1) | `kfs`: `qpdir (untyped int constant 2147483648) overflows int` |
| Go assembly (1) | `test/mem`: `grow_wasm.s` is not assembled, `undefined symbol: main.growMemory` |

`mount` matters most: much of the system and its tests use it.

The first cause is one package. Every one of those guests links
`crypto/mldsa` (through `pgauth` or `sdk/auth`), and a 15-line program
that makes an ML-DSA-65 key and signs with it fails the same way.
Binaryen's Asyncify pass, which TinyGo runs for its goroutine
scheduler, grows past 12 GiB on it and is killed, or aborts after 100
to 130 s under a 9 GiB limit. `-opt=1`, `-opt=2` and `-opt=z` all fail.
`crypto/mlkem` (508 KiB) and `argon2` build.

No guest failed for a missing `reflect` feature or an unsupported
package at build time. The loadable module (`appl/lib/testmod`,
`-buildmode=c-shared`, `//go:wasmexport`) builds and loads.

A full TinyGo build took 15 min 40 s the first time (six at once, cold
cache, including the eleven failures) and 3 min 44 s the second, when
those eleven were not tried. One guest
takes 1.3 s (`echo`) to 7 s (`acme`) to link, against about 0.1 s for
standard Go with a warm cache; all 227 standard Go guests build in 30 s
with a warm cache.

Building `sh` twice from two source directories on this host gave the
same bytes. Builds on different hosts were not compared.

## Correctness

`go test ./cmd/emu` has 159 top-level tests. Every guest test runs on
both wazero modes and compares them. The modes never disagreed on
TinyGo guests.

| Root | Pass | Fail | Skipped | Subtests pass / fail |
|---|---|---|---|---|
| standard Go, the 49 tests TinyGo failed | 49 | 0 | - | 80 / 0 |
| TinyGo only (214 guests) | 106 | 48 (and one hung) | 4 | 678 / 14 |
| TinyGo, standard Go for the 13 | 125 | 30 | 4 | 682 / 10 |
| the same with the path fix below | 142 | 13 | 4 | 685 / 7 |

The first row is the 48 tests that failed on TinyGo and the one that
hung, run again on `root`: all pass. The full suite was not run on
standard Go here. The last two rows ran those 49 tests only and take
the other results from the second row, so a regression among the other
106 from mixing toolchains or from the fix would not show. The four skipped tests need
a display, the Rust sibling or the self-hosted toolchain.
`TestMcpRemote` hung on the TinyGo-only root, where `rcmd` is missing;
it has no timeout of its own and was killed by hand.

The sh corpus (`root/lib/test/sh`, 16 scripts): 14 pass on both modes.
`limits` and `mash` fail, for the second and fourth reasons below.

What fails, by cause:

**1. wasi-libc opens absolute paths relative to the current directory
(17 tests).** TinyGo's `os` package opens files through wasi-libc. It
matches a path against the preopened directories after removing the
leading slashes, and the host's two preopens, `/` (fd 3) and `.`
(fd 4), both have the empty prefix. The last one wins, so every path
goes to the host as relative to fd 4, which the host takes as the
prog's current directory. With the dot at `/` nothing shows. After
`cd /tmp/q`, `cat /lib/hello.txt` opens `/tmp/q/lib/hello.txt`: it
fails, or reads the wrong file if that exists. Guests that use
`sys.Open` are not affected, which is why `sh`'s own redirections
work. This one bug explains `TestAescbc`, `TestAr`, `TestTar` (dates:
`/dev/time` did not open), `TestDbm`, `TestDbfs`, `TestRawdbfs`,
`TestDiskKfs`, `TestInstall`, both `mk` tests, `TestCmds2CGzip`, six
`TestMcp*` tests and two of the `TestMcpScripts` scripts.

A fix in the guest SDK was tried and not committed: a file built only
by TinyGo that registers `/` again, so it wins, and tells libc the
current directory, so relative paths still work.

```go
//go:build tinygo

package sys

import "os"

//export __wasilibc_register_preopened_fd
func registerPreopen(fd int32, prefix *byte) int32

func init() {
	dot, err := Fd2path(4)
	root := []byte("/\x00")
	registerPreopen(3, &root[0])
	if err == nil {
		_ = os.Chdir(dot)
	}
}
```

It is not committed because it needs a build constraint outside
`cmd/emu/host` and costs each prog two more host calls at start. A real
port should decide whether the fix belongs here or in the ABI (for
example, no `.` preopen for modules built with wasi-libc).

**2. Tests that assume a guest is megabytes (5 tests).** `TestLimits`,
the corpus's `limits` and `mcplimits` set a memory limit that `echo`
or a module load must not fit in; a TinyGo `echo` fits. `TestMcpJobs`
and `TestMcpResources` use a guest's `.wasm` as a large file. These are
gains that the goldens do not expect.

**3. Debugging tools and their goldens (5 tests).** `TestStack`,
`TestTrace`, `TestCprof`, `TestMprof` and `TestWasmdump` expect the Go
runtime's function names and module layout (`runtime.run$1$gowrapper`
is what TinyGo has). The tools work; their output differs.

**4. Fixed stacks (1 script).** A TinyGo goroutine has a fixed stack,
64 KiB by default. `mash`'s `fn rec { rec }` reaches its own nesting
limit under standard Go, whose stacks grow; under TinyGo it overflows
first and the module traps (`wasm error: unreachable`). With
`-stack-size=1MB` the script passes. A trap is not a Go panic: nothing
recovers it and the prog dies with a wasm stack trace.

**5. `int` is 32 bits (2 tests, 1 build failure).** Go's `int` on wasm
is 64 bits, TinyGo's is 32. `dd -bs` with a huge count wraps instead
of reporting `argument out of range`, `test -older 1000y` overflows,
and `kfs` does not compile. Other uses of `int` for sizes, offsets and
times would wrap silently above 2 GiB.

**6. Partial standard library (1 test).** TinyGo replaces `crypto/tls`
with a reduced package. `webgrab` of an `https` URL sent plain HTTP to
the TLS server in `TestCmds3CNet`.

Seen but not the cause of a test failure:

- `syscall.Errno` has no text: an `os` error prints as `errno 64`
  where standard Go prints a message. Guests that print `sys.Errstr()`
  are unaffected.
- Running out of memory is a trap in `runtime.growHeap` (`out of
  bounds memory access`), not `fatal error: out of memory` with exit
  status `2` as docs/abi.md says.
- A nil map write panics with `nil pointer dereference`, not
  `assignment to entry in nil map`. `recover` works.
- Map iteration starts at a different place each time, as in Go. No
  test depended on the order.
- Goroutines, channels, `select` and `sys.Alt` passed their tests
  (`TestM3`, the Styx servers, `rio`, `acme`, `wm`).

## Size

| Command | Standard Go KiB | TinyGo KiB | Ratio | gzip: Go KiB | gzip: TinyGo KiB |
|---|---|---|---|---|---|
| `echo` | 2,035 | 64 | 31.5 | 593 | 23 |
| `cat` | 2,138 | 76 | 28.0 | 622 | 27 |
| `ed` | 2,279 | 178 | 12.8 | 661 | 63 |
| `grep` | 2,476 | 243 | 10.2 | 714 | 95 |
| `ls` | 2,644 | 274 | 9.6 | 760 | 106 |
| `sh` | 2,618 | 322 | 8.1 | 751 | 111 |
| `mash` | 2,627 | 338 | 7.8 | 751 | 124 |
| `memfs` | 2,749 | 299 | 9.2 | 785 | 116 |
| `sed` | 2,696 | 303 | 8.9 | 771 | 118 |
| `mk` | 2,940 | 421 | 7.0 | 837 | 159 |
| `tar` | 3,094 | 466 | 6.6 | 877 | 174 |
| `rio` | 3,160 | 498 | 6.3 | 898 | 186 |
| `acme` | 3,474 | 592 | 5.9 | 978 | 216 |
| `lethe` | 3,519 | 712 | 4.9 | 1,056 | 292 |
| `auth/authsrv` | 3,468 | 246 | 14.1 | 1,044 | 97 |
| `auth/getauthinfo` | 3,783 | 1,215 | 3.1 | 1,123 | 509 |

| Total | Standard Go | TinyGo | Ratio |
|---|---|---|---|
| the 214 guests both build | 564.2 MiB | 60.8 MiB | 9.3 |
| smallest / median / largest guest | 1.95 / 2.58 / 7.18 MiB | 26 KiB / 272 KiB / 1.19 MiB | median 9.9 |
| the kernel's 26 default commands | 60.8 MiB | 5.06 MiB | 12.0 |
| the kernel's default root, `root.tar.gz` | 18.78 MB | 2.07 MB | 9.1 |
| `kernel.elf` with that root | 32.4 MB | 15.7 MB | 2.1 |

All 227 standard Go guests are 611 MiB.

## Speed on the emu

`go run ./tools/bench -root <root> -rounds 5`, twice for each
toolchain, alternating. Each cell is the median of the first run and of
the second, in seconds. The TinyGo root has the standard Go `mount`
(TinyGo cannot build it); `mount` only sets up `cat-styx`, it is not in
the timed path.

| Benchmark | compiler: Go | compiler: TinyGo | interpreter: Go | interpreter: TinyGo |
|---|---|---|---|---|
| `cat-root` | 0.087, 0.063 | 0.027, 0.051 | 0.369, 0.369 | 0.112, 0.111 |
| `cat-styx` | 0.522, 0.385 | 0.761, 0.559 | 0.670, 0.618 | 0.402, 0.513 |
| `sh-builtin-10k` | 0.305, 0.316 | 0.338, 0.288 | 2.16, 2.14 | 2.58, 2.55 |
| `spawn-1k-total` | 7.45, 7.48 | 1.53, 1.22 | 10.8, 8.89 | 3.12, 3.12 |
| `fs-memfs-1k-create` | 0.677, 0.707 | 2.01, 0.618 | 3.36, 3.57 | 1.86, 2.03 |
| `fs-memfs-1k-read` | 0.459, 0.395 | 1.08, 0.352 | 1.92, 2.08 | 1.34, 1.36 |
| `fs-memfs-1k-stat` | 0.159, 0.151 | 0.716, 0.147 | 1.10, 0.907 | 0.529, 0.602 |
| `fs-memfs-1k-remove` | 0.201, 0.253 | 0.591, 0.263 | 1.10, 1.84 | 0.884, 0.592 |
| `fs-root-1k-create` | 0.062, 0.058 | 0.128, 0.046 | 0.221, 0.220 | 0.071, 0.070 |
| `fs-root-1k-read` | 0.053, 0.069 | 0.038, 0.026 | 0.266, 0.266 | 0.133, 0.133 |
| `fs-root-1k-stat` | 0.014, 0.013 | 0.014, 0.012 | 0.064, 0.064 | 0.057, 0.056 |
| `fs-root-1k-remove` | 0.017, 0.017 | 0.019, 0.016 | 0.034, 0.034 | 0.030, 0.030 |

- **Spawn** is the clear gain: 7.5 ms to 1.2-1.5 ms a spawn on the
  compiler, 9-11 ms to 3.1 ms in the interpreter. docs/perf.md puts a
  Go spawn's time in instantiating a large module, the host's garbage
  collector and the Go runtime's start; all three shrink with the
  module. The Rust target of 1 ms is close on the compiler.
- **`cat-root`** is 3.3 times faster in the interpreter and 1.2 to 3
  times on the compiler.
- **`sh-builtin-10k`**, which only computes and allocates, is the same
  on the compiler and 19 to 22% slower in the interpreter (2.55 s
  against 2.15 s; five more runs gave 2.77 s against 2.27 s). TinyGo's
  generated code and its simpler collector are not faster than Go's.
- **The memfs rows on the compiler are noise**: TinyGo's two runs differ
  by a factor of 3 (2.01 s and 0.62 s for create). docs/perf.md
  explains why these rows depend on thread hand-offs in the host. In
  the interpreter, where the guest's own work dominates, TinyGo is
  mostly 1.4 to 2 times faster.
- `cat-styx` is inside the noise on the compiler.

### Compiling and starting

One emu run of one command, timed from outside, in milliseconds:
median and range of 5 runs with an empty compilation cache, and of 15
runs with it warm. Each run starts the emu, `emuinit` and the command.
The interpreter keeps no cache: it translates every module at every
start of the emu.

| Command | Mode | Go cold | TinyGo cold | Go warm | TinyGo warm |
|---|---|---|---|---|---|
| `echo hi` | compiler | 1177 (1111-1213) | 119 (92-175) | 104 (75-129) | 31 (14-37) |
| `echo hi` | interpreter | 227 (161-290) | 41 (40-44) | 215 (175-303) | 45 (22-55) |
| `sh -c 'echo hi'` | compiler | 1885 (1831-2045) | 342 (326-456) | 152 (85-222) | 40 (29-58) |
| `sh -c 'echo hi'` | interpreter | 269 (247-283) | 106 (58-152) | 393 (273-542) | 77 (61-109) |
| `ls /lib` | compiler | 1374 (1205-1495) | 228 (177-316) | 109 (81-145) | 41 (19-57) |
| `ls /lib` | interpreter | 238 (232-378) | 49 (48-53) | 223 (156-295) | 68 (50-104) |

A first run on the compiler is 5 to 10 times faster, and 2.5 to 5.5
times in the interpreter. A warm start is 3 to 5 times faster. The warm numbers are noisy (the ranges are wide), but the
ranges of the two toolchains do not overlap.

## Memory

### Guest memory

`/prog/<pid>/mem` gives the wasm linear memory a prog holds. Read while
the prog was at rest or blocked writing to a pipe (compiler mode; the
mode does not change it):

| Prog | Standard Go | TinyGo |
|---|---|---|
| `sh`, idle | 3.54 MiB | 257 KiB |
| `sh`, after the 10,000-assignment loop | 7.29 MiB | 257 KiB |
| `memfs`, empty | 3.55 MiB | 512 KiB |
| `memfs` holding one 10 MiB file | 44.0 MiB | 64.0 MiB |
| `cat` of a 10 MiB file | 2.79 MiB | 256 KiB |
| `ls -l` of a directory of 5,000 files | 6.54 MiB | 2.00 MiB |
| `sort -r` of 5,000 lines | 4.04 MiB | 1.00 MiB |

A small prog needs a fourteenth of the memory. The exception is a
large heap: TinyGo's heap doubles when it is full and never shrinks,
so `memfs` with a 10 MiB file holds 64 MiB against Go's 44 MiB.
TinyGo progs also hold a fixed stack for every goroutine (64 KiB by
default, more if `-stack-size` is raised for cause 4 above).

### The emu process

Maximum resident set of the emu (`/usr/bin/time -v`), warm cache,
median and range of three runs, in MiB:

| Workload | compiler: Go | compiler: TinyGo | interpreter: Go | interpreter: TinyGo |
|---|---|---|---|---|
| `sh -c 'echo hi'` | 79 (77-80) | 17 (17-17) | 194 (193-194) | 31 (30-34) |
| 20 `sleep`s at once | 217 (214-244) | 26 (26-27) | 368 (359-372) | 44 (43-46) |
| 100 `sleep`s at once | 629 (618-632) | 59 (58-60) | 826 (822-842) | 82 (82-86) |
| the sh corpus, one emu | 277 (275-292) | 48 (46-49) | 1046 (1037-1050) | 173 (163-175) |

From 20 to 100 progs, each `sleep` costs the host 5.2 MiB with
standard Go and 0.41 MiB with TinyGo on the compiler (5.7 and 0.48 MiB
in the interpreter). The corpus rows are not quite the same work: under
TinyGo `mash` traps partway through its script.

### Compiled code

Bytes wazero's compiler adds to its cache for one module (arm64 code),
beside the module's size:

| Module | Go: wasm | Go: compiled | TinyGo: wasm | TinyGo: compiled |
|---|---|---|---|---|
| `cat` | 2.09 MiB | 8.41 MiB | 76 KiB | 433 KiB |
| `ls` | 2.58 MiB | 10.6 MiB | 275 KiB | 1.48 MiB |
| `memfs` | 2.68 MiB | 11.2 MiB | 300 KiB | 1.63 MiB |
| `mash` | 2.57 MiB | 10.7 MiB | 339 KiB | 1.97 MiB |
| `acme` | 3.39 MiB | 14.7 MiB | 593 KiB | 3.39 MiB |
| `lethe` | 3.44 MiB | 13.5 MiB | 712 KiB | 6.68 MiB |

Compiled Go code is about 4 times its module; TinyGo's is 5.5 to 9
times, because Asyncify adds a save and restore path to every function
that can block. It is still 2 to 19 times less code. The interpreter's
form of a module is not measured directly; the `sh -c 'echo hi'` row
above (194 against 31 MiB for three modules) shows it.

## The kernel

`tools/initrd -root` and `tools/kernel -root` pack another root; no
other change was needed. All 26 default commands build with TinyGo.
KVM, one CPU, the interpreter engine (the kernel's default).

**The corpus** (`go run ./tools/kernel -kvm -corpus -root root-tinygo`):
12 of 16 scripts pass, 16 of 16 with standard Go. `limits` and `mash`
fail as on the emu. `misc` and `pipes` fail because output of the
`mash` script's background commands arrives after the trap, in the next
scripts' output; they are not separate bugs as far as their diffs show.
The run, with the kernel's build and boot, took 9 s against 25 s.

**The heap** (`/dev/memory`, the main pool: the kernel's Go heap in
use, which includes garbage not yet collected, and the memory its
runtime has taken), at four points of one boot:

| Point | Go: in use | Go: taken | TinyGo: in use | TinyGo: taken |
|---|---|---|---|---|
| after boot, in `sh` | 322 MB | 332 MB | 31 MB | 37 MB |
| 20 `sleep`s running | 462 MB | 572 MB | 44 MB | 54 MB |
| after they exit | 642 MB | 653 MB | 50 MB | 67 MB |
| after `memfs` and five `cat \| grep \| wc` | 848 MB | 864 MB | 86 MB | 92 MB |

The kernel's heap is a tenth of the size. A kernel with a TinyGo root
could run in far less than the 1.5 GiB it has now.

**The benchmarks** (`go run ./tools/bench -root <root> -vm '' -kernel
-kvm -rounds 3`, twice each; the two runs agreed within 3%, the second
is shown), median and fastest in seconds:

| Benchmark | Standard Go | TinyGo |
|---|---|---|
| `cat-root` | 0.371 (0.369) | 0.108 (0.108) |
| `cat-styx` | 0.812 (0.588) | 0.485 (0.273) |
| `sh-builtin-10k` | 2.22 (2.22) | 2.57 (2.57) |
| `spawn-1k-total` | 9.42 (9.38) | 3.09 (3.08) |
| `fs-memfs-1k-create` | 3.44 (3.06) | 1.67 (1.49) |
| `fs-memfs-1k-read` | 1.77 (1.75) | 1.26 (1.12) |
| `fs-memfs-1k-stat` | 1.05 (1.02) | 0.567 (0.565) |
| `fs-memfs-1k-remove` | 1.17 (1.06) | 0.656 (0.609) |

The kernel matches the emu's interpreter column: spawn 3 times faster,
`cat` 3.4 times, memfs 1.4 to 2 times, and `sh-builtin-10k` 16% slower.
With one CPU the memfs rows are steady here.

## What a port would need

1. **Fix path resolution** (cause 1), in the SDK or the ABI, and run
   the whole suite with it.
2. **ML-DSA.** Eleven guests, `mount` among them, do not build. Either
   Binaryen's Asyncify pass is fixed or given a list of functions to
   skip, or those guests stay standard Go. A root can mix the two
   (`tools/build -tinygo -fill`); the ABI is the same.
3. **Audit `int`.** Sizes, offsets, times and constants that need more
   than 31 bits must be `int64`. `kfs`, `dd` and `test` are known.
4. **Stacks.** Choose a `-stack-size`, per command where recursion is
   deep (`mash`, `sh`, parsers), and know that every goroutine pays it.
5. **Traps.** Decide what a prog's exit status is after a wasm trap or
   out of memory, and update docs/abi.md, which describes the Go
   runtime's messages.
6. **`crypto/tls`** and any other package TinyGo replaces: test each
   command that uses one. `webgrab` is known.
7. **Goldens.** The debugging tools' tests and the tests that assume a
   guest's size need a second set of expectations.
8. **Go assembly** (`test/mem`) needs another way to grow memory.
9. **The build.** A second pinned toolchain (TinyGo, its LLVM and
   Binaryen) on all three CI runners, a check that its output is byte
   for byte the same across them, three to four minutes more build
   time, and a memory limit on the linker step.
10. **The rule against build constraints** (CLAUDE.md) needs an
    exception if any fix is TinyGo-only code.

## Recommendation

**Keep standard Go as the guest toolchain. Offer TinyGo as an option
for the kernel's initrd. Do not switch.**

For TinyGo:

- The gains are large where docs/perf.md says the system is weakest:
  spawn is 3 to 6 times faster, a first compile 5 to 10 times, a prog
  costs a tenth of the memory, and the kernel's root and heap shrink
  9 to 10 times.
- The ABI needs no change to run TinyGo guests, and a root can mix
  both, so it can be adopted one command at a time.

Against switching:

- Eleven guests, `mount` and the signing tools among them, cannot be
  built at all, for a reason outside the project's control.
- Two differences are silent: absolute paths resolved against the
  current directory, and 32-bit `int`. The first has a known fix. The
  second needs an audit of every guest, and the tests found only the
  three cases they happen to cover.
- Fidelity is the project's first rule. Standard Go passes every test
  TinyGo failed; TinyGo passes 142 of 155 after the fix, and the rest
  need decisions (stacks, traps, TLS), not only new goldens.
- Computing is not faster. In the interpreter, the kernel's default,
  `sh` is 16 to 22% slower.
- It is a second toolchain to pin, build and keep reproducible.

The kernel's initrd is the place where TinyGo pays most and costs
least: its 26 commands all build, none needs ML-DSA, and size and
memory matter there. The steps are items 1 and 4 above, then
`tools/kernel -corpus -root root-tinygo` passing 16 of 16 (with a
`limits` golden that fits), then a CI job for it.

## Not measured

- The whole acceptance suite on standard Go, on the mixed root and on
  the root with the path fix: only the 49 tests that failed or hung on
  TinyGo were run again.
- Whether TinyGo's output is byte for byte the same on other hosts and
  operating systems. Only one host was used.
- The kernel under TCG, with more than one CPU, under `-stress`, or
  with the `compiler` and `aot` engines and TinyGo guests; ahead-of-time
  images of TinyGo modules.
- Graphics beyond the tests that pass (`rio`, `wm`, `acme`, the draw
  tests): no interactive use.
- Long-running servers: heap growth and fragmentation of TinyGo's
  collector over hours.
- Guests with many goroutines under load, and how a raised
  `-stack-size` changes the memory table.
- The interpreter's memory for a module, except through the emu's
  resident set.
- Time to compile a module apart from the rest of a cold start.
- Any TinyGo version other than 0.44.0, and macOS or x86-64 hosts.
