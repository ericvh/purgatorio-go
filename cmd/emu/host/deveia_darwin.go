package host

import (
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultEiaPorts are the serial callout devices, /dev/cu.*, as
// Inferno's MacOSX emu finds them through IOKit.
func DefaultEiaPorts() []string {
	out, _ := filepath.Glob("/dev/cu.*")
	sort.Strings(out)
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

var eiaspeeds = []int{0, 50, 75, 110, 134, 150, 200, 300, 600, 1200, 1800, 2400, 4800,
	7200, 9600, 14400, 19200, 28800, 38400, 57600, 76800, 115200, 230400}

func tcget(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TIOCGETA) }

// tcset is tcsetattr with TCSANOW, or TCSADRAIN when drain is set.
func tcset(fd int, ts *unix.Termios, drain bool) error {
	if drain {
		return unix.IoctlSetTermios(fd, unix.TIOCSETAW, ts)
	}
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, ts)
}

// setspeed is cfsetispeed and cfsetospeed; false for a rate not in the
// table.
func setspeed(ts *unix.Termios, baud int) bool {
	for _, s := range eiaspeeds {
		if s == baud {
			ts.Ispeed, ts.Ospeed = uint64(baud), uint64(baud)
			return true
		}
	}
	return false
}

// getspeed is cfgetospeed, as a rate.
func getspeed(ts *unix.Termios) (int, bool) {
	for _, s := range eiaspeeds {
		if uint64(s) == ts.Ospeed {
			return s, true
		}
	}
	return 0, false
}

func flushout(fd int) error { return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 2) } // FWRITE

func sendbreak(fd int) error {
	if err := unix.IoctlSetInt(fd, unix.TIOCSBRK, 0); err != nil {
		return err
	}
	time.Sleep(400 * time.Millisecond)
	return unix.IoctlSetInt(fd, unix.TIOCCBRK, 0)
}
