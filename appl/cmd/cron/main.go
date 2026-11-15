// Command cron runs commands from a crontab at the minutes it names.
// Inferno has no cron; this follows the Rust sibling's, after Plan 9's
// cron(8).
//
//	cron [-f crontab] [-t time]
//
// The crontab defaults to /cron/$user/cron. Each line is five time
// fields, minute (0-59), hour (0-23), day of the month (1-31), month
// (1-12) and day of the week (0-6, 0 is Sunday), then a command that is
// run with sh -c. A field is * or a comma-separated list of numbers and
// ranges a-b, each optionally followed by /step. As in Unix cron, when
// both day fields are restricted a day matching either one runs. #
// starts a comment. Times are GMT.
//
// cron checks once a minute and rereads the crontab each time, so edits
// take effect at the next minute. With -t time (seconds since the
// epoch) it makes a single pass for the minute holding time, running the
// due commands one after another in crontab order, and exits.
package main

import (
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("cron [-f crontab] [-t time]")
	file := ""
	once, haveonce := 0, false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'f':
			file = a.EArg()
		case 't':
			n, err := strconv.Atoi(a.EArg())
			if err != nil {
				a.Usage()
			}
			once, haveonce = n, true
		default:
			a.Usage()
		}
	}
	if len(a.Argv()) != 0 {
		a.Usage()
	}
	if file == "" {
		b, _ := os.ReadFile("/dev/user")
		file = "/cron/" + strings.TrimSpace(string(b)) + "/cron"
	}
	if haveonce {
		if err := pass(file, daytime.Gmt(once), true); err != "" {
			os.Stderr.WriteString("cron: " + err + "\n")
			sys.Exits("fail:" + err)
		}
		return
	}
	for {
		// Sleep to the start of the next minute.
		now := daytime.Now()
		next := (now/60 + 1) * 60
		sys.Sleep((next - now) * 1000)
		if err := pass(file, daytime.Gmt(next), false); err != "" {
			os.Stderr.WriteString("cron: " + err + "\n")
		}
	}
}

// pass starts every entry of file due at tm, waiting for each in turn
// when wait is set.
func pass(file string, tm *daytime.Tm, wait bool) string {
	text, err := os.ReadFile(file)
	if err != nil {
		return "can't open " + file + ": " + sys.Errstr()
	}
	var wf *os.File
	if wait {
		me, _ := sys.Pctl(0, nil)
		fd, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
		if err != nil {
			return "can't open wait file: " + sys.Errstr()
		}
		wf = os.NewFile(uintptr(fd), "wait")
		defer wf.Close()
	}
	for n, line := range strings.Split(string(text), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e, perr := parse(line)
		if perr != "" {
			os.Stderr.WriteString("cron: " + file + ":" + strconv.Itoa(n+1) + ": " + perr + "\n")
			continue
		}
		if !e.due(tm) {
			continue
		}
		pid, err := sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", e.cmd}, sys.NEWFD,
			[][2]int{{0, 0}, {1, 1}, {2, 2}})
		if err != nil {
			os.Stderr.WriteString("cron: " + e.cmd + ": " + sys.Errstr() + "\n")
			continue
		}
		if wait {
			waitfor(wf, pid)
		}
	}
	return ""
}

func waitfor(wf *os.File, pid int) {
	buf := make([]byte, 256)
	for {
		n, err := wf.Read(buf)
		if err != nil || n == 0 || strings.HasPrefix(string(buf[:n]), strconv.Itoa(pid)+" ") {
			return
		}
	}
}

// set is the allowed values of one field, as bits.
type set uint64

func (s set) has(v int) bool { return v >= 0 && v < 64 && s&(1<<v) != 0 }

type entry struct {
	min, hour, mday, mon, wday set
	anymday, anywday           bool // the day fields were *
	cmd                        string
}

func parse(line string) (*entry, string) {
	rest := line
	var f [5]string
	for i := range f {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		end := strings.IndexFunc(rest, unicode.IsSpace)
		if end < 0 {
			end = len(rest)
		}
		if end == 0 {
			return nil, "too few fields"
		}
		f[i], rest = rest[:end], rest[end:]
	}
	cmd := strings.TrimSpace(rest)
	if cmd == "" {
		return nil, "no command"
	}
	e := &entry{anymday: f[2] == "*", anywday: f[4] == "*", cmd: cmd}
	var err string
	for _, x := range []struct {
		s      *set
		f      string
		lo, hi int
	}{{&e.min, f[0], 0, 59}, {&e.hour, f[1], 0, 23}, {&e.mday, f[2], 1, 31}, {&e.mon, f[3], 1, 12}, {&e.wday, f[4], 0, 6}} {
		if *x.s, err = field(x.f, x.lo, x.hi); err != "" {
			return nil, err
		}
	}
	return e, ""
}

func (e *entry) due(tm *daytime.Tm) bool {
	mday, wday := e.mday.has(tm.Mday), e.wday.has(tm.Wday)
	day := mday && wday
	if !e.anymday && !e.anywday {
		day = mday || wday
	}
	return e.min.has(tm.Min) && e.hour.has(tm.Hour) && e.mon.has(tm.Mon+1) && day
}

func field(s string, lo, hi int) (set, string) {
	bad := "bad field '" + s + "'"
	var bits set
	for _, part := range strings.Split(s, ",") {
		rng, step := part, 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return 0, bad
			}
			rng, step = r, n
		}
		var a, b int
		if rng == "*" {
			a, b = lo, hi
		} else if x, y, ok := strings.Cut(rng, "-"); ok {
			var e1, e2 error
			a, e1 = strconv.Atoi(x)
			b, e2 = strconv.Atoi(y)
			if e1 != nil || e2 != nil {
				return 0, bad
			}
		} else {
			v, err := strconv.Atoi(rng)
			if err != nil {
				return 0, bad
			}
			a, b = v, v
			if step > 1 {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, bad
		}
		for v := a; v <= b; v += step {
			bits |= 1 << v
		}
	}
	return bits, ""
}
