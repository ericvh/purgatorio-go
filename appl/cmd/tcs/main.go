// Command tcs translates text between character sets (Inferno tcs(1)).
// The character sets, their aliases and converters are listed in
// /lib/convcs/charsets; the converters are built in.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const defcsfile = "/lib/convcs/charsets"

var stderr = os.Stderr

func main() {
	a := arg.New(os.Args)
	a.SetUsage("tcs [-C configfile] [-l] [-f ics] [-t ocs] file ...")
	lflag, vflag := false, false
	ics, ocs, csfile := "utf8", "utf8", ""
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'C':
			csfile = a.Arg()
		case 'f':
			ics = a.Arg()
		case 'l':
			lflag = true
		case 't':
			ocs = a.Arg()
		case 'v':
			vflag = true
		default:
			a.Usage()
		}
	}
	files := a.Argv()
	out := bufio.NewWriter(os.Stdout)
	if csfile == "" {
		csfile = defcsfile
	}
	cf, err := readcfg(csfile)
	if err != "" {
		fmt.Fprintf(stderr, "convcs: %s\n", err)
		sys.Exits("fail:init")
	}
	if lflag {
		if len(files) > 0 {
			dumpaliases(out, cf, files[0], vflag)
		} else {
			dumpconvs(out, cf, vflag)
		}
		return
	}
	newstob, err := cf.getstob(ocs)
	if err != "" {
		fmt.Fprintf(stderr, "tcs: %s: %s\n", ocs, err)
		sys.Exits("fail:badarg")
	}
	newbtos, err := cf.getbtos(ics)
	if err != "" {
		fmt.Fprintf(stderr, "tcs: %s: %s\n", ics, err)
		sys.Exits("fail:badarg")
	}
	if len(files) == 0 {
		convert(out, os.Stdin, "standard input", newbtos(), newstob())
		return
	}
	for _, f := range files {
		fd, e := os.Open(f)
		if e != nil {
			fmt.Fprintf(stderr, "tcs: cannot open %s: %s\n", f, sys.Errstr())
			sys.Exits("fail:open")
		}
		convert(out, fd, f, newbtos(), newstob())
		fd.Close()
	}
}

func convert(out *bufio.Writer, in io.Reader, name string, b btos, s stob) {
	buf := make([]byte, 8192)
	unc := 0
	for {
		n, err := in.Read(buf[unc:])
		if n > 0 {
			n += unc
			r, nc := b.btos(buf[:n], false)
			if len(r) > 0 {
				out.Write(s.stob(r))
			}
			unc = copy(buf, buf[nc:n])
			continue
		}
		if err == io.EOF || err == nil {
			break
		}
		fmt.Fprintf(stderr, "tcs: error reading %s: %s\n", name, sys.Errstr())
		sys.Exits("fail:read error")
	}
	if r, _ := b.btos(buf[:unc], true); len(r) > 0 {
		out.Write(s.stob(r))
	}
	out.Write(s.stob(nil))
	if out.Flush() != nil {
		fmt.Fprintf(stderr, "tcs: write error: %s\n", sys.Errstr())
		sys.Exits("fail:write error")
	}
}

func dumpconvs(out *bufio.Writer, cf *cfg, verbose bool) {
	for i, cs := range cf.enum() {
		if !verbose {
			if i > 0 {
				out.WriteByte(' ')
			}
			out.WriteString(cs.name)
			continue
		}
		ms := ""
		switch {
		case cs.btos && cs.stob:
		case cs.btos:
			ms = "(from)"
		case cs.stob:
			ms = "(to)"
		}
		fmt.Fprintf(out, "%s%s\t%s\n", cs.name, ms, cs.desc)
	}
	if !verbose {
		out.WriteByte('\n')
	}
	out.Flush()
}

func dumpaliases(out *bufio.Writer, cf *cfg, cs string, verbose bool) {
	desc, al := cf.aliases(cs)
	if al == nil {
		fmt.Fprintf(stderr, "%s\n", desc)
		return
	}
	if verbose {
		out.WriteString(desc + "\n")
	}
	out.WriteString(strings.Join(al, " ") + "\n")
	out.Flush()
}

// getbtos and getstob find a character set's converter and return a
// function making a fresh one, so that each file starts in the initial
// state.
func (c *cfg) getbtos(cs string) (func() btos, string) {
	impl, targ, err := c.converter(cs, "btos")
	if err != "" {
		return nil, err
	}
	switch impl {
	case "utf8":
		return func() btos { return &utf8btos{} }, ""
	case "utf16":
		big := targ != "le"
		return func() btos { return &utf16btos{big: big} }, ""
	case "cp":
		cp, err := codepage(targ)
		if err != "" {
			return nil, err
		}
		return func() btos { return &cpbtos{cp} }, ""
	}
	return nil, "cannot load converter: " + impl + ": unknown converter"
}

func (c *cfg) getstob(cs string) (func() stob, string) {
	impl, targ, err := c.converter(cs, "stob")
	if err != "" {
		return nil, err
	}
	switch impl {
	case "utf8":
		return func() stob { return utf8stob{} }, ""
	case "utf16":
		big, hdr := targ != "le", targ != "le" && targ != "be"
		return func() stob { return &utf16stob{big: big, header: hdr} }, ""
	case "cp":
		cp, err := codepage(targ)
		if err != "" {
			return nil, err
		}
		m := map[rune]byte{}
		for i := range cp {
			m[cp[i]] = byte(i)
		}
		return func() stob { return cpstob(m) }, ""
	}
	return nil, "cannot load converter: " + impl + ": unknown converter"
}

func (c *cfg) converter(cs, kind string) (string, string, string) {
	cs = strings.ToLower(cs)
	rec, err := c.alias(cs)
	if err != "" {
		return "", "", err
	}
	impl, t := rec.lookup(kind)
	if impl == "" {
		return "", "", "no converter for " + cs
	}
	return impl, t.lookup("arg"), ""
}

// codepage reads a table of the 256 runes that bytes 0 to 255 stand for.
func codepage(path string) ([]rune, string) {
	if path == "" {
		return nil, "codepage path required"
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, path + ": " + sys.Errstr()
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4*256))
	cp := []rune(string(b))
	if len(cp) != 256 {
		return nil, path + ": bad codepage"
	}
	return cp, ""
}
