# Purgatorio-Go

A clean-sheet rebuild of Inferno in Go. The hosted `emu` (and later the
kernel) is Go; applications are Go compiled to `GOOS=wasip1 GOARCH=wasm`
and run on wazero, replacing Limbo and Dis. Inferno's model stays:
everything is a file, per-prog name spaces built with `bind`/`mount`, and
one protocol, Styx (9P2000). It is the Go sibling of
`github.com/ericvh/purgatorio` (Rust); that repo's docs are a useful
reference for semantics already worked out.

- `docs/plan.md`: the design, what Go changes relative to the Rust plan,
  milestones (Section 11) and open decisions (Section 12).
- `docs/abi.md`: the guest ABI (WASI p1 plus the `purgatorio` module).
- The task list is GitHub issues (`gh issue list --repo ericvh/purgatorio-go`).

## Commands

```sh
go run ./tools/build                 # build guests into root/bin (tests need this first)
go test -race ./...                  # unit and acceptance tests, both wazero modes
go run ./cmd/emu echo hello world
gofmt -l .                           # must print nothing
go vet ./...
GOOS=wasip1 GOARCH=wasm go vet ./sdk/... ./appl/...
GOOS=wasip1 GOARCH=wasm go build ./abi ./styx ./platform ./core ./vm
go test -run '^$' -fuzz FuzzUnmarshal -fuzztime 30s ./styx
```

The toolchain is pinned in `go.mod`. Before pushing, run gofmt, both vets,
the guest build and the tests you touched.

## Rules

- **Portable packages** (`abi`, `styx`, `platform`, `core`, `vm`) import no
  `os`, `net`, `syscall`, `os/*`, `path/filepath` and no third-party module.
  The kernel reuses them. `internal/portcheck` enforces this; they must also
  build for wasip1.
- **No build constraints or `runtime.GOOS` outside `cmd/emu/host/`**, except
  the `wasip1` split in `sdk/`. OS-specific host code goes in
  `*_linux.go` / `*_darwin.go` there.
- **Every blocking path in `core` uses a `Rendez`** so `kill` can interrupt
  it.
- **aarch64 is first-class:** decode wire formats with `encoding/binary`
  little-endian reads, no `unsafe` casts; never assume 4 KiB pages; run
  `-race` on arm64.
- **Inferno semantics, not POSIX.** Error strings match Inferno exactly
  (`abi.Enonexist` = `file does not exist`, ...), as do `ns` output, wait
  messages and command behavior. Inferno's man pages are the spec. No
  Inferno code, fonts or man pages are copied into the repo (Apache-2.0).
- **Guest `.wasm` must be byte-identical across build hosts.** CI compares
  hashes from all three runners. Do not add anything host-dependent to
  `tools/build`.
- Do not add non-standard functions to the `wasi_snapshot_preview1`
  namespace. New host calls go in the `purgatorio` module; bump
  `abi.Version` and update `docs/abi.md` when the ABI changes.
- Host functions are written once in `vm` against `vm.Memory`; engine
  adapters (`vm/wazero`) stay thin. Every guest test runs on both wazero
  modes and must give identical results.

## Working style

- `next` is the integration branch. Branch from `next` and open pull requests
  against `next`. The owner promotes `next` to `main` by fast-forward. Never
  push to `main` directly and never force-push `next`. Keep branches current
  by rebasing, not merging.
- Track work in GitHub issues; a PR says `Closes #n`.
- Commit subjects are prefixed with the milestone (`M1: add #c`, `K1: ...`).
  Subjects and bodies are short, plain sentences that say what changed and
  why.
- Docs and the README use plain, direct English: short sentences, no
  marketing tone.
- New commands are `package main` directories under `appl/cmd/<name>`;
  `tools/build` picks them up with no other edit.
