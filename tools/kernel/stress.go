package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// stressChecker runs the sh corpus and the conformance guests over and
// over, concurrently, in many progs, for a number of seconds.
//
// Each of progs workers is a background sh with its own copy of the name
// space (pctl forkns) and its own memfs on /tmp. It takes the jobs in
// turn, starting at a different one, until the run is over: a corpus
// script runs in a fresh memfs on /mnt, as -corpus runs it, and its
// output must match its golden; a guest must print what the emu's test
// for it expects and exit with an empty status. Every finished job
// prints a line, ok or FAIL.
//
// A watchdog on the host fails the run if no job finishes for watch
// seconds; it then asks QEMU's monitor where each CPU is, and kills it.
//
// Left out, because they use names every prog shares: the network
// guests (fixed ports) and test/styx (port 6666).
type stressChecker struct {
	secs, progs int
	watch       time.Duration
	scripts     []string
	elf         string
	monitor     string // QEMU's monitor socket
}

// stressGuests are the guests, under /bin/test, and what they print.
var stressGuests = []struct {
	name string
	want []string
}{
	{"abi", []string{"errstr ok"}},
	{"m3", []string{"ramfile changed", "mounted hello from the root", "chans ok"}},
	{"load", []string{`echo "hello" <nil>`, "fail it failed", "nope no such function: nope",
		"same prog true", "big 100000 <nil>", "command: /bin/echo.wasm: not a loadable module"}},
}

// quote quotes s for sh.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// setup writes the run as one sh command. It defines no functions and
// only stress_ variables, as both are passed on to the sh that runs a
// corpus script, and it clears $apid, which the corpus checks.
func (c *stressChecker) setup() checkConfig {
	var err error
	c.scripts, err = filepath.Glob("root/lib/test/sh/*.sh")
	check(err)
	var jobs []string
	for _, sc := range c.scripts {
		jobs = append(jobs, strings.TrimSuffix(filepath.Base(sc), ".sh"))
	}
	for _, g := range stressGuests {
		jobs = append(jobs, "test/"+g.name)
	}
	var b strings.Builder
	// /n/remote holds the run's flags, in a memfs every worker shares.
	b.WriteString("load std; memfs /n/remote; ")
	for w := range c.progs {
		// The flags are bound into /tmp too: m3 mounts over /n.
		fmt.Fprintf(&b, "{stress_w=%d; pctl forkns; memfs /tmp; mkdir /tmp/test /tmp/flags; bind -c /n/remote /tmp/flags; ", w)
		for _, g := range stressGuests {
			b.WriteString("{")
			for i, l := range g.want {
				if i > 0 {
					b.WriteString("; ")
				}
				b.WriteString("echo " + quote(l))
			}
			fmt.Fprintf(&b, "} > /tmp/test/%s.want; ", g.name)
		}
		k := w * len(jobs) / c.progs
		rot := append(append([]string{}, jobs[k:]...), jobs[:k]...)
		b.WriteString("apid=(); while {! test -e /tmp/flags/stop} {for stress_j in " + strings.Join(rot, " ") + " {")
		b.WriteString("if {test -e /lib/test/sh/$stress_j^.sh} {" +
			"unmount /mnt >[2] /dev/null; memfs /mnt; mkdir /mnt/shtest; " +
			"sh /lib/test/sh/$stress_j^.sh > /tmp/out >[2=1]; stress_st=''; stress_want=/lib/test/sh/$stress_j^.out" +
			"} {" +
			"/bin/$stress_j^.wasm > /tmp/out >[2=1]; stress_st=$status; stress_want=/tmp/$stress_j^.want" +
			"}; ")
		b.WriteString("if {cmp -s /tmp/out $stress_want && ~ $stress_st ''} {echo '@@ ok' $stress_w $stress_j} " +
			"{echo '@@ FAIL' $stress_w $stress_j status $stress_st; diff $stress_want /tmp/out}")
		b.WriteString("}}; echo '@@ done' $stress_w; echo done > /tmp/flags/done^$stress_w} & ")
	}
	// Every 30 seconds, the heap (#c/memory's main pool) and the progs.
	mem := "stress_m=`{cat /dev/memory}; stress_p=`{ps | wc -l}; echo '@@ mem heap' ${hd $stress_m} progs $stress_p; "
	fmt.Fprintf(&b, "echo '@@ stress' %d progs %d s; ", c.progs, c.secs)
	var ticks []string
	for i := 30; i <= c.secs; i += 30 {
		ticks = append(ticks, fmt.Sprint(i))
	}
	if len(ticks) > 0 {
		b.WriteString("for stress_t in " + strings.Join(ticks, " ") + " {sleep 30; " + mem + "}; ")
	}
	if r := c.secs % 30; r > 0 {
		fmt.Fprintf(&b, "sleep %d; ", r)
	}
	b.WriteString("echo stop > /n/remote/stop; ")
	for w := range c.progs {
		fmt.Fprintf(&b, "while {! test -e /n/remote/done%d} {sleep 1}; ", w)
	}
	b.WriteString(mem + "echo '@@ end'")
	return checkConfig{
		init: b.String(),
		cmds: []string{"cmp", "diff", "unmount", "mount", "test/abi", "test/m3", "test/load", "lib/testmod"},
	}
}

var stressLine = regexp.MustCompile(`^@@ (ok|FAIL|done) (\d+)(?: (\S+))?`)

func (c *stressChecker) run(q *exec.Cmd) int {
	var (
		mu      sync.Mutex
		last    = time.Now()
		ok      = map[string]int{}
		failed  = map[string]int{}
		done    = map[string]bool{}
		watched bool
	)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			mu.Lock()
			idle := time.Since(last)
			mu.Unlock()
			if idle > c.watch {
				mu.Lock()
				watched = true
				mu.Unlock()
				fmt.Printf("watchdog: no job finished in %v\n", idle.Round(time.Second))
				c.where()
				_ = q.Process.Kill()
				return
			}
		}
	}()
	out, err := console(q, time.Duration(c.secs)*time.Second+30*time.Minute, func(s string, _ io.Writer) {
		m := stressLine.FindStringSubmatch(s)
		if m == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch m[1] {
		case "ok":
			ok[m[3]]++
			last = time.Now()
		case "FAIL":
			failed[m[3]]++
			last = time.Now()
		case "done":
			done[m[2]] = true
		}
	})
	close(stop)
	mu.Lock()
	defer mu.Unlock()
	var r results
	r.check("kernel ran", err == nil && strings.Contains(out, "@@ end"), fmt.Sprint(err))
	r.check("no deadlock", !watched, "the watchdog fired")
	r.check("every worker finished", len(done) == c.progs, fmt.Sprintf("%d of %d", len(done), c.progs))
	var names []string
	runs, fails := 0, 0
	for n := range ok {
		names = append(names, n)
	}
	for n := range failed {
		if ok[n] == 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("     %-9s %5d runs %3d failed\n", n, ok[n]+failed[n], failed[n])
		runs += ok[n] + failed[n]
		fails += failed[n]
	}
	r.check("jobs ran", runs > 0, "none finished")
	r.check("no job failed", fails == 0, fmt.Sprintf("%d of %d runs failed", fails, runs))
	return r.status()
}

// where prints where each CPU is, from QEMU's monitor, with the
// kernel's symbols.
func (c *stressChecker) where() {
	conn, err := net.DialTimeout("unix", c.monitor, 5*time.Second)
	if err != nil {
		fmt.Println("watchdog: monitor:", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	_, _ = io.WriteString(conn, "info registers -a\n")
	time.Sleep(2 * time.Second)
	_, _ = io.WriteString(conn, "quit\n")
	var pcs []string
	sc := bufio.NewScanner(conn)
	pcRE := regexp.MustCompile(`PC=([0-9a-f]+)`)
	for sc.Scan() {
		if m := pcRE.FindStringSubmatch(sc.Text()); m != nil {
			pcs = append(pcs, "0x"+m[1])
		}
	}
	if len(pcs) == 0 {
		return
	}
	a2l := exec.Command("go", "tool", "addr2line", c.elf)
	a2l.Stdin = strings.NewReader(strings.Join(pcs, "\n") + "\n")
	b, err := a2l.Output()
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i, pc := range pcs {
		where := ""
		if err == nil && 2*i+1 < len(lines) {
			where = lines[2*i] + " " + lines[2*i+1]
		}
		fmt.Printf("watchdog: cpu %d at %s %s\n", i, pc, where)
	}
}

// monitorSocket names a socket for QEMU's monitor.
func monitorSocket() string {
	f, err := os.CreateTemp("", "kernel-monitor-*.sock")
	check(err)
	name := f.Name()
	f.Close()
	os.Remove(name)
	return name
}
