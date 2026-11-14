# Performance (K4)

This is the benchmark suite, its results on one host, and how they
compare with the targets the Rust sibling set for its kernel
(`github.com/ericvh/purgatorio`, docs/perf.md, Section 4).

## The suite

```sh
go run ./tools/build                           # the guests, including test/bench
go run ./tools/bench                           # the emu on both wazero modes
go run ./tools/bench -vm '' -kernel -kvm       # the kernel under KVM
go run ./tools/bench -kernel -kvm -rounds 3    # all three
```

`tools/bench` runs `root/lib/bench/bench.sh` inside each system. The
`bench` guest (appl/test/bench) times each benchmark inside the system,
so neither the emu's start nor the kernel's boot is counted. It prints
the median of the rounds and, in brackets, the fastest, in seconds.

| Name | What it does |
|---|---|
| `cat-root` | `cat` of a 100 MiB file in `/tmp` to `/dev/null`: a host directory on the emu, the RAM tree in the kernel |
| `cat-styx` | the same file through `mount -A {export /tmp} /n/remote`, a Styx conversation over a pipe |
| `sh-builtin-10k` | `sh -c` with four nested `for` loops over ten words, 10,000 assignments |
| `spawn-1k-total` | the `bench` guest spawning `echo -n` 1,000 times and waiting for each |
| `fs-memfs-1k-*` | creating 1,000 files of 64 bytes in one directory on a fresh `memfs`, then reading, stating and removing each |
| `fs-root-1k-*` | the same in `/tmp` (the emu only) |

The kernel's `/tmp` is in its RAM tree, which cannot be written, so
`tools/bench` packs the 100 MiB file into the kernel's root
(`tools/kernel -files`). The kernel has 1.5 GiB for its heap, too
little for a memfs holding the file, which is why the suite has no cat
from memfs.

## Results

All numbers are from 2026-11-14 on one Apple Silicon host (Asahi Linux
6.14, aarch64, 8 cores, QEMU with KVM). Other agents were building and
testing on the host at the same time: the load average was 3 to 12.
Read the fastest sample; differences under about 20% are noise.

"Before" is `next` at `cc247f3` plus the suite. "After" adds the two
changes below and the memory limits (K4), whose allocator is now in the
path of every memory grow. The kernel's "before" column is one round.

| Benchmark | Rust target | emu compiler before | after | emu interpreter before | after | kernel (KVM) before | after |
|---|---|---|---|---|---|---|---|
| `cat-root` | 0.5 | 0.077 (0.068) | 0.057 (0.056) | 0.359 (0.357) | 0.406 (0.396) | 0.411 | 0.396 (0.375) |
| `cat-styx` | 1 | 0.452 (0.428) | 0.353 (0.303) | 0.704 (0.671) | 0.737 (0.729) | 0.578 | 0.983 (0.681) |
| `sh-builtin-10k` | 0.5 | 0.293 (0.282) | 0.268 (0.268) | 2.13 (2.12) | 2.50 (2.31) | 2.24 | 2.61 (2.45) |
| `spawn-1k-total` | 1 | 9.21 (8.62) | 5.39 (5.19) | 10.7 (10.2) | 9.78 (9.70) | 10.0 | 11.3 (10.2) |
| `fs-memfs-1k-create` | 0.1 | 1.20 (0.734) | 0.580 (0.541) | 3.85 (3.17) | 4.14 (3.70) | 3.06 | 4.09 (3.58) |
| `fs-memfs-1k-read` | 0.1 | 0.638 (0.598) | 0.334 (0.300) | 3.01 (2.99) | 2.39 (2.31) | 2.83 | 2.32 (2.18) |
| `fs-memfs-1k-stat` | 0.1 | 0.534 (0.172) | 0.195 (0.178) | 0.975 (0.965) | 1.20 (1.15) | 0.974 | 1.32 (1.22) |
| `fs-memfs-1k-remove` | 0.1 | 0.635 (0.262) | 0.238 (0.191) | 1.71 (1.15) | 1.20 (1.15) | 1.15 | 1.37 (1.25) |
| `fs-root-1k-create` | 0.1 | 0.081 (0.063) | 0.061 (0.057) | 0.217 (0.216) | 0.232 (0.232) | - | - |
| `fs-root-1k-read` | 0.1 | 0.052 (0.049) | 0.045 (0.044) | 0.266 (0.266) | 0.326 (0.310) | - | - |
| `fs-root-1k-stat` | 0.1 | 0.013 (0.012) | 0.013 (0.012) | 0.063 (0.063) | 0.066 (0.066) | - | - |
| `fs-root-1k-remove` | 0.1 | 0.020 (0.017) | 0.018 (0.017) | 0.033 (0.033) | 0.035 (0.034) | - | - |

The Rust targets are for its kernel under KVM. The kernel here runs
wazero's interpreter (TamaGo gives it no executable memory for the
compiler), so the kernel column is the one to hold to them. The compiler
column shows what the same core does with compiled guests.

Against the targets, by fastest sample:

- **Met everywhere:** `cat-root` and `cat-styx`, and `fs-root` on the emu
  (in the interpreter only `fs-root-1k-create` and `-read` miss, and by
  less than 3.5 times).
- **Met on the compiler only:** `sh-builtin-10k` (0.27 s). The
  interpreter and the kernel take 2.3 to 2.5 s.
- **Missed everywhere:** `spawn-1k-total` (5.2 ms a spawn on the
  compiler, 10 ms in the interpreter and the kernel, against 1 ms) and
  `fs-memfs` (2 to 5 times the target on the compiler, 11 to 37 times in
  the interpreter and the kernel).

## What changed

Two changes came from profiling the misses; each is its own commit.

- **Module cache lookups.** Every spawn hashed its module with SHA-256 to
  find the compiled module in vm/wazero's cache, a tenth of a spawn's
  time for a Go module of 2 to 3 MB. The cache is now keyed by a seeded
  `maphash`, and a hit is checked against the module's bytes, so a
  collision cannot run the wrong module. `Machine.Load` reads the module
  into a buffer of the file's length instead of growing one 64 KiB read
  at a time. A spawn takes about 18% less on the compiler in a direct
  comparison.
- **One walk for open and remove.** `path_open` and `path_unlink_file`
  stat a path before opening or removing it, which on a mount is a
  second walk and an extra RPC. The walk's qid already says whether the
  file is a directory, so they now walk once. 1,000 files on memfs need
  1,000 fewer walks, and the read step takes about a quarter less.

The other gains between the columns (`cat-styx`, the memfs create and
remove on the compiler) are larger than these changes explain. Read them
as the difference in host load between the two runs.

## Why spawn, memfs and the interpreter miss

**Spawn.** A Go guest is large: 2.2 to 2.4 MB of initial memory, a
megabyte of data segments, and a table of about 5,600 functions. A CPU
profile of 300 spawns on the compiler, after the changes above:

- wazero instantiating the module: applying the data segments and
  evaluating the element segments' constant expressions, about 25%;
- the host's garbage collector, about 30%. Each spawn allocates the
  module's memory and wazero's per-instance structures. With `GOGC=400`
  a spawn took about 15% less, but the emu does not set it, because it
  raises memory use for large guests such as the Go compiler;
- the Go runtime starting inside the guest, about 15%;
- reading the module through the name space, about 10%.

Most of it is in wazero or comes from Go guests being large. Getting
near 1 ms would take reusing instantiated module state between spawns
(wazero has no snapshot of an instance) or smaller guests.

**memfs.** Each file operation is several Styx RPCs (a create is a walk,
a create, a write and clunks). Each RPC goes from the client prog's
goroutine through a pipe to the memfs server, a Go guest, and back,
about 20 to 70 µs on the compiler. Two things show where the time goes:

- With `GOMAXPROCS=1` the same steps take a half to a third as long.
  The cost is mostly handing work between goroutines on different OS
  threads (futex wakeups), not computing.
- Fids that walks leave behind are clunked when the garbage collector
  finds them (`runtime.AddCleanup` in core/devmnt.go), on a goroutine
  each. They arrive in bursts of thousands that queue on the
  conversation with the benchmark's own RPCs.

The fs-root rows, served by the host directory with no Styx, meet the
target. Inferno clunks a fid when its last Chan is closed; doing that,
and keeping the client and server on fewer thread hand-offs, are the
next things to try.

**The interpreter and the kernel.** For guest code that computes,
wazero's interpreter is about 8 times slower than its compiler
(`sh-builtin-10k`: 2.3 s against 0.27 s). The kernel matches the emu's
interpreter within the noise, so the kernel adds little of its own; the
engine sets the speed. The Rust sibling reached its targets in its
kernel with wasmi tuned for aarch64, or with modules compiled ahead of
time. The equivalent here is wazero's compiler in the kernel, with modules
compiled ahead of time: the next section.

## Ahead-of-time images in the kernel

The kernel now runs wazero's compiler, on executable memory of its own
(kernel/codemem, with a small patch to wazero: third_party/wazero), and
loads modules compiled ahead of time on the build host (`tools/aot`,
kernel/aot; kernel/README.md has the design). Its engine is chosen at
boot: `interpreter` (the default), `aot` (images where valid, the
interpreter for the rest) or `compiler` (every module compiled in the
kernel at its first run).

Measured on 2026-11-14 on the host above under KVM, one CPU, three
rounds in one boot each, median and fastest in brackets
(`go run ./tools/bench -vm '' -kernel -kvm -engines interpreter,aot,compiler`).
The load average was 7.6 to 9.5.

| Benchmark | Rust target | interpreter | aot | compiler |
|---|---|---|---|---|
| `cat-root` | 0.5 | 0.487 (0.365) | 0.057 (0.056) | 0.074 (0.059) |
| `cat-styx` | 1 | 0.871 (0.835) | 0.233 (0.231) | 0.291 (0.264) |
| `sh-builtin-10k` | 0.5 | 2.52 (2.29) | 0.285 (0.277) | 0.288 (0.275) |
| `spawn-1k-total` | 1 | 11.2 (10.7) | 3.83 (3.81) | 5.67 (3.47) |
| `fs-memfs-1k-create` | 0.1 | 3.51 (3.37) | 0.473 (0.440) | 0.471 (0.445) |
| `fs-memfs-1k-read` | 0.1 | 1.83 (1.79) | 0.276 (0.262) | 0.283 (0.273) |
| `fs-memfs-1k-stat` | 0.1 | 1.12 (1.08) | 0.129 (0.126) | 0.133 (0.131) |
| `fs-memfs-1k-remove` | 0.1 | 1.28 (1.28) | 0.163 (0.159) | 0.174 (0.167) |

- Compiled guests are 6 to 9 times faster than interpreted ones in the
  kernel (`sh-builtin-10k` 0.28 s against 2.3 s), as on the emu, and
  the kernel with images is as fast as the emu's compiler column above.
  `cat-root`, `cat-styx` and `sh-builtin-10k` now meet the targets in
  the kernel.
- `spawn-1k-total` drops from 10.7 to 3.8 ms a spawn and the memfs
  rows by 7 to 9 times, but both still miss, for the reasons above
  (instantiating a large Go guest, and Styx hand-offs).
- `aot` and `compiler` run the same code; they differ only in the
  first run of each module. A run loads each command's code once and
  then shares it, so the benchmarks cannot tell them apart.

The first run of a module is where they differ. With `aot` an image
took, on average over the modules of `tools/kernel -kvm -aot-check`,
4.3 ms to read from the RAM tree, 2.6 ms to check (the module's
digest: SHA-256 over 2 to 3 MB) and 59 ms to load (wazero decodes and
validates the module, reads the code from the image, compiles the
small entry trampolines and copies the code into code memory). Under
TCG the same took 11, 26 and 488 ms. Compiling a Go guest instead takes
0.5 to 1.8 s on the host's own compiler, about ten times as long. The
images are not signed yet, so the check has no signature to verify.

Costs:

- Images are about 4 times the size of their modules: 11 MB for
  `sh.wasm`'s 2.6 MB. The 20 commands `tools/kernel` packs by default
  make a compressed root of 48 MB, against 14 MB.
- Code memory comes from the heap in 32 MiB arenas that are kept: the
  corpus on four CPUs held 128 MiB, of which 76 MiB was in use at the
  end.
- The corpus under TCG takes 71 s with images and 131 s on the
  interpreter, so the CI job that runs it with images is the faster
  one.

AOT is off by default, as in the Rust sibling, until images are signed
and verified (docs/signing.md); the kernel only loads unsigned images
when booted with `aot unsigned`, as `tools/kernel -aot` does.
