package host

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openpty opens a pseudo-terminal and returns its master and the path of
// its slave.
func openpty() (*os.File, string, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, "", err
	}
	fd := int(m.Fd())
	for _, req := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(fd, req, 0); err != nil {
			m.Close()
			return nil, "", err
		}
	}
	var name [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		m.Close()
		return nil, "", e
	}
	return m, string(name[:bytes.IndexByte(name[:], 0)]), nil
}
