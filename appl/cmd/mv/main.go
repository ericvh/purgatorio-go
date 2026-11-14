// Command mv moves files (Inferno mv(1)). Within a directory it renames
// with wstat; across directories it copies the file and removes the
// original. Directories can only be renamed.
package main

import (
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

func main() {
	if len(os.Args) < 3 {
		os.Stderr.WriteString("usage: mv fromfile tofile\n")
		os.Stderr.WriteString("       mv fromfile ... todir\n")
		sys.Exits("fail:usage")
	}
	arr := os.Args[1:]
	last := arr[len(arr)-1]
	var todir, toelem string
	if dirto, err := sys.Stat(last); err == nil && dirto.Mode&sys.DMDIR != 0 {
		dirfrom, err := sys.Stat(arr[0])
		if len(arr) == 2 && err == nil && dirfrom.Mode&sys.DMDIR != 0 {
			todir, toelem = split(last)
		} else {
			todir = last
		}
	} else {
		todir, toelem = split(last)
	}
	if len(arr) > 2 && toelem != "" {
		os.Stderr.WriteString("mv: " + last + " not a directory\n")
		sys.Exits("fail:error")
	}
	failed := false
	for _, from := range arr[:len(arr)-1] {
		if mv(from, todir, toelem) < 0 {
			failed = true
		}
	}
	if failed {
		sys.Exits("fail:error")
	}
}

func errf(s string) { os.Stderr.WriteString("mv: " + s + "\n") }

func mv(from, todir, toelem string) int {
	dirb, err := sys.Stat(from)
	if err != nil {
		errf("can't stat " + from + ": " + sys.Errstr())
		return -1
	}
	fromdir, fromelem := split(from)
	fromname := fromdir + fromelem
	if toelem == "" {
		if !strings.HasSuffix(todir, "/") {
			todir += "/"
		}
		toelem = fromelem
	}
	if toelem == "" {
		errf("null last name element moving " + fromname)
		return -1
	}
	toname := todir + toelem
	if samefile(fromdir, todir) {
		if samefile(fromname, toname) {
			errf(fromname + " and " + toname + " are the same")
			return -1
		}
		_, err := sys.Stat(toname)
		if err == nil && dirb.Mode&sys.DMDIR != 0 {
			errf("can't rename a directory to an existing name")
			return -1
		}
		if err == nil {
			hardremove(toname)
		}
		d := sys.NullDir()
		d.Name = toelem
		if sys.Wstat(fromname, &d) == nil {
			return 0
		}
		if dirb.Mode&sys.DMDIR != 0 {
			errf("can't rename directory " + fromname + ": " + sys.Errstr())
			return -1
		}
	}
	// Renaming won't work: copy.
	if dirb.Mode&sys.DMDIR != 0 {
		errf(fromname + " is a directory, not copied to " + toname)
		return -1
	}
	fdf, err := sys.Open(fromname, sys.OREAD)
	if err != nil {
		errf("can't open " + fromname + ": " + sys.Errstr())
		return -1
	}
	ff := os.NewFile(uintptr(fdf), fromname)
	fdt, err := sys.Create(toname, sys.OWRITE, dirb.Mode)
	if err != nil {
		ff.Close()
		errf("can't create " + toname + ": " + sys.Errstr())
		return -1
	}
	tf := os.NewFile(uintptr(fdt), toname)
	stat := copy1(ff, tf, fromname, toname)
	ff.Close()
	tf.Close()
	// As in Inferno, the original goes even when the copy failed.
	if err := sys.Remove(fromname); err != nil {
		errf("can't remove " + fromname + ": " + sys.Errstr())
		return -1
	}
	return stat
}

func copy1(ff, tf *os.File, from, to string) int {
	buf := make([]byte, atomicio)
	for {
		n, err := ff.Read(buf)
		if n > 0 {
			if w, _ := tf.Write(buf[:n]); w != n {
				errf("error writing " + to + ": " + sys.Errstr())
				return -1
			}
		}
		if err == io.EOF {
			return 0
		}
		if err != nil {
			errf("error reading " + from + ": " + sys.Errstr())
			return -1
		}
	}
}

// split divides name at its last slash, keeping the slash with the
// directory part; a bare name is in "./".
func split(name string) (string, string) {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i+1], name[i+1:]
	}
	if name == ".." {
		return "../", "."
	}
	return "./", name
}

func samefile(a, b string) bool {
	if a == b {
		return true
	}
	da, err1 := sys.Stat(a)
	db, err2 := sys.Stat(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return da.Qid.Path == db.Qid.Path && da.Qid.Vers == db.Qid.Vers && da.Dev == db.Dev && da.Type == db.Type
}

func hardremove(a string) {
	if err := sys.Remove(a); err != nil {
		errf("can't remove " + a + ": " + sys.Errstr())
		sys.Exits("fail:mv")
	}
	for sys.Remove(a) == nil {
	}
}
