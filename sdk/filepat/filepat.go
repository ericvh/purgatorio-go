// Package filepat is Inferno's Filepat module (filepat(2)): file name
// patterns with * ? [class] and \ to quote, matched one path element at
// a time.
package filepat

import (
	"sort"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Expand returns the files matching pat, in order, or nil if none do.
func Expand(pat string) []string {
	elem := strings.FieldsFunc(pat, func(r rune) bool { return r == '/' })
	if len(elem) == 0 {
		return dir(pat, "", false)
	}
	var files []string
	if pat[0] == '/' {
		files = []string{"/"}
	}
	for i, e := range elem {
		mustbedir := i < len(elem)-1
		if files == nil {
			files = dir(e, "", mustbedir)
		} else {
			var nf []string
			for _, d := range files {
				nf = append(nf, dir(e, d, mustbedir)...)
			}
			files = nf
		}
		if len(files) == 0 {
			break
		}
	}
	return files
}

func join(dir, name string) string {
	if dir == "/" || dir == "" {
		return dir + name
	}
	return dir + "/" + name
}

func dir(pat, d string, mustbedir bool) []string {
	if pat == "." || pat == ".." {
		return []string{join(d, pat)}
	}
	dirname := d
	if d == "" {
		dirname = "."
	}
	fd, err := sys.Open(dirname, sys.OREAD)
	if err != nil {
		return nil
	}
	defer syscall.Close(fd)
	var ents []sys.Dir
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		ents = append(ents, ds...)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
	var files []string
	for i, e := range ents {
		if i > 0 && ents[i-1].Name == e.Name {
			continue
		}
		if Match(pat, e.Name) && (!mustbedir || e.Mode&sys.DMDIR != 0) {
			files = append(files, join(d, e.Name))
		}
	}
	return files
}
