// Command plumber routes plumbing messages between applications
// (Inferno plumber(8)). It reads the rules of plumbing(6), makes
// /chan/plumb.input and one /chan/plumb.<port> for each port the rules
// name, with file2chan, and then serves them in the background. A
// message written to plumb.input is matched against the rules and queued
// for the port they select; a read of that port blocks until a message
// is queued.
//
// A program named by "plumb start" is spawned directly, or, with -w,
// started by writing to the window manager's channel (default /chan/wm).
// /chan is a directory of #s in Inferno; when it is not, the plumber
// binds #s before it.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/names"
	"github.com/ericvh/purgatorio-go/sdk/plumbmsg"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/regex"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

type output struct {
	name    string
	io      *sys.FileIO
	queue   [][]byte
	started int
	startup string
	waiting bool
}

type port struct {
	name        string
	startup     string
	alwaysstart bool
}

type inmesg struct {
	msg        *plumbmsg.Msg
	text       string
	match      []regex.Range
	port       int
	startup    string
	args       []string
	attrs      []*plumbmsg.Attr
	clearclick bool
	set        bool
	n          [10]string
	dir        string
	file       string
}

// What process makes of a message, besides the index of its port.
const (
	handled    = -1
	unknown    = -2
	notstarted = -3
)

var (
	outputs   []*output
	input     *sys.FileIO
	rules     []*Rule
	ports     []*port
	wmstartup bool
	wmchan    = "/chan/wm"
	verbose   bool
	boundchan bool
)

func usage() {
	os.Stderr.WriteString("Usage: plumb [-vw] [-c wmchan] [initfile ...]\n")
	sys.Exits("fail:usage")
}

func main() {
	a := arg.New(os.Args)
	nogrp := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'w':
			wmstartup = true
		case 'c':
			if wmchan = a.Arg(); wmchan == "" {
				usage()
			}
		case 'v':
			verbose = true
		case 'n':
			nogrp = true
		default:
			usage()
		}
	}
	var err string
	rules, err = loadRules(a.Argv())
	if err != "" {
		os.Stderr.WriteString("plumb: " + err + "\n")
		sys.Exits("fail:init")
	}
	if !nogrp {
		sys.Pctl(sys.NEWPGRP, nil)
	}

	for _, r := range rules {
		for _, act := range r.action {
			if act.pred != "to" && act.pred != "alwaysstart" {
				continue
			}
			p := findport(act.arg)
			if p == nil {
				p = &port{name: act.arg, alwaysstart: act.pred == "alwaysstart"}
				ports = append([]*port{p}, ports...)
			}
			for _, s := range r.action {
				if s.pred == "start" {
					p.startup = s.arg
				}
			}
			break
		}
	}

	input = makefile("plumb.input")
	for _, p := range ports {
		outputs = append(outputs, &output{name: p.name, io: makefile("plumb." + p.name), startup: p.startup})
	}

	sys.Detach()
	serve()
}

func findport(name string) *port {
	for _, p := range ports {
		if p.name == name {
			return p
		}
	}
	return nil
}

func makefile(file string) *sys.FileIO {
	io, err := sys.File2chan("/chan", file)
	if err != nil && !boundchan && sys.Errstr() == "directory not a srv device" {
		boundchan = true
		if sys.Bind("#s", "/chan", sys.MBEFORE) == nil {
			io, err = sys.File2chan("/chan", file)
		}
	}
	if err != nil {
		os.Stderr.WriteString("plumb: can't establish /chan/" + file + ": " + sys.Errstr() + "\n")
		sys.Exits("fail:error")
	}
	return io
}

// serve answers writes of plumb.input and, for each port with messages
// queued, reads of that port. Reads of an empty port wait in the kernel
// until it has a message, as the plumber does not take them.
func serve() {
	for {
		ops := []sys.AltOp{{C: input.Write, Data: make([]byte, 64*1024+12)}}
		var which []int
		for j, o := range outputs {
			if len(o.queue) > 0 {
				ops = append(ops, sys.AltOp{C: o.io.Read, Data: make([]byte, 16)})
				which = append(which, j)
			}
		}
		i, err := sys.Alt(ops, true)
		if err != nil {
			sys.Exits("fail:error")
		}
		op := ops[i]
		if op.Closed {
			sys.Exits("fail:i/o on hungup channel")
		}
		if len(op.GotCap) == 0 || len(op.Got) < 12 {
			continue // a close: not interesting
		}
		rc := op.GotCap[0]
		if i == 0 {
			data := op.Got[12:]
			res := handle(parse(data))
			e := ""
			if res == unknown {
				e = "no matching plumb rule"
			}
			replyWrite(rc, len(data), e)
			continue
		}
		j := which[i-1]
		count := int(binary.LittleEndian.Uint32(op.Got[8:]))
		o := outputs[j]
		m := o.queue[0]
		if count < len(m) {
			replyRead(rc, nil, "buffer too short for message")
			continue
		}
		o.queue = o.queue[1:]
		replyRead(rc, m, "")
	}
}

// The file2chan replies of docs/abi.md: 0 and the data or count, or 1
// and an error.
func replyRead(rc sys.Chan, data []byte, e string) {
	var m []byte
	if e != "" {
		m = append([]byte{1}, e...)
	} else {
		m = append([]byte{0}, data...)
	}
	_ = rc.Send(m)
	_ = rc.Close()
}

func replyWrite(rc sys.Chan, n int, e string) {
	var m []byte
	if e != "" {
		m = append([]byte{1}, e...)
	} else {
		m = binary.LittleEndian.AppendUint32([]byte{0}, uint32(n))
	}
	_ = rc.Send(m)
	_ = rc.Close()
}

// handle routes one message and returns its fate.
func handle(in *inmesg) int {
	if in == nil {
		return handled
	}
	j, msg := process(in)
	switch j {
	case handled:
	case unknown:
		if in.msg.Src != "acme" {
			os.Stderr.WriteString("plumb: don't know who message goes to\n")
		}
	case notstarted:
		os.Stderr.WriteString("plumb: can't start application\n")
	default:
		outputs[j].queue = append(outputs[j].queue, msg)
	}
	return j
}

func parse(a []byte) *inmesg {
	msg := plumbmsg.Unpack(a)
	if msg == nil {
		return nil
	}
	in := &inmesg{msg: msg}
	if msg.Dst != "" {
		if control(in) {
			return nil
		}
		toport(in, msg.Dst)
	} else {
		in.port = -1
	}
	in.match = nomatch(10)
	in.attrs = plumbmsg.String2attrs(msg.Attr)
	return in
}

func nomatch(n int) []regex.Range {
	m := make([]regex.Range, n)
	for i := range m {
		m[i] = regex.Range{Beg: -1, End: -1}
	}
	return m
}

// control handles a client's start and stop messages, sent to "plumb".
func control(in *inmesg) bool {
	msg := in.msg
	if msg.Kind != "text" || msg.Dst != "plumb" {
		return false
	}
	text := string(msg.Data)
	switch text {
	case "start":
		start(msg.Src, 1)
	case "stop":
		start(msg.Src, -1)
	default:
		fmt.Fprintf(os.Stderr, "plumb: unrecognized control message from %s: %s\n", msg.Src, text)
	}
	return true
}

func start(name string, startstop int) {
	for _, o := range outputs {
		if o.name == name {
			o.waiting = false
			o.started += startstop
			return
		}
	}
	fmt.Fprintf(os.Stderr, "plumb: \"start\" message from unrecognized port %s\n", name)
}

// startup starts prog in dir, through the window manager with -w.
func startup(dir, prog string, args []string) {
	if wmstartup {
		if fd, err := sys.Open(wmchan, sys.OWRITE); err == nil {
			var w []string
			for _, s := range append([]string{dir, prog}, args...) {
				w = append(w, quote.Quote(s))
			}
			syscall.Write(fd, []byte("s "+strings.Join(w, " ")))
			sys.Close(fd)
			return
		}
	}
	path := prog
	if !strings.HasSuffix(path, ".wasm") {
		path += ".wasm"
	}
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "#") && !strings.HasPrefix(path, "./") {
		path = "/bin/" + path
	}
	// The child takes the current directory from us.
	here := ""
	if fd, err := sys.Open(".", sys.OREAD); err == nil {
		here, _ = sys.Fd2path(fd)
		sys.Close(fd)
	}
	sys.Chdir(dir)
	_, err := sys.Spawn(path, append([]string{prog}, args...), sys.NEWFD|sys.NEWPGRP|sys.FORKNS,
		[][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "plumb: can't load %s: %s\n", prog, sys.Errstr())
	}
	if here != "" {
		sys.Chdir(here)
	}
}

// shouldqueue says whether to queue messages for out while the program
// started for it connects.
func shouldqueue(out *output) bool {
	p := findport(out.name)
	if p == nil {
		os.Stderr.WriteString("plumb: can't happen in shouldqueue\n")
		return false
	}
	if p.alwaysstart {
		return false
	}
	return out.waiting
}

// process finds the port for in and packs the message for it.
func process(in *inmesg) (int, []byte) {
	if !clarify(in) || in.port < 0 {
		return unknown, nil
	}
	a := in.msg.Pack()
	j := in.port
	o := outputs[j]
	if o.started == 0 && !shouldqueue(o) {
		var path string
		var args []string
		switch {
		case in.startup != "":
			path = macro(in, in.startup)
			args = expand(in, in.args)
		case o.startup != "":
			path = o.startup
			args = []string{in.text}
		default:
			return notstarted, nil
		}
		logf("start %s port %s\n", path, o.name)
		o.waiting = true
		startup(in.msg.Dir, path, args)
		return handled, nil
	}
	var text string
	if in.msg.Kind != "text" {
		text = "message of type " + in.msg.Kind
	} else {
		r := []rune(in.text)
		for i := range r {
			if r[i] == '\n' {
				r = r[:i]
				break
			}
			if i > 50 {
				r = append(r[:i:i], []rune("...")...)
				break
			}
		}
		text = string(r)
	}
	logf("send \"%s\" to %s", text, o.name)
	return j, a
}

func expand(in *inmesg, args []string) []string {
	var r []string
	for _, s := range args {
		r = append(r, macro(in, s))
	}
	return r
}

// clarify resolves the message's destination by the rules if it has
// none.
func clarify(in *inmesg) bool {
	in.clearclick = false
	in.set = false
	msg := in.msg
	if msg.Kind != "text" {
		return false
	}
	in.text = string(msg.Data)
	if msg.Dst != "" {
		return true
	}
	return dorules(in)
}

func dorules(in *inmesg) bool {
	if verbose {
		logf("msg: %s", inmesg2s(in))
	}
	for _, r := range rules {
		if matchrule(in, r) {
			applyrule(in, r)
			if verbose {
				logf("yes")
			}
			return true
		} else if verbose {
			logf("no")
		}
	}
	return false
}

func inmesg2s(in *inmesg) string {
	m := in.msg
	s := fmt.Sprintf("src=%s; dst=%s; dir=%s; kind=%s; attr='%s'", m.Src, m.Dst, m.Dir, m.Kind, m.Attr)
	if m.Kind == "text" {
		s += "; data='" + string(m.Data) + "'"
	}
	return s
}

func matchrule(in *inmesg, r *Rule) bool {
	for i := range in.match {
		in.match[i] = regex.Range{Beg: -1, End: -1}
	}
	for _, p := range r.pattern {
		if !matchpattern(in, p) {
			return false
		}
	}
	return true
}

func applyrule(in *inmesg, r *Rule) {
	for _, p := range r.action {
		applypattern(in, p)
	}
	if in.clearclick {
		var al []*plumbmsg.Attr
		for _, a := range in.attrs {
			if a.Name != "click" {
				al = append([]*plumbmsg.Attr{a}, al...)
			}
		}
		in.attrs = al
		in.msg.Attr = plumbmsg.Attrs2string(al)
		if in.set {
			in.text = macro(in, "$0")
			in.msg.Data = []byte(in.text)
		}
	}
}

func matchpattern(in *inmesg, p *Pattern) bool {
	msg := in.msg
	var text string
	switch p.field {
	case "src":
		text = msg.Src
	case "dst":
		text = msg.Dst
	case "dir":
		text = msg.Dir
	case "kind":
		text = msg.Kind
	case "attr":
		text = msg.Attr
	case "data":
		text = in.text
	default:
		fmt.Fprintf(os.Stderr, "plumb: don't recognize pattern field %s\n", p.field)
		return false
	}
	if verbose {
		logf("'%s' %s '%s'\n", text, p.pred, p.arg)
	}
	switch p.pred {
	case "is":
		return text == p.arg
	case "isfile", "isdir":
		text = p.arg
		if p.expand {
			text = macro(in, text)
		}
		if text == "" {
			return false
		}
		if in.msg.Dir != "" && text[0] != '/' && text[0] != '#' {
			text = in.msg.Dir + "/" + text
		}
		text = names.Cleanname(text)
		d, err := sys.Stat(text)
		if err != nil {
			return false
		}
		isdir := d.Mode&sys.DMDIR != 0
		if p.pred == "isfile" && !isdir {
			in.file = text
			return true
		}
		if p.pred == "isdir" && isdir {
			in.dir = text
			return true
		}
		return false
	case "matches":
		val, click := plumbmsg.Lookup(in.attrs, "click")
		if p.field != "data" {
			click = false
		}
		r := []rune(text)
		if !click {
			m := p.re.Execute(r)
			if m == nil || m[0].Beg != 0 || m[0].End != len(r) {
				return false
			}
			in.match = m
			setvars(in, r)
			return true
		}
		c := 0
		fmt.Sscan(val, &c)
		m := clickmatch(p.re, r, c)
		if m == nil {
			return false
		}
		if in.match[0].Beg != -1 {
			return m[0] == in.match[0]
		}
		in.match = m
		setvars(in, r)
		in.clearclick = true
		in.set = true
		return true
	case "set":
		text = p.arg
		if p.expand {
			text = macro(in, text)
		}
		switch p.field {
		case "src":
			msg.Src = text
		case "dst":
			msg.Dst = text
		case "dir":
			msg.Dir = text
		case "kind":
			msg.Kind = text
		case "attr":
			msg.Attr = text
		case "data":
			in.text = text
			msg.Data = []byte(text)
			msg.Kind = "text"
			in.set = false
		}
		return true
	default:
		fmt.Fprintf(os.Stderr, "plumb: don't recognize pattern predicate %s\n", p.pred)
	}
	return false
}

func applypattern(in *inmesg, p *Pattern) {
	if p.field != "plumb" {
		fmt.Fprintf(os.Stderr, "plumb: don't recognize action field %s\n", p.field)
		return
	}
	switch p.pred {
	case "to", "alwaysstart":
		if in.port >= 0 {
			return
		}
		toport(in, p.arg)
	case "start":
		in.startup = p.arg
		in.args = p.extra
	default:
		fmt.Fprintf(os.Stderr, "plumb: don't recognize action %s\n", p.pred)
	}
}

func toport(in *inmesg, name string) int {
	for i, o := range outputs {
		if o.name == name {
			in.msg.Dst = name
			in.port = i
			return i
		}
	}
	in.port = -1
	fmt.Fprintf(os.Stderr, "plumb: unrecognized port %s\n", name)
	return -1
}

// clickmatch finds the leftmost match that reaches the click position.
func clickmatch(re *regex.Re, text []rune, click int) []regex.Range {
	for i := 0; i <= click && i < len(text); i++ {
		m := re.ExecuteSE(text, i, -1, i == 0, true)
		if m == nil {
			continue
		}
		if m[0].Beg >= i && m[0].End >= click {
			return m
		}
	}
	return nil
}

func setvars(in *inmesg, text []rune) {
	i := 0
	for ; i < len(in.match) && i < len(in.n) && in.match[i].Beg >= 0; i++ {
		in.n[i] = string(text[in.match[i].Beg:in.match[i].End])
	}
	for ; i < len(in.n); i++ {
		in.n[i] = ""
	}
}

// macro replaces the $ variables in text.
func macro(in *inmesg, text string) string {
	var word strings.Builder
	i, j := 0, 0
	for i < len(text) {
		c := text[i]
		i++
		if c != '$' {
			continue
		}
		if i == len(text) {
			break
		}
		word.WriteString(text[j : i-1])
		res, skip := dollar(in, text[i:])
		word.WriteString(res)
		i += skip
		j = i
	}
	if j < len(text) {
		word.WriteString(text[j:])
	}
	return word.String()
}

func dollar(in *inmesg, text string) (string, int) {
	if text[0] == '$' {
		return "$", 1
	}
	if text[0] >= '0' && text[0] <= '9' {
		return in.n[text[0]-'0'], 1
	}
	if len(text) < 3 {
		return "$", 0
	}
	switch text[:3] {
	case "src":
		return in.msg.Src, 3
	case "dst":
		return in.msg.Dst, 3
	case "dir":
		return in.dir, 3
	}
	if len(text) < 4 {
		return "$", 0
	}
	switch text[:4] {
	case "attr":
		return in.msg.Attr, 4
	case "data":
		return in.text, 4
	case "file":
		return in.file, 4
	case "kind":
		return in.msg.Kind, 4
	}
	return "$", 0
}

func logf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if s == "" {
		return
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	os.Stdout.WriteString("plumb: " + s)
}
