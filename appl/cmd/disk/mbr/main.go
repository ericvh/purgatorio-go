// Command mbr installs new boot code in the master boot record of a PC
// disk, keeping its partition table (Inferno disk/mbr). The default code
// prints that the disk is not bootable; -m takes it from mbrfile. -9
// also replaces the table with one Plan 9 partition covering the disk
// after the first track.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/disks"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// defmbr is the default boot block: it prints an error and waits for a
// key to reboot.
var defmbr = func() []byte {
	b := make([]byte, 512)
	copy(b, []byte{0xEB, 0x3C})
	copy(b[0x3E:], []byte{
		0xFA, 0xFC, 0x8C, 0xC8, 0x8E, 0xD8, 0x8E, 0xD0,
		0xBC, 0x00, 0x7C, 0xBE, 0x77, 0x7C, 0xE8, 0x19,
		0x00, 0x33, 0xC0, 0xCD, 0x16, 0xBB, 0x40, 0x00,
		0x8E, 0xC3, 0xBB, 0x72, 0x00, 0xB8, 0x34, 0x12,
		0x26, 0x89, 0x07, 0xEA, 0x00, 0x00, 0xFF, 0xFF,
		0xEB, 0xD6, 0xAC, 0x0A, 0xC0, 0x74, 0x09, 0xB4,
		0x0E, 0xBB, 0x07, 0x00, 0xCD, 0x10, 0xEB, 0xF2,
		0xC3})
	copy(b[0x77:], "Not a bootable disc or disc error\r\nPress almost any key to reboot...")
	return b
}()

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "disk/mbr: %s\n", s)
	sys.Exits("fail:error")
}

func main() {
	flag9 := false
	mbrfile := ""
	a := arg.New(os.Args)
	a.SetUsage("disk/mbr [-m mbrfile] disk")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case '9':
			flag9 = true
		case 'm':
			mbrfile = a.EArg()
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		a.Usage()
	}
	disk := disks.Open(args[0], sys.ORDWR, false)
	if disk == nil {
		fatal(fmt.Sprintf("opendisk %s: %s", args[0], sys.Errstr()))
	}
	if disk.Dtype == "floppy" {
		fatal("will not install mbr on floppy")
	}
	if disk.Secsize != 512 {
		fatal(fmt.Sprintf("secsize %d invalid: must be 512", disk.Secsize))
	}
	secsize := disk.Secsize
	mbr := make([]byte, secsize*disk.S)
	if _, err := disk.Fd.Seek(0, io.SeekStart); err != nil {
		fatal(fmt.Sprintf("seek to boot sector: %s\n", sys.Errstr()))
	}
	if n, _ := io.ReadFull(disk.Fd, mbr[:secsize]); n != secsize {
		fatal("reading boot sector: " + sys.Errstr())
	}
	var nmbr int
	if mbrfile == "" {
		nmbr = disks.Toffset
		copy(mbr, defmbr)
	} else {
		buf := make([]byte, secsize*(disk.S+1))
		f, err := os.Open(mbrfile)
		if err != nil {
			fatal(fmt.Sprintf("open %s: %s", mbrfile, sys.Errstr()))
		}
		nmbr, err = io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			fatal(fmt.Sprintf("read %s: %s", mbrfile, sys.Errstr()))
		}
		f.Close()
		if nmbr > secsize*disk.S {
			fatal(fmt.Sprintf("master boot record too large %d > %d", nmbr, secsize*disk.S))
		}
		if nmbr < secsize {
			nmbr = secsize
		}
		copy(buf[disks.Toffset:], mbr[disks.Toffset:secsize])
		copy(mbr, buf[:nmbr])
	}
	if flag9 {
		for i := disks.Toffset; i < secsize; i++ {
			mbr[i] = 0
		}
		p := disks.PCpart{Ptype: disks.Type9, Offset: int64(disk.S), Size: disk.Secs - int64(disk.S)}
		copy(mbr[disks.Toffset:], p.Bytes(disk))
	}
	mbr[secsize-2] = disks.Magic0
	mbr[secsize-1] = disks.Magic1
	nmbr = (nmbr + secsize - 1) &^ (secsize - 1)
	if disk.Wfd == nil {
		fatal("seek to MBR sector: " + sys.Errstr())
	}
	if _, err := disk.Wfd.Seek(0, io.SeekStart); err != nil {
		fatal(fmt.Sprintf("seek to MBR sector: %s\n", sys.Errstr()))
	}
	if n, _ := disk.Wfd.Write(mbr[:nmbr]); n != nmbr {
		fatal("writing MBR: " + sys.Errstr())
	}
}
