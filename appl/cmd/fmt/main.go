// Command fmt fills and indents paragraphs (Inferno fmt(1)).
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

const maxtab = 8

var (
	extraindent = 0  // spaces to indent every line
	length      = 70 // columns per output line
	join        = true
	out         *bufio.Writer
)

type word struct {
	text   []rune
	indent int // -1 for a blank line
	bol    bool
}

// words splits its input into words lazily, one line at a time.
type words struct {
	in     *bufio.Reader
	indent int
	q      []*word
	eof    bool
}

func (w *words) next() *word {
	for len(w.q) == 0 {
		if w.eof {
			return nil
		}
		s, err := w.in.ReadString('\n')
		if err != nil {
			w.eof = true
			if s == "" {
				return nil
			}
		}
		w.parse(strings.TrimSuffix(s, "\n"))
	}
	x := w.q[0]
	w.q = w.q[1:]
	return x
}

func (w *words) parse(line string) {
	r := []rune(line)
	w.indentof(r)
	bol := true
	for i := 0; i < len(r); {
		if r[i] == ' ' || r[i] == '\t' {
			i++
			continue
		}
		l := i
		for l < len(r) && r[l] != ' ' && r[l] != '\t' {
			l++
		}
		w.q = append(w.q, &word{r[i:l], w.indent, bol})
		bol = false
		i = l
	}
	if bol {
		w.q = append(w.q, &word{nil, -1, true})
	}
}

// indentof sets the current indent from the line's leading white space;
// a line of only white space leaves it alone.
func (w *words) indentof(r []rune) {
	ind := 0
	for _, c := range r {
		switch c {
		case ' ':
			ind++
		case '\t':
			ind += maxtab
			ind -= ind % maxtab
		default:
			w.indent = ind
			return
		}
	}
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("fmt [-j] [-i indent] [-l length] [file...]")
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'i':
			extraindent = atoi(a.EArg())
		case 'j':
			join = false
		case 'w', 'l':
			length = atoi(a.EArg())
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if length <= extraindent {
		os.Stderr.WriteString("fmt: line length<=indentation\n")
		sys.Exits("fail:length")
	}
	out = bufio.NewWriter(os.Stdout)
	failed := false
	if len(args) == 0 {
		format(os.Stdin)
	}
	for i, name := range args {
		f, err := os.Open(name)
		if err != nil {
			out.Flush()
			fmt.Fprintf(os.Stderr, "fmt: can't open %s: %s\n", name, sys.Errstr())
			failed = true
			continue
		}
		format(f)
		f.Close()
		if i < len(args)-1 {
			out.WriteByte('\n')
		}
	}
	out.Flush()
	if failed {
		sys.Exits("fail:open")
	}
}

func format(f io.Reader) {
	ws := &words{in: bufio.NewReader(f)}
	nw := ws.next()
	for nw != nil {
		w := nw
		if w.indent == -1 {
			out.WriteByte('\n')
			nw = ws.next()
			continue
		}
		col := extraindent + w.indent
		printindent(col)
		for {
			out.WriteString(string(w.text))
			col += len(w.text)
			if nw = ws.next(); nw == nil || nw.indent != w.indent {
				break
			}
			nsp := nspaceafter(w.text)
			if col+nsp+len(nw.text) > extraindent+length || !join && nw.bol {
				break
			}
			for j := 0; j < nsp; j++ {
				out.WriteByte(' ')
			}
			col += nsp
			w = nw
		}
		out.WriteByte('\n')
	}
}

func printindent(w int) {
	for ; w >= maxtab; w -= maxtab {
		out.WriteByte('\t')
	}
	for ; w > 0; w-- {
		out.WriteByte(' ')
	}
}

// nspaceafter gives two spaces after a full stop, unless the word looks
// like a title.
func nspaceafter(s []rune) int {
	if len(s) < 2 || len(s) < 4 && s[0] >= 'A' && s[0] <= 'Z' {
		return 1
	}
	if c := s[len(s)-1]; c == '.' || c == '!' || c == '?' {
		return 2
	}
	return 1
}

// atoi converts as Limbo's int of a string: leading space, a sign and
// digits, ignoring the rest.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}
