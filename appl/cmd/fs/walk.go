package main

// walk, proto, entries, print, ls and size.

import (
	"fmt"
	"os"
	"sort"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

// readdir reads a directory, sorted by name with duplicates removed,
// as Readdir->init with NAME|COMPACT.
func readdir(path string) ([]*sys.Dir, error) {
	fd, err := sys.Open(path, sys.OREAD)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	var a []*sys.Dir
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil, err
		}
		if len(ds) == 0 {
			break
		}
		for i := range ds {
			a = append(a, &ds[i])
		}
	}
	sort.SliceStable(a, func(i, j int) bool { return a[i].Name < a[j].Name })
	var r []*sys.Dir
	for i, d := range a {
		if i > 0 && a[i-1].Name == d.Name {
			continue
		}
		r = append(r, d)
	}
	return r, nil
}

func walkrun(r *Report, _ []option, args []value) value {
	path := vs(args[0])
	d, err := sys.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fs: walk: cannot stat %s: %s\n", quote.Quote(path), err)
		return nil
	}
	if d.Mode&sys.DMDIR == 0 {
		fmt.Fprintf(os.Stderr, "fs: walk: %s is not a directory\n", quote.Quote(path))
		return nil
	}
	c := make(fschan)
	errorc := r.start("walk")
	proc(func() { fswalkproc(path, c, atomicio, errorc) })
	return vX{c}
}

// loopcheck holds the directories on the current path.
type loopcheck map[[3]uint64]bool

func dirkey(d *sys.Dir) [3]uint64 {
	return [3]uint64{d.Qid.Path, uint64(d.Qid.Type)<<16 | uint64(d.Type), uint64(d.Dev)}
}

// The walk uses full path names where Inferno's changes directory.
func fswalkproc(path string, c fschan, blocksize int, errorc chan string) {
	d, err := sys.Stat(path)
	if err != nil {
		report(errorc, fmt.Sprintf("cannot stat %s: %s", quote.Quote(path), err))
		sendnulldir(c)
		quit(errorc)
	}
	d.Name = path
	reply := make(chan int)
	c <- fsmsg{fsdata{d, nil}, reply}
	if <-reply == Down {
		lc := loopcheck{dirkey(d): true}
		if path[len(path)-1] != '/' {
			path += "/"
		}
		fswalkdir(path, c, blocksize, lc, errorc)
		c <- fsmsg{fsdata{}, reply}
		<-reply
	}
	quit(errorc)
}

func fswalkdir(path string, c fschan, blocksize int, lc loopcheck, errorc chan string) {
	reply := make(chan int)
	a, err := readdir(path)
	if err != nil {
		report(errorc, fmt.Sprintf("cannot readdir %s: %s", quote.Quote(path), err))
		return
	}
	loop := make([]bool, len(a))
	for i, d := range a {
		if d.Mode&sys.DMDIR != 0 {
			if lc[dirkey(d)] {
				loop[i] = true
			} else {
				lc[dirkey(d)] = true
			}
		}
	}
	defer func() {
		for i, d := range a {
			if d.Mode&sys.DMDIR != 0 && !loop[i] {
				delete(lc, dirkey(d))
			}
		}
	}()
	for i, d := range a {
		if d.Mode&sys.DMDIR != 0 {
			if loop[i] {
				report(errorc, "filesystem loop at "+quote.Quote(path+d.Name))
				continue
			}
			c <- fsmsg{fsdata{d, nil}, reply}
			switch <-reply {
			case Quit:
				quit(errorc)
			case Down:
				fswalkdir(path+d.Name+"/", c, blocksize, lc, errorc)
				c <- fsmsg{fsdata{}, reply}
				if <-reply == Quit {
					quit(errorc)
				}
			case Skip:
				return
			}
		} else if fswalkfile(path, d, c, blocksize, errorc) == Skip {
			return
		}
	}
}

// sendfile sends a file's entry and, if asked, its contents.
func sendfile(path string, d *sys.Dir, c fschan, blocksize int, errorc chan string) int {
	reply := make(chan int)
	fd, err := sys.Open(path, sys.OREAD)
	if err != nil {
		report(errorc, fmt.Sprintf("cannot open %s: %s", quote.Quote(path), err))
		return Next
	}
	defer syscall.Close(fd)
	c <- fsmsg{fsdata{d, nil}, reply}
	switch r := <-reply; r {
	case Quit:
		quit(errorc)
	case Skip, Next:
		return r
	}
	length := d.Length
	for n := uint64(0); n < length; {
		nr := blocksize
		if n+uint64(blocksize) > length {
			nr = int(length - n)
		}
		buf := make([]byte, nr)
		nr, err := syscall.Read(fd, buf)
		if err != nil || nr <= 0 {
			if err != nil {
				report(errorc, fmt.Sprintf("error reading %s: %s", quote.Quote(path), sys.Errstr()))
			} else {
				report(errorc, fmt.Sprintf("%s is shorter than expected (%d/%d)", quote.Quote(path), n, length))
			}
			break
		}
		buf = buf[:nr]
		c <- fsmsg{fsdata{nil, buf}, reply}
		switch <-reply {
		case Quit:
			quit(errorc)
		case Skip:
			return Next
		}
		n += uint64(nr)
	}
	c <- fsmsg{fsdata{}, reply}
	if <-reply == Quit {
		quit(errorc)
	}
	return Next
}

func fswalkfile(path string, d *sys.Dir, c fschan, blocksize int, errorc chan string) int {
	return sendfile(path+d.Name, d, c, blocksize, errorc)
}

func entriesrun(_ *Report, _ []option, args []value) value {
	sc := entrychan{make(chan int), make(chan entry)}
	src := vx(args[0])
	proc(func() { entriesproc(src, sc) })
	return vT{sc}
}

func entriesproc(c fschan, sc entrychan) {
	if <-sc.sync == 0 {
		(<-c).reply <- Quit
		return
	}
	indent := 0
	var names []string
	name := ""
	for {
		m := <-c
		d := m.d
		if d.dir != nil {
			var p string
			depth := indent
			if d.dir.Mode&sys.DMDIR != 0 {
				names = append(names, name)
				if indent == 0 {
					name = d.dir.Name
				} else {
					if name[len(name)-1] != '/' {
						name += "/"
					}
					name += d.dir.Name
				}
				indent++
				m.reply <- Down
				p = name
			} else {
				p = name
				if p[len(p)-1] != '/' {
					p += "/"
				}
				p += d.dir.Name
				m.reply <- Next
			}
			if p != "" {
				sc.c <- entry{d.dir, p, depth}
			}
		} else {
			m.reply <- Next
			if d.dir == nil && d.data == nil {
				indent--
				if indent == 0 {
					break
				}
				name, names = names[len(names)-1], names[:len(names)-1]
			}
		}
	}
	sc.c <- entry{}
}

func printrun(r *Report, _ []option, args []value) value {
	sync := make(chan int)
	c := vt(args[0])
	errorc := r.start("print")
	proc(func() {
		if <-sync == 0 {
			c.sync <- 0
			quit(errorc)
		}
		c.sync <- 1
		for e := <-c.c; e.dir != nil; e = <-c.c {
			fmt.Printf("%s\n", e.path)
		}
		quit(errorc)
	})
	return vV{sync}
}

func sizerun(r *Report, _ []option, args []value) value {
	sync := make(chan int)
	c := vt(args[0])
	errorc := r.start("size")
	proc(func() {
		if <-sync == 0 {
			c.sync <- 0
			quit(errorc)
		}
		c.sync <- 1
		var size uint64
		for e := <-c.c; e.dir != nil; e = <-c.c {
			size += e.dir.Length
		}
		fmt.Printf("%d\n", size)
		quit(errorc)
	})
	return vV{sync}
}

func lsrun(r *Report, opts []option, args []value) value {
	sync := make(chan int)
	c := vt(args[0])
	errorc := r.start("ls")
	proc(func() {
		now := daytime.Now()
		mflag, uflag := false, false
		if <-sync == 0 {
			c.sync <- 0
			quit(errorc)
		}
		c.sync <- 1
		for _, o := range opts {
			switch o.opt {
			case 'm':
				mflag = true
			case 'u':
				uflag = true
			}
		}
		for e := <-c.c; e.dir != nil; e = <-c.c {
			d := e.dir
			t := d.Mtime
			if uflag {
				t = d.Atime
			}
			s := fmt.Sprintf("%s %c %d %s %s %d %s %s\n", modes(d.Mode), rune(d.Type), d.Dev,
				d.UID, d.GID, d.Length, daytime.Filet(now, int(t)), e.path)
			if mflag {
				s = "[" + d.MUID + "] " + s
			}
			fmt.Print(s)
		}
		quit(errorc)
	})
	return vV{sync}
}
