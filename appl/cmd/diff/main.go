// Command diff compares files or directories line by line
// (Inferno diff(1)), using Harold Stone's algorithm as Inferno and
// Plan 9 do: lines are hashed, the longest common subsequence of hashes
// is found, and spurious matches from hash collisions are broken.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const usageText = "usage: diff [ -efbwr ] file1 ... file2"

const (
	reg = iota
	bin
)

const halfint = 16

type cand struct{ x, y, pred int }

type line struct {
	serial int
	value  int32
}

var (
	mode  rune // 0, 'e' or 'f'
	bflag int  // 1: ignore runs of blanks; 2: ignore all blanks
	rflag bool // recurse down directory trees
	mflag bool // doing several files or a directory

	out = bufio.NewWriter(os.Stdout)

	file        [2][]line
	sfile       [2][]line
	slen        [2]int
	ilen        [2]int
	pref, suff  int
	clen        int
	firstchange bool
	clist       []cand
	jv          []int // J: the match vector
	ixold       []int
	ixnew       []int
	input       [2]*iobuf
	file1       string
	file2       string
	anychange   bool

	tmpname  = []string{"/tmp/diff1", "/tmp/diff2"}
	whichtmp int
)

func main() {
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'e', 'f':
			mode = o
		case 'w':
			bflag = 2
		case 'b':
			bflag = 1
		case 'r':
			rflag = true
		case 'm':
			mflag = true
		default:
			fatal(usageText)
		}
	}
	arr := a.Argv()
	j := len(arr)
	if j < 2 {
		fatal(usageText)
	}
	tsb, err := sys.Stat(arr[j-1])
	if err != nil {
		fatal("can't stat " + arr[j-1] + ": " + sys.Errstr())
	}
	if j > 2 {
		if tsb.Qid.Type&sys.QTDIR == 0 {
			fatal(usageText)
		}
		mflag = true
	} else {
		fsb, err := sys.Stat(arr[0])
		if err != nil {
			fatal("can't stat " + arr[0] + ": " + sys.Errstr())
		}
		if fsb.Qid.Type&sys.QTDIR != 0 && tsb.Qid.Type&sys.QTDIR != 0 {
			mflag = true
		}
	}
	for i := 0; i < j-1; i++ {
		diff(arr[i], arr[j-1], 0)
		rmtmpfiles()
	}
	rmtmpfiles()
	out.Flush()
	if anychange {
		sys.Exits("fail:some")
	}
}

// iobuf is a whole file in memory, read a line at a time as Bufio does.
type iobuf struct {
	b   []byte
	off int
}

// gets returns the next line with its newline, or "" at the end.
func (f *iobuf) gets() string {
	if f.off >= len(f.b) {
		return ""
	}
	rest := f.b[f.off:]
	n := len(rest)
	if i := strings.IndexByte(string(rest), '\n'); i >= 0 {
		n = i + 1
	}
	f.off += n
	return string(rest[:n])
}

func (f *iobuf) seek(off int) { f.off = off }

func openbuf(name string) (*iobuf, error) {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil, err
	}
	fp := os.NewFile(uintptr(fd), name)
	defer fp.Close()
	b, err := io.ReadAll(fp)
	if err != nil {
		return nil, err
	}
	return &iobuf{b: b}, nil
}

// sortlines is the shell sort of CACM #201: by value, then serial,
// over a[1:n+1].
func sortlines(a []line, n int) {
	m := 0
	for i := 1; i <= n; i *= 2 {
		m = 2*i - 1
	}
	for m /= 2; m != 0; m /= 2 {
		for j := 1; j <= n-m; j++ {
			ai, aim := j, j+m
			for {
				if a[aim].value > a[ai].value || a[aim].value == a[ai].value && a[aim].serial > a[ai].serial {
					break
				}
				a[ai], a[aim] = a[aim], a[ai]
				aim = ai
				ai -= m
				if !(ai > 0 && aim >= ai) {
					break
				}
			}
		}
	}
}

func unsort(f []line, l int) []int {
	a := make([]int, l+1)
	for i := 1; i <= l; i++ {
		a[f[i].serial] = int(f[i].value)
	}
	return a
}

func prune() {
	for pref = 0; pref < ilen[0] && pref < ilen[1] && file[0][pref+1].value == file[1][pref+1].value; pref++ {
	}
	for suff = 0; suff < ilen[0]-pref && suff < ilen[1]-pref &&
		file[0][ilen[0]-suff].value == file[1][ilen[1]-suff].value; suff++ {
	}
	for j := 0; j < 2; j++ {
		sfile[j] = file[j][pref:]
		slen[j] = ilen[j] - pref - suff
		for i := 0; i <= slen[j]; i++ {
			sfile[j][i].serial = i
		}
	}
}

func equiv(a []line, n int, b []line, m int, c []int) {
	i, j := 1, 1
	for i <= n && j <= m {
		switch {
		case a[i].value < b[j].value:
			a[i].value = 0
			i++
		case a[i].value == b[j].value:
			a[i].value = int32(j)
			i++
		default:
			j++
		}
	}
	for ; i <= n; i++ {
		a[i].value = 0
	}
	b[m+1].value = 0
	for j = 1; j <= m; j++ {
		c[j] = -b[j].serial
		for b[j+1].value == b[j].value {
			j++
			c[j] = b[j].serial
		}
	}
	c[j] = -1
}

func newcand(x, y, pred int) int {
	clist = append(clist, cand{x, y, pred})
	clen = len(clist)
	return clen - 1
}

func search(c []int, k, y int) int {
	if clist[c[k]].y < y { // quick look for the typical case
		return k + 1
	}
	i, j := 0, k+1
	var l int
	for {
		l = (i + j) / 2
		if l <= i {
			break
		}
		t := clist[c[l]].y
		switch {
		case t > y:
			j = l
		case t < y:
			i = l
		default:
			return l
		}
	}
	return l + 1
}

func stone(a []int, n int, b []int, c []int) int {
	k := 0
	c[0] = newcand(0, 0, 0)
	for i := 1; i <= n; i++ {
		j := a[i]
		if j == 0 {
			continue
		}
		y := -b[j]
		oldl := 0
		oldc := c[0]
		for {
			if y > clist[oldc].y {
				l := search(c, k, y)
				if l != oldl+1 {
					oldc = c[l-1]
				}
				if l <= k {
					if clist[c[l]].y > y {
						tc := c[l]
						c[l] = newcand(i, y, oldc)
						oldc = tc
						oldl = l
					}
				} else {
					c[l] = newcand(i, y, oldc)
					k++
					break
				}
			}
			j++
			if y = b[j]; y <= 0 {
				break
			}
		}
	}
	return k
}

func unravel(p int) {
	for i := 0; i <= ilen[0]; i++ {
		switch {
		case i <= pref:
			jv[i] = i
		case i > ilen[0]-suff:
			jv[i] = i + ilen[1] - ilen[0]
		default:
			jv[i] = 0
		}
	}
	for q := clist[p]; q.y != 0; q = clist[q.pred] {
		jv[q.x+pref] = q.y + pref
	}
}

func output() {
	m := ilen[0]
	jv[0] = 0
	jv[m+1] = ilen[1] + 1
	if mode != 'e' {
		for i0, i1 := 1, 0; i0 <= m; i0 = i1 + 1 {
			for i0 <= m && jv[i0] == jv[i0-1]+1 {
				i0++
			}
			j0 := jv[i0-1] + 1
			i1 = i0 - 1
			for i1 < m && jv[i1+1] == 0 {
				i1++
			}
			j1 := jv[i1+1] - 1
			jv[i1] = j1
			change(i0, i1, j0, j1)
		}
	} else {
		for i0, i1 := m, 0; i0 >= 1; i0 = i1 - 1 {
			for i0 >= 1 && jv[i0] == jv[i0+1]-1 && jv[i0] != 0 {
				i0--
			}
			j0 := jv[i0+1] - 1
			i1 = i0 + 1
			for i1 > 1 && jv[i1-1] == 0 {
				i1--
			}
			j1 := jv[i1-1] + 1
			jv[i1] = j1
			change(i1, i0, j1, j0)
		}
	}
	if m == 0 {
		change(1, 0, 1, ilen[1])
	}
}

func diffreg(f, t string) {
	b0, b0type := prepare(0, f)
	if b0 == nil {
		return
	}
	b1, b1type := prepare(1, t)
	if b1 == nil {
		return
	}
	if b0type == bin || b1type == bin {
		if string(b0.b) != string(b1.b) {
			fmt.Fprintf(out, "Binary files %s %s differ\n", f, t)
			anychange = true
		}
		return
	}
	clist = nil
	prune()
	sortlines(sfile[0], slen[0])
	sortlines(sfile[1], slen[1])
	member := make([]int, slen[1]+2)
	equiv(sfile[0], slen[0], sfile[1], slen[1], member)
	class := unsort(sfile[0], slen[0])
	klist := make([]int, slen[0]+2)
	k := stone(class, slen[0], member, klist)
	jv = make([]int, ilen[0]+2)
	unravel(klist[k])
	ixold = make([]int, ilen[0]+2)
	ixnew = make([]int, ilen[1]+2)
	b0.seek(0)
	b1.seek(0)
	check(b0, b1)
	output()
}

// readhash hashes the next line, less its last character, by summing
// its characters shifted 7 bits apart in 16-bit hunks. It returns 0 at
// the end of the file.
func readhash(bp *iobuf) int32 {
	s := bp.gets()
	if s == "" {
		return 0
	}
	buf := []rune(s)
	buf = buf[:len(buf)-1]
	sum := int32(1)
	shift := 0
	switch bflag {
	case 0:
		for _, c := range buf {
			shift &= halfint - 1
			sum += int32(c) << shift
			shift += 7
		}
	case 1:
		// Coalesce runs of blanks. As in Inferno, the character after a
		// run is skipped.
		space := 0
		for p := 0; p < len(buf); p++ {
			if buf[p] == ' ' || buf[p] == '\t' {
				space++
				continue
			}
			if space != 0 {
				shift += 7
				space = 0
			}
			shift &= halfint - 1
			sum += int32(buf[p]) << shift
			p++
			shift += 7
		}
	default:
		// Ignore all blanks.
		for _, c := range buf {
			if c == ' ' || c == '\t' {
				continue
			}
			shift &= halfint - 1
			sum += int32(c) << shift
			shift += 7
		}
	}
	return sum
}

func prepare(i int, name string) (*iobuf, int) {
	bp, err := openbuf(name)
	if err != nil {
		errorf("cannot open " + name + ": " + sys.Errstr())
		return nil, 0
	}
	head := bp.b[:min(len(bp.b), 1024)]
	// A bad UTF-8 sequence, other than one cut short at the end of
	// the sample, means a binary file.
	str := []rune(string(head))
	for j := 0; j < len(str)-2; j++ {
		if str[j] == utf8.RuneError {
			return bp, bin
		}
	}
	p := make([]line, 4)
	j := 0
	for {
		h := readhash(bp)
		if h == 0 {
			break
		}
		j++
		if j+3 >= len(p) {
			p = append(p, make([]line, len(p))...)
		}
		p[j].value = h
	}
	ilen[i] = j
	file[i] = p
	input[i] = bp
	if i == 0 {
		file1 = name
		firstchange = false
	} else {
		file2 = name
	}
	return bp, reg
}

func squishspace(buf string) string {
	var b strings.Builder
	space := 0
	for _, c := range buf {
		if c == ' ' || c == '\t' {
			space++
			continue
		}
		if space != 0 && bflag == 1 {
			b.WriteByte(' ')
			space = 0
		}
		b.WriteRune(c)
	}
	return b.String()
}

func chop(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

// check breaks matches that hashing made but the text does not, and
// records each line's offset. As in Inferno, the last line of the first
// file is not checked.
func check(bf, bt *iobuf) {
	f, t := 1, 1
	ixold[0], ixnew[0] = 0, 0
	for ; f < ilen[0]; f++ {
		fbuf := chop(bf.gets())
		ixold[f] = bf.off
		if jv[f] == 0 {
			continue
		}
		var tbuf string
		for {
			tbuf = chop(bt.gets())
			ixnew[t] = bt.off
			t++
			if t-1 >= jv[f] {
				break
			}
		}
		if bflag != 0 {
			fbuf = squishspace(fbuf)
			tbuf = squishspace(tbuf)
		}
		if fbuf != tbuf {
			jv[f] = 0
		}
	}
	for t < ilen[1] {
		bt.gets()
		ixnew[t] = bt.off
		t++
	}
}

func prange(a, b int, sep string) {
	if a > b {
		fmt.Fprintf(out, "%d", b)
	} else {
		fmt.Fprintf(out, "%d", a)
	}
	if a < b {
		fmt.Fprintf(out, "%s%d", sep, b)
	}
}

func fetch(f []int, a, b int, bp *iobuf, s string) {
	bp.seek(f[a-1])
	for ; a <= b; a++ {
		out.WriteString(s)
		out.WriteString(bp.gets())
	}
}

func change(a, b, c, d int) {
	if a > b && c > d {
		return
	}
	anychange = true
	if mflag && !firstchange {
		fmt.Fprintf(out, "diff %s %s\n", file1, file2)
		firstchange = true
	}
	op := byte('c')
	if a > b {
		op = 'a'
	} else if c > d {
		op = 'd'
	}
	if mode != 'f' {
		prange(a, b, ",")
		out.WriteByte(op)
		if mode != 'e' {
			prange(c, d, ",")
		}
	} else {
		out.WriteByte(op)
		prange(a, b, " ")
	}
	out.WriteByte('\n')
	if mode == 0 {
		fetch(ixold, a, b, input[0], "< ")
		if a <= b && c <= d {
			out.WriteString("---\n")
		}
		fetch(ixnew, c, d, input[1], "> ")
	} else {
		fetch(ixnew, c, d, input[1], "")
	}
	if mode != 0 && c <= d {
		out.WriteString(".\n")
	}
}

func scandir(name string) []string {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return nil
	}
	defer os.NewFile(uintptr(fd), name).Close()
	var names []string
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil
		}
		if len(ds) == 0 {
			break
		}
		for _, d := range ds {
			names = append(names, d.Name)
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func diffdir(f, t string, level int) {
	df := scandir(f)
	dt := scandir(t)
	i, j := 0, 0
	for i < len(df) || j < len(dt) {
		if j == len(dt) || i < len(df) && df[i] < dt[j] {
			if mode == 0 {
				fmt.Fprintf(out, "Only in %s: %s\n", f, df[i])
			}
			i++
			continue
		}
		if i == len(df) || j < len(dt) && df[i] > dt[j] {
			if mode == 0 {
				fmt.Fprintf(out, "Only in %s: %s\n", t, dt[j])
			}
			j++
			continue
		}
		diff(f+"/"+df[i], t+"/"+dt[j], level+1)
		i++
		j++
	}
}

// regular reports a file that can be read twice: not a directory, pipe
// or network file.
func regular(d *sys.Dir) bool {
	return d.Qid.Type&sys.QTDIR == 0 && d.Type != '|' && d.Type != 'I'
}

func rmtmpfiles() {
	for whichtmp > 0 {
		whichtmp--
		_ = sys.Remove(tmpname[whichtmp])
	}
}

// mktmpfile copies an unseekable input to a temporary file.
func mktmpfile(in *os.File) (string, *sys.Dir) {
	p := tmpname[whichtmp]
	whichtmp++
	fd, err := sys.Create(p, sys.OWRITE, 0o600)
	if err != nil {
		errorf("cannot create " + p + ": " + sys.Errstr())
		return "", nil
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	_, werr := io.Copy(f, in)
	sb, serr := sys.Fstat(fd)
	if werr != nil || serr != nil {
		errorf("cannot read/write " + p + ": " + sys.Errstr())
		return "", nil
	}
	return p, sb
}

func statfile(name string) (string, *sys.Dir) {
	sb, err := sys.Stat(name)
	if err != nil {
		if _, ferr := sys.Fstat(0); name != "-" || ferr != nil {
			errorf("cannot stat " + name + ": " + sys.Errstr())
			return "", nil
		}
		return mktmpfile(os.Stdin)
	}
	if !regular(sb) && sb.Qid.Type&sys.QTDIR == 0 {
		fd, err := sys.Open(name, sys.OREAD)
		if err != nil {
			errorf("cannot open " + name + ": " + sys.Errstr())
			return "", nil
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		return mktmpfile(f)
	}
	return name, sb
}

func lastelem(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i < len(s)-1 {
		return s[i+1:]
	}
	return s
}

func diff(f, t string, level int) {
	fp, fsb := statfile(f)
	if fp == "" {
		return
	}
	tp, tsb := statfile(t)
	if tp == "" {
		return
	}
	switch {
	case fsb.Qid.Type&sys.QTDIR != 0 && tsb.Qid.Type&sys.QTDIR != 0:
		if rflag || level == 0 {
			diffdir(fp, tp, level)
		} else {
			fmt.Fprintf(out, "Common subdirectories: %s and %s\n", fp, tp)
		}
	case regular(fsb) && regular(tsb):
		diffreg(fp, tp)
	case fsb.Qid.Type&sys.QTDIR == 0:
		diffreg(fp, tp+"/"+lastelem(f))
	default:
		diffreg(fp+"/"+lastelem(t), tp)
	}
}

func fatal(s string) {
	out.Flush()
	os.Stderr.WriteString("diff: " + s + "\n")
	sys.Exits("fail:error")
}

func errorf(s string) {
	out.Flush()
	os.Stderr.WriteString("diff: " + s + "\n")
}
