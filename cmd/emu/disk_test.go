package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
)

// The disk and install tools (Inferno's appl/cmd/disk and
// appl/cmd/install) work on image files and trees in a scratch tree
// under root/tmp, which the emu sees as /tmp/m5disk.

const diskRoot = "/tmp/m5disk"

func diskDir() string { return filepath.Join(root, "tmp", "m5disk") }

// diskTime is the fixtures' modification time.
var diskTime = time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)

// diskFiles is the scratch tree each case starts from: a source tree and
// proto files describing it.
var diskFiles = map[string]string{
	"src/dis/a.dis":     "one\n",
	"src/dis/lib/b":     "two\n",
	"src/dis/lib/c":     "",
	"src/README":        "read me\n",
	"src/adm/users":     "",
	"proto":             "dis\n\ta.dis\n\tlib\n\t\tb\n",
	"allproto":          "README\ndis\n\t+\n",
	"users":             "-1:adm:adm:\n0:none:adm:\n9999:noworld::\n100:glenda:glenda:\n",
	"absproto":          "tmp/m5disk/src\n\tdis\n\t\t*\n",
	"badproto":          "dis\n\tnosuch\n\ta.dis 7x\n",
	"lproto":            "f\nd\n\tg\n",
	"srv/f":             "one\n",
	"srv/d/g":           "two\n",
	"empty.log":         "",
	"note.txt":          "some text\n",
	"9load":             "loader\n",
	"src/dis/lib/zeros": strings.Repeat("\x00", 3000) + "end\n",
}

func diskReset(t *testing.T) {
	t.Helper()
	dir := diskDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for name, data := range diskFiles {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"n", "m", "out", "inst", "cli"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			if fi.IsDir() {
				os.Chmod(p, 0o755)
			}
			os.Chtimes(p, diskTime, diskTime)
		}
		return nil
	})
}

// diskRun runs script with sh on both wazero modes, each from a fresh
// scratch tree, and returns the output and status, which must agree.
func diskRun(t *testing.T, script string) (string, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "emuinit.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(diskDir()) })
	var outs, sts []string
	for _, m := range testModes() {
		diskReset(t)
		if err := os.WriteFile(filepath.Join(diskDir(), "s.sh"), []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		var out lockedBuffer
		h := &host.Host{In: strings.NewReader(""), Out: &out}
		sys, err := host.Boot(h, root, getEngines(t)[m])
		if err != nil {
			t.Fatal(err)
		}
		st, err := runWatched(t, sys, m, &out, []string{"sh", diskRoot + "/s.sh"})
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		sys.Init.Exit(st)
		outs, sts = append(outs, out.String()), append(sts, st)
	}
	if len(outs) > 1 && (outs[0] != outs[1] || sts[0] != sts[1]) {
		t.Fatalf("modes disagree:\ncompiler: %q %q\ninterpreter: %q %q", outs[0], sts[0], outs[1], sts[1])
	}
	return outs[0], sts[0]
}

func diskCheck(t *testing.T, script, want string) {
	t.Helper()
	out, st := diskRun(t, script)
	if out != want || st != "" {
		t.Errorf("got status %q, output:\n%s", st, out)
		gl, wl := strings.Split(out, "\n"), strings.Split(want, "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Errorf("first difference at line %d:\ngot  %q\nwant %q", i+1, g, w)
				break
			}
		}
	}
}

// mkfs -a makes an archive, mkext unpacks it.
func TestDiskArchive(t *testing.T) {
	diskCheck(t, `cd /tmp/m5disk
disk/mkfs -a -U inferno -G sys -s src proto > arch
cat arch
disk/mkext -d out < arch
cat out/dis/a.dis out/dis/lib/b
disk/mkext -h < arch
disk/mkext -h -d x < arch /dis/lib
disk/mkfs -a -s src allproto > all
disk/mkext -v -d out < all >[2=1]
cmp src/dis/lib/zeros out/dis/lib/zeros
echo cmp:$status
disk/mkfs -a -x -s src proto
disk/mkfs -a -s src badproto > /dev/null
echo status:$status
disk/mkfs -a -s src noproto
echo status:$status
echo 'end of archive' | disk/mkext -h
echo '/a 644 a b 0' | disk/mkext
echo status:$status
disk/mkfs
echo status:$status
`, `processing proto
file system made
/dis 20000000755 inferno sys 981201600 0
/dis/a.dis 644 inferno sys 981201600 4
one
/dis/lib 20000000755 inferno sys 981201600 0
/dis/lib/b 644 inferno sys 981201600 4
two
end of archive
done
one
two
done
/dis 20000000755 inferno sys 981201600 0
/dis/a.dis 644 inferno sys 981201600 4
/dis/lib 20000000755 inferno sys 981201600 0
/dis/lib/b 644 inferno sys 981201600 4
mkext: can't set modes for /: permission denied
done
x/dis/lib 20000000755 inferno sys 981201600 0
x/dis/lib/b 644 inferno sys 981201600 4
processing allproto
file system made
x out/README 8 bytes
x out/dis/a.dis 4 bytes
x out/dis/lib/b 4 bytes
x out/dis/lib/c 0 bytes
x out/dis/lib/zeros 3004 bytes
done
cmp:
processing proto
file system made
/dis	981201600	0
/dis/a.dis	981201600	4
/dis/lib	981201600	0
/dis/lib/b	981201600	4
end of archive
processing badproto
mkfs: badproto:2: can't stat file src/dis/nosuch: file does not exist
mkfs: badproto:3: bad mode specification 7x
file system made
status:
processing noproto
mkfs: can't open noproto: file does not exist: skipping
file system made
status:skipped protos
done
mkext: too few fields in file header
premature end of archive
status:|eof
usage: mkfs [-aprvxS] [-d root] [-n kfscmdname] [-s src-fs] [-u userfile] [-z n] [-G group] [-U user] proto ...
status:usage
`)
}

// kfs with its console: kfscmd, and mkfs copying into a kfs. The
// unmount ends the first kfs's conversation, so it marks the image clean
// and goes before a second kfs opens the same image under the same name.
func TestDiskKfs(t *testing.T) {
	// mkfs copies into a kfs mounted on /n/kfs.
	kd := filepath.Join(root, "n", "kfs")
	if err := os.MkdirAll(kd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(kd) })
	diskCheck(t, `cd /tmp/m5disk
zeros 512 400 > kfs.img
echo ream
mount -c {kfs -r -b 512 -n m5 kfs.img} /n/kfs
ls /chan
disk/mkfs -n m5 -u users -s src allproto
disk/kfscmd -n m5 users
cat /n/kfs/adm/users
ls -l /n/kfs/README | sed 's/^([^ ]+) . [0-9]+ ([^ ]+) ([^ ]+) +([0-9]+) .*/\1 \2 \3 \4/'
cmp src/dis/lib/zeros /n/kfs/dis/lib/zeros
echo cmp:$status
disk/kfscmd -n m5 check
disk/kfscmd -n m5 'check Pq' 'check pq'
disk/kfscmd -n m5 bogus
echo status:$status
disk/kfscmd -n m5 'check z'
disk/kfscmd -n m5 allowoff sync disallow allow
rm /n/kfs/dis/lib/zeros
disk/kfscmd -n m5 'check q'
unmount /n/kfs
echo again
mount -c {kfs -n m5 kfs.img} n
ls n
unmount n
echo read only
mount -c {kfs -R kfs.img} n
cat n/dis/a.dis
cp src/README n/new
unmount n
echo unclean
dd -if kfs.img -bs 528 -count 1 > k2.img >[2] /dev/null
zeros 1 4 >> k2.img
dd -if kfs.img -bs 4 -iseek 133 >> k2.img >[2] /dev/null
mount -c {kfs -n k2 k2.img} n
disk/kfscmd -n k2 'check q'
ls n
unmount n
echo errors
kfs nosuch
zeros 512 64 > junk
kfs junk
kfs -r -b 1000 junk
disk/kfscmd -n no.such check
echo status:$status
disk/mkfs -n no.such allproto
echo status:$status
`, `ream
kfs: reaming the file system using 512 byte blocks
kfs: initializing minimal user table
/chan/kfs.m5.cmd
processing allproto
file system made
-1:adm:adm:
0:none:adm:
9999:noworld::
100:glenda:glenda:
--rw-r--r-- none none 8
cmp:
checking file system: main
check free list
lo = 13; hi = 399
      10 files
     399 blocks in the file system
      12 used blocks
     387 free blocks
      19 maximum qid path
/adm/users
/README
/dis/a.dis
/dis/lib/b
/dis/lib/c
/dis/lib/zeros
check free list
lo = 13; hi = 399
/
/adm/
/dis/
/dis/lib/
check free list
lo = 13; hi = 399
kfscmd: bogus: unknown kfs request
status:error
kfscmd: 'check z': illegal check flag: z
check free list
lo = 11; hi = 399
again
n/README
n/adm
n/dis
read only
one
cp: cannot create n/new: file system read only
unclean
kfs needs check
check free list
lo = 11; hi = 399
n/README
n/adm
n/dis
errors
kfs: can't open nosuch: file does not exist
kfs: kfs magic in trouble
kfs: invalid block size 1000
kfscmd: can't open /chan/kfs.no.such.cmd: file does not exist
status:error
mkfs: can't open /chan/kfs.no.such.cmd: file does not exist
status:open kfscmd
`)
}

// mbr, fdisk and prep on raw image files: a 12 MB image has 12
// cylinders of the guessed geometry, 64 heads of 32 sectors.
func TestDiskPart(t *testing.T) {
	diskCheck(t, `cd /tmp/m5disk
zeros 512 24576 > img
disk/fdisk -p img
echo status:$status
disk/mbr img
dd -if img -bs 1 -iseek 119 -count 33 >[2] /dev/null
echo
disk/fdisk -p img
disk/fdisk -baw img
disk/fdisk -p img
echo 'p
d p1
a p2 3 5
a p1 5 .+50%
a p3 1 2
a s1 $ $
a p4 11 12
t p2 dos
A p2
A s1
x
p
P
h
q
w
q' | disk/fdisk img
disk/fdisk -p img
echo 't p1
?
FAT32
A p3
p
w
q
q' | disk/fdisk -r img
dd -if img -bs 1 -iseek 446 -count 64 >[2] /dev/null | xd
disk/mbr -9 img
disk/fdisk -p img
echo hello > code
disk/mbr -m code img
dd -if img -bs 1 -count 6 >[2] /dev/null
disk/fdisk -p img
disk/mbr -m 9load nosuch
echo status:$status
zeros 512 24576 > img2
disk/prep -bw -a 9fat -a nvram img2
dd -if img2 -bs 512 -iseek 1 -count 1 >[2] /dev/null | tr -d '\0'
disk/prep -p img2
echo 'p
d 9fat
. 207
.
a fs
.
.+100
a other . $
a bad/name 1 2
a fs 1 2
a x 10 5
a y 0 0
P
q
w
q' | disk/prep img2
disk/prep -p img2
disk/prep -a nosuch img2
echo status:$status
disk/prep -a nvram -a nvram img2
disk/prep -a nvram img2
echo
`, `fdisk: did not find master boot record
status:error
Not a bootable disc or disc error
part plan9 32 24576
cylinder = 1048576 bytes
 * p1                  0 12       (12 cylinders, 12.00 MB) PLAN9
>>>  * p1                  0 12       (12 cylinders, 12.00 MB) PLAN9
>>> >>> >>> >>> >>> ?start out of range
>>> >>> >>> >>> ?cannot set secondary partition active
>>> ?unknown command
>>>    empty               0 1        (1 cylinders, 1.00 MB)
'  p3                  1 2        (1 cylinders, 1.00 MB) PLAN9
   empty               2 3        (1 cylinders, 1.00 MB)
'* p2                  3 5        (2 cylinders, 2.00 MB) PLAN9
'  p1                  5 11       (6 cylinders, 6.00 MB) PLAN9
'  p4                 11 12       (1 cylinders, 1.00 MB) PLAN9
>>> part plan9 2048 4096
part plan9.1 6144 10240
part plan9.2 10240 22528
part plan9.3 22528 24576
>>> . [newdot] - display or set value of dot
a name [start [end]] - add partition
d name - delete partition
h - print help message
p - print partition table
P - print commands to update sd(3) device
w - write partition table
q - quit
A name - set partition active
P - print table in ctl format
R - restore disk back to initial configuration and exit
e - show empty dos partitions
t name [type] - set partition type

>>> ?changes unwritten
>>> >>> part plan9 2048 4096
part plan9.1 6144 10240
part plan9.2 10240 22528
part plan9.3 22528 24576
cylinder = 1048576 bytes
   empty               0 1        (1 cylinders, 1.00 MB)
   p1                  1 2        (1 cylinders, 1.00 MB) PLAN9
   empty               2 3        (1 cylinders, 1.00 MB)
 * p2                  3 5        (2 cylinders, 2.00 MB) PLAN9
   p3                  5 11       (6 cylinders, 6.00 MB) PLAN9
   p4                 11 12       (1 cylinders, 1.00 MB) PLAN9
>>> new partition type [? for list]: EMPTY           FAT12           XENIX           XENIX USR       
FAT16           EXTENDED        FATHUGE         HPFS            
AIXBOOT         AIXDATA         OS/2BOOT        FAT32           
FAT32LBA        EXTHUGE         UNFORMATTED     HPFS2           
IBMRECOVERY     PLAN9           CPM0            DMDDO           
GB              SPEEDSTOR       SYSV386         NETWARE         
PCIX            MINIXV1.3       MINIXV1.5       LINUXSWAP       
LINUX           LINUXEXTENDED   AMOEBA          AMOEBABB        
BSD386          BSDI            BSDISWAP        OTHER           
CPM             DELLRECOVERY    SPEEDSTOR12     SPEEDSTOR16     
LANSTEP         BB              
new partition type [? for list]: >>> >>>    empty               0 1        (1 cylinders, 1.00 MB)
'  p1                  1 2        (1 cylinders, 1.00 MB) FAT32
   empty               2 3        (1 cylinders, 1.00 MB)
'  p2                  3 5        (2 cylinders, 2.00 MB) PLAN9
'* p3                  5 11       (6 cylinders, 6.00 MB) PLAN9
   p4                 11 12       (1 cylinders, 1.00 MB) PLAN9
>>> ?read only
>>> ?changes unwritten
>>> 0000000 00000101 393f2001 00080000 00080000
0000010 80000103 393f2004 00180000 00100000
0000020 00000105 393f200a 00280000 00300000
0000030 0000010b 393f200b 00580000 00080000
0000040 
0000040
part plan9 32 24576
hello
part plan9 32 24576
disk/mbr: opendisk nosuch: file does not exist
status:error
9fat 24575
nvram 1
part 9fat 0 24575
part nvram 24575 24576
part 9fat 0 24575
part nvram 24575 24576
  9fat                0 24575    (24575 sectors, 11.99 MB)
  nvram           24575 24576    (1 sectors, 512 B )
>>>   9fat                0 24575    (24575 sectors, 11.99 MB)
  nvram           24575 24576    (1 sectors, 512 B )
>>> >>> >>> 	. 207
>>> start sector: end [207..24575] >>> >>> ?bad character in name
>>> ?already have partition with name 'fs'
>>> ?end out of range
>>> ?size zero partition
>>> part fs 207 307
part other 307 24575
part nvram 24575 24576
>>> ?changes unwritten
>>> >>> part fs 207 307
part other 307 24575
part nvram 24575 24576
don't know how to create automatic partition nosuch
usage: disk/prep [-bfprw] [-a partname]... [-s sectorsize] /dev/sdC0/plan9
status:usage
you said -a nvram more than once.
usage: disk/prep [-bfprw] [-a partname]... [-s sectorsize] /dev/sdC0/plan9
partitions already exist; not repartitioning
  empty               0 207      (207 sectors, 103.50 KB)
  fs                207 307      (100 sectors, 50.00 KB)
  other             307 24575    (24268 sectors, 11.84 MB)
  nvram           24575 24576    (1 sectors, 512 B )
>>> 
`)
}

// format makes FAT file systems that dossrv reads back.
func TestDiskFormat(t *testing.T) {
	diskCheck(t, `cd /tmp/m5disk
zeros 512 4 > f.img
disk/format -d -l purg f.img 9load note.txt
echo status:$status
ls -l f.img | sed 's/.* ([0-9]+) [A-Z].*/\1/'
dd -if f.img -bs 1 -iseek 3 -count 8 >[2] /dev/null
echo
dd -if f.img -bs 1 -iseek 43 -count 19 >[2] /dev/null
echo
dossrv -f f.img -m m
ls m
cat m/note.txt m/9load
unmount m
zeros 512 24576 > h.img
disk/format -d -t hard -r 2 -c 2 h.img note.txt
dd -if h.img -bs 1 -iseek 11 -count 6 >[2] /dev/null | xd
dossrv -f h.img -m m
cp src/README m/readme.txt
cat m/note.txt m/readme.txt
unmount m
echo 'BOOT CODE that is long enough to cover the parameter block of the FAT, and more' > boot
disk/format -b boot h.img
dd -if h.img -bs 1 -iseek 62 -count 6 >[2] /dev/null
echo
dossrv -f h.img -m m
cat m/readme.txt
unmount m
echo short > short
disk/format -b short h.img
cp f.img 9fat
echo 'part 9fat 0 10' | dd -of p.img -bs 512 -oseek 1 >[2] /dev/null
dd -if f.img -bs 512 -count 2880 >> p.img >[2] /dev/null
disk/format -d p.img
echo status:$status
disk/format -d -t 8inch f.img
disk/format -d nosuch
echo status:$status
disk/format -df new.img
ls new.img
disk/format
echo status:$status
`, `add 9load at clust 2
add note.txt at clust 3
Initializing FAT file system
type 3½HD, 80 tracks, 2 heads, 18 sectors/track, 512 bytes/sec
Adding file 9load, length 7
add 9load at clust 2
Adding file note.txt, length 10
add note.txt at clust 3
used 1536 bytes
status:
1474560
Plan9.00
purg       FAT12   
m/9load
m/note.txt
some text
loader
add note.txt at clust 2
Initializing FAT file system
type hard, 12 tracks, 64 heads, 32 sectors/track, 512 bytes/sec
Adding file note.txt, length 10
add note.txt at clust 2
used 2048 bytes
0000000 00020202 00020000
0000006
some text
read me
used 0 bytes
the FA
read me
warning: pbs too small
warning: pbs too small
used 0 bytes
there's a plan9 partition on the disk
and you didn't specify -r 2 (or greater).
either specify -r 2 or -x to disable this check.
status:failed disk sanity check
format: unknown floppy type 8inch
format: opendisk nosuch: file does not exist
status:error
Initializing FAT file system
type 3½HD, 80 tracks, 2 heads, 18 sectors/track, 512 bytes/sec
used 512 bytes
new.img
usage: disk/format [-df] [-b bootblock] [-c csize] [-l label] [-r nresrv] [-t type] disk [files ...]
status:usage
`)
}
