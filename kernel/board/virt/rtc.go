package virt

// The PL031 real-time clock of QEMU virt: seconds since the epoch.
const (
	RTC_BASE = 0x09010000
	rtcDR    = 0x00
)

// Seconds reads the RTC.
func Seconds() int64 { return int64(read32(RTC_BASE + rtcDR)) }
