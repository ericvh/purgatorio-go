package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// timeof is a target's date stamp; 0 if it does not exist. force
// rereads the file rather than using the cached directory listing.
func timeof(name string, force bool) int {
	if strings.Contains(name, "(") {
		return atimeof(force, name)
	}
	if force {
		return mtime(name)
	}
	return filetime(name)
}

func touch(name string) {
	fmt.Fprintf(bout, "touch(%s)\n", name)
	if nflag {
		return
	}
	if strings.Contains(name, "(") {
		atouch(name)
	} else if !chgtime(name) {
		perror(name)
		exit()
	}
}

func deletefile(name string) {
	if !strings.Contains(name, "(") {
		if sys.Remove(name) != nil {
			perror(name)
		}
	} else {
		fmt.Fprintf(os.Stderr, "hoon off; mk can'tdelete archive members\n")
	}
}

// timeinit gives the -w names the current time.
func timeinit(s string) {
	t := now()
	for _, n := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' }) {
		times[n] = t
	}
}

func chgtime(name string) bool {
	if _, err := sys.Stat(name); err == nil {
		d := sys.NullDir()
		d.Mtime = uint32(now())
		return sys.Wstat(name, &d) == nil
	}
	fd, err := sys.Create(name, sys.OWRITE, 0o666)
	if err != nil {
		return false
	}
	syscall.Close(fd)
	return true
}

// dirtime caches the times of the files in dir, named with prefix path.
func dirtime(dir, path string) {
	fd, err := sys.Open(dir, sys.OREAD)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	for {
		db, err := sys.Dirread(fd)
		if err != nil || len(db) == 0 {
			break
		}
		for _, d := range db {
			t := int(d.Mtime)
			if t == 0 {
				continue
			}
			n := path + d.Name
			if _, ok := times[n]; ok {
				continue
			}
			times[n] = t
		}
	}
}

func bulkmtime(dir string, isdir bool) {
	s, prefix := ".", ""
	if isdir {
		s = dir
		if dir == "/" {
			prefix = dir
		} else {
			prefix = dir + "/"
		}
		if d, err := sys.Stat(dir); err == nil && d.Qid.Type&sys.QTDIR == 0 {
			fmt.Fprintf(os.Stderr, "mk: %s is not a directory path=%x\n", dir, uint32(d.Qid.Path))
			exit()
		}
	}
	if bulked[s] {
		return
	}
	bulked[s] = true
	dirtime(s, prefix)
}

func mtime(name string) int {
	i := strings.LastIndexByte(name, '/')
	switch {
	case i == 0:
		bulkmtime("/", true)
	case i > 0:
		bulkmtime(name[:i], true)
	default:
		bulkmtime("", false)
	}
	d, err := sys.Stat(name)
	if err != nil {
		return 0
	}
	return int(d.Mtime)
}

func filetime(name string) int {
	if t, ok := times[name]; ok {
		return t
	}
	return mtime(name)
}

// Archives: names of the form a(b) are member b of the ar(1) archive a.

const (
	armag   = "!<arch>\n"
	sarmag  = 8
	sarname = 16
	sarhdr  = 60
)

var dolong bool

func atimeof(force bool, name string) int {
	archive, member, ok := split(name)
	if !ok {
		exit()
	}
	t := mtime(archive)
	if at, seen := aggs[archive]; seen {
		if force || t > at {
			atimes(archive)
			aggs[archive] = t
		}
	} else {
		atimes(archive)
		aggs[archive] = t
	}
	if !dolong && len(member) > sarname {
		member = member[:sarname]
	}
	return times[archive+"("+member+")"]
}

func atouch(name string) {
	archive, member, ok := split(name)
	if !ok {
		exit()
	}
	fd, err := sys.Open(archive, sys.ORDWR)
	if err != nil {
		fd, err = sys.Create(archive, sys.OWRITE, 0o666)
		if err != nil {
			perror(archive)
			exit()
		}
		writefd(fd, []byte(armag))
	}
	defer syscall.Close(fd)
	if _, ok := times[name]; !ok {
		return
	}
	// Change the member's date in place.
	if _, err := sys.Seek(fd, sarmag, 0); err != nil {
		return
	}
	buf := make([]byte, sarhdr)
	for readfull(fd, buf) {
		if hdrname(buf) == member {
			if _, err := sys.Seek(fd, sarname-sarhdr, 1); err == nil {
				writefd(fd, []byte(fmt.Sprintf("%-12d", now())))
			}
			break
		}
		t := atoi(string(buf[48:58]))
		if t&1 != 0 {
			t++
		}
		if _, err := sys.Seek(fd, int64(t), 1); err != nil {
			break
		}
	}
}

func readfull(fd int, b []byte) bool {
	n := 0
	for n < len(b) {
		m, err := syscall.Read(fd, b[n:])
		if err != nil || m <= 0 {
			return false
		}
		n += m
	}
	return true
}

func hdrname(b []byte) string {
	name := []byte(string(b[:sarname]))
	i := sarname - 1
	for i > 0 && name[i] == ' ' {
		i--
	}
	return string(name[:i+1])
}

func atimes(ar string) {
	fd, err := sys.Open(ar, sys.OREAD)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	magic := make([]byte, sarmag)
	if !readfull(fd, magic) {
		return
	}
	b := make([]byte, sarhdr)
	for readfull(fd, b) {
		t := atoi(string(b[16:28]))
		if t == 0 {
			t = 1
		}
		name := []byte(string(b[:sarname]))
		i := sarname - 1
		for i > 0 && name[i] == ' ' {
			i--
		}
		if name[i] == '/' {
			i--
		}
		times[ar+"("+string(name[:i+1])+")"] = t
		size := atoi(string(b[48:58]))
		if size&1 != 0 {
			size++
		}
		if _, err := sys.Seek(fd, int64(size), 1); err != nil {
			break
		}
	}
}

// typex reports whether file is an archive, or will be.
func typex(file string) bool {
	fd, err := sys.Open(file, sys.OREAD)
	if err != nil {
		if !bitched[file] {
			fmt.Fprintf(bout, "%s doesn't exist: assuming it will be an archive\n", file)
			bitched[file] = true
		}
		return true
	}
	defer syscall.Close(fd)
	buf := make([]byte, sarmag)
	if !readfull(fd, buf) {
		return false
	}
	return string(buf) == armag
}

func split(name string) (string, string, bool) {
	i := strings.IndexByte(name, '(')
	if i < 0 {
		return "", "", false
	}
	archive, member := name[:i], name[i+1:]
	if j := strings.IndexByte(member, ')'); j >= 0 {
		member = member[:j]
	}
	if typex(archive) {
		return archive, member, true
	}
	fmt.Fprintf(os.Stderr, "mk: '%s' is not an archive\n", name)
	return "", member, false
}
