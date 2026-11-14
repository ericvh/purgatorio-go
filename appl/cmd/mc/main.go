// Command mc prints its input lines in as many columns as fit across the
// page (Inferno mc(1)). With no window to measure, the page is 65
// characters wide unless -c says otherwise.
package main

import (
	"bufio"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	columns = 65
	mintab  = 1
)

type word struct {
	s string
	w int
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("mc [-c columns] [file ...]")
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'c':
			columns = atoi(a.EArg()) * mintab
		default:
			a.Usage()
		}
	}
	files := a.Argv()
	if len(files) == 0 {
		files = []string{"/fd/0"}
	}
	var words []word
	maxwidth := 0
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			os.Stderr.WriteString("mc: can't open " + name + ": " + sys.Errstr() + "\n")
			continue
		}
		in := bufio.NewReader(f)
		for {
			s, err := in.ReadString('\n')
			if s != "" {
				if s[len(s)-1] == '\n' {
					s = s[:len(s)-1]
				}
				w := utf8.RuneCountInString(s)
				words = append(words, word{s, w})
				maxwidth = max(maxwidth, w)
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	outcols(words, maxwidth)
}

func outcols(words []word, maxwidth int) {
	maxwidth = nexttab(maxwidth + mintab - 1)
	numcols := max(columns/maxwidth, 1)
	nlines := (len(words) + numcols - 1) / numcols
	out := bufio.NewWriter(os.Stdout)
	for i := 0; i < nlines; i++ {
		col, endcol := 0, 0
		for j := i; j < len(words); j += nlines {
			endcol += maxwidth
			out.WriteString(words[j].s)
			col += words[j].w
			if j+nlines < len(words) {
				for col < endcol {
					out.WriteByte(' ')
					col = nexttab(col)
				}
			}
		}
		out.WriteByte('\n')
	}
	out.Flush()
}

// nexttab is the next column; without a window there are no tab stops.
func nexttab(col int) int { return col + 1 }

// atoi converts a leading decimal number, as Limbo's int of a string.
func atoi(s string) int {
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || i == 0 && s[i] == '-') {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}
