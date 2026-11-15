// Package pedit is the partition table editor that prep(8) and fdisk(8)
// share (Inferno's appl/cmd/disk/prep/pedit.b, from Plan 9). It reads
// commands from standard input:
//
//	. [newdot]            display or set dot
//	a name [start [end]]  add a partition
//	d name                delete a partition
//	h                     print help
//	p                     print the partition table
//	P                     print the commands that give the table to sd(3)
//	w                     write the table
//	q                     quit
//
// A command the editor does not know goes to the Ext function.
package pedit

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/disks"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Maxpart is the most partitions a table may hold.
const Maxpart = 32

// Part is a partition.
type Part struct {
	Name     string
	Ctlname  string
	Start    int64
	End      int64
	Ctlstart int64
	Ctlend   int64
	Changed  bool
	Tag      int
}

// Edit is an editing session; the function fields are the command's own
// parts of the editor.
type Edit struct {
	Disk    *disks.Disk
	Ctlpart []*Part
	Part    []*Part

	Add      func(e *Edit, name string, a, b int64) string
	Del      func(e *Edit, p *Part) string
	Ext      func(e *Edit, f []string) string
	Help     func(e *Edit) string
	Okname   func(e *Edit, name string) string
	Sum      func(e *Edit, p *Part, a, b int64)
	Write    func(e *Edit) string
	Printctl func(e *Edit, w io.Writer)

	Unit string
	Dot  int64
	End  int64

	Changed bool
	warned  bool
	lastcmd byte
}

var bin = bufio.NewReader(os.Stdin)

// Exits ends the command with status s, as Limbo's raise "fail:s" or
// exit.
func Exits(s string) {
	if s != "" {
		sys.Exits("fail:" + s)
	}
	os.Exit(0)
}

// Mk makes an editor whose positions are in unit.
func Mk(unit string) *Edit { return &Edit{Unit: unit} }

// Getline reads the next line of input; at end of file it exits.
func (e *Edit) Getline() string {
	p, err := bin.ReadString('\n')
	if p == "" && err != nil {
		if e.Changed {
			fmt.Fprintf(os.Stderr, "?warning: changes not written\n")
		}
		Exits("")
	}
	return strings.TrimLeft(p, " \t\n\r")
}

// Findpart finds the partition called name.
func (e *Edit) Findpart(name string) *Part {
	for _, p := range e.Part {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func okname(e *Edit, name string) string {
	if name == "" {
		return "partition has no name"
	}
	for _, p := range e.Part {
		if name == p.Name {
			return fmt.Sprintf("already have partition with name '%s'", name)
		}
	}
	return ""
}

// Addpart adds p to the table, in order of start.
func (e *Edit) Addpart(p *Part) string {
	if err := okname(e, p.Name); err != "" {
		return err
	}
	if len(e.Part) >= Maxpart {
		return "too many partitions"
	}
	i := len(e.Part)
	e.Part = append(e.Part, p)
	for ; i > 0 && p.Start < e.Part[i-1].Start; i-- {
		e.Part[i] = e.Part[i-1]
		e.Part[i-1] = p
	}
	if p.Changed {
		e.Changed = true
	}
	return ""
}

// Delpart removes p.
func (e *Edit) Delpart(p *Part) string {
	for i, q := range e.Part {
		if q == p {
			e.Part = append(e.Part[:i:i], e.Part[i+1:]...)
			e.Changed = true
			return ""
		}
	}
	panic("internal error: Part not found")
}

func editdot(e *Edit, argv []string) string {
	if len(argv) == 1 {
		fmt.Printf("\t. %d\n", e.Dot)
		return ""
	}
	if len(argv) > 2 {
		return "args"
	}
	ndot, err := Parseexpr(argv[1], e.Dot, e.End, e.End)
	if err != "" {
		return err
	}
	e.Dot = ndot
	return ""
}

func editadd(e *Edit, argv []string) string {
	if len(argv) < 2 {
		return "args"
	}
	name := argv[1]
	if err := okname(e, name); err != "" {
		return err
	}
	if e.Okname != nil {
		if err := e.Okname(e, name); err != "" {
			return err
		}
	}
	var q string
	if len(argv) >= 3 {
		q = argv[2]
	} else {
		fmt.Fprintf(os.Stderr, "start %s: ", e.Unit)
		q = e.Getline()
	}
	start, err := Parseexpr(q, e.Dot, e.End, e.End)
	if err != "" {
		return err
	}
	if start < 0 || start >= e.End {
		return "start out of range"
	}
	for _, p := range e.Part {
		if p.Start <= start && start < p.End {
			return fmt.Sprintf("start %s in partition '%s'", e.Unit, p.Name)
		}
	}
	maxend := e.End
	for _, p := range e.Part {
		if start < p.Start && p.Start < maxend {
			maxend = p.Start
		}
	}
	if len(argv) >= 4 {
		q = argv[3]
	} else {
		fmt.Fprintf(os.Stderr, "end [%d..%d] ", start, maxend)
		q = e.Getline()
	}
	end, err := Parseexpr(q, e.Dot, maxend, e.End)
	if err != "" {
		return err
	}
	if start == end {
		return "size zero partition"
	}
	if end <= start || end > maxend {
		return "end out of range"
	}
	if len(argv) > 4 {
		return "args"
	}
	if err := e.Add(e, name, start, end); err != "" {
		return err
	}
	e.Dot = end
	return ""
}

func editdel(e *Edit, argv []string) string {
	if len(argv) != 2 {
		return "args"
	}
	p := e.Findpart(argv[1])
	if p == nil {
		return "no such partition"
	}
	return e.Del(e, p)
}

const helptext = ". [newdot] - display or set value of dot\n" +
	"a name [start [end]] - add partition\n" +
	"d name - delete partition\n" +
	"h - print help message\n" +
	"p - print partition table\n" +
	"P - print commands to update sd(3) device\n" +
	"w - write partition table\n" +
	"q - quit\n"

func edithelp(e *Edit, _ []string) string {
	fmt.Print(helptext)
	if e.Help != nil {
		return e.Help(e)
	}
	return ""
}

func editprint(e *Edit, argv []string) string {
	if len(argv) != 1 {
		return "args"
	}
	var lastend int64
	for _, p := range e.Part {
		if lastend < p.Start {
			e.Sum(e, nil, lastend, p.Start)
		}
		e.Sum(e, p, p.Start, p.End)
		lastend = p.End
	}
	if lastend < e.End {
		e.Sum(e, nil, lastend, e.End)
	}
	return ""
}

func editwrite(e *Edit, argv []string) string {
	if len(argv) != 1 {
		return "args"
	}
	if e.Disk.Rdonly {
		return "read only"
	}
	if err := e.Write(e); err != "" {
		return err
	}
	for _, p := range e.Part {
		p.Changed = false
	}
	e.Changed = false
	return ""
}

func editquit(e *Edit, argv []string) string {
	if len(argv) != 1 {
		e.warned = false
		return "args"
	}
	if e.Changed && (!e.warned || e.lastcmd != 'q') {
		e.warned = true
		return "changes unwritten"
	}
	Exits("")
	return ""
}

func editctlprint(e *Edit, argv []string) string {
	if len(argv) != 1 {
		return "args"
	}
	if e.Printctl != nil {
		e.Printctl(e, os.Stdout)
	} else {
		e.Ctldiff(os.Stdout)
	}
	return ""
}

var cmds = []struct {
	c byte
	f func(*Edit, []string) string
}{
	{'.', editdot},
	{'a', editadd},
	{'d', editdel},
	{'?', edithelp},
	{'h', edithelp},
	{'P', editctlprint},
	{'p', editprint},
	{'w', editwrite},
	{'q', editquit},
}

// Runcmd runs one command line.
func (e *Edit) Runcmd(cmd string) {
	f := strings.FieldsFunc(cmd, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(f) < 1 {
		return
	}
	if len(f[0]) != 1 {
		fmt.Fprintf(os.Stderr, "?\n")
		return
	}
	err := ""
	found := false
	for _, c := range cmds {
		if c.c == f[0][0] {
			err = c.f(e, f)
			found = true
			break
		}
	}
	if !found {
		if e.Ext != nil {
			err = e.Ext(e, f)
		} else {
			err = "unknown command"
		}
	}
	if err != "" {
		fmt.Fprintf(os.Stderr, "?%s\n", err)
	}
	e.lastcmd = f[0][0]
}

func ctlmkpart(name string, start, end int64, changed bool) *Part {
	return &Part{Name: name, Ctlname: name, Start: start, End: end, Changed: changed}
}

// rdctlpart reads the partitions sd(3) has inside the one being edited.
func rdctlpart(e *Edit) {
	d := e.Disk
	e.Ctlpart = nil
	if d.Ctlfd == nil {
		return
	}
	d.Ctlfd.Seek(0, io.SeekStart)
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(d.Ctlfd, buf)
	if n <= 0 {
		return
	}
	s := string(buf[:n])
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if !strings.HasPrefix(line, "part ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 4 || f[0] != "part" {
			break
		}
		a, _ := strconv.ParseInt(f[2], 10, 64)
		b, _ := strconv.ParseInt(f[3], 10, 64)
		if a >= b {
			break
		}
		if a < d.Offset || d.Offset+d.Secs < b {
			continue
		}
		a -= d.Offset
		b -= d.Offset
		if f[1] == d.Part {
			continue
		}
		e.Ctlpart = append(e.Ctlpart, ctlmkpart(f[1], a, b, false))
	}
}

func ctlstart(p *Part) int64 {
	if p.Ctlstart != 0 {
		return p.Ctlstart
	}
	return p.Start
}

func ctlend(p *Part) int64 {
	if p.Ctlend != 0 {
		return p.Ctlend
	}
	return p.End
}

func areequiv(p, q *Part) bool {
	if p.Ctlname == "" || q.Ctlname == "" {
		return false
	}
	return p.Ctlname == q.Ctlname && ctlstart(p) == ctlstart(q) && ctlend(p) == ctlend(q)
}

func unchange(e *Edit, p *Part) {
	for _, q := range e.Ctlpart {
		if p.Start <= q.Start && q.End <= p.End {
			q.Changed = false
		}
	}
}

// Ctldiff writes to w the ctl messages that make sd(3)'s partitions match
// the table: delpart for each that changed, then part for each in the
// table. It returns false if a write failed.
func (e *Edit) Ctldiff(w io.Writer) bool {
	rdctlpart(e)
	for _, p := range e.Ctlpart {
		p.Changed = true
	}
	for _, c := range e.Ctlpart {
		for _, p := range e.Part {
			if areequiv(c, p) {
				unchange(e, c)
				break
			}
		}
	}
	ok := true
	for _, p := range e.Ctlpart {
		if p.Changed {
			if _, err := fmt.Fprintf(w, "delpart %s\n", p.Ctlname); err != nil {
				fmt.Fprintf(os.Stderr, "delpart failed: %s: %s\n", p.Ctlname, sys.Errstr())
				ok = false
			}
		}
	}
	offset := e.Disk.Offset
	for _, p := range e.Part {
		if p.Ctlname != "" {
			if _, err := fmt.Fprintf(w, "part %s %d %d\n", p.Ctlname, offset+ctlstart(p), offset+ctlend(p)); err != nil {
				fmt.Fprintf(os.Stderr, "adding part failed: %s: %s\n", p.Ctlname, sys.Errstr())
				ok = false
			}
		}
	}
	return ok
}
