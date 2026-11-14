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
go run ./tools/kernel -net                # TCP to and from the host over user networking
go run ./tools/kernel -disk-check         # #S on a scratch disk image
go run ./tools/kernel -smp 4 -stress 600  # 4 CPUs; the corpus and guests in 8 progs at once
go run ./tools/kernel -disk my.img -netdev - -cmds dial,listen   # devices for an interactive session
```

QEMU always gets a virtio entropy device (`-rng=false` drops it).
`-disk image` adds a virtio block device on a raw image (repeatable),
`-netdev opts` a virtio network device on QEMU user networking (`-` for
no extra options, or for example `hostfwd=tcp::5555-:5555`), and `-cmds`
more commands for the root.

The kernel embeds its root, packed by `tools/initrd` from root/ into
kernel/initrd/root.tar.gz: lib and the commands the tests use (all of bin
for -corpus). It boots the portable core over that tree as `#/`, with
`#c`, `#e`, `#|`, `#d`, `#p`, `#s`, `#M`, `#^`, `#¤`, `#*`, `#k`, `#I`
(bound after /net) and `#S`, runs guests on wazero's interpreter, and
starts `emuinit` with the command in /lib/kernel/init (sh -l without
one).

`board/virt` supplies TamaGo's runtime hooks for the machine: RAM at
0x40000000, the PL011 console at 0x09000000, the generic timer, GICv3
(in group 1, which KVM delivers, unlike TamaGo's group 0 driver), the
random seed and PSCI power off, which ends QEMU, and the PL031 RTC for
the time of day. Interrupts reach a goroutine through TamaGo's signal
relay; the console and the virtio devices are interrupt driven.

`virtio` drives virtio-mmio devices in the modern layout (QEMU needs
`-global virtio-mmio.force-legacy=false`, which tools/kernel passes):
split virtqueues on heap memory, which is identity mapped and does not
move, and drivers for the entropy, block and network devices. The
entropy device keys the runtime's generator (ChaCha8; before it is
found, a counter-seeded splitmix64) and backs #c/random.

`#S` (`devsd.go`) is Inferno's sd(3) over the block devices: `#S/sdC0`
has `ctl` (geometry and partitions; `part name start end` and `delpart
name`, in sectors) and `data`, the whole disk, and the partitions.

`netstack` is the IP stack behind #I: gVisor's tcpip, set up with
go-net's gnet glue, on the virtio network device, with a loopback
interface. It is configured for QEMU user networking: 10.0.2.15/24,
gateway 10.0.2.2, name server 10.0.2.3 (cs and Srv lookups resolve
through it). It is IPv4 only. Without a network device it has only
loopback.

The kernel runs on every CPU QEMU gives it (`-smp n`, TCG or KVM).
TamaGo implements SMP for amd64 only, but its runtime hooks are the
same on arm64, and `board/virt/smp.go` supplies them: `goos.Task`
starts an M on a CPU of its own, `goos.ProcID` names the CPU, and
`goos.Wake` sends that CPU an SGI. An ELF kernel gets no device tree
from QEMU, so the CPUs are the GIC's redistributors; the others are
started with PSCI `CPU_ON` and take the boot CPU's page tables and
exception vectors. On tamago an M never gives up its P, so an idle M
stays in the scheduler and calls `goos.Idle`, which sleeps in WFI
until an interrupt, a wake SGI, or the virtual timer, set for the next
Go timer or at most a millisecond on. Nothing tells an idle CPU that
another made work for it, or that the world is stopping for a
collection; it finds out when it next looks.

The interrupt entry (`irq_arm64.s`) replaces TamaGo's, which leaves the
CPU that took an interrupt masked until the servicing goroutine
unmasks it, a thing only that CPU can do. Ours ends every interrupt at
once, on the CPU's own interrupt stack: a device interrupt is disabled
at the distributor, marked pending and relayed to the servicing
goroutine, wherever it runs, which runs the handler and enables it
again. Device interrupts go to the boot CPU; each CPU has the wake SGI
and its timer. A console write holds the UART for all its bytes.

`-stress secs` runs the sh corpus and the conformance guests (abi, m3,
load) over and over in `-stress-progs` workers (8), each with its own
copy of the name space and its own /tmp, and checks every run's output;
every 30 seconds it prints the heap and the number of progs.
A watchdog fails the run when no job finishes for `-stress-watch`
seconds (180), printing where each CPU is from QEMU's monitor.

`cmd/kernel` is the kernel; `platform.go`, `devices.go` and `boot.go`
are its side of the portable core.
