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
- **M2:** Inferno's `sh` (sh.y with std, string and expr) and the
  everyday commands (`ls /bin`): ls, cp, mv, rm, cat, grep, sed, ed,
  sort, diff, tail, wc, ps, kill, bind, ns, date, test, mk, cd and more.
- **M3:** `#M` (the 9P client), `export`, `#s` and file2chan, kernel
  channels with alt; memfs, ramfile, mount, sdk/styxservers, and file
  servers: tarfs, archfs, 9660srv, dossrv, lockfs, trfs, iostats,
  styxmon.
- **M4:** `#I` on host sockets, `#C` with `os`, cs, dial, listen,
  styxlisten, telnet, ftpfs, webgrab, whois, rcmd, cpu, plumber.
- **M5:** loadable modules (`load`/`call`); Inferno's keyring
  authentication (`sdk/keyring`, `sdk/auth`), the ssl device `#D`,
  `mount -k`, and the signer and login tools.
- **Phase K:** the native kernel boots on QEMU's aarch64 virt machine
  through TamaGo (TCG and KVM), runs the portable core and the sh corpus,
  and has GICv3 interrupts, virtio rng, blk and net, `#S` disks, `#I` on
  gVisor's TCP/IP stack, and kfs (kernel/README.md).
- **Phase G:** memdraw (pixel-exact against Inferno's C libmemdraw),
  `#i` and `#m`, sdk/draw, a guest-side Tk (sdk/tk: the packer and grid,
  images, text tags, marks and search), wm/wm with its menu of programs
  from /lib/wmsetup, wm/sh, wm/edit, wm/clock and wm/about, and `emu -g`
  showing the screen in a browser.
- **K4:** per-prog and per-domain memory and prog limits (`memlimit`,
  `#pdomain`, docs/abi.md).
- **Self-hosting:** Go's compiler and linker run as guests and build Go
  programs inside the emu (docs/selfhost.md).

Devices: `#/ #c #e #U #| #d #p #s #M #I #C #D #i #m #^ #¤ #* #k #t`, and
`#S` in the kernel.

Not yet, compared with the Rust sibling: SMP in the kernel (K4), the
debugging files of `/prog/n` and profiling (`#P`), acme, a
native host window, and a test of authentication against a live
Inferno. The Rust project's own
additions that are not Inferno (Lethe, MCP, AOT images) are out of scope
here.

## Build

Needs Go 1.27 or later.

```sh
go run ./tools/build                    # build guests into root/bin
go run ./cmd/emu echo hello world
go run ./cmd/emu cat /dev/sysname /dev/user
go run ./cmd/emu sh -c 'ls /bin | wc -l'
go run ./cmd/emu                         # interactive sh -l
go run ./cmd/emu -vm interpreter echo hello world
go run ./cmd/emu -g 800x600 wm/wm       # open the URL it prints
go test ./...                           # needs the guests built first
```

## Layout

```
abi/ styx/ platform/ core/ vm/   portable packages, shared with the future kernel
libsec/                          portable ciphers and digests Go lacks (MD4, IDEA, RC4)
vm/wazero/                       the wazero engine adapter
cmd/emu/                         the hosted emu; host code in cmd/emu/host/
sdk/                             guest SDK
appl/cmd/<name>/                 commands -> root/bin/<name>.wasm
appl/cmd/wm/<name>/              window programs -> root/bin/wm/<name>.wasm
appl/test/<name>/                conformance guests -> root/bin/test/<name>.wasm
root/                            the tree emu serves
tools/build/                     guest build
tools/bench/                     the benchmark suite (docs/perf.md)
docs/                            plan.md (design and milestones), abi.md, perf.md
```

## License

Apache-2.0. Inferno's sources are a reference only; no Inferno code, fonts or
man pages are copied here.
