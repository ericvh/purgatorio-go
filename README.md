# Purgatorio-Go

A clean-sheet rebuild of Inferno in Go. The hosted emulator and the native
aarch64 kernel are Go; applications are Go compiled to WebAssembly
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
- **M4:** `#I` on host sockets, `#C` with `os`, `#t` (the host's serial
  ports, docs/plan.md 5.3), ndb/cs, dial, listen,
  styxlisten, telnet, ftpfs, webgrab, whois, rcmd, cpu, plumber.
- **M5:** loadable modules (`load`/`call`); Inferno's keyring
  authentication (`sdk/keyring`, `sdk/auth`), the ssl device `#D`,
  `mount -k`, and the signer and login tools. Beside it, the Rust
  sibling's own authentication with a post-quantum layer: `pgauth`
  (Ed25519 and hybrid Ed25519 + ML-DSA-65 keys, `pgcert1` and `pgcert2`
  certificates, the version 3 and hybrid version 4 handshakes, encrypted
  records) and `pgauth/modsig` (signed modules and AOT images;
  docs/auth.md, docs/signing.md). The classical formats interoperate
  with the Rust sibling; the hybrid ones are this port's own and do not
  match the hybrid formats the Rust sibling has since defined. Factotum with Inferno's p9any and
  p9sk1, for Plan 9's exportfs protocol: 9export and both imports
  (docs/rcmd.md). The network commands ndb/dns, dnsquery, csquery,
  registry and regquery, `/net/ipifc` (the host's interfaces,
  read-only), ip/ping (`/net/icmp`, echo only), sntp, tftpd, bootpd,
  rip (routes kept in `/net/iproute`), dhcp and virgild, and sendmail.
  Inferno's other shell, mash, with its make and history modules, and
  fs(1) with all its verbs (docs/mash.md). dbm, dbfs, rawdbfs, ar,
  tar, cron, the disk tools (disk/fdisk, format, kfscmd, mbr, mkext,
  mkfs, prep) and the install tools. Broken progs and the debugging
  tools stack, broke, trace, mdb, cprof, mprof, wasmdump and wasmdep
  over `#p` and `#P` (docs/debug.md).
- **Phase K:** the native kernel boots on QEMU's aarch64 virt machine
  through TamaGo (TCG and KVM), runs the portable core and the sh corpus,
  and has GICv3 interrupts, virtio rng, blk and net, `#S` disks, `#I` on
  gVisor's TCP/IP stack (IPv4 and IPv6, with `/net/ipifc`, which
  ip/dhcp configures, routes from `/net/iproute`, multicast,
  `/net/icmp`, `/net/icmpv6` and the stats files), `#l` (ether(3), raw
  frames beside the IP stack), and kfs (kernel/README.md).
- **Phase G:** memdraw (pixel-exact against Inferno's C libmemdraw),
  `#i` and `#m`, sdk/draw, a guest-side Tk (sdk/tk: the packer and grid,
  images, text tags, marks and search), wm/wm with its menu of programs
  from /lib/wmsetup, wm/sh, wm/edit, wm/clock, wm/about and wm/coffee,
  acme with its /mnt/acme file server and the plumber's edit port, win
  (a shell in an acme window) and B, rio after Plan 9's rio(1) and rio(4),
  with hold mode, window(1) and rio in a rio window (docs/plan.md,
  Phase G), and `emu -g` showing the screen in a
  native window (X11 on Linux, Cocoa on macOS, both without cgo; the
  window can be resized) or, with `-web`, in a browser.
- **K4:** per-prog and per-domain memory and prog limits (`memlimit`,
  `#pdomain`, docs/abi.md); the debugging files of /prog/n, and `#P`
  with prof (docs/plan.md 5.1 and 5.2); the benchmark suite
  (docs/perf.md); SMP in the kernel on every CPU QEMU gives
  it, with a stress mode; wazero's compiler in the kernel, and modules
  compiled ahead of time on an arm64 host into images the kernel loads
  (`tools/aot`, `tools/kernel -aot`; kernel/README.md, docs/perf.md),
  signed hybrid by default (docs/signing.md).
- **M6:** Lethe, the Rust sibling's stateless read-only file protocol,
  wire-compatible with it in the clear mode and with classical tickets:
  the codec and server engine (`lethe/`), the
  host server `lethed`, the client device `#λ` in the emu and the
  kernel, and the `lethe` command; tickets with this port's hybrid
  post-quantum exchange by default, the Rust classical one with `-p
  classical`, and the clear mode with `-A` (docs/lethe.md).
- **Self-hosting:** Go's compiler and linker run as guests and build Go
  programs inside the emu (docs/selfhost.md).
- **MCP (M6):** `mcpd`, a Model Context Protocol server on stdio
  (`emu -r root mcpd`) that gives an LLM client a sandboxed session:
  sh, file, job and namespace tools, resources, a `#pdomain` sandbox
  with memory and prog limits, `-n` policies and remote sessions through
  `rcmd`; and `mcpfs`, which mounts another MCP server's tools as files.
  A port of the Rust sibling's (docs/mcp.md).

Devices (`cat /dev/drivers`):

- emu: `#/ #c #e #U #| #d #p #P #s #M #^ #¤ #* #k #I #λ #C #i #m #D #t`
- kernel: `#/ #c #e #| #d #p #s #M #^ #¤ #* #k #I #λ #S #l`

Not yet: no test runs against a real Inferno, plan9port or Linux v9fs
(docs/plan.md, Section 9), so Styx and authentication interop with
Inferno itself are unchecked. The Rust project's own additions that are
not Inferno, Lethe and MCP, are both here.

## Build

Needs Go 1.27 or later.

```sh
go run ./tools/build                    # build guests into root/bin
go run ./cmd/emu echo hello world
go run ./cmd/emu cat /dev/sysname /dev/user
go run ./cmd/emu sh -c 'ls /bin | wc -l'
go run ./cmd/emu                         # interactive sh -l
go run ./cmd/emu -vm interpreter echo hello world
go run ./cmd/emu -g 800x600 wm/wm       # wm in a window on the display
go run ./cmd/emu -g 800x600 -web wm/wm  # in a browser: open the URL it prints
go run ./cmd/emu -g 800x600 rio         # rio: button 3 on the background for New
go test ./...                           # needs the guests built first
```

kernel/README.md has the kernel's build and run lines.

## Layout

```
abi/ styx/ platform/ core/ vm/   portable packages, shared by the emu and the kernel
memdraw/                         portable: Inferno's libmemdraw and libmemlayer
libsec/                          portable ciphers Go lacks (MD4, IDEA, RC4, ChaCha20-Poly1305, XChaCha20-Poly1305)
pgauth/                          portable keys, certificates, handshakes, records; pgauth/modsig
lethe/                           Lethe's codec and server engine, portable (docs/lethe.md)
vm/wazero/                       the wazero engine adapter
cmd/emu/                         the hosted emu; host code in cmd/emu/host/
cmd/lethed/                      the Lethe host server
kernel/                          the native aarch64 kernel, a module of its own (kernel/README.md)
sdk/                             guest SDK
appl/cmd/<name>/                 commands -> root/bin/<name>.wasm
appl/cmd/wm/<name>/              window programs -> root/bin/wm/<name>.wasm
appl/lib/<name>/                 loadable modules -> root/bin/lib/<name>.wasm
appl/test/<name>/                conformance guests -> root/bin/test/<name>.wasm
root/                            the tree emu serves
tools/build/                     guest build
tools/bench/                     the benchmark suite (docs/perf.md)
tools/kernel/ tools/initrd/      build and boot the kernel under QEMU, and pack its root
tools/aot/                       images of modules compiled ahead of time, for the kernel
tools/gotools/                   Go's compiler and linker as guests (docs/selfhost.md)
third_party/wazero/              the kernel's patch to wazero
internal/portcheck/              the portable-package check
docs/                            plan.md (design and milestones), abi.md, auth.md, signing.md,
                                 rcmd.md, lethe.md, mcp.md, mash.md, debug.md, perf.md, selfhost.md
```

## License

Apache-2.0. Inferno's sources are a reference only; no Inferno code, fonts or
man pages are copied here.
