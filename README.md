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

M1 (hello from WASM in a name space) is in:

- `core`: channels, devices, name spaces with unions, fd and env groups,
  progs with pctl, spawn and wait, after Inferno's `emu/port`.
- Devices: `#/`, `#c`, `#e`, `#|`, `#d`, `#p`, and `#U` for the host tree.
- `vm`: all of WASI p1 over the name space, plus the `purgatorio` module.
- `emu` boots the name space as Inferno's emu does and runs `emuinit`.
- Guests: `echo`, `cat`, `emuinit`, and the `test/abi` conformance guest.

The shell and the rest of the commands come next (M2). See `docs/plan.md`.

## Build

Needs Go 1.27 or later.

```sh
go run ./tools/build                    # build guests into root/bin
go run ./cmd/emu echo hello world
go run ./cmd/emu cat /dev/sysname /dev/user
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
