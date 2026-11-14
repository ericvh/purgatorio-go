package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// cmds3bDir is the group's scratch directory: /tmp/cmds3b in the emu.
var cmds3bDir = filepath.Join(root, "tmp", "cmds3b")

// cmds3bFATImage builds a FAT12 (or, with fat16, FAT16) image holding
// HELLO.TXT, a file with a long name spanning two clusters, and a
// directory SUBDIR with NOTE.TXT, all dated 2020-01-02 03:04:06.
func cmds3bFATImage(fat16 bool) []byte {
	le := binary.LittleEndian
	nsect, fatsize, rootents := 2880, 9, 224
	if fat16 {
		nsect, fatsize, rootents = 4608, 18, 512
	}
	img := make([]byte, nsect*512)
	b := img[:512]
	copy(b, []byte{0xeb, 0x3c, 0x90})
	copy(b[3:], "PURGFAT ")
	le.PutUint16(b[11:], 512)
	b[13] = 1 // sectors per cluster
	le.PutUint16(b[14:], 1)
	b[16] = 2
	le.PutUint16(b[17:], uint16(rootents))
	le.PutUint16(b[19:], uint16(nsect))
	b[21] = 0xf8
	le.PutUint16(b[22:], uint16(fatsize))
	le.PutUint16(b[24:], 9)
	le.PutUint16(b[26:], 2)
	b[38] = 0x29
	copy(b[43:], "NO NAME    FAT     ")
	b[510], b[511] = 0x55, 0xaa
	rootaddr := 1 + 2*fatsize
	dataaddr := rootaddr + rootents*32/512
	setfat := func(n, v int) {
		for f := 0; f < 2; f++ {
			fat := img[(1+f*fatsize)*512:]
			if fat16 {
				le.PutUint16(fat[2*n:], uint16(v))
			} else if o := n * 3 / 2; n&1 == 0 {
				fat[o] = byte(v)
				fat[o+1] = fat[o+1]&0xf0 | byte(v>>8)&0x0f
			} else {
				fat[o] = fat[o]&0x0f | byte(v<<4)
				fat[o+1] = byte(v >> 4)
			}
		}
	}
	eoc := 0xfff
	if fat16 {
		eoc = 0xffff
	}
	setfat(0, eoc&^7|0x8)
	setfat(1, eoc)
	clust := func(c int) []byte { return img[(dataaddr+c-2)*512:] }
	ent := func(d []byte, name string, attr byte, start, length int) {
		copy(d, name)
		d[11] = attr
		le.PutUint16(d[22:], 3<<11|4<<5|3)
		le.PutUint16(d[24:], 40<<9|1<<5|2)
		le.PutUint16(d[26:], uint16(start))
		le.PutUint32(d[28:], uint32(length))
	}
	rootdir := img[rootaddr*512:]
	hello := "hello, dos\n"
	copy(clust(2), hello)
	setfat(2, eoc)
	ent(rootdir[0:], "HELLO   TXT", 0x20, 2, len(hello))
	// The long name's entries come before its short entry, last part
	// first.
	long, short := "Long File Name.txt", "LONGFI~1TXT"
	sum := 0
	for i := 0; i < 11; i++ {
		sum = ((sum&1)<<7 | (sum&0xfe)>>1 + int(short[i])) & 0xff
	}
	u := append(utf16.Encode([]rune(long)), 0)
	for len(u)%13 != 0 {
		u = append(u, 0xffff)
	}
	nl := len(u) / 13
	for i := 0; i < nl; i++ {
		seq := nl - i
		d := rootdir[32*(1+i):]
		d[0] = byte(seq)
		if i == 0 {
			d[0] |= 0x40
		}
		d[11] = 0x0f
		d[13] = byte(sum)
		for k, o := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
			le.PutUint16(d[o:], u[(seq-1)*13+k])
		}
	}
	data := strings.Repeat("0123456789", 60)
	copy(clust(3), data[:512])
	copy(clust(4), data[512:])
	setfat(3, 4)
	setfat(4, eoc)
	ent(rootdir[32*(1+nl):], short, 0x20, 3, len(data))
	ent(rootdir[32*(2+nl):], "SUBDIR     ", 0x10, 5, 0)
	setfat(5, eoc)
	sd := clust(5)
	ent(sd[0:], ".          ", 0x10, 5, 0)
	ent(sd[32:], "..         ", 0x10, 0, 0)
	note := "a note\n"
	ent(sd[64:], "NOTE    TXT", 0x20, 6, len(note))
	copy(clust(6), note)
	setfat(6, eoc)
	return img
}

// cmds3bSetup makes the scratch directory with the given files.
func cmds3bSetup(t *testing.T, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(cmds3bDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if data == nil {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Each script starts from pristine copies, since emu runs it once per
// wazero mode.
const cmds3bDosScript = `m=/tmp/cmds3b/m
cp /tmp/cmds3b/f12.orig /tmp/cmds3b/f12.img
cp /tmp/cmds3b/f16.orig /tmp/cmds3b/f16.img
dossrv -f /tmp/cmds3b/f12.img -m $m
ls -l $m
cat $m/hello.txt $m/subdir/note.txt
wc -c $m/'Long File Name.txt'
ls $m/subdir/..
echo new file > $m/new.txt
echo mixed case > $m/'A Much Longer Name.text'
mkdir $m/newdir $m/newdir/deeper
echo deep > $m/newdir/deeper/x
ls $m/newdir/deeper/../..
cat /lib/hello.txt /lib/hello.txt > $m/subdir/two
rm $m/hello.txt
rm $m/newdir
mv $m/new.txt $m/renamed.txt
mv $m/subdir/note.txt $m/subdir/'Renamed Note.txt'
mkdir $m/subdir
unmount $m
dossrv -f /tmp/cmds3b/f12.img -m $m
ls -p $m $m/subdir $m/newdir $m/newdir/deeper
cat $m/renamed.txt $m/'A Much Longer Name.text' $m/newdir/deeper/x $m/subdir/two $m/subdir/'Renamed Note.txt'
rm $m/newdir/deeper/x $m/newdir/deeper $m/newdir
ls -p $m
unmount $m
dossrv -f /tmp/cmds3b/f16.img -m $m
cp /tmp/cmds3b/big $m/big.bin
chmod 444 $m/hello.txt
ls -l $m/hello.txt
cp /lib/hello.txt $m/hello.txt
chmod 666 $m/hello.txt
echo more >> $m/hello.txt
load std
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 {echo $i > $m/subdir/'file number '^$i}
unmount $m
dossrv -f /tmp/cmds3b/f16.img -m $m
cmp /tmp/cmds3b/big $m/big.bin
echo cmp $status end
cat $m/hello.txt
ls $m/subdir | wc -l
cat $m/subdir/'file number 1' $m/subdir/'file number 20'
rm $m/subdir/'file number 1'
ls -l $m/subdir/note.txt
unmount $m
dossrv -f /tmp/cmds3b/big -m $m
dossrv -f /tmp/cmds3b/nonexist -m $m
dossrv -m $m
`

const cmds3bDosOut = `--rw-rw-rw- M 1 dos srv 600 Jan 02  2020 /tmp/cmds3b/m/Long File Name.txt
--rw-rw-rw- M 1 dos srv  11 Jan 02  2020 /tmp/cmds3b/m/hello.txt
d-rwxrwxrwx M 1 dos srv   0 Jan 02  2020 /tmp/cmds3b/m/subdir
hello, dos
a note
    600 /tmp/cmds3b/m/Long File Name.txt
/tmp/cmds3b/m/subdir/../Long File Name.txt
/tmp/cmds3b/m/subdir/../hello.txt
/tmp/cmds3b/m/subdir/../subdir
/tmp/cmds3b/m/newdir/deeper/../../A Much Longer Name.text
/tmp/cmds3b/m/newdir/deeper/../../Long File Name.txt
/tmp/cmds3b/m/newdir/deeper/../../hello.txt
/tmp/cmds3b/m/newdir/deeper/../../new.txt
/tmp/cmds3b/m/newdir/deeper/../../newdir
/tmp/cmds3b/m/newdir/deeper/../../subdir
rm: /tmp/cmds3b/m/newdir: permission denied
mkdir: /tmp/cmds3b/m/subdir already exists
A Much Longer Name.text
Long File Name.txt
newdir
renamed.txt
subdir
Renamed Note.txt
two
deeper
x
new file
mixed case
deep
hello from the root
hello from the root
a note
A Much Longer Name.text
Long File Name.txt
renamed.txt
subdir
--r--r--r-- M 3 dos srv 11 Jan 02  2020 /tmp/cmds3b/m/hello.txt
cp: cannot create /tmp/cmds3b/m/hello.txt: permission denied
cmp  end
hello, dos
more
     21
1
20
--rw-rw-rw- M 4 dos srv 7 Jan 02  2020 /tmp/cmds3b/m/subdir/note.txt
dossrv: mount /tmp/cmds3b/m: unknown format
dossrv: mount /tmp/cmds3b/m: can't open /tmp/cmds3b/nonexist: file does not exist
usage: dossrv [-v] [-s] [-F] [-c] [-S secpertrack] [-f devicefile] [-m mountpoint]
`

// TestCmds3BDossrv serves FAT12 and FAT16 images made here, changes them
// through dossrv and checks that the changes are there after a remount.
func TestCmds3BDossrv(t *testing.T) {
	big := make([]byte, 30000)
	x := uint32(7)
	for i := range big {
		x = x*1103515245 + 12345
		big[i] = byte(x >> 16)
	}
	cmds3bSetup(t, map[string][]byte{
		"f12.orig": cmds3bFATImage(false),
		"f16.orig": cmds3bFATImage(true),
		"big":      big,
		"dos.sh":   []byte(cmds3bDosScript),
		"m":        nil,
	})
	out, st := emu(t, "", "sh", "/tmp/cmds3b/dos.sh")
	if out != cmds3bDosOut || st != "fail:usage" {
		t.Errorf("got %q %q\nwant %q", out, st, cmds3bDosOut)
	}
}

// The lock test holds a write lock on a until /tmp/cmds3b/go appears,
// and checks that a reader waits for it. lockfs's standard error goes to
// /dev/null: the emu's mount driver clunks some fids twice, and lockfs
// reports each as a bad fid.
const cmds3bLockScript = `load std
d=/tmp/cmds3b/ld
m=/tmp/cmds3b/lm
rm -f $d/c $d/d /tmp/cmds3b/go /tmp/cmds3b/out
echo one > $d/a
echo two > $d/b
lockfs $d $m >[2] /dev/null
ls -p $m
cat $m/a
echo three > $m/c
cat $d/c
echo four >> $m/a
cat $m/a
rm $m/b
mkdir $m/x
mv $m/c $m/d
ls -p $m $d
{echo start; while {! test -e /tmp/cmds3b/go} {sleep 1}; echo end} > $m/a &
while {! ~ ` + "`" + `{cat $d/a} start} {sleep 1}
rm $m/a
cat $m/a > /tmp/cmds3b/out &
sleep 1
echo before ` + "`" + `{cat /tmp/cmds3b/out}
echo > /tmp/cmds3b/go
while {! ~ ` + "`" + `{cat /tmp/cmds3b/out} start} {sleep 1}
cat /tmp/cmds3b/out
lockfs -A -p tcp!127.0.0.1!34519 $d >[2] /dev/null
mount -A tcp!127.0.0.1!34519 /tmp/cmds3b/ln
ls -p /tmp/cmds3b/ln
cat /tmp/cmds3b/ln/d
unmount /tmp/cmds3b/ln
ps | grep lockfs | getlines {kill ${hd ${split ' ' $line}}}
lockfs -p tcp!127.0.0.1!34520 $d
lockfs
`

const cmds3bLockOut = `a
b
one
three
one
four
mkdir: can't create /tmp/cmds3b/lm/x: permission denied
a
d
a
d
sub
rm: /tmp/cmds3b/lm/a: file is locked
before
start
end
a
d
three
lockfs: cannot init auth: keyring authentication is not available; use -A
usage: lockfs [-A] [-a alg]... [-p addr] dir [mountpoint]
`

// TestCmds3BLockfs runs lockfs over a directory of the host, mounted
// locally and served on a loopback port. The script kills the servers at
// the end: emu runs it twice in one process, and the port must be free.
func TestCmds3BLockfs(t *testing.T) {
	cmds3bSetup(t, map[string][]byte{
		"ld/sub":  nil,
		"lm":      nil,
		"ln":      nil,
		"lock.sh": []byte(cmds3bLockScript),
	})
	out, st := emu(t, "", "sh", "/tmp/cmds3b/lock.sh")
	if out != cmds3bLockOut || st != "fail:usage" {
		t.Errorf("got %q %q\nwant %q", out, st, cmds3bLockOut)
	}
}
