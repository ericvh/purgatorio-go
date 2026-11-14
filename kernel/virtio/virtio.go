// Package virtio drives virtio devices (Virtual I/O Device, version 1.2)
// over the virtio-mmio transport of QEMU's virt machine, in the modern
// (version 2) register layout: QEMU needs
// -global virtio-mmio.force-legacy=false.
//
// The kernel's memory is identity mapped and Go's heap does not move, so
// rings and buffers are ordinary heap slices whose addresses are given
// to the device. The rings are written with atomic loads and stores
// where the device synchronises with the driver.
package virtio

import (
	"errors"
	"sync/atomic"
	"unsafe"
)

// Device IDs.
const (
	IDNet     = 1
	IDBlock   = 2
	IDEntropy = 4
)

// Feature bits common to every device.
const FeatureVersion1 = 1 << 32

// Transport registers (virtio-mmio, section 4.2.2).
const (
	regMagic          = 0x000
	regVersion        = 0x004
	regDeviceID       = 0x008
	regDeviceFeatures = 0x010
	regDeviceFeatSel  = 0x014
	regDriverFeatures = 0x020
	regDriverFeatSel  = 0x024
	regQueueSel       = 0x030
	regQueueNumMax    = 0x034
	regQueueNum       = 0x038
	regQueueReady     = 0x044
	regQueueNotify    = 0x050
	regInterruptStat  = 0x060
	regInterruptACK   = 0x064
	regStatus         = 0x070
	regQueueDescLow   = 0x080
	regQueueDescHigh  = 0x084
	regQueueDrvLow    = 0x090
	regQueueDrvHigh   = 0x094
	regQueueDevLow    = 0x0a0
	regQueueDevHigh   = 0x0a4
	regConfig         = 0x100

	magic = 0x74726976 // "virt"
)

// Device status bits.
const (
	statusAck        = 1
	statusDriver     = 2
	statusDriverOK   = 4
	statusFeaturesOK = 8
	statusFailed     = 128
)

// QEMU virt has 32 virtio-mmio transports from 0x0a000000, 0x200 apart,
// on SPIs 16 to 47.
const (
	MMIOBase  = 0x0a000000
	MMIOSize  = 0x200
	MMIOSlots = 32
)

// Device is one virtio-mmio transport with a device behind it.
type Device struct {
	Base     uintptr
	Slot     int
	ID       uint32
	Features uint64
	queues   []*Queue
}

func (d *Device) reg(off uintptr) *uint32 { return (*uint32)(unsafe.Add(nil, d.Base+off)) }
func (d *Device) read(off uintptr) uint32 { return atomic.LoadUint32(d.reg(off)) }
func (d *Device) write(off uintptr, v uint32) {
	atomic.StoreUint32(d.reg(off), v)
}

// Scan returns the transports with a modern device behind them, in the
// order of QEMU's command line, which fills the transports from the
// last.
func Scan() []*Device {
	var devs []*Device
	for i := MMIOSlots - 1; i >= 0; i-- {
		d := &Device{Base: uintptr(MMIOBase + i*MMIOSize), Slot: i}
		if d.read(regMagic) != magic || d.read(regVersion) != 2 {
			continue
		}
		if d.ID = d.read(regDeviceID); d.ID != 0 {
			devs = append(devs, d)
		}
	}
	return devs
}

// Find returns the first device of type id, or nil.
func Find(devs []*Device, id uint32) *Device {
	for _, d := range devs {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// Init resets the device and negotiates the features of want that the
// device offers; VERSION_1 is always asked for.
func (d *Device) Init(want uint64) error {
	d.write(regStatus, 0)
	for d.read(regStatus) != 0 {
	}
	d.write(regStatus, statusAck)
	d.write(regStatus, statusAck|statusDriver)
	var have uint64
	for i := uint32(0); i < 2; i++ {
		d.write(regDeviceFeatSel, i)
		have |= uint64(d.read(regDeviceFeatures)) << (32 * i)
	}
	if have&FeatureVersion1 == 0 {
		return errors.New("virtio: not a modern device")
	}
	d.Features = have & (want | FeatureVersion1)
	for i := uint32(0); i < 2; i++ {
		d.write(regDriverFeatSel, i)
		d.write(regDriverFeatures, uint32(d.Features>>(32*i)))
	}
	d.write(regStatus, statusAck|statusDriver|statusFeaturesOK)
	if d.read(regStatus)&statusFeaturesOK == 0 {
		d.write(regStatus, statusFailed)
		return errors.New("virtio: features refused")
	}
	return nil
}

// Ready tells the device the driver is set up.
func (d *Device) Ready() {
	d.write(regStatus, statusAck|statusDriver|statusFeaturesOK|statusDriverOK)
}

// Config32 reads the 32-bit word at off in the device's configuration
// space.
func (d *Device) Config32(off uintptr) uint32 { return d.read(regConfig + off) }

// Config64 reads a 64-bit field as two words.
func (d *Device) Config64(off uintptr) uint64 {
	return uint64(d.Config32(off)) | uint64(d.Config32(off+4))<<32
}

// IRQ is the device's GIC interrupt number.
func (d *Device) IRQ() int { return 32 + 16 + d.Slot }

// Interrupt acknowledges the device's interrupt and wakes the waiters of
// its queues. It is the device's interrupt handler, and may be called
// at any time to poll.
func (d *Device) Interrupt() {
	if s := d.read(regInterruptStat); s != 0 {
		d.write(regInterruptACK, s)
	}
	for _, q := range d.queues {
		q.kick()
	}
}
