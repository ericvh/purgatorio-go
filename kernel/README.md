# The native kernel

The kernel is Go on bare metal through TamaGo (`GOOS=tamago`), for QEMU's
aarch64 `virt` machine. It is its own Go module because it needs the
TamaGo toolchain.

```sh
git clone --depth 1 --branch tamago-go1.27.4 https://github.com/usbarmory/tamago-go ~/sdk/tamago-go
(cd ~/sdk/tamago-go/src && GOROOT_BOOTSTRAP=$(go env GOROOT) ./make.bash)
go run ./tools/kernel          # QEMU TCG, Cortex-A53
go run ./tools/kernel -kvm     # aarch64 host with /dev/kvm
```

`board/virt` supplies TamaGo's runtime hooks for the machine: RAM at
0x40000000, the PL011 console at 0x09000000, the generic timer, GICv3, an
RNG seed from the counter (no virtio-rng yet) and PSCI power off, which
ends QEMU. `cmd/kernel` is the kernel itself.
