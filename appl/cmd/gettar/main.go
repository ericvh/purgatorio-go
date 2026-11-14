// Command gettar unpacks the tar archive on standard input into the
// current directory tree (Inferno gettar(1)).
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	tblock = 512
	nblock = 20
)

type header struct {
	name              string
	size, mode, mtime int
	skip              bool
}

var (
	tarbuf        = make([]byte, nblock*tblock)
	nblk          = nblock
	recno         = nblock
	verbose, keep bool
	absolute      bool
)

func fail(mess string) {
	fmt.Fprintf(os.Stderr, "gettar: %s\n", mess)
	sys.Exits("fail:error")
}

func getblock() []byte {
	if recno >= nblk {
		i, err := os.Stdin.Read(tarbuf)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			fail("read error: " + sys.Errstr())
		}
		if i%tblock != 0 {
			fail("blocksize error")
		}
		nblk = i / tblock
		recno = 0
	}
	recno++
	return tarbuf[(recno-1)*tblock : recno*tblock]
}

func getdir() *header {
	b := getblock()
	if b == nil || b[0] == 0 {
		return nil
	}
	name := nullterm(b[0:100])
	if b[345] != 0 {
		name = nullterm(b[345:500]) + "/" + name
	}
	if !absolute {
		if name[0] == '#' {
			name = "./" + name
		} else if name[0] == '/' {
			name = "." + name
		}
	}
	magic := b[257:262]
	if magic[0] != 0 && string(magic) != "ustar" {
		fail("bad magic " + name)
	}
	chk := octal(b[148:156])
	copy(b[148:156], "        ")
	for _, c := range b {
		chk -= int(c)
	}
	if chk != 0 {
		fail("directory checksum error " + name)
	}
	h := &header{name: name, skip: true}
	switch t := b[156]; t {
	case '0', '7', 0:
		h.skip = false
		h.size = octal(b[124:136])
		h.mode = 0o777 & octal(b[100:108])
		h.mtime = octal(b[136:148])
	case '1':
		fmt.Fprintf(os.Stderr, "gettar: skipping link %s -> %s\n", name, nullterm(b[157:257]))
	case '2', 's':
		fmt.Fprintf(os.Stderr, "gettar: skipping symlink %s\n", name)
	case '3', '4', '6':
		fmt.Fprintf(os.Stderr, "gettar: skipping special file %s\n", name)
	case '5':
		if strings.HasSuffix(name, "/") {
			checkdir(name + ".")
		} else {
			checkdir(name + "/.")
		}
	default:
		fail(fmt.Sprintf("unrecognized typeflag %d for %s", t, name))
	}
	return h
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("gettar [-kTRv] [file ...]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'k':
			keep = true
		case 'v':
			verbose = true
		case 'R':
			absolute = true
		default:
			a.Usage()
		}
	}
	names := a.Argv()
	for h := getdir(); h != nil; h = getdir() {
		var out *os.File
		if !h.skip {
			if (len(names) == 0 || matched(h.name, names)) && !(keep && exists(h.name)) {
				if verbose {
					fmt.Fprintf(os.Stderr, "%s\n", h.name)
				}
				checkdir(h.name)
				fd, err := sys.Create(h.name, sys.OWRITE, 0o666)
				if err != nil {
					fmt.Fprintf(os.Stderr, "gettar: cannot create %s: %s\n", h.name, sys.Errstr())
					h.skip = true
				} else {
					out = os.NewFile(uintptr(fd), h.name)
				}
			} else {
				h.skip = true
			}
		}
		bytes := h.size
		blocks := (bytes + tblock - 1) / tblock
		if h.skip {
			for ; blocks > 0; blocks-- {
				getblock()
			}
			continue
		}
		for ; blocks > 0; blocks-- {
			buf := getblock()
			n := min(bytes, tblock)
			if buf == nil {
				fail("write error for " + h.name + ": " + sys.Errstr())
			}
			if _, err := out.Write(buf[:n]); err != nil {
				fail("write error for " + h.name + ": " + sys.Errstr())
			}
			bytes -= n
		}
		out.Close()
		d := sys.NullDir()
		d.Mode = uint32(h.mode)
		d.Mtime = uint32(h.mtime)
		if sys.Wstat(h.name, &d) != nil {
			d.Mtime = ^uint32(0) // try just the mode
			if sys.Wstat(h.name, &d) != nil {
				fmt.Fprintf(os.Stderr, "gettar: cannot set mode/mtime %s %#o %d: %s\n",
					h.name, h.mode, uint32(h.mtime), sys.Errstr())
			}
		}
	}
}

// checkdir makes the directories leading to name.
func checkdir(name string) {
	comps := strings.FieldsFunc(name, func(r rune) bool { return r == '/' })
	path := ""
	for i, c := range comps {
		if c == ".." {
			fail(".. pathnames forbidden")
		}
		if i == len(comps)-1 {
			break
		}
		if path == "" {
			path = c
		} else {
			path += "/" + c
		}
		st, err := sys.Stat(path)
		if err != nil {
			fd, err := sys.Create(path, sys.OREAD, sys.DMDIR|0o777)
			if err != nil {
				fail("cannot mkdir " + path + ": " + sys.Errstr())
			}
			os.NewFile(uintptr(fd), path).Close()
		} else if st.Mode&sys.DMDIR == 0 {
			fail("found non-directory at " + path)
		}
	}
}

func exists(path string) bool {
	_, err := sys.Stat(path)
	return err == nil
}

func matched(n string, names []string) bool {
	for _, p := range names {
		if strings.HasPrefix(n, p) && (len(p) == len(n) || n[len(p)] == '/') {
			return true
		}
	}
	return false
}

func octal(b []byte) int {
	sum := 0
	for _, c := range b {
		if c == ' ' {
			continue
		}
		if c == 0 {
			break
		}
		sum = 8*sum + int(c) - '0'
	}
	return sum
}

func nullterm(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
