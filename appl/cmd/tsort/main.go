// Command tsort turns the partial order on standard input into a total
// order on standard output (Inferno tsort(1)). Each input line names an
// item followed by the items it precedes.
package main

import (
	"bufio"
	"os"
	"strings"
)

type item struct {
	name string
	mark int // 0 unvisited, 2 on the stack, 1 done
	succ []*item
}

var (
	items  []*item // in input order
	byname = map[string]*item{}
	dep    []*item // reversed output
	stderr = os.Stderr
)

func main() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, _ := in.ReadString('\n')
		if line == "" {
			break
		}
		fld := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
		if len(fld) > 0 {
			a := find(fld[0])
			for _, f := range fld[1:] {
				a.precede(find(f))
			}
		}
	}
	for _, q := range items {
		if q.mark == 0 {
			visit(q, nil)
		}
	}
	out := bufio.NewWriter(os.Stdout)
	for i := len(dep) - 1; i >= 0; i-- {
		out.WriteString(dep[i].name + "\n")
	}
	out.Flush()
}

func find(s string) *item {
	if it := byname[s]; it != nil {
		return it
	}
	it := &item{name: s}
	byname[s] = it
	items = append(items, it)
	return it
}

// precede records that a comes before b. Successors are visited newest
// first, as Inferno keeps them on a list.
func (a *item) precede(b *item) {
	if a == b {
		return
	}
	for _, s := range a.succ {
		if s == b {
			return
		}
	}
	a.succ = append([]*item{b}, a.succ...)
}

// visit walks q's successors depth first; parents is the stack, used to
// report a cycle.
func visit(q *item, parents []*item) {
	q.mark = 2
	parents = append(parents, q)
	for _, s := range q.succ {
		switch s.mark {
		case 0:
			visit(s, parents)
		case 2:
			stderr.WriteString("tsort: cycle in input\n")
			i := len(parents) - 1
			for parents[i] != s {
				i--
			}
			for _, p := range parents[i:] {
				stderr.WriteString("tsort: " + p.name + "\n")
			}
		}
	}
	q.mark = 1
	dep = append(dep, q)
}
