// Command cal prints a calendar (Inferno cal(1)): the current month, a
// month of the current year, a whole year, or a month of a year. Months
// may be named. The calendar is Julian up to September 1752, Gregorian
// after.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const dayw = " S  M Tu  W Th  F  S"

var smon = [...]string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

var out = bufio.NewWriter(os.Stdout)

func main() {
	args := os.Args[1:]
	if len(args) > 2 {
		os.Stderr.WriteString("usage: cal [month] [year]\n")
		sys.Exits("fail:usage")
	}
	defer out.Flush()
	switch len(args) {
	case 0:
		tm := daytime.Local(daytime.Now())
		month(tm.Mon+1, tm.Year+1900)
	case 1:
		// A month of this year if it looks like one, else a year.
		y := abs(number(args[0]))
		if y >= 1 && y <= 12 {
			month(y, daytime.Local(daytime.Now()).Year+1900)
		} else {
			year(y)
		}
	case 2:
		month(abs(number(args[0])), number(args[1]))
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func badarg() {
	out.Flush()
	os.Stderr.WriteString("cal: bad argument\n")
	sys.Exits("fail:bad argument")
}

var names = []struct {
	word string
	n    int
}{
	{"january", 1}, {"february", 2}, {"march", 3}, {"april", 4},
	{"may", 5}, {"june", 6}, {"july", 7}, {"august", 8},
	{"sept", 9}, {"september", 9}, {"october", 10}, {"november", 11},
	{"december", 12},
}

// number converts a decimal argument, or a month name or its first three
// letters to the negated month number.
func number(s string) int {
	if len(s) >= 3 {
		for _, m := range names {
			if s == m.word || s == m.word[:3] {
				return -m.n
			}
		}
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			badarg()
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func month(m, y int) {
	if m < 1 || m > 12 || y < 1 || y > 9999 {
		badarg()
	}
	fmt.Fprintf(out, "   %s %d\n", smon[m-1], y)
	fmt.Fprintf(out, "%s\n", dayw)
	for _, l := range cal(m, y) {
		fmt.Fprintf(out, "%s\n", l)
	}
}

func year(y int) {
	if y < 1 || y > 9999 {
		badarg()
	}
	fmt.Fprintf(out, "\n\n\n")
	fmt.Fprintf(out, "%32s%d\n", "", y)
	fmt.Fprintf(out, "\n")
	for i := 0; i < 12; i += 3 {
		fmt.Fprintf(out, "%9s%.3s%20s%.3s%20s%.3s\n", "", smon[i], "", smon[i+1], "", smon[i+2])
		fmt.Fprintf(out, "%s   %s   %s\n", dayw, dayw, dayw)
		var ms [3][6]string
		for j := range ms {
			ms[j] = cal(i+j+1, y)
		}
		for l := 0; l < 6; l++ {
			var s strings.Builder
			for j := range ms {
				fmt.Fprintf(&s, "%-20.20s   ", ms[j][l])
			}
			fmt.Fprintf(out, "%s\n", strings.TrimRight(s.String(), " "))
		}
	}
}

// cal returns the six week lines of month m of year y.
func cal(m, y int) [6]string {
	mon := [13]int{0, 31, 0, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	d := jan1(y)
	switch (jan1(y+1) + 7 - d) % 7 {
	case 1: // a common year
		mon[2] = 28
	case 2: // a leap year
		mon[2] = 29
	default: // 1752, when September lost eleven days
		mon[2] = 29
		mon[9] = 19
	}
	for i := 1; i < m; i++ {
		d += mon[i]
	}
	d %= 7
	var lines [6]string
	l := 0
	s := strings.Repeat("   ", d)
	for i := 1; i <= mon[m]; i++ {
		if i == 3 && mon[m] == 19 {
			i += 11
			mon[m] += 11
		}
		s += fmt.Sprintf("%2d", i)
		if d++; d == 7 {
			d = 0
			lines[l] = s
			l++
			s = ""
		} else {
			s += " "
		}
	}
	if s != "" {
		lines[l] = strings.TrimRight(s, " ")
	}
	return lines
}

// jan1 is the weekday of January 1 of year y.
func jan1(y int) int {
	d := 4 + y + (y+3)/4 // Julian: a leap day every four years
	if y > 1800 {        // Gregorian: three fewer every 400 years
		d -= (y - 1701) / 100
		d += (y - 1601) / 400
	}
	if y > 1752 { // the changeover
		d += 3
	}
	return d % 7
}
