# The native kernel

The kernel is Go on bare metal through TamaGo (`GOOS=tamago`), for QEMU's
aarch64 `virt` machine. It is its own Go module because it needs the
TamaGo toolchain.

```sh
git clone --depth 1 --branch tamago-go1.27.4 https://github.com/usbarmory/tamago-go ~/sdk/tamago-go
(cd ~/sdk/tamago-go/src && GOROOT_BOOTSTRAP=$(go env GOROOT) ./make.bash)
go run ./tools/build                      # the guests, into root/bin
go run ./tools/kernel                     # interactive sh -l, QEMU TCG (Cortex-A53)
go run ./tools/kernel -kvm                # the same under KVM on an aarch64 host
go run ./tools/kernel -c 'ls /bin | wc'   # run one command and power off
go run ./tools/kernel -corpus             # the sh script corpus against its goldens
```

The kernel embeds its root, packed by `tools/initrd` from root/ into
kernel/initrd/root.tar.gz: lib and the commands the tests use (all of bin
for -corpus). It boots the portable core over that tree as `#/`, with
`#c`, `#e`, `#|`, `#d`, `#p`, `#s`, `#M`, `#^`, `#¤`, `#*` and `#k`, runs
guests on wazero's interpreter, and starts `emuinit` with the command in
/lib/kernel/init (sh -l without one).

`board/virt` supplies TamaGo's runtime hooks for the machine: RAM at
0x40000000, the PL011 console at 0x09000000, the generic timer, GICv3, an
RNG seed from the counter (no virtio-rng yet) and PSCI power off, which
ends QEMU, and the PL031 RTC for the time of day. `cmd/kernel` is the
kernel; `platform.go` and `boot.go` are its side of the portable core.
