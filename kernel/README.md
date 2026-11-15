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
go run ./tools/kernel -net                # TCP to and from the host, ARP and snoop over user networking
go run ./tools/kernel -disk-check         # #S on a scratch disk image
go run ./tools/kernel -smp 4 -stress 600  # 4 CPUs; the corpus and guests in 8 progs at once
go run ./tools/kernel -aot -corpus        # modules compiled ahead of time and signed (arm64 host)
go run ./tools/kernel -aot-check          # which modules the aot engine runs from images
go run ./tools/kernel -engine compiler    # wazero's compiler in the kernel
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
and `#l` (bound after /net) and `#S`, runs guests on wazero (the interpreter
unless /lib/kernel/engine says otherwise, below), and starts `emuinit`
with the command in /lib/kernel/init (sh -l without one).

## Engines and ahead-of-time images

/lib/kernel/engine (`tools/kernel -engine`) chooses how guests run
(`engine.go`):

- `interpreter`, the default: wazero's interpreter.
- `compiler`: wazero's compiler, compiling each module in the kernel
  when it is first run. A Go guest takes about half a second to compile
  under KVM, so this is for testing the compiler and code memory.
- `aot`: a module with a valid image beside it runs as the native code
  in the image; any other runs on the interpreter. An image needs a
  trusted hybrid (Ed25519 + ML-DSA-65) signature; `aot classical` takes
  Ed25519 signatures too.

An image is wazero's compiled module in the Rust sibling's signed image
file (docs/signing.md, "Ahead-of-time images"), with kind 2, made on the
build host by `tools/aot`: `/bin/sh.aot` beside `/bin/sh.wasm`. wazero
compiles for the host only, so the images are made on an arm64 host
(Linux or macOS), and they are the same on every one (CI compares
them; both signatures are deterministic). `tools/aot` signs with a key
from a seed file (`-key`), or with a public test key, hybrid unless
`-classical`. `tools/kernel -aot` compiles the modules it packs, packs
each image beside its module, lists the test key in /lib/keys/aot and
boots with `aot` (`-aot -classical`: the classical test key and `aot
classical`). root/ has no /lib/keys/aot, so a system built from it runs
no image until its operator lists a signer.

`aot` (`aot/aot.go`) looks for `<path>.aot` beside the `<path>.wasm` a
prog runs, read through the prog's name space, and uses it only if it
parses, is wazero's arm64 code for the kernel's wazero, names the
digest of the module the prog loaded, neither digest is revoked, and
one of its signatures is trusted under /lib/keys/aot as the policy
allows. The kernel reads /lib/keys/aot and /lib/keys/revoked once, at
boot, from the boot name space, so no prog can change them. Then it puts the code where wazero's compilation cache finds
it, and wazero loads it instead of compiling. Otherwise, or if wazero
does not take the code, the module runs on the interpreter. A
profiled prog (`#P`) always runs on the interpreter. A loaded module
is shared, as on the interpreter, by every prog that runs it until the
engine's cache drops it. At the end of a run the kernel prints what the
engine did: how many modules came from images, how many were refused,
and the time an image took to read, to hash the module, to verify and
to load.

The compiler needs executable memory, which TamaGo does not give: its
page tables map RAM flat, writable and never executable outside the
kernel's text. `codemem` makes it without any mapping being both
writable and executable. It takes arenas of whole 2 MiB blocks from the
heap (at least 32 MiB at a time, 768 MiB in all) and keeps them; each
block's level 2 entry is replaced by a table of 4 KiB pages with the
same attributes (break before make: nothing else uses the block). Code
is written into pages mapped writable and never executable; then the
data cache is cleaned to the point of unification (`DC CVAU`), the
pages are remapped read-only and executable, their TLB entries are
invalidated on every CPU (`TLBI VAAE1IS`), and the instruction cache is
invalidated over them (`IC IVAU`, broadcast) before a `DSB` and an
`ISB`. Freed pages become writable and not executable again. The flat
mapping is the pages' only one, so code that runs has no writable
alias. Pages are reused only after wazero has closed the module whose
code they held, so no CPU can still be running it.

Another CPU may have fetched from refilled pages before, and may still
hold those instructions in its pipeline. The architecture asks it to
synchronize its context (an `ISB`, or taking or returning from an
exception) after the invalidation completes and before it runs the new
code; Go's scheduler has no hook on the way into guest code, and a
goroutine moves between CPUs at any call. So `Protect` ends as Linux's
`flush_icache_range` does, by interrupting the other CPUs
(`virt.SyncInstructions`, board/virt/isync.go, like
`kick_all_cpus_sync`): it sends SGI 1 to every other running CPU and
waits until each has taken it. The handler executes an `ISB` and
stores the request it saw in its CPU's state; the caller acknowledges
for the CPU it runs on itself while it waits, so it does not depend on
taking interrupts and two callers cannot deadlock. Two kinds of CPU are
not interrupted: one not yet running an M, and one idle in `WFI`. Each
clears its flag, waits for the store with a `DSB`, then executes an
`ISB` before it runs Go code again; the caller read the flag after its
own `DSB`, so that `ISB` comes after the maintenance. A module load
calls `Protect` a few times (its code, its entry trampolines); a spawn
of a cached module calls it none. A sync took about 5 µs under KVM on
four CPUs and about 120 µs under TCG, against 0.2 and 0.4 ms when idle
CPUs were woken too; the engine's summary line counts them.

wazero has no hook for executable memory or for a cache that is not a
directory, so the kernel builds with a small patch to it
(third_party/wazero/README.md).

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
through it). It speaks IPv4 and IPv6. Without a network device it has
only loopback.

`/net/ipifc` shows and changes the stack's interfaces, as ip(3)
describes: `ipifc/0` is bound to `ether /net/ether0` and `ipifc/1` to
`loopback /dev/null` (only `ipifc/0`, the loopback, without a network
device). `add`, `remove` and `connect` add and remove gVisor addresses,
IPv4 or IPv6 (`add fec0::15 /64`), each with a route to its network;
`bind` and `unbind` attach and detach the two media, and `clone` makes
unbound interfaces. `ip/dhcp /net/ipifc/0` sets the address from QEMU's
DHCP server, after the address is removed. `iprouting` and
`iprouting 0` turn gVisor's IPv4 and IPv6 forwarding on and off
(`Forwarding` in `/net/ipifc/stats`). `addmulti` and `remmulti` join
and leave a multicast group (IGMP, MLD); so does a conversation's
`addmulti ifcaddr [group]`, as in devip.c, until it is closed, which
is how ip/rip -2 joins 224.0.0.9 and a UDP conversation receives a
group's datagrams. `local` lists the groups. `status` gives gVisor's
packet counts for the interface.

`/net/iproute` is the stack's routing. It starts with the default
route set at boot and goes to gVisor's route table after every `add`,
`remove` and `flush`, so ip/rip's and ip/dhcp's routes take effect.
gVisor's table is the route to each interface's network, then each
listed route whose gateway one of those networks holds, through that
interface, as Inferno's route lookup finds one; a route whose gateway
no interface reaches stays listed and is used once one does. A route
whose mask is not a prefix stays listed but is not used: gVisor routes
by prefix. The null address ip/dhcp adds routes everything through its
interface while it is there, as ipifcadd makes it.

IPv6 addresses and routes use Inferno's formats in `status`, `local`
and `iproute`. Neighbour discovery is gVisor's. There are no router
advertisements, address autoconfiguration or duplicate address
detection (`sendra6`, `recvra6`, `addpref6` and `setpar6` are not
supported); a link-local address is added like any other. TCP and UDP
endpoints are IPv6 ones that also take IPv4, so `/net/tcp` and
`/net/udp` dial and announce both, and `announce *!port` takes both.

`/net/icmp` and `/net/icmpv6` are over gVisor's raw ICMP endpoints, in
icmp.c's and icmp6.c's data format (a 20- or 40-byte IP header, then
the message): a conversation sends any message, with its local port as
the ident, and reads the messages with that ident from its remote
address (router solicitations and advertisements by type, as icmp6.c).
The stack answers echo requests and neighbour discovery itself. A
destination unreachable or ttl exceeded about one of the
conversation's messages hangs it up with Inferno's text. `stats` in
`/net/tcp`, `/net/udp`, `/net/icmp` and `/net/icmpv6` give gVisor's
counters under Inferno's names; those gVisor does not keep are 0.
icmp6.c's `headers` request, which takes the addresses from each
message, is not supported.

`#l` is ether(3) (`core/devether.go`, after Inferno's netif.c and
devether.c), bound after /net: `ether0` has `clone`, `addr`, `stats`
and `ifstats`, and a directory for each of its eight connections with
`data`, `ctl`, `stats`, `type` and `ifstats`. A connection's ctl takes
`connect type` (decimal, -1 for every type), `promiscuous`, `bridge`,
`headersonly`, `addmulti` and `remmulti`, `scanbs` and `nonblocking`;
its data reads whole frames of its type and writes frames out (60 to
1514 bytes; the source address is set to the interface's). It sits on
the link between the virtio device and gVisor: every frame received
goes to the connections that want it and to gVisor, and every frame
gVisor sends goes through it to the device, so a promiscuous connection
hears both. In Inferno the IP stack is a client of #l with its own
connections for IP and ARP (0x800, 0x806); here gVisor is beside #l
and takes every frame from the wire, so those types are free for
programs, and a frame a program writes to the interface's own address
or to broadcast also goes to gVisor, as Inferno's loopback would
deliver it to the stack's connection. The multicast filter (`addmulti`)
applies to the connections only: gVisor gets every multicast frame.
The device has no statistics of its own, so `ifstats` is empty.

`/net/ipifc/n/snoop` (ip(3)) gives copies of the IP packets interface
n sends and receives on its medium, one packet per read, without the
Ethernet header, as Inferno's packet medium gives them. Its readers
share one queue, as in Inferno, so each packet goes to one of them.
Only the Ethernet medium is tapped: the loopback interface's snoop
stays empty.

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

`cmd/kernel` is the kernel; `platform.go`, `devices.go`, `boot.go` and
`engine.go` are its side of the portable core.
