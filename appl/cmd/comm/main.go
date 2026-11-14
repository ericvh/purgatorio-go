// Command comm selects or rejects lines common to two sorted files
// (Inferno comm(1)).
package main

import (
	"bufio"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	cols = 7
	ldr  = []string{"", "\t", "\t\t"}
	out  = bufio.NewWriter(os.Stdout)
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("comm [-123] file1 file2")
	for o := a.Opt(); o != 0; o = a.Opt() {
		if o >= '1' && o <= '3' {
			cols &^= 1 << (o - '1')
		} else {
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 2 {
		a.Usage()
	}
	if cols&1 == 0 {
		ldr[1] = ""
		ldr[2] = ldr[2][1:]
	}
	if cols&2 == 0 {
		ldr[2] = ldr[2][1:]
	}
	ib1 := openfil(args[0])
	ib2 := openfil(args[1])
	lb1, ok1 := gets(ib1)
	if !ok1 {
		lb2, ok2 := gets(ib2)
		if !ok2 {
			exit()
		}
		copyrest(ib2, lb2, 2)
	}
	lb2, ok2 := gets(ib2)
	if !ok2 {
		copyrest(ib1, lb1, 1)
	}
	for {
		switch compare(lb1, lb2) {
		case 0:
			wr(lb1, 3)
			if lb1, ok1 = gets(ib1); !ok1 {
				if lb2, ok2 = gets(ib2); !ok2 {
					exit()
				}
				copyrest(ib2, lb2, 2)
			}
			if lb2, ok2 = gets(ib2); !ok2 {
				copyrest(ib1, lb1, 1)
			}
		case 1:
			wr(lb1, 1)
			if lb1, ok1 = gets(ib1); !ok1 {
				copyrest(ib2, lb2, 2)
			}
		case 2:
			wr(lb2, 2)
			if lb2, ok2 = gets(ib2); !ok2 {
				copyrest(ib1, lb1, 1)
			}
		}
	}
}

func exit() {
	out.Flush()
	os.Exit(0)
}

func gets(b *bufio.Reader) (string, bool) {
	s, _ := b.ReadString('\n')
	return s, s != ""
}

func wr(s string, n int) {
	if cols&(1<<(n-1)) != 0 {
		out.WriteString(ldr[n-1])
		out.WriteString(s)
	}
}

func copyrest(b *bufio.Reader, s string, n int) {
	for ok := true; ok; s, ok = gets(b) {
		wr(s, n)
	}
	exit()
}

// compare returns 0 when the lines are equal, 1 when a sorts first and
// 2 when b does, comparing characters.
func compare(as, bs string) int {
	a, b := []rune(as), []rune(bs)
	i := 0
	for ; i < len(a); i++ {
		if i >= len(b) || a[i] < b[i] {
			return 1
		}
		if a[i] != b[i] {
			return 2
		}
	}
	if i == len(b) {
		return 0
	}
	return 2
}

func openfil(s string) *bufio.Reader {
	if s == "-" {
		return bufio.NewReader(os.Stdin)
	}
	fd, err := sys.Open(s, sys.OREAD)
	if err != nil {
		os.Stderr.WriteString("comm: cannot open " + s + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:open")
	}
	return bufio.NewReader(os.NewFile(uintptr(fd), s))
}
