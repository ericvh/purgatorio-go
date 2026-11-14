# Purgatorio-Go

A clean-sheet rebuild of Inferno in Go. The hosted emulator (and later the
kernel) is Go; applications are Go compiled to WebAssembly
(`GOOS=wasip1 GOARCH=wasm`) and run on [wazero](https://wazero.io),
replacing Limbo and Dis. Inferno's model stays: everything is a file,
per-prog name spaces built with `bind` and `mount`, and one protocol, Styx
(9P2000).

This is the Go sibling of [Purgatorio](https://github.com/ericvh/purgatorio),
which does the same in Rust.

## Status

Bootstrap (M0). What works:

- `styx`: the full 9P2000 codec, with round-trip, truncation and fuzz tests.
- `vm`: the WASI p1 calls the Go runtime needs, implemented natively (not
  wazero's WASI), plus `purgatorio.errstr`, `werrstr` and `exits`.
- `emu` runs a guest from `root/bin` on wazero's compiler or interpreter.
- Guests: `echo`, and the `test/abi` conformance guest.

Name spaces, devices and the shell come next (M1, M2). See `docs/plan.md`.

## Build

Needs Go 1.27 or later.

```sh
go run ./tools/build                    # build guests into root/bin
go run ./cmd/emu echo hello world
go run ./cmd/emu -vm interpreter echo hello world
go test ./...                           # needs the guests built first
```

## Layout

```
abi/ styx/ platform/ core/ vm/   portable packages, shared with the future kernel
vm/wazero/                       the wazero engine adapter
cmd/emu/                         the hosted emu; host code in cmd/emu/host/
sdk/                             guest SDK
appl/cmd/<name>/                 commands -> root/bin/<name>.wasm
appl/test/<name>/                conformance guests -> root/bin/test/<name>.wasm
root/                            the tree emu serves
tools/build/                     guest build
docs/                            plan.md (design and milestones), abi.md
```

## License

Apache-2.0. Inferno's sources are a reference only; no Inferno code, fonts or
man pages are copied here.
