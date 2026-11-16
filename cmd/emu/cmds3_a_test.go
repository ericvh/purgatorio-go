package main

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
	"unicode/utf16"
)

// Group A of the third batch of commands: Styx file servers and filters
// (tarfs, archfs, 9660srv, trfs, iostats, styxmon). The fixtures are
// made here and written to root/tmp/cmds3a, which the emu sees as
// /tmp/cmds3a.

var cmds3aDir = root + "/tmp/cmds3a"

// cmds3aTime is the fixtures' modification time: noon, so that no zone
// moves it to another day.
var cmds3aTime = time.Date(1999, 1, 2, 12, 0, 0, 0, time.UTC)

func cmds3aSetup(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(cmds3aDir); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "tree/sub dir"} {
		if err := os.MkdirAll(filepath.Join(cmds3aDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{
		"t.tar":                       cmds3aTar(t),
		"a.arch":                      cmds3aArch(),
		"plain.iso":                   cmds3aISO(false, false),
		"rock.iso":                    cmds3aISO(true, false),
		"joliet.iso":                  cmds3aISO(false, true),
		"tree/a file":                 []byte("spaced\n"),
		"tree/sub dir/inner":          []byte("inner\n"),
		"tree/plain":                  []byte("plain\n"),
		"bad.tar":                     bytes.Repeat([]byte{'x'}, 300),
		"tree/sub dir/second one.txt": []byte("second\n"),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(cmds3aDir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func cmds3aTar(t *testing.T) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	add := func(h *tar.Header, data string) {
		if h.Format == tar.FormatUnknown {
			h.Format = tar.FormatUSTAR
		}
		h.ModTime = cmds3aTime
		h.Size = int64(len(data))
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	add(&tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755, Uname: "bob", Gname: "staff"}, "")
	add(&tar.Header{Name: "dir/a.txt", Typeflag: tar.TypeReg, Mode: 0o644, Uname: "bob", Gname: "staff"}, "hello tar\n")
	add(&tar.Header{Name: "dir/link", Typeflag: tar.TypeSymlink, Linkname: "a.txt", Mode: 0o777}, "")
	add(&tar.Header{Name: "top", Typeflag: tar.TypeReg, Mode: 0o640, Uid: 7, Gid: 8, Format: tar.FormatGNU}, "top file\n")
	add(&tar.Header{Name: "implied/deep/f", Typeflag: tar.TypeReg, Mode: 0o444, Uname: "ann", Gname: "ann"}, "deep\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// cmds3aArch is an archive as mkfs -a writes it.
func cmds3aArch() []byte {
	m := cmds3aTime.Unix()
	var b bytes.Buffer
	ent := func(name, mode, uid, gid, data string) {
		b.WriteString(name + " " + mode + " " + uid + " " + gid + " " + strconv.FormatInt(m, 10) + " " + strconv.Itoa(len(data)) + "\n" + data)
	}
	ent("/lib", "20000000775", "sys", "sys", "")
	ent("/lib/x.txt", "644", "bob", "staff", "hello\n")
	ent("/lib/sub/y", "444", "bob", "staff", "yy\n")
	ent("/other", "664", "ann", "users", "zz\n")
	b.WriteString("end of archive\n")
	return b.Bytes()
}

// ISO 9660 image layout, in 2048-byte sectors.
const (
	isoSect     = 2048
	isoPVD      = 16
	isoSVD      = 17
	isoTerm     = 18
	isoPathL    = 19
	isoPathM    = 20
	isoRoot     = 21
	isoSub      = 22
	isoData1    = 23
	isoData2    = 24
	isoJRoot    = 25
	isoJSub     = 26
	isoNSectors = 27
)

func isoBoth16(b []byte, v int) {
	binary.LittleEndian.PutUint16(b, uint16(v))
	binary.BigEndian.PutUint16(b[2:], uint16(v))
}

func isoBoth32(b []byte, v int) {
	binary.LittleEndian.PutUint32(b, uint32(v))
	binary.BigEndian.PutUint32(b[4:], uint32(v))
}

// isoRec makes a directory record.
func isoRec(extent, size int, dir bool, name, sysuse []byte) []byte {
	n := 33 + len(name)
	if n&1 != 0 {
		n++
	}
	n += len(sysuse)
	if n&1 != 0 {
		n++
	}
	r := make([]byte, n)
	r[0] = byte(n)
	isoBoth32(r[2:], extent)
	isoBoth32(r[10:], size)
	tm := cmds3aTime
	copy(r[18:25], []byte{byte(tm.Year() - 1900), byte(tm.Month()), byte(tm.Day()),
		byte(tm.Hour()), byte(tm.Minute()), byte(tm.Second()), 0})
	if dir {
		r[25] = 2
	}
	isoBoth16(r[28:], 1)
	r[32] = byte(len(name))
	copy(r[33:], name)
	off := 33 + len(name)
	if off&1 != 0 {
		off++
	}
	copy(r[off:], sysuse)
	return r
}

func rrPX(mode uint32) []byte {
	b := make([]byte, 36)
	copy(b, "PX")
	b[2], b[3] = 36, 1
	isoBoth32(b[4:], int(mode))
	isoBoth32(b[12:], 1)
	return b
}

func rrNM(name string) []byte {
	b := append([]byte{'N', 'M', byte(5 + len(name)), 1, 0}, name...)
	return b
}

func ucs2(s string) []byte {
	var b []byte
	for _, r := range utf16.Encode([]rune(s)) {
		b = append(b, byte(r>>8), byte(r))
	}
	return b
}

// cmds3aISO makes a small CD image: a root with hello.txt and subdir,
// which holds inner.txt. rock adds Rock Ridge names and modes; joliet
// adds a Joliet tree with long mixed-case names.
func cmds3aISO(rock, joliet bool) []byte {
	img := make([]byte, isoNSectors*isoSect)
	sect := func(n int) []byte { return img[n*isoSect : (n+1)*isoSect] }
	data1, data2 := "hello iso\n", "inner file\n"
	copy(sect(isoData1), data1)
	copy(sect(isoData2), data2)

	var su = func(px uint32, nm string) []byte {
		if !rock {
			return nil
		}
		return append(rrPX(px), rrNM(nm)...)
	}
	var rootDot []byte
	if rock {
		sp := []byte{'S', 'P', 7, 1, 0xBE, 0xEF, 0}
		er := append([]byte{'E', 'R', 18, 1, 10, 0, 0, 1}, "RRIP_1991A"...)
		rootDot = append(append(sp, er...), rrPX(0o40755)...)
	}
	dirent := func(entries ...[]byte) []byte {
		var b []byte
		for _, e := range entries {
			b = append(b, e...)
		}
		return b
	}
	root := dirent(
		isoRec(isoRoot, isoSect, true, []byte{0}, rootDot),
		isoRec(isoRoot, isoSect, true, []byte{1}, nil),
		isoRec(isoData1, len(data1), false, []byte("HELLO.TXT;1"), su(0o100640, "Hello.txt")),
		isoRec(isoSub, isoSect, true, []byte("SUBDIR"), su(0o40700, "SubDir")),
	)
	sub := dirent(
		isoRec(isoSub, isoSect, true, []byte{0}, nil),
		isoRec(isoRoot, isoSect, true, []byte{1}, nil),
		isoRec(isoData2, len(data2), false, []byte("INNER.TXT;1"), su(0o100600, "Inner File.txt")),
	)
	copy(sect(isoRoot), root)
	copy(sect(isoSub), sub)

	// Path tables: root and subdir.
	pt := func(be bool) []byte {
		put := func(b []byte, v int) {
			if be {
				binary.BigEndian.PutUint32(b, uint32(v))
			} else {
				binary.LittleEndian.PutUint32(b, uint32(v))
			}
		}
		put16 := func(b []byte, v int) {
			if be {
				binary.BigEndian.PutUint16(b, uint16(v))
			} else {
				binary.LittleEndian.PutUint16(b, uint16(v))
			}
		}
		b := make([]byte, 10+14)
		b[0] = 1
		put(b[2:], isoRoot)
		put16(b[6:], 1)
		e := b[10:]
		e[0] = 6
		put(e[2:], isoSub)
		put16(e[6:], 1)
		copy(e[8:], "SUBDIR")
		return b
	}
	ptl, ptm := pt(false), pt(true)
	copy(sect(isoPathL), ptl)
	copy(sect(isoPathM), ptm)

	vd := func(n int, typ byte, rootext int) {
		v := sect(n)
		v[0] = typ
		copy(v[1:], "CD001")
		v[6] = 1
		copy(v[8:40], bytes.Repeat([]byte{' '}, 32))
		copy(v[40:72], bytes.Repeat([]byte{' '}, 32))
		copy(v[40:], "CMDS3A")
		isoBoth32(v[80:], isoNSectors)
		isoBoth16(v[120:], 1)
		isoBoth16(v[124:], 1)
		isoBoth16(v[128:], isoSect)
		isoBoth32(v[132:], len(ptl))
		binary.LittleEndian.PutUint32(v[140:], isoPathL)
		binary.BigEndian.PutUint32(v[148:], isoPathM)
		copy(v[156:190], isoRec(rootext, isoSect, true, []byte{0}, nil))
		v[881] = 1
	}
	vd(isoPVD, 1, isoRoot)
	if joliet {
		vd(isoSVD, 2, isoJRoot)
		copy(sect(isoSVD)[88:], []byte{0x25, 0x2F, 0x45})
		jroot := dirent(
			isoRec(isoJRoot, isoSect, true, []byte{0}, nil),
			isoRec(isoJRoot, isoSect, true, []byte{1}, nil),
			isoRec(isoData1, len(data1), false, ucs2("Hello Joliet.txt;1"), nil),
			isoRec(isoJSub, isoSect, true, ucs2("Sub Dir"), nil),
		)
		jsub := dirent(
			isoRec(isoJSub, isoSect, true, []byte{0}, nil),
			isoRec(isoJRoot, isoSect, true, []byte{1}, nil),
			isoRec(isoData2, len(data2), false, ucs2("Inner.txt;1"), nil),
		)
		copy(sect(isoJRoot), jroot)
		copy(sect(isoJSub), jsub)
	}
	t := sect(isoTerm)
	t[0] = 0xFF
	copy(t[1:], "CD001")
	t[6] = 1
	return img
}

// TestCmds3AArchives: tarfs and archfs mount archives made here; ls -l and cat show what they serve.
func TestCmds3AArchives(t *testing.T) {
	cmds3aSetup(t)
	cmds3aRun(t, `D=/tmp/cmds3a
tarfs $D/t.tar $D/m1
ls $D/m1
ls -l $D/m1/dir $D/m1/top $D/m1/implied/deep
cat $D/m1/dir/a.txt
tarfs -p $D/t.tar $D/m2
ls -l $D/m2/top $D/m2/implied
cat $D/m2/top
tarfs $D/bad.tar $D/m3
echo $status
tarfs $D/t.tar
echo $status
archfs -m $D/m3 $D/a.arch
ls $D/m3 $D/m3/lib
ls -l $D/m3/lib/x.txt $D/m3/lib/sub/y $D/m3/other
cat $D/m3/lib/x.txt $D/m3/other
archfs -m $D/m4 $D/a.arch /lib/sub
ls $D/m4 $D/m4/lib $D/m4/lib/sub
archfs $D/t.tar
echo $status
archfs
echo $status
`,
		"/tmp/cmds3a/m1/dir\n"+
			"/tmp/cmds3a/m1/implied\n"+
			"/tmp/cmds3a/m1/top\n"+
			"--rw-r--r-- M 1 bob staff 10 Jan 02  1999 /tmp/cmds3a/m1/dir/a.txt\n"+
			"ls: /tmp/cmds3a/m1/implied/deep: file does not exist\n"+
			"--rw-r----- M 1 7 8 9 Jan 02  1999 /tmp/cmds3a/m1/top\n"+
			"hello tar\n"+
			"--r--r--r-- M 2 7 8 9 Jan 02  1999 /tmp/cmds3a/m2/top\n"+
			"d-r-xr-xr-x M 2 ann ann 5 Jan 02  1999 /tmp/cmds3a/m2/implied/deep\n"+
			"top file\n"+
			"tarfs: error reading /tmp/cmds3a/bad.tar: short read: expected 512, got 300\n"+
			"error\n"+
			"usage: tarfs [-a|-b|-ac|-bc] [-D] file mountpoint\n"+
			"usage\n"+
			"/tmp/cmds3a/m3/lib\n"+
			"/tmp/cmds3a/m3/other\n"+
			"/tmp/cmds3a/m3/lib/sub\n"+
			"/tmp/cmds3a/m3/lib/x.txt\n"+
			"--r--r--r-- M 3 bob staff 3 Jan 02  1999 /tmp/cmds3a/m3/lib/sub/y\n"+
			"--rw-r--r-- M 3 bob staff 6 Jan 02  1999 /tmp/cmds3a/m3/lib/x.txt\n"+
			"--rw-rw-r-- M 3 ann users 3 Jan 02  1999 /tmp/cmds3a/m3/other\n"+
			"hello\n"+
			"zz\n"+
			"/tmp/cmds3a/m4/lib\n"+
			"/tmp/cmds3a/m4/lib/sub\n"+
			"/tmp/cmds3a/m4/lib/sub/y\n"+
			"archfs: too few fields in file header\n"+
			"error\n"+
			"usage: archfs [-ab] [-m mntpt] archive [prefix ...]\n"+
			"usage\n")
}

// TestCmds3AISO: 9660srv serves plain ISO 9660, Rock Ridge and Joliet images.
func TestCmds3AISO(t *testing.T) {
	cmds3aSetup(t)
	cmds3aRun(t, `D=/tmp/cmds3a
9660srv $D/plain.iso $D/m1
ls -l $D/m1 $D/m1/subdir
cat $D/m1/hello.txt $D/m1/subdir/inner.txt $D/m1/hello.txt';1' $D/m1/subdir/../hello.txt
ls $D/m1/subdir/..
cat $D/m1/nope
ls $D/m1/hello.txt/x
9660srv $D/rock.iso $D/m2
ls -l $D/m2 $D/m2/SubDir
cat $D/m2/SubDir/'Inner File.txt'
9660srv $D/joliet.iso $D/m4
ls -l $D/m4 $D/m4/'Sub Dir'
cat $D/m4/'Hello Joliet.txt'
9660srv -J $D/joliet.iso $D/m5
ls -l $D/m5
9660srv $D/t.tar $D/m6
echo $status
9660srv $D/plain.iso
echo $status
`,
		"--r--r--r-- M 1 cdrom iso 10 Jan 02  1999 /tmp/cmds3a/m1/hello.txt\n"+
			"d-r-xr-xr-x M 1 cdrom iso  0 Jan 02  1999 /tmp/cmds3a/m1/subdir\n"+
			"--r--r--r-- M 1 cdrom iso 11 Jan 02  1999 /tmp/cmds3a/m1/subdir/inner.txt\n"+
			"hello iso\n"+
			"inner file\n"+
			"hello iso\n"+
			"hello iso\n"+
			"/tmp/cmds3a/m1/subdir/../hello.txt\n"+
			"/tmp/cmds3a/m1/subdir/../subdir\n"+
			"cat: cannot open /tmp/cmds3a/m1/nope: file does not exist\n"+
			"ls: /tmp/cmds3a/m1/hello.txt/x: not a directory\n"+
			"--rw-r----- M 2 cdrom ridge 10 Jan 02  1999 /tmp/cmds3a/m2/Hello.txt\n"+
			"d-rwx------ M 2 cdrom ridge  0 Jan 02  1999 /tmp/cmds3a/m2/SubDir\n"+
			"--rw------- M 2 cdrom ridge 11 Jan 02  1999 /tmp/cmds3a/m2/SubDir/Inner File.txt\n"+
			"inner file\n"+
			"--r--r--r-- M 3 cdrom joliet 10 Jan 02  1999 /tmp/cmds3a/m4/Hello Joliet.txt\n"+
			"d-r-xr-xr-x M 3 cdrom joliet  0 Jan 02  1999 /tmp/cmds3a/m4/Sub Dir\n"+
			"--r--r--r-- M 3 cdrom joliet 11 Jan 02  1999 /tmp/cmds3a/m4/Sub Dir/Inner.txt\n"+
			"hello iso\n"+
			"--r--r--r-- M 4 cdrom iso 10 Jan 02  1999 /tmp/cmds3a/m5/hello.txt\n"+
			"d-r-xr-xr-x M 4 cdrom iso  0 Jan 02  1999 /tmp/cmds3a/m5/subdir\n"+
			"9660srv: mount /tmp/cmds3a/t.tar /tmp/cmds3a/m6 failed: can't read volume descriptor\n"+
			"\n"+
			"usage: 9660srv [-rabc] [-9JR] [-s] cd_device dir\n"+
			"usage\n")
}

// TestCmds3AFilters: trfs, styxmon and iostats sit between a client and a server.
func TestCmds3AFilters(t *testing.T) {
	cmds3aSetup(t)
	cmds3aRun(t, `D=/tmp/cmds3a
trfs $D/tree $D/m1
ls $D/m1 $D/m1/sub*
cat $D/m1/a*file
echo made > $D/m1/new' 'file
ls $D/tree
rm $D/m1/new' 'file
ls $D/m1
trfs $D/tree
echo $status
mount -c {styxmon {memfs -s}} $D/m2 >[2] $D/mon.out
echo hello > $D/m2/f
cat $D/m2/f
grep 'Version|Create|Write' $D/mon.out
styxmon
echo $status
iostats cat $D/tree/plain >[2] $D/io.out
grep 'tree/plain|^write|^Message' $D/io.out
iostats -d -f $D/db.out echo hi
grep Version $D/db.out
iostats
echo $status
`,
		"/tmp/cmds3a/m1/a\u00a0file\n"+
			"/tmp/cmds3a/m1/plain\n"+
			"/tmp/cmds3a/m1/sub\u00a0dir\n"+
			"/tmp/cmds3a/m1/sub\u00a0dir/inner\n"+
			"/tmp/cmds3a/m1/sub\u00a0dir/second\u00a0one.txt\n"+
			"spaced\n"+
			"/tmp/cmds3a/tree/a file\n"+
			"/tmp/cmds3a/tree/new file\n"+
			"/tmp/cmds3a/tree/plain\n"+
			"/tmp/cmds3a/tree/sub dir\n"+
			"/tmp/cmds3a/m1/a\u00a0file\n"+
			"/tmp/cmds3a/m1/plain\n"+
			"/tmp/cmds3a/m1/sub\u00a0dir\n"+
			"usage: trfs dir mountpoint\n"+
			"usage\n"+
			"hello\n"+
			"Tmsg.Version(65535,8216,\"9P2000\")\n"+
			"Rmsg.Version(65535,8216,\"9P2000\")\n"+
			"Tmsg.Create(T,F,\"f\",8r666,2)\n"+
			"Rmsg.Create(T,Qid(16r1,0,16r00),8192)\n"+
			"Tmsg.Write(T,F,0,array[6] of byte)\n"+
			"Rmsg.Write(T,6)\n"+
			"usage: usage: styxmon [-d] cmd [arg...]\n"+
			"usage\n"+
			"plain\n"+
			"write     0 bytes, 0 Kb/sec\n"+
			"Message    Count   Low  High  Time   Avg           T        R\n"+
			"    1        2        6        0        0 /tmp/cmds3a/tree/plain\n"+
			"hi\n"+
			"->Tmsg.Version(65535,8216,\"9P2000\")\n"+
			"<-Rmsg.Version(65535,8216,\"9P2000\")\n"+
			"usage: iostats [-d] [-f debugfile] cmds [args ...]\n"+
			"usage\n")
}

// cmds3aRun runs script in the shell and compares its output. The tags
// and fids of Create and Write lines from styxmon depend on the kernel's
// allocation, so they are replaced by T and F.
func cmds3aRun(t *testing.T, script, want string) {
	t.Helper()
	out, st := emu(t, "", "sh", "-c", script)
	out = cmds3aTmsg.ReplaceAllString(out, "${1}(T,F,")
	out = cmds3aRmsg.ReplaceAllString(out, "${1}(T,")
	if out != want || st != "" {
		t.Errorf("got status %q, output:\n%s\nwant:\n%s", st, out, want)
	}
}

var (
	cmds3aTmsg = regexp.MustCompile(`(?m)^(Tmsg\.(?:Create|Write))\(\d+,\d+,`)
	cmds3aRmsg = regexp.MustCompile(`(?m)^(Rmsg\.(?:Create|Write))\(\d+,`)
)
