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

What works, milestone by milestone (docs/plan.md, Section 11):

- **M1:** `core` follows Inferno's `emu/port`: channels, the device table,
  name spaces with unions, fd and env groups, progs with pctl, spawn and
  wait. `vm` implements all of WASI p1 over the name space, plus the
  `purgatorio` module. `emu` boots the name space as Inferno's emu does.
- **M2:** Inferno's `sh` (sh.y with std, string and expr), and the
  everyday commands: ls, cp, mv, rm, mkdir, cat, grep, sort, diff, tail,
  wc, ps, kill, bind, ns, date, test and others (`ls /bin`).
- **M3:** `#M` (the 9P client), `export`, `#s` and file2chan, kernel
  channels with alt, and `ramfile`.
- **M4:** `#I` on host sockets, `#C` with `os`, `sdk/dial`, and the
  devices `#^`, `#¤`, `#*`, `#k`.
- **M5 (in part):** loadable modules (`load`/`call`, `sdk/module`).

Devices: `#/ #c #e #U #| #d #p #s #M #I #C #^ #¤ #* #k`.

Next: the Styx servers and network commands (memfs, mount, cs,
styxlisten), Inferno's keyring authentication and `#D`, graphics (`#i`),
and the native aarch64 kernel. See the open pull requests and issues.

## Build

Needs Go 1.27 or later.

```sh
go run ./tools/build                    # build guests into root/bin
go run ./cmd/emu echo hello world
go run ./cmd/emu cat /dev/sysname /dev/user
go run ./cmd/emu sh -c 'ls /bin | wc -l'
go run ./cmd/emu                         # interactive sh -l
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
