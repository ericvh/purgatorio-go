package host

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// DefaultEiaPorts are Inferno's Linux serial devices.
func DefaultEiaPorts() []string {
	var out []string
	for i := 0; i < 8; i++ {
		out = append(out, fmt.Sprintf("/dev/ttyS%d", i))
	}
	return out
}

var eiaspeeds = []struct {
	baud int
	flag uint32
}{
	{0, unix.B0}, {50, unix.B50}, {75, unix.B75}, {110, unix.B110}, {134, unix.B134},
	{150, unix.B150}, {200, unix.B200}, {300, unix.B300}, {600, unix.B600},
	{1200, unix.B1200}, {1800, unix.B1800}, {2400, unix.B2400}, {4800, unix.B4800},
	{9600, unix.B9600}, {19200, unix.B19200}, {38400, unix.B38400}, {57600, unix.B57600},
	{115200, unix.B115200}, {230400, unix.B230400}, {460800, unix.B460800},
}

func tcget(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TCGETS) }

// tcset is tcsetattr with TCSANOW, or TCSADRAIN when drain is set.
func tcset(fd int, ts *unix.Termios, drain bool) error {
	if drain {
		return unix.IoctlSetTermios(fd, unix.TCSETSW, ts)
	}
	return unix.IoctlSetTermios(fd, unix.TCSETS, ts)
}

// setspeed is cfsetispeed and cfsetospeed; false for a rate not in the
// table.
func setspeed(ts *unix.Termios, baud int) bool {
	for _, s := range eiaspeeds {
		if s.baud == baud {
			ts.Cflag &^= unix.CBAUD
			ts.Cflag |= s.flag
			ts.Ispeed, ts.Ospeed = uint32(baud), uint32(baud)
			return true
		}
	}
	return false
}

// getspeed is cfgetospeed, as a rate.
func getspeed(ts *unix.Termios) (int, bool) {
	for _, s := range eiaspeeds {
		if ts.Cflag&unix.CBAUD == s.flag {
			return s.baud, true
		}
	}
	return 0, false
}

func flushout(fd int) error { return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCOFLUSH) }

func sendbreak(fd int) error { return unix.IoctlSetInt(fd, unix.TCSBRK, 0) }
