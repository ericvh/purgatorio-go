# The kernel's patch to wazero

The kernel runs wazero's compiler, which needs executable memory. wazero
gets it from `mmap` and `mprotect`, chosen by build tags on `GOOS`, and
has no hook for a system without them (`GOOS=tamago`). wazero's
compilation cache also only reads files from a directory, and the kernel
has no host file system to give it. So the kernel builds with a patched
copy of wazero.

The patch is the files here. They replace or join the files of the
wazero version in `UPSTREAM`, which also has the SHA-256 of each upstream
file they replace. `tools/kernel` (`patch.go`) makes the copy at every
build: it takes wazero's Go files from the module cache (as verified by
the root module's go.sum), leaves out tests and examples, puts these
files over them, and writes the result to `kernel/.wazero`, which
`kernel/go.mod` names with a `replace`. It refuses to build if the
version or an upstream file has changed, so a wazero update has to port
the patch.

The emu, `tools/aot` and everything else in the root module use wazero
unpatched. The patch changes no code generation, so the images
`tools/aot` makes with upstream wazero are what the kernel's copy
loads; the kernel checks that the version recorded in each image is its
own.

## The changes

- `internal/platform/platform.go`: `tamago` joins the systems the
  compiler supports on arm64. One word.
- `internal/platform/mmap_unsupported.go`: its build tag leaves out
  `tamago`. One word.
- `internal/platform/mmap_tamago.go` (new): on `tamago`, the platform's
  code segment functions call the hook below.
- `experimental/codememory_purgatorio.go` (new): `CodeMemory`, a hook
  for executable memory (map writable, protect as read-only and
  executable, unmap), set with `SetCodeMemory`. The kernel sets it to
  `kernel/codemem`.
- `cache_purgatorio.go` (new): `NewCompilationCacheWithStore`, a
  compilation cache over a store the caller supplies, and `CacheKey`,
  the key under which the compiler looks a module up. The kernel's
  store (`kernel/aot`) serves the code of a verified image.
- `internal/engine/wazevo/image_purgatorio.go` (new): `ImageKey`, which
  `CacheKey` uses.

## Why not a fork or an overlay

A `replace` to a full copy of wazero in the repository would add about
90,000 lines that nobody reviews. `go build -overlay` cannot replace
files in the module cache. Making the copy at build time keeps the
repository to the patch, and the kernel already needs a build step
(`tools/initrd`) before `go build` works.

These changes could go upstream as a hook for executable memory and a
public cache store; then the patch would go.
