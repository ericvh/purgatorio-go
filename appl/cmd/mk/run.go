package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Job is a recipe to run.
type Job struct {
	r     *Rule
	n     *Node // list of node targets
	stem  string
	match [nregexp]*string
	p     []string // prerequisites
	np    []string // new prerequisites
	t     []string // targets
	at    []string // all targets
	nproc int
}

type event struct {
	pid int
	job *Job
}

var (
	jobs       []*Job
	events     []event
	nevents    int
	nrunning   int
	nproclimit int
)

func run(j *Job) {
	jobs = append(jobs, j)
	if nrunning < nproclimit {
		sched()
	}
}

func sched() {
	if len(jobs) == 0 {
		usage()
		return
	}
	j := jobs[0]
	jobs = jobs[1:]
	if debug&dExec != 0 {
		fmt.Fprintf(os.Stdout, "firing up job for target %s\n", strings.Join(j.t, " "))
	}
	slot := nextslot()
	events[slot].job = j
	e := buildenv(j, slot)
	buf := shprint(*j.r.recipe, e)
	if !tflag && (nflag || j.r.attr&quiet == 0) {
		bout.WriteString(buf)
	}
	if nflag || tflag {
		bout.Flush()
		for n := j.n; n != nil; n = n.next {
			if tflag {
				if n.flags&virtual == 0 {
					touch(n.name)
				} else if explain {
					fmt.Fprintf(bout, "no touch of virtual '%s'\n", n.name)
				}
			}
			n.time = now()
			n.setmade(made)
		}
		return
	}
	if debug&dExec != 0 {
		fmt.Fprintf(os.Stdout, "recipe='%s'", *j.r.recipe)
	}
	bout.Flush()
	flags := "-e"
	if j.r.attr&nominuse != 0 {
		flags = ""
	}
	events[slot].pid = execshpid(flags, *j.r.recipe, e)
	usage()
	nrunning++
	if debug&dExec != 0 {
		fmt.Fprintf(os.Stdout, "pid for target %s = %d\n", strings.Join(j.t, " "), events[slot].pid)
	}
}

// waitup waits for a recipe to finish and records its result. With
// echildok > 0 having nothing to wait for is not an error.
func waitup(echildok int, _ []int) int {
	var pid int
	var msg string
	for {
		pid, msg = waitfor()
		if pid == -1 {
			if echildok > 0 {
				return 1
			}
			fmt.Fprintf(os.Stderr, "mk: (waitup %d) ", echildok)
			perror("mk wait")
			exit()
		}
		if debug&dExec != 0 {
			fmt.Fprintf(os.Stdout, "waitup got pid=%d, status='%s'\n", pid, msg)
		}
		if pidslot(pid) >= 0 {
			break
		}
		if debug&dExec != 0 {
			fmt.Fprintf(os.Stderr, "mk: wait returned unexpected process %d\n", pid)
		}
		pnew(pid, msg)
	}
	slot := pidslot(pid)
	j := events[slot].job
	usage()
	nrunning--
	events[slot].pid = -1
	fake := false
	if msg != "" {
		e := buildenv(j, slot)
		fmt.Fprintf(os.Stderr, "mk: %s: exit status=%s", front(shprint(*j.r.recipe, e)), msg)
		done := 0
		for n := j.n; n != nil; n = n.next {
			if n.flags&deletef != 0 {
				if done == 0 {
					fmt.Fprintf(os.Stderr, ", deleting")
				}
				done++
				fmt.Fprintf(os.Stderr, " '%s'", n.name)
				deletefile(n.name)
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
		if kflag {
			runerrs++
			fake = true
		} else {
			jobs = nil
			exit()
		}
	}
	for _, w := range j.t {
		if n, ok := nodes[w]; ok {
			update(fake, n)
		}
	}
	if nrunning < nproclimit {
		sched()
	}
	return 0
}

// nproc reads $NPROC. Inferno's mk always runs one recipe at a time
// whatever it says, and so does this one.
func nproc() {
	if w, ok := vars["NPROC"]; ok && len(w) > 0 && w[0] != "" {
		nproclimit = atoi(w[0])
	}
	nproclimit = 1
	if debug&dExec != 0 {
		fmt.Fprintf(os.Stdout, "nprocs = %d\n", nproclimit)
	}
	for nevents < nproclimit {
		events = append(events, event{})
		nevents++
	}
}

func nextslot() int {
	for i := 0; i < nproclimit; i++ {
		if events[i].pid <= 0 {
			return i
		}
	}
	assert("out of slots!!", false)
	return 0
}

func pidslot(pid int) int {
	for i := 0; i < nevents; i++ {
		if events[i].pid == pid {
			return i
		}
	}
	return -1
}

// front shortens a recipe for an error message: its first three words,
// ... and the last word.
func front(s string) string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
	if len(f) > 5 {
		f = []string{f[0], f[1], f[2], "...", f[len(f)-1]}
	}
	var b strings.Builder
	for _, x := range f {
		b.WriteString(x + " ")
	}
	return b.String()
}

// The wait file and the progs mk has started.

var (
	wfd     = -1
	wprocs  int
	pending []waitmsg // exits read while waiting for another prog
)

type waitmsg struct {
	pid    int
	status string
}

func openwait() {
	w := fmt.Sprintf("%d/wait", mypid)
	fd, err := sys.Open("#p/"+w, sys.OREAD)
	if err != nil {
		fd, err = sys.Open("/prog/"+w, sys.OREAD)
	}
	if err != nil {
		perror("fd == nil in wait")
		return
	}
	wfd = fd
}

// wait reads the next exit of a child: its pid and the whole wait
// message when the status is not empty.
func wait() (int, string) {
	if wprocs == 0 || wfd < 0 {
		return -1, ""
	}
	buf := make([]byte, waitlen)
	n, err := syscall.Read(wfd, buf)
	if err != nil || n <= 0 {
		perror("bad read in wait")
		wprocs = 0
		return -1, ""
	}
	status := string(buf[:n])
	s := ""
	if !strings.HasSuffix(status, ":") {
		s = status
	}
	wprocs--
	return atoi(status), s
}

// waitfor is wait, with the message cut to mk's ERRLEN, after any
// exits already read.
func waitfor() (int, string) {
	for i, p := range pending {
		if pidslot(p.pid) >= 0 {
			pending = append(pending[:i], pending[i+1:]...)
			return p.pid, p.status
		}
	}
	pid, s := wait()
	if len(s) > errlen {
		s = s[:errlen]
	}
	return pid, s
}

func pnew(pid int, status string) { pending = append(pending, waitmsg{pid, status}) }

// waitpid waits for pid and returns 1 if it failed, else 0.
func waitpid(pid int) int {
	if pid <= 0 {
		return 1
	}
	for i, p := range pending {
		if p.pid == pid {
			pending = append(pending[:i], pending[i+1:]...)
			if p.status != "" {
				return 1
			}
			return 0
		}
	}
	for {
		p, s := wait()
		if p == -1 {
			fmt.Fprintf(os.Stderr, "mk: (waitup -3) ")
			perror("mk wait")
			exit()
		}
		if p == pid {
			if s != "" {
				return 1
			}
			return 0
		}
		pnew(p, s)
	}
}

// Running sh.

// spawnsh starts sh with args, its standard input stdin (when non-nil)
// and standard output out, and returns its pid.
func spawnsh(args []string, stdin *string, out int) int {
	in := 0
	var p [2]int
	if stdin != nil {
		var err error
		p, err = sys.Pipe()
		if err != nil {
			perror("pipe")
			exit()
		}
		in = p[0]
	}
	argv := append([]string{shname0}, args...)
	if debug&dExec != 0 {
		fmt.Fprintf(os.Stdout, "executing %s with args (%s)\n", shell, strings.Join(argv, ", "))
	}
	pid, err := sys.Spawn(shell, argv, sys.NEWFD|sys.FORKENV, [][2]int{{0, in}, {1, out}, {2, 2}})
	if stdin != nil {
		syscall.Close(p[0])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "x %s: %s\n", shell, sys.Errstr())
		if stdin != nil {
			syscall.Close(p[1])
		}
		return -1
	}
	wprocs++
	if stdin != nil {
		if debug&dExec != 0 {
			fmt.Fprintf(os.Stdout, "writing '%s' to shell\n", *stdin)
		}
		writefd(p[1], []byte(*stdin))
		syscall.Close(p[1])
	}
	return pid
}

// execshpid runs a recipe: sh with flags reads cmd, expanded, on its
// standard input.
func execshpid(flags, cmd string, e []envent) int {
	cmd = shprint(cmd, e)
	if e != nil {
		exportenv(e)
	}
	var args []string
	if flags != "" {
		args = append(args, flags)
	}
	return spawnsh(args, &cmd, 1)
}

// execsh runs cmd as execshpid does and returns its output, less one
// final newline.
func execsh(flags, cmd string, _ bool, e []envent) string {
	cmd = shprint(cmd, e)
	if e != nil {
		exportenv(e)
	}
	p, err := sys.Pipe()
	if err != nil {
		perror("pipe")
		exit()
	}
	var args []string
	if flags != "" {
		args = append(args, flags)
	}
	pid := spawnsh(args, &cmd, p[1])
	syscall.Close(p[1])
	out := string(readfd(p[0]))
	syscall.Close(p[0])
	out = strings.TrimSuffix(out, "\n")
	if pid > 0 {
		waitpid(pid)
	}
	return out
}

// pipecmd runs sh -c cmd and returns its pid and, when wantout is set,
// its output.
func pipecmd(cmd string, e []envent, wantout bool) (int, []byte) {
	cmd = shprint(cmd, e)
	if debug&dExec != 0 {
		fmt.Fprintf(os.Stdout, "pipecmd='%s'", cmd)
	}
	if e != nil {
		exportenv(e)
	}
	if !wantout {
		return spawnsh([]string{"-c", cmd}, nil, 1), nil
	}
	p, err := sys.Pipe()
	if err != nil {
		perror("pipe")
		exit()
	}
	pid := spawnsh([]string{"-c", cmd}, nil, p[1])
	syscall.Close(p[1])
	out := readfd(p[0])
	syscall.Close(p[0])
	return pid, out
}

// The environment.

type envent struct {
	name   string
	values []string
}

var (
	myenv = []string{"target", "stem", "prereq", "pid", "nproc", "newprereq", "alltarget", "newmember",
		"stem0", "stem1", "stem2", "stem3", "stem4", "stem5", "stem6", "stem7", "stem8", "stem9"}
)

// envy is the environment execinit and buildenv build.
var envy []envent

func initenv() {
	for _, p := range myenv {
		internals[p] = true
	}
	readenv()
}

// readenv imports mk's environment into its variables, in a copy of
// the environment group.
func readenv() {
	_, _ = sys.Pctl(sys.FORKENV, nil)
	if !access("/env/autoload") {
		if fd, err := sys.Create("/env/autoload", sys.OWRITE, 0o666); err == nil {
			writefd(fd, []byte("std"))
			syscall.Close(fd)
		}
	}
	fd, err := sys.Open("/env", sys.OREAD)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	for {
		d, err := sys.Dirread(fd)
		if err != nil || len(d) == 0 {
			break
		}
		for _, e := range d {
			if e.Name == "" || shname(e.Name) != len(e.Name) || internals[e.Name] {
				continue
			}
			p, err := readfile("/env/" + e.Name)
			if err != nil || len(p) == 0 {
				continue
			}
			origenv[e.Name] = origval{p, true}
			p = []byte(strings.TrimSuffix(string(p), "\x00"))
			setvar(e.Name, encodenulls(string(p)))
		}
	}
}

// encodenulls breaks a value into words at NULs and \001s.
func encodenulls(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == 0 || r == 1 })
}

// mk exports a recipe's variables into its own environment group
// before starting the recipe with a copy of it, where Inferno's mk
// exports them in the child. origenv holds what each variable it has
// written was at the start, so each recipe sees mk's environment plus
// its own variables only.
type origval struct {
	val    []byte
	exists bool
}

var (
	origenv = map[string]origval{}
	written = map[string]bool{}
)

func restoreenv() {
	for name := range written {
		o := origenv[name]
		if o.exists {
			if fd, err := sys.Create("/env/"+name, sys.OWRITE, 0o666); err == nil {
				writefd(fd, o.val)
				syscall.Close(fd)
			}
		} else {
			_ = sys.Remove("/env/" + name)
		}
	}
	written = map[string]bool{}
}

func exportenv(e []envent) {
	restoreenv()
	for _, v := range e {
		_, sy := vars[v.name]
		hasvalue := len(v.values) > 0 && v.values[0] != ""
		if !sy && !hasvalue {
			continue
		}
		nam := "/env/" + v.name
		written[v.name] = true
		if !hasvalue {
			_ = sys.Remove(nam)
			continue
		}
		fd, err := sys.Create(nam, sys.OWRITE, 0o666)
		if err != nil {
			perror(nam)
			continue
		}
		var b strings.Builder
		for i, w := range v.values {
			if w != "" {
				b.WriteString(w)
				if i+1 < len(v.values) {
					b.WriteByte(' ')
				}
			}
		}
		if !writefd(fd, []byte(b.String())) {
			perror(nam)
		}
		syscall.Close(fd)
	}
}

func envinsert(name string, value []string) { envy = append(envy, envent{name, value}) }

func envupd(name string, value []string) {
	for i := range envy {
		if envy[i].name == name {
			envy[i].values = value
			return
		}
	}
	envinsert(name, value)
}

// execinit builds the environment for commands: mk's own variables,
// empty, then every exported variable.
func execinit() {
	envy = nil
	for _, p := range myenv {
		envinsert(p, []string{""})
	}
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if noexport[k] || internals[k] {
			continue
		}
		envinsert(k, vars[k])
	}
}

func buildenv(j *Job, slot int) []envent {
	envupd("target", dup(j.t))
	if j.r.attr&regexpattr != 0 {
		envupd("stem", []string{""})
	} else {
		envupd("stem", []string{j.stem})
	}
	envupd("prereq", dup(j.p))
	envupd("pid", []string{itoa(mypid)})
	envupd("nproc", []string{itoa(slot)})
	envupd("newprereq", dup(j.np))
	envupd("alltarget", dup(j.at))
	var members []string
	for _, w := range j.np {
		if i := strings.IndexByte(w, '('); i >= 0 {
			if k := strings.IndexByte(w[i+1:], ')'); k >= 0 {
				members = append(members, w[i+1:i+1+k])
			}
		}
	}
	envupd("newmember", members)
	for i := 0; i < nregexp; i++ {
		name := "stem" + itoa(i)
		if j.r.attr&regexpattr != 0 && j.match[i] != nil {
			envupd(name, []string{*j.match[i]})
		} else {
			envupd(name, []string{""})
		}
	}
	return envy
}

func dup(w []string) []string { return append([]string(nil), w...) }

// shprint expands the variables mk set itself in a recipe, leaving
// quoted text and `{...} alone.
func shprint(s string, env []envent) string {
	var b strings.Builder
	for s != "" {
		r, n := utf8.DecodeRuneInString(s)
		if r == '$' {
			s = vexpand(s, env, &b)
			continue
		}
		b.WriteRune(r)
		s = s[n:]
		s = copyq(s, r, &b)
	}
	return b.String()
}

func mygetenv(name string, env []envent) (string, bool) {
	if env == nil {
		return "", false
	}
	if !weset[name] && !internals[name] {
		return "", false
	}
	for _, e := range env {
		if e.name == name {
			return strings.Join(e.values, " "), true
		}
	}
	return "", false
}

func vexpand(w string, env []envent, b *strings.Builder) string {
	p := w[1:]
	var name, q string
	brace := false
	if strings.HasPrefix(p, "{") {
		p = p[1:]
		if i := strings.IndexByte(p, '}'); i >= 0 {
			name, q, brace = p[:i], p[i+1:], true
		} else {
			name, q = p, ""
		}
	} else {
		n := shname(p)
		name, q = p[:n], p[n:]
	}
	_ = brace
	if s, ok := mygetenv(name, env); ok {
		b.WriteString(s)
	} else {
		b.WriteString(w[:len(w)-len(q)])
	}
	return q
}

func copysingle(s string, b *strings.Builder) string {
	for s != "" {
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		b.WriteRune(r)
		if r == '\'' {
			break
		}
	}
	return s
}

func copyq(s string, q rune, b *strings.Builder) string {
	if q == '\'' {
		return copysingle(s, b)
	}
	if q != '`' {
		return s
	}
	for s != "" {
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		b.WriteRune(r)
		if r == '}' {
			break
		}
		if r == '\'' {
			s = copysingle(s, b)
		}
	}
	return s
}
