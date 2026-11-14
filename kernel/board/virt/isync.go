package virt

import "sync/atomic"

// Instruction synchronization across CPUs, for code written at run time
// (kernel/codemem).
//
// A CPU that writes code makes it visible to every CPU's instruction
// fetches: it cleans the data cache to the point of unification,
// invalidates the instruction cache (broadcast) and waits for that with
// a DSB. That is not enough for another CPU, which may have fetched
// from the same addresses before and may still hold those instructions
// in its pipeline. The architecture asks each other CPU to execute a
// context synchronization event (an ISB, or taking or returning from an
// exception) after the invalidation has completed and before it runs
// the new code (Arm ARM B2.2.5, concurrent modification and execution
// of instructions). Changes to translation tables ask the same of a
// CPU's instruction fetches.
//
// Go's scheduler has no hook on the way into guest code where every
// CPU could check for new code, and a goroutine moves between CPUs at
// any call. So SyncInstructions does what Linux's kick_all_cpus_sync
// does: it interrupts the other CPUs and waits until each has taken the
// interrupt. Unlike Linux it leaves idle CPUs asleep (see below).

// syncGen counts the requests. A CPU's pcpu.synced is the latest
// request it has synchronized for.
var syncGen uint64

// sgiSync is the SGI SyncInstructions sends.
const sgiSync = 1

// resendSync is how long SyncInstructions waits for a CPU before it
// sends it the SGI again (ns). An SGI is not lost; this only bounds
// what a mistake would cost.
const resendSync = 10_000_000

func isb()
func ackSync()

// SyncInstructions returns once every running CPU has executed a
// context synchronization event after the call began. The caller has
// completed its cache and TLB maintenance with a DSB.
//
// It sends the SGI to each other running CPU that is not idle. The
// handler (irqEntry) runs on an exception entry, executes an ISB, and
// stores the request it saw in its CPU's synced. The caller waits for
// each running CPU's synced to reach its request. While it waits it
// acknowledges for the CPU it is on itself, with an ISB, so it neither
// waits for its own interrupt nor deadlocks when it runs with
// interrupts masked, or when two CPUs ask at once.
//
// Two kinds of CPU need no interrupt, because they execute an ISB that
// comes after the caller's maintenance before they run any more Go
// code:
//
//   - One that is not running an M yet (pcpu.online clear): it sets
//     online, waits for the store with a DSB, then executes an ISB.
//   - One that is idle in WFI (pcpu.idle set): on its way out of
//     waitIRQ it clears idle, waits with a DSB, then executes an ISB.
//
// In both, the caller read the flag after its own DSB, so the flag's
// store, and the other CPU's DSB and ISB after it, completed later
// than the maintenance did. A sleeping CPU is not woken, so the cost
// is the CPUs that are running.
func SyncInstructions() {
	gen := atomic.AddUint64(&syncGen, 1)
	var sent uint32
	me := self()
	for i := range cpus {
		c := &cpus[i]
		if c != me && atomic.LoadUint32(&c.online) != 0 && atomic.LoadUint32(&c.idle) == 0 {
			sendSGI(c.sgi | sgiSync<<24)
			sent |= 1 << i
		}
	}
	for i := range cpus {
		c := &cpus[i]
		if atomic.LoadUint32(&c.online) == 0 {
			continue
		}
		t := nanotime()
		for atomic.LoadUint64(&c.synced) < gen && atomic.LoadUint32(&c.idle) == 0 {
			ackSync() // for the CPU this runs on now
			if c == self() {
				continue
			}
			// The caller moved off c, or c missed the SGI.
			if now := nanotime(); sent&(1<<i) == 0 || now-t > resendSync {
				sendSGI(c.sgi | sgiSync<<24)
				sent |= 1 << i
				t = now
			}
		}
	}
}
