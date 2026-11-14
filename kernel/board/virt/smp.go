package virt

import (
	"fmt"
	"runtime"
	"runtime/goos"
	"sync/atomic"
	"unsafe"
)

// SMP for the TamaGo runtime on arm64. TamaGo implements it for amd64
// only, but the runtime's hooks are the same on every architecture:
// goos.Task starts an M on a CPU of its own, goos.ProcID names the CPU
// an M runs on, goos.Wake wakes the M on a CPU, and goos.Idle is how an
// M with nothing to run waits. On tamago an M never gives up its P, so
// there is one M per CPU, started once, and an idle M stays in the
// scheduler, calling goos.Idle between looks for work.
//
// QEMU gives an ELF kernel no device tree, so the CPUs are found from
// the GIC: there is a redistributor for each, and the last one says so.
// The others are started with PSCI CPU_ON, through HVC as for power off
// (QEMU's conduit at EL1, and KVM's), at apEntry (smp_arm64.s). Each
// turns on the MMU with the boot CPU's tables and registers, takes the
// same exception vectors and its own interrupt stack, enables its GIC
// CPU interface, and waits with WFE for Task to give it an M.

const (
	maxCPUs      = 16
	irqStackSize = 4096

	// maxIdle bounds how long an idle CPU sleeps. Nothing tells a CPU
	// that another made work for it, or that the world is being
	// stopped for a collection (the runtime wakes only an M asleep on a
	// lock or note, with Wake); it finds out when it next looks.
	maxIdle = 1_000_000 // ns

	psciCPUOn        = 0xc4000003
	psciAffinityInfo = 0xc4000004
)

// pcpu is a CPU's own state; TPIDR_EL1 points to its CPU's. The
// assembly reaches the fields through go_asm.h.
type pcpu struct {
	stackTop uint64 // the top of its interrupt stack
	gicr     uint64 // its redistributor
	mpidr    uint64
	id       uint64 // its index in cpus, the M's procid
	woken    uint32 // set by the wake SGI and the idle timer
	up       uint32 // set by the CPU once it waits for a task
	sgi      uint64 // ICC_SGI1R_EL1's target fields for it

	// What apEntry starts from: the boot CPU's registers.
	mair, tcr, ttbr0, sctlr, vbar uint64

	// The M Task gives it: its g0's stack, its g0 and mstart.
	taskSP, taskG, taskPC uint64
}

var (
	cpus      [maxCPUs]pcpu
	irqStacks [maxCPUs][irqStackSize]byte
	ncpu      int    // CPUs running
	nextCPU   uint32 // the last CPU Task gave an M
	cntfrq    uint64 // timer ticks per second
)

func apEntry()
func apEntryPC() uint64
func self() *pcpu
func setSelf(c *pcpu)
func psci(fn, a1, a2, a3 uint64) int64
func cleanDcache(addr, n uint64)
func sev()
func waitIRQ(woken *uint32, ticks uint64)
func sendSGI(v uint64)
func readCNTFRQ() uint64
func readSysregs(c *pcpu)
func syncIcache(addr uint64)

// initCPU fills in CPU i's pcpu for the CPU with this MPIDR and
// redistributor.
func initCPU(i int, mpidr uint64, gicr uint32) *pcpu {
	c := &cpus[i]
	c.id = uint64(i)
	c.mpidr = mpidr
	c.gicr = uint64(gicr)
	c.stackTop = uint64(uintptr(unsafe.Pointer(&irqStacks[i][irqStackSize-16])))
	aff1, aff2, aff3 := mpidr>>8&0xff, mpidr>>16&0xff, mpidr>>32&0xff
	c.sgi = aff3<<48 | aff2<<32 | aff1<<16 | 1<<(mpidr&0xf)
	return c
}

// initBoot sets up the boot CPU: its pcpu, the GIC, the interrupt entry
// and the idle hook. It runs in Hwinit1, before the scheduler.
func initBoot() {
	mpidr := readMPIDR()
	gicr := uint32(0)
	redistributors(func(base, aff uint32) bool {
		if aff == affinity(mpidr) {
			gicr = base
			return false
		}
		return true
	})
	c := initCPU(0, mpidr, gicr)
	setSelf(c)
	ncpu = 1
	cntfrq = readCNTFRQ()
	gicInit(c)
	patchVectors()
	goos.Idle = idle
}

// patchVectors points the IRQ entries of TamaGo's vector table, for
// both stack pointer selections, at irqEntry. Each entry loads its
// target from the word after two instructions.
func patchVectors() {
	readSysregs(&cpus[0])
	pc := irqEntryPC()
	for _, off := range []uint64{0x080, 0x280} {
		w := cpus[0].vbar + off + 8
		*(*uint64)(unsafe.Add(nil, uintptr(w))) = pc
		syncIcache(w)
	}
}

// StartCPUs starts the other CPUs, up to n in all (every CPU if n <= 0),
// gives the runtime their Ms and sets GOMAXPROCS to the CPUs running,
// which it returns. Call it once, early in main.
func StartCPUs(n int) int {
	if n <= 0 || n > maxCPUs {
		n = maxCPUs
	}
	readSysregs(&cpus[0])
	entry := apEntryPC()
	type redist struct {
		base, aff uint32
	}
	var others []redist
	redistributors(func(base, aff uint32) bool {
		if aff != affinity(cpus[0].mpidr) {
			others = append(others, redist{base, aff})
		}
		return true
	})
	for _, r := range others {
		if ncpu >= n {
			break
		}
		mpidr := uint64(r.aff&0xffffff) | uint64(r.aff>>24)<<32
		c := initCPU(ncpu, mpidr, r.base)
		c.mair, c.tcr, c.ttbr0, c.sctlr, c.vbar = cpus[0].mair, cpus[0].tcr, cpus[0].ttbr0, cpus[0].sctlr, cpus[0].vbar
		gicrInit(r.base)
		// The CPU reads its pcpu with the MMU and caches off.
		cleanDcache(uint64(uintptr(unsafe.Pointer(c))), uint64(unsafe.Sizeof(*c)))
		if st := psci(psciCPUOn, mpidr, entry, uint64(uintptr(unsafe.Pointer(c)))); st != 0 {
			fmt.Printf("smp: CPU_ON %#x failed: %d\n", mpidr, st)
			break
		}
		deadline := nanotime() + 1e9
		for atomic.LoadUint32(&c.up) == 0 && nanotime() < deadline {
		}
		if atomic.LoadUint32(&c.up) == 0 {
			fmt.Printf("smp: CPU %#x did not start\n", mpidr)
			break
		}
		ncpu++
	}
	if ncpu > 1 {
		goos.ProcID = procID
		goos.Wake = wake
		goos.Task = task
	}
	runtime.GOMAXPROCS(ncpu)
	return ncpu
}

// procID is the runtime's goos.ProcID: the CPU's index.
func procID() uint64 { return self().id }

// task is the runtime's goos.Task: it gives the next CPU an M, to run
// mstart on the M's g0 and g0's stack. The runtime's own stack (sp) is
// left unused: mstart takes the stack bounds from g0, which, unless the
// stack is the system's, the runtime allocated for it. It runs without
// a P, so it must not allocate or write pointers.
func task(sp, mp, gp, fn unsafe.Pointer) {
	i := int(atomic.AddUint32(&nextCPU, 1))
	if i >= ncpu {
		print("smp: no CPU left for an M\n")
		PowerOff()
	}
	c := &cpus[i]
	hi := *(*uintptr)(unsafe.Add(gp, unsafe.Sizeof(uintptr(0)))) // g.stack.hi
	c.taskSP = uint64(hi) &^ 15
	c.taskG = uint64(uintptr(gp))
	atomic.StoreUint64(&c.taskPC, uint64(uintptr(fn)))
	sev()
}

// wake is the runtime's goos.Wake: it sends the wake SGI to the CPU of
// the M with this procid.
func wake(procid uint64) {
	if procid >= uint64(ncpu) {
		return
	}
	c := &cpus[procid]
	if c == self() {
		atomic.StoreUint32(&c.woken, 1)
		return
	}
	sendSGI(c.sgi | sgiWake<<24)
}

// idle is the runtime's goos.Idle: with nothing to run until the time
// until (ns; 0 or less for no limit), the CPU sleeps in WFI until an
// interrupt, the wake SGI, or the virtual timer, which it sets for until
// or maxIdle from now, whichever is sooner. A wake that came since the
// last idle returns at once.
func idle(until int64) {
	c := self()
	if atomic.SwapUint32(&c.woken, 0) != 0 {
		return
	}
	d := int64(maxIdle)
	if until > 0 {
		left := until - nanotime()
		if left <= 0 {
			return
		}
		d = min(d, left)
	}
	t := uint64(d) * cntfrq / 1e9
	waitIRQ(&c.woken, max(t, 1))
	atomic.StoreUint32(&c.woken, 0)
}

// CPUs returns the number of CPUs running.
func CPUs() int { return ncpu }
