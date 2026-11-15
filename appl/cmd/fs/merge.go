package main

// merge, setroot, write and mergewrite.

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func compare(cmp cmpchan, d0, d1 *sys.Dir) int {
	mask := 0
	if d0 != nil {
		mask |= 1
	}
	if d1 != nil {
		mask |= 2
	}
	if cmp == nil {
		return mask
	}
	reply := make(chan int)
	cmp <- cmpquery{d0, d1, reply}
	return <-reply & mask
}

func mergerun(_ *Report, opts []option, args []value) value {
	recurse := true
	var cmp cmpchan
	for _, o := range opts {
		switch o.opt {
		case '1':
			recurse = false
		case 'c':
			cmp = vm(o.args[0])
		}
	}
	dst := make(fschan)
	c0, c1 := vx(args[0]), vx(args[1])
	rest := args[2:]
	go mergeproc(c0, c1, dst, recurse, cmp, len(rest) == 0)
	for i, a := range rest {
		dst1 := make(fschan)
		go mergeproc(dst, vx(a), dst1, recurse, cmp, i == len(rest)-1)
		dst = dst1
	}
	return vX{dst}
}

type mergeexit struct{}

// mergeproc merges two trees, whose directories are sorted by name.
func mergeproc(c0, c1, dst fschan, recurse bool, cmp cmpchan, killcmp bool) {
	myreply := make(chan int)
	m0 := <-c0
	m1 := <-c1
	if compare(cmp, m0.d.dir, m1.d.dir) == 2 {
		dst <- fsmsg{fsdata{m1.d.dir, nil}, myreply}
	} else {
		dst <- fsmsg{fsdata{m0.d.dir, nil}, myreply}
	}
	r := <-myreply
	m0.reply <- r
	m1.reply <- r
	if r == Down {
		func() {
			defer func() {
				if x := recover(); x != nil {
					if _, ok := x.(mergeexit); !ok {
						panic(x)
					}
				}
			}()
			mergedir(c0, c1, dst, recurse, cmp)
		}()
	}
	if cmp != nil && killcmp {
		cmp <- cmpquery{}
	}
}

func mergedir(c0, c1, dst fschan, recurse bool, cmp cmpchan) {
	myreply := make(chan int)
	var reply0, reply1 chan int
	var d0, d1 *sys.Dir
	eof0, eof1 := false, false
	for {
		if !eof0 && d0 == nil {
			m := <-c0
			d0, reply0 = m.d.dir, m.reply
			if d0 == nil {
				reply0 <- Next
				eof0 = true
			}
		}
		if !eof1 && d1 == nil {
			m := <-c1
			d1, reply1 = m.d.dir, m.reply
			if d1 == nil {
				reply1 <- Next
				eof1 = true
			}
		}
		if eof0 && eof1 {
			break
		}
		wd0, wd1 := d0, d1
		if d0 != nil && d1 != nil && d0.Name != d1.Name {
			if d0.Name < d1.Name {
				wd1 = nil
			} else {
				wd0 = nil
			}
		}
		var wc0, wc1 fschan
		var wreply0, wreply1 chan int
		var weof1 bool
		c := compare(cmp, wd0, wd1)
		if wd0 != nil && wd1 != nil {
			if c != 0 && recurse && wd0.Mode&wd1.Mode&sys.DMDIR != 0 {
				dst <- fsmsg{fsdata{wd0, nil}, myreply}
				r := <-myreply
				reply0 <- r
				reply1 <- r
				d0, d1 = nil, nil
				switch r {
				case Quit:
					panic(mergeexit{})
				case Skip:
					return
				case Down:
					mergedir(c0, c1, dst, true, cmp)
				}
				continue
			}
			// With a clash that cannot be merged, take c0's, unless
			// cmp says otherwise.
			if c == 2 {
				reply0 <- Next
				d0 = nil
			} else {
				reply1 <- Next
				d1 = nil
			}
		}
		switch {
		case c&1 != 0:
			wd0, wc0, wreply0 = d0, c0, reply0
			wd1, wc1, wreply1, weof1 = d1, c1, reply1, eof1
			d0 = nil
		case c&2 != 0:
			wd0, wc0, wreply0 = d1, c1, reply1
			wd1, wc1, wreply1, weof1 = d0, c0, reply0, eof0
			d1 = nil
		default:
			if wd0 == nil {
				reply1 <- Next
				d1 = nil
			} else {
				reply0 <- Next
				d0 = nil
			}
			continue
		}
		dst <- fsmsg{fsdata{wd0, nil}, myreply}
		r := <-myreply
		wreply0 <- r
		if r == Down {
			r = copyfs(wc0, dst)
		}
		switch r {
		case Quit, Skip:
			if wd1 == nil && !weof1 {
				wreply1 = (<-wc1).reply
			}
			wreply1 <- r
			if r == Quit {
				panic(mergeexit{})
			}
			return
		}
	}
	dst <- fsmsg{fsdata{}, myreply}
	if <-myreply == Quit {
		panic(mergeexit{})
	}
}

func setrootrun(_ *Report, opts []option, args []value) value {
	root := vs(args[0])
	if root == "" && opts == nil {
		fmt.Fprintf(os.Stderr, "fs: setroot: empty path\n")
		return nil
	}
	dst := make(fschan)
	src := vx(args[1])
	cflag := opts != nil
	go setroot(src, dst, root, cflag)
	return vX{dst}
}

func setroot(src, dst fschan, root string, cflag bool) {
	m := <-src
	if cflag {
		createroot(src, dst, root, m.d.dir, m.reply)
		return
	}
	myreply := make(chan int)
	rd := *m.d.dir
	rd.Name = root
	dst <- fsmsg{fsdata{&rd, nil}, myreply}
	if <-myreply == Down {
		m.reply <- Down
		copyfs(src, dst)
	}
}

func createroot(src, dst fschan, root string, d *sys.Dir, reply chan int) {
	if root == "" {
		root = d.Name
	}
	elems := tokenize(root, "/")
	if root[0] == '/' {
		elems = append([]string{"/"}, elems...)
	}
	myreply := make(chan int)
	lev := 0
	r := -1
loop:
	for _, e := range elems {
		rd := *d
		rd.Name = e
		dst <- fsmsg{fsdata{&rd, nil}, myreply}
		r = <-myreply
		switch r {
		case Quit:
			(<-src).reply <- Quit
			return
		case Skip:
			break loop
		case Next:
			lev++
			break loop
		}
		lev++
	}
	if r == Down {
		reply <- Down
		if copyfs(src, dst) == Quit {
			return
		}
	} else {
		reply <- Quit
	}
	for ; lev > 1; lev-- {
		dst <- fsmsg{fsdata{}, myreply}
		if <-myreply == Quit {
			(<-src).reply <- Quit
			return
		}
	}
}

func writerun(r *Report, _ []option, args []value) value {
	sync := make(chan int)
	root := vs(args[0])
	c := vx(args[1])
	errorc := r.start("fswrite")
	proc(func() {
		if <-sync == 0 {
			(<-c).reply <- Quit
			quit(errorc)
		}
		m := <-c
		d := m.d
		if root != "" {
			nd := *d.dir
			nd.Name = root
			d.dir = &nd
		}
		fswritedir(d.dir.Name, d.dir.Name, d, m.reply, c, errorc)
		quit(errorc)
	})
	return vV{sync}
}

// fswritedir writes an entry; path is its name for reports, and file
// the name to create, as Inferno's fs names it relative to the
// directory it has changed to.
func fswritedir(path, file string, d fsdata, dreply chan int, c fschan, errorc chan string) {
	if d.dir.Mode&sys.DMDIR != 0 {
		fd, err := sys.Create(file, sys.OREAD, d.dir.Mode|0o300)
		if err != nil {
			if fd, err = sys.Open(file, sys.OREAD); err != nil {
				dreply <- Next
				report(errorc, fmt.Sprintf("cannot create %s, mode %o: %s", quote.Quote(path), d.dir.Mode|0o300, err))
				return
			}
		}
		defer sys.Close(fd)
		dreply <- Down
		path += "/"
		file += "/"
		for {
			m := <-c
			if m.d.dir == nil {
				m.reply <- Next
				break
			}
			fswritedir(path+m.d.dir.Name, file+m.d.dir.Name, m.d, m.reply, c, errorc)
		}
		if d.dir.Mode&0o300 != 0o300 {
			ws := sys.NullDir()
			ws.Mode = d.dir.Mode
			if sys.Fwstat(fd, &ws) != nil {
				report(errorc, fmt.Sprintf("cannot wstat %s: %s", quote.Quote(path), sys.Errstr()))
			}
		}
		return
	}
	fd, err := sys.Create(file, sys.OWRITE, d.dir.Mode)
	if err != nil {
		dreply <- Next
		report(errorc, fmt.Sprintf("cannot create %s, mode %o: %s", quote.Quote(path), d.dir.Mode|0o300, err))
		return
	}
	defer sys.Close(fd)
	dreply <- Down
	writedata(path, fd, c, errorc)
}

// writedata writes a file's data to fd.
func writedata(path string, fd int, c fschan, errorc chan string) {
	for {
		m := <-c
		if m.d.data == nil {
			m.reply <- Next
			return
		}
		if err := writefd(fd, m.d.data); err != nil {
			report(errorc, fmt.Sprintf("error writing %s: %s", quote.Quote(path), err))
			m.reply <- Skip
			return
		}
		m.reply <- Next
	}
}

func mergewriterun(r *Report, _ []option, args []value) value {
	sync := make(chan int)
	cmp := vm(args[0])
	root := vs(args[1])
	c := vx(args[2])
	errorc := r.start("mergewrite")
	proc(func() {
		if <-sync == 0 {
			(<-c).reply <- Quit
			quit(errorc)
		}
		m := <-c
		d := m.d.dir
		if root != "" {
			nd := *d
			nd.Name = root
			d = &nd
		}
		mergewritedir(d.Name, d.Name, cmp, d, m.reply, c, errorc)
		quit(errorc)
	})
	return vV{sync}
}

func mergewritedir(path, file string, cmp cmpchan, dir *sys.Dir, dreply chan int, c fschan, errorc chan string) {
	if dir.Mode&sys.DMDIR == 0 {
		fd, err := sys.Create(file, sys.OWRITE, dir.Mode)
		if err != nil {
			dreply <- Next
			report(errorc, fmt.Sprintf("cannot create %s, mode %o: %s", quote.Quote(path), dir.Mode|0o300, err))
			return
		}
		defer sys.Close(fd)
		dreply <- Down
		writedata(path, fd, c, errorc)
		return
	}
	fd, err := sys.Create(file, sys.OREAD, dir.Mode|0o300)
	made := err == nil
	if err != nil {
		if fd, err = sys.Open(file, sys.OREAD); err != nil {
			dreply <- Next
			report(errorc, fmt.Sprintf("cannot create %s, mode %o: %s", quote.Quote(path), dir.Mode|0o300, err))
			return
		}
	}
	defer sys.Close(fd)
	dreply <- Down
	var entries []*sys.Dir
	if !made {
		entries, _ = readdir(file)
	}
	i := 0
	eod := false
	var d0, d1 *sys.Dir
	var reply chan int
	path += "/"
	file += "/"
	for {
		if !eod && d0 == nil {
			m := <-c
			d0, reply = m.d.dir, m.reply
			if d0 == nil {
				reply <- Next
				eod = true
			}
		}
		if d1 == nil && i < len(entries) {
			d1 = entries[i]
			i++
		}
		if d0 == nil && d1 == nil {
			break
		}
		wd0, wd1 := d0, d1
		if d0 != nil && d1 != nil && d0.Name != d1.Name {
			if d0.Name < d1.Name {
				wd1 = nil
			} else {
				wd0 = nil
			}
		}
		r := compare(cmp, wd0, wd1)
		if wd1 != nil && r&2 == 0 {
			if wd1.Mode&sys.DMDIR != 0 {
				rmdir(file + wd1.Name)
			} else {
				remove(file + wd1.Name)
			}
			d1 = nil
		}
		if wd0 != nil {
			if r&1 == 0 {
				reply <- Next
			} else {
				mergewritedir(path+wd0.Name, file+wd0.Name, cmp, d0, reply, c, errorc)
			}
			d0 = nil
		}
	}
	if dir.Mode&0o300 != 0o300 {
		ws := sys.NullDir()
		ws.Mode = dir.Mode
		if sys.Fwstat(fd, &ws) != nil {
			report(errorc, fmt.Sprintf("cannot wstat %s: %s", quote.Quote(path), sys.Errstr()))
		}
	}
}

func rmdir(name string) {
	ds, _ := readdir(name)
	for _, d := range ds {
		path := name + "/" + d.Name
		if d.Mode&sys.DMDIR != 0 {
			rmdir(path)
		} else {
			remove(path)
		}
	}
	remove(name)
}

func remove(name string) {
	if err := sys.Remove(name); err != nil {
		fmt.Fprintf(os.Stderr, "mergewrite: cannot remove %s: %s\n", quote.Quote(name), err)
	}
}
