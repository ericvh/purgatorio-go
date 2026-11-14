// Command mdb is Inferno's binary file editor (mdb(1)).
//
//	mdb [-w] file [command]
//
// Commands are [address][,count][command]: /f and ?f print the file at
// dot in format f, =f prints dot itself, /w and /W write two- and
// four-byte values. Inferno's $ commands describe a Dis object; here
// they describe a wasm module: $h the header, $i the imports, $l the
// exports (Inferno's links), $D the function types (its type
// descriptors), $d the data segments, and ?i lists functions from
// index dot where Inferno disassembles instructions. $H and $s print
// nothing: a wasm module has no handler table or source path.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

var (
	mfd      *os.File
	m        *wasmobj.Module
	dot      int64
	lastaddr int64
	count    int64 = 1
	lastcmd  string
	out      = bufio.NewWriter(os.Stdout)
)

func atoi(s string) int64 {
	base := 10
	if s == "" {
		return 0
	}
	if s[0] == '0' {
		base = 8
		s = s[1:]
		if s == "" {
			return 0
		}
		if s[0] == 'x' || s[0] == 'X' {
			base = 16
			s = s[1:]
		}
	}
	// As String's toint: the longest prefix of digits in the base.
	i := 0
	for ; i < len(s); i++ {
		if d := digit(s[i]); d < 0 || d >= base {
			break
		}
	}
	n, _ := strconv.ParseInt(s[:i], base, 64)
	return n
}

func digit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

func eatws(s string) string { return strings.TrimLeft(s, " \t") }

func gotnum(s string) bool   { return s != "" && s[0] >= '0' && s[0] <= '9' }
func gotalpha(s string) bool { return s != "" && digit(s[0]) >= 10 }

func eatnum(s string) string {
	for gotnum(s) || gotalpha(s) {
		s = s[1:]
	}
	return s
}

// getexpr reads an expression: its rest, its value and whether there
// was one.
func getexpr(s string) (string, int64, bool) {
	var v int64
	op := byte('+')
	for {
		ov := v
		s = eatws(s)
		if s == "" {
			return "", 0, false
		}
		switch {
		case s[0] == '.' || s[0] == '+' || s[0] == '^':
			v = dot
			s = s[1:]
		case s[0] == '"':
			v = lastaddr
			s = s[1:]
		case s[0] == '(':
			s, v, _ = getexpr(s[1:])
			if s != "" {
				s = s[1:]
			}
		case gotnum(s):
			v = atoi(s)
			s = eatnum(s)
		default:
			return s, 0, false
		}
		switch op {
		case '+':
			v = ov + v
		case '-':
			v = ov - v
		case '*':
			v = ov * v
		case '%':
			if v != 0 {
				v = ov / v
			}
		case '&':
			v = ov & v
		case '|':
			v = ov | v
		}
		if s == "" {
			return "", v, true
		}
		switch s[0] {
		case '+', '-', '*', '%', '&', '|':
			op = s[0]
			s = s[1:]
		default:
			return eatws(s), v, true
		}
	}
}

func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func docmd(s string) {
	s = eatws(s)
	s, n, ok := getexpr(s)
	if ok {
		dot, lastaddr = n, n
	}
	count = 1
	if s != "" && s[0] == ',' {
		if s, n, ok = getexpr(s[1:]); ok {
			count = n
		}
	}
	if s == "" {
		if s = lastcmd; s == "" {
			return
		}
	}
	lastcmd = s
	switch cmd := s[0]; cmd {
	case '?', '/':
		switch at(s, 1) {
		case 'w':
			writemem(2, s[2:])
		case 'W':
			writemem(4, s[2:])
		case 'i':
			funcs()
		default:
			dumpmem(s[1:], cmd)
		}
	case '$':
		switch at(s, 1) {
		case 'D':
			types()
		case 'h':
			hdr()
		case 'l':
			exports()
		case 'i':
			imports()
		case 'd':
			data()
		case 'H', 's':
		}
	case '=':
		dumpmem(s[1:], cmd)
	default:
		out.Flush()
		fmt.Fprintf(os.Stderr, "invalid cmd: %c\n", cmd)
	}
}

func octal(n uint32, d int) string {
	s := ""
	for {
		s = strconv.Itoa(int(n%8)) + s
		n /= 8
		d--
		if d < 1 {
			break
		}
	}
	return "0" + s
}

func printable(c int32) string {
	switch {
	case c >= 32 && c <= 126:
		return string(rune(c))
	case c == '\n':
		return `\n`
	case c == '\r':
		return `\r`
	case c == '\b':
		return `\b`
	case c == '\a':
		return `\a`
	case c == '\v':
		return `\v`
	}
	return fmt.Sprintf(`\x%2.2x`, uint32(c))
}

func dumpmem(s string, t byte) {
	n := int64(0)
	for c := count; c > 0; c-- {
		for p := 0; p < len(s); p++ {
			f := s[p]
			switch f {
			case 'b', 'c', 'C':
				n = 1
			case 'x', 'd', 'u', 'o':
				n = 2
			case 'X', 'D', 'U', 'O':
				n = 4
			case 's', 'S', 'r', 'R':
				fmt.Fprintf(out, "'%c' format not yet supported\n", f)
				continue
			case 'n':
				fmt.Fprintf(out, "\n")
				continue
			case '+':
				dot++
				continue
			case '-':
				dot--
				continue
			case '^':
				dot -= n
				continue
			default:
				fmt.Fprintf(out, "unknown format '%c'\n", f)
				continue
			}
			var v int32
			if t == '=' {
				v = int32(dot)
			} else {
				b := make([]byte, n)
				mfd.ReadAt(b, dot)
				for i := range b {
					v |= int32(b[i]) << (8 * i)
				}
			}
			switch f {
			case 'c':
				fmt.Fprintf(out, "%c", rune(v))
			case 'C':
				fmt.Fprintf(out, "%s", printable(v))
			case 'b':
				fmt.Fprintf(out, "%#.2x ", uint32(v))
			case 'x':
				fmt.Fprintf(out, "%#.4x ", uint32(v))
			case 'X':
				fmt.Fprintf(out, "%#.8x ", uint32(v))
			case 'd':
				fmt.Fprintf(out, "%-4d ", v)
			case 'D':
				fmt.Fprintf(out, "%-8d ", v)
			case 'u':
				fmt.Fprintf(out, "%-4d ", uint32(v))
			case 'U':
				fmt.Fprintf(out, "%-8d ", uint32(v))
			case 'o':
				fmt.Fprintf(out, "%s ", octal(uint32(v), 6))
			case 'O':
				fmt.Fprintf(out, "%s ", octal(uint32(v), 11))
			}
			if t != '=' {
				dot += n
			}
		}
	}
	fmt.Fprintf(out, "\n")
}

func writemem(n int, s string) {
	s = eatws(s)
	off := dot
	for {
		var v int64
		var ok bool
		if s, v, ok = getexpr(s); !ok {
			return
		}
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(v >> (8 * i))
		}
		if k, err := mfd.WriteAt(b, off); err != nil || k != n {
			out.Flush()
			fmt.Fprintf(os.Stderr, "mdb: write error: %s\n", sys.Errstr())
		}
		off += int64(n)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: mdb [-w] file [command]\n")
	sys.Exits("fail:usage")
}

func main() {
	argv := os.Args
	if len(argv) < 2 {
		usage()
	}
	argv = argv[1:]
	writeable := false
	if len(argv[0]) > 0 && argv[0][0] == '-' {
		if argv[0] != "-w" {
			usage()
		}
		writeable = true
		argv = argv[1:]
	}
	if len(argv) == 0 {
		usage()
	}
	fname := argv[0]
	cmd := ""
	if len(argv) > 1 {
		cmd = argv[1]
	}
	flag := os.O_RDONLY
	if writeable {
		flag = os.O_RDWR
	}
	var err error
	if mfd, err = os.OpenFile(fname, flag, 0); err != nil {
		fmt.Fprintf(os.Stderr, "mdb: cannot open %s: %s\n", fname, sys.Errstr())
		sys.Exits("fail:cannot open")
	}
	if b, err := io.ReadAll(mfd); err == nil {
		m, _ = wasmobj.Parse(b)
	}
	if cmd != "" {
		docmd(cmd)
	} else {
		in := bufio.NewReader(os.Stdin)
		for {
			s, err := in.ReadString('\n')
			if s == "" && err != nil {
				break
			}
			docmd(strings.TrimSuffix(s, "\n"))
		}
	}
	out.Flush()
}

func hdr() {
	if m == nil {
		return
	}
	fmt.Fprintf(out, "%.8x Version %d wasm module\n", 0x6d736100, 1)
	fmt.Fprintf(out, "%8d types\n", len(m.Types))
	fmt.Fprintf(out, "%8d imports\n", len(m.Imports))
	fmt.Fprintf(out, "%8d functions\n", len(m.Funcs))
	fmt.Fprintf(out, "%8d exports\n", len(m.Exports))
	fmt.Fprintf(out, "%8d data segments\n", len(m.Datas))
	fmt.Fprintf(out, "%8d start function\n\n", m.Start)
	if _, ok := m.Custom(modsig.Section); !ok {
		fmt.Fprintf(out, "Module is Insecure\n")
	}
}

func imports() {
	if m == nil {
		return
	}
	for _, im := range m.Imports {
		ty := ""
		if im.Kind == wasmobj.KFunc && int(im.Type) < len(m.Types) {
			ty = " " + m.Types[im.Type].String()
		}
		fmt.Fprintf(out, "\timport %s%s, \"%s.%s\"\n", wasmobj.KindName(im.Kind), ty, im.Module, im.Name)
	}
}

func exports() {
	if m == nil {
		return
	}
	for _, e := range m.Exports {
		fmt.Fprintf(out, "\tlink %s %d, \"%s\"\n", wasmobj.KindName(e.Kind), e.Index, e.Name)
	}
}

func types() {
	if m == nil {
		return
	}
	for i, t := range m.Types {
		fmt.Fprintf(out, "\ttype $%d, %s\n", i, t)
	}
}

func data() {
	if m == nil {
		return
	}
	for _, d := range m.Datas {
		if d.Active {
			fmt.Fprintf(out, "\tdata @mem+%d, %d\n", d.Offset, len(d.Bytes))
		} else {
			fmt.Fprintf(out, "\tdata passive, %d\n", len(d.Bytes))
		}
	}
}

// funcs lists count functions from index dot, as das lists
// instructions.
func funcs() {
	if m == nil {
		return
	}
	syms := m.Syms()
	nf := int64(m.Imported(wasmobj.KFunc) + len(m.Funcs))
	for i := dot; count > 0 && i < nf; i++ {
		count--
		if i%10 == 0 {
			fmt.Fprintf(out, "#%d\n", i)
		}
		name := "-"
		if f := syms.Lookup(uint32(i)); f != nil {
			name = f.Name
		}
		t, _ := m.FuncType(uint32(i))
		fmt.Fprintf(out, "\t%s %s\n", name, t)
	}
}
