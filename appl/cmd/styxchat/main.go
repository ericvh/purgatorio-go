// Command styxchat talks Styx by hand (Inferno styxchat(8)). As a client
// it reads T-messages written as text, one per line, sends them and
// prints the replies; with -s it is the server, reading R-messages and
// printing the T-messages that arrive. The connection is a file, a
// network address with -n, or standard input, with the text then read
// from /dev/cons.
//
// Inferno reads the connection and the text in two threads; here the
// reading half is a second prog running this module.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

var (
	msgsize = 64 * 1024
	nexttag = 1
	verbose = 0
	client  = true
)

func main() {
	if len(os.Args) > 0 && os.Args[0] == readerName {
		reader(os.Args[1:])
		return
	}
	addr := false
	a := arg.New(os.Args)
	a.SetUsage("styxchat [-nsv] [-m messagesize] [dest]")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			msgsize = int(tonum(a.EArg()))
		case 's':
			client = false
		case 'n':
			addr = true
		case 'v':
			verbose++
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	var conn, in *os.File
	if len(args) == 0 {
		conn = os.Stdin
		fd, err := sys.Open("/dev/cons", sys.ORDWR)
		if err != nil {
			fatal("can't open /dev/cons: " + sys.Errstr())
		}
		sys.Dup(fd, 1)
		in = os.NewFile(uintptr(fd), "/dev/cons")
	} else {
		if len(args) > 1 {
			a.Usage()
		}
		in = os.Stdin
		dest := args[0]
		switch {
		case addr && client:
			dest = dial.Netmkaddr(dest, "net", "styx")
			c, err := dial.Dial(dest, "")
			if err != nil {
				fatal("can't dial " + dest + ": " + sys.Errstr())
			}
			conn = c.Dfd
		case addr:
			dest = dial.Netmkaddr(dest, "net", "styx")
			lc, err := dial.Announce(dest)
			if err != nil {
				fatal("can't announce " + dest + ": " + sys.Errstr())
			}
			c, err := dial.Listen(lc)
			if err != nil {
				fatal("can't listen on " + dest + ": " + sys.Errstr())
			}
			if conn, err = dial.Accept(c); err != nil {
				fatal("can't open " + c.Dir + "/data: " + sys.Errstr())
			}
		default:
			fd, err := sys.Open(dest, sys.ORDWR)
			if err != nil {
				fatal("can't open " + dest + ": " + sys.Errstr())
			}
			conn = os.NewFile(uintptr(fd), dest)
		}
	}
	chat(conn, in)
}

func fatal(s string) {
	os.Stderr.WriteString("styxchat: " + s + "\n")
	sys.Exits("fail:error")
}

func out(s string) { os.Stdout.WriteString(s) }

// readerName is argv[0] of the prog that reads the connection.
const readerName = "styxchat:reader"

// chat runs the reading half in a prog of its own, as Inferno runs it in
// a thread, and the writing half here, then waits for the reader to end.
func chat(conn, in *os.File) {
	me, _ := sys.Pctl(sys.NEWPGRP, nil)
	wfd, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
	if err != nil {
		fatal("can't open wait file: " + sys.Errstr())
	}
	mode := "c"
	if !client {
		mode = "s"
	}
	argv := []string{readerName, mode, strconv.Itoa(msgsize), strconv.Itoa(verbose)}
	pid, err := sys.Spawn("/bin/styxchat.wasm", argv, sys.NEWFD,
		[][2]int{{0, int(conn.Fd())}, {1, 1}, {2, 2}})
	if err != nil {
		fatal("can't start reader: " + sys.Errstr())
	}
	writer(conn, in)
	wait := os.NewFile(uintptr(wfd), "wait")
	buf := make([]byte, 256)
	for {
		n, err := wait.Read(buf)
		if err != nil || n == 0 {
			return
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(msg, strconv.Itoa(pid)+" ") {
			continue
		}
		if i := strings.Index(msg, "\":"); i >= 0 && msg[i+2:] != "" {
			sys.Exits(msg[i+2:])
		}
		return
	}
}

// reader is the reading half: it prints each message from fd 0 until
// the other end hangs up.
func reader(args []string) {
	if len(args) != 3 {
		return
	}
	client = args[0] == "c"
	msgsize, _ = strconv.Atoi(args[1])
	verbose, _ = strconv.Atoi(args[2])
	for receive(os.Stdin) {
	}
}

// writer is the writing half: it sends a message for each line of text.
func writer(conn, in *os.File) {
	r := bufio.NewReader(in)
	for {
		l, err := r.ReadString('\n')
		if l != "" {
			line(l, conn)
		}
		if err != nil {
			return
		}
	}
}

// receive reads and prints one message; false means the other end hung up.
func receive(conn *os.File) bool {
	msg, rerr := readmsg(conn)
	what, mtext := "R", "server hungup"
	if !client {
		what, mtext = "T", "clients hungup"
	}
	if msg == nil && rerr == "" {
		out("styxchat: " + mtext + "\n")
		return false
	}
	var f *styx.Fcall
	if rerr == "" {
		var err error
		f, err = styx.Unmarshal(msg, 0)
		if err != nil || (f.Type&1 == 1) != client || f.Type == styx.Terror {
			f, rerr = nil, "bad 9P "+what+"-message format"
		}
	}
	if f == nil {
		out("<- " + what + "msg.Readerror(0,\"" + rerr + "\")\n")
		quit()
	}
	if client {
		out("<- " + text(f) + "\n" + rdump(f))
	} else {
		out("<- " + text(f) + "\n")
	}
	return true
}

// quit kills the prog group, the writing half with it, and fails.
func quit() {
	me, _ := sys.Pctl(0, nil)
	if fd, err := sys.Open("/prog/"+strconv.Itoa(me)+"/ctl", sys.OWRITE); err == nil {
		f := os.NewFile(uintptr(fd), "ctl")
		f.WriteString("killgrp")
		f.Close()
	}
	sys.Exits("fail:error")
}

// readmsg reads one whole message, or returns nil at end of file, or an
// error string, as Inferno's Styx readmsg.
func readmsg(conn *os.File) ([]byte, string) {
	var sb [4]byte
	if n, err := io.ReadFull(conn, sb[:]); err != nil {
		if n == 0 && (err == io.EOF || sys.Errstr() == "") {
			return nil, ""
		}
		return nil, sys.Errstr()
	}
	ml := int(binary.LittleEndian.Uint32(sb[:]))
	if ml <= 4 {
		return nil, "invalid 9P message size"
	}
	if ml > msgsize {
		return nil, "9P message longer than agreed"
	}
	b := make([]byte, ml)
	copy(b, sb[:])
	if n, err := io.ReadFull(conn, b[4:]); err != nil {
		if n == 0 || err == io.ErrUnexpectedEOF {
			return nil, "9P message truncated"
		}
		return nil, sys.Errstr()
	}
	return b, ""
}

// line handles one line of text: a message to send or a command.
func line(l string, conn *os.File) {
	if l != "" && l[0] == '#' {
		return
	}
	var f *styx.Fcall
	var err string
	if client {
		f, err = tparse(l)
	} else {
		f, err = rparse(l)
	}
	if f == nil {
		if err != "" {
			out("?" + err + "\n")
		}
		return
	}
	if client && f.Tag == 0 {
		f.Tag = uint16(nexttag)
	}
	b, merr := styx.Marshal(nil, f)
	if merr != nil {
		what := "R"
		if client {
			what = "T"
		}
		os.Stderr.WriteString("styxchat: " + what + "-message conversion failed\n")
		return
	}
	if client {
		out("-> " + text(f) + "\n" + tdump(f))
	} else {
		out("-> " + text(f) + "\n")
	}
	if len(b) > msgsize {
		out(fmt.Sprintf("?message bigger than agreed: %d bytes\n", len(b)))
		return
	}
	if n, werr := conn.Write(b); werr != nil || n != len(b) {
		if client {
			out("?write error to server: " + sys.Errstr() + "\n")
		} else {
			out("?write error to clients: " + sys.Errstr() + "\n")
		}
	}
	if client && f.Tag != styx.NOTAG {
		nexttag++
	}
}

var names = map[uint8]string{
	styx.Tversion: "Version", styx.Rversion: "Version",
	styx.Tauth: "Auth", styx.Rauth: "Auth",
	styx.Tattach: "Attach", styx.Rattach: "Attach",
	styx.Rerror: "Error",
	styx.Tflush: "Flush", styx.Rflush: "Flush",
	styx.Twalk: "Walk", styx.Rwalk: "Walk",
	styx.Topen: "Open", styx.Ropen: "Open",
	styx.Tcreate: "Create", styx.Rcreate: "Create",
	styx.Tread: "Read", styx.Rread: "Read",
	styx.Twrite: "Write", styx.Rwrite: "Write",
	styx.Tclunk: "Clunk", styx.Rclunk: "Clunk",
	styx.Tremove: "Remove", styx.Rremove: "Remove",
	styx.Tstat: "Stat", styx.Rstat: "Stat",
	styx.Twstat: "Wstat", styx.Rwstat: "Wstat",
}

// text formats a message as Inferno's Tmsg.text and Rmsg.text do.
func text(f *styx.Fcall) string {
	kind := "Tmsg."
	if f.Type&1 == 1 {
		kind = "Rmsg."
	}
	s := fmt.Sprintf("%s%s(%d", kind, names[f.Type], f.Tag)
	switch f.Type {
	case styx.Tversion, styx.Rversion:
		return s + fmt.Sprintf(",%d,\"%s\")", int32(f.Msize), f.Version)
	case styx.Tauth:
		return s + fmt.Sprintf(",%d,\"%s\",\"%s\")", f.Afid, f.Uname, f.Aname)
	case styx.Tflush:
		return s + fmt.Sprintf(",%d)", f.Oldtag)
	case styx.Tattach:
		return s + fmt.Sprintf(",%d,%d,\"%s\",\"%s\")", f.Fid, f.Afid, f.Uname, f.Aname)
	case styx.Twalk:
		s += fmt.Sprintf(",%d,%d", f.Fid, f.Newfid)
		if len(f.Wname) == 0 {
			return s + ",nil)"
		}
		q := make([]string, len(f.Wname))
		for i, n := range f.Wname {
			q[i] = "\"" + n + "\""
		}
		return s + ",array[] of {" + strings.Join(q, ",") + "})"
	case styx.Topen:
		return s + fmt.Sprintf(",%d,%d)", f.Fid, f.Mode)
	case styx.Tcreate:
		return s + fmt.Sprintf(",%d,\"%s\",8r%o,%d)", f.Fid, f.Name, f.Perm, f.Mode)
	case styx.Tread:
		return s + fmt.Sprintf(",%d,%d,%d)", f.Fid, int64(f.Offset), f.Count)
	case styx.Twrite:
		return s + fmt.Sprintf(",%d,%d,array[%d] of byte)", f.Fid, int64(f.Offset), len(f.Data))
	case styx.Tclunk, styx.Tremove, styx.Tstat:
		return s + fmt.Sprintf(",%d)", f.Fid)
	case styx.Twstat, styx.Rstat:
		d, err := styx.UnmarshalDir(f.Stat)
		if err != nil {
			d = &styx.Dir{}
		}
		if f.Type == styx.Twstat {
			return s + fmt.Sprintf(",%d,%s)", f.Fid, dirtext(d))
		}
		return s + "," + dirtext(d) + ")"
	case styx.Rauth, styx.Rattach:
		return s + "," + qidtext(f.Qid) + ")"
	case styx.Rerror:
		return s + ",\"" + f.Ename + "\")"
	case styx.Rflush, styx.Rclunk, styx.Rremove, styx.Rwstat:
		return s + ")"
	case styx.Rwalk:
		q := make([]string, len(f.Wqid))
		for i, x := range f.Wqid {
			q[i] = qidtext(x)
		}
		return s + ",array[] of {" + strings.Join(q, ",") + "})"
	case styx.Ropen, styx.Rcreate:
		return s + fmt.Sprintf(",%s,%d)", qidtext(f.Qid), int32(f.Iounit))
	case styx.Rread:
		return s + fmt.Sprintf(",array[%d] of byte)", len(f.Data))
	case styx.Rwrite:
		return s + fmt.Sprintf(",%d)", int32(f.Count))
	}
	return s + ",ILLEGAL)"
}

func qidtext(q styx.Qid) string {
	return fmt.Sprintf("Qid(16r%x,%d,16r%.2x)", q.Path, int32(q.Vers), q.Type)
}

func dirtext(d *styx.Dir) string {
	return fmt.Sprintf("Dir(\"%s\",\"%s\",\"%s\",%s,8r%o,%d,%d,%d,16r%x,%d)",
		d.Name, d.UID, d.GID, qidtext(d.Qid), d.Mode, int32(d.Atime), int32(d.Mtime),
		int64(d.Length), d.Type, int32(d.Dev))
}

func rdump(f *styx.Fcall) string {
	if verbose == 0 || f.Type != styx.Rread {
		return ""
	}
	return dump(f.Data, verbose > 1)
}

func tdump(f *styx.Fcall) string {
	if verbose == 0 || f.Type != styx.Twrite {
		return ""
	}
	return dump(f.Data, verbose > 1)
}

func isprint(c byte) bool {
	return c >= 0x20 && c < 0x7f || c == '\n' || c == '\t' || c == '\r'
}

func textdump(a []byte) string {
	var b strings.Builder
	b.WriteString("\ttext(\"")
	for _, c := range a {
		switch c {
		case '\t':
			b.WriteString("\\t")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '"':
			b.WriteString("\\\"")
		default:
			if isprint(c) {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, "\\u%.4x", c)
			}
		}
	}
	b.WriteString("\")\n")
	return b.String()
}

func dump(a []byte, withText bool) string {
	if len(a) == 0 {
		return ""
	}
	printable := true
	for _, c := range a {
		if !isprint(c) {
			printable = false
			break
		}
	}
	if printable {
		return textdump(a)
	}
	s := fmt.Sprintf("\tdump(%x)\n", a)
	if withText {
		s += textdump(a)
	}
	return s
}

// unquoted splits s into words as Inferno's str->unquoted: blanks
// separate words, and single quotes protect blanks, with ” standing for
// a quote inside them.
func unquoted(s string) []string {
	var args []string
	var w strings.Builder
	inword, inquote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inword = true
			if inquote && i+1 < len(s) && s[i+1] == '\'' {
				w.WriteByte('\'')
				i++
			} else {
				inquote = !inquote
			}
		case !inquote && (c == ' ' || c == '\t' || c == '\n'):
			if inword {
				args = append(args, w.String())
				w.Reset()
				inword = false
			}
		default:
			inword = true
			w.WriteByte(c)
		}
	}
	if inword {
		args = append(args, w.String())
	}
	return args
}

// tonum converts a number as str->tobig with base 0: decimal, or
// radix notation such as 16rff, after optional blanks and a sign.
func tonum(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	base := int64(10)
	start := i
	for j := start; j < start+3 && j < len(s); j++ {
		if s[j] == 'r' && j > start {
			base = 0
			for _, c := range s[start:j] {
				base = base*10 + int64(c-'0')
			}
			if base < 2 || base > 36 {
				return 0
			}
			i = j + 1
			break
		} else if s[j] < '0' || s[j] > '9' {
			break
		}
	}
	var n int64
	for ; i < len(s); i++ {
		c := s[i]
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'z':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			d = int64(c-'A') + 10
		default:
			d = base
		}
		if d >= base {
			break
		}
		n = n*base + d
	}
	if neg {
		n = -n
	}
	return n
}

func val(s string) int64 {
	if s == "~0" {
		return -1
	}
	return tonum(s)
}

func fid(s string) uint32 {
	if s == "nofid" || s == "NOFID" {
		return styx.NOFID
	}
	return uint32(val(s))
}

func tag(s string) uint16 {
	if s == "~0" || s == "notag" || s == "NOTAG" {
		return styx.NOTAG
	}
	return uint16(tonum(s))
}

func tparse(s string) (*styx.Fcall, string) {
	av := unquoted(s)
	if len(av) == 0 {
		return nil, ""
	}
	argc := len(av)
	f := &styx.Fcall{}
	switch av[0] {
	case "Tversion":
		if argc != 3 {
			return nil, "usage: Tversion messagesize version"
		}
		*f = styx.Fcall{Type: styx.Tversion, Tag: styx.NOTAG, Msize: uint32(tonum(av[1])), Version: av[2]}
	case "Tauth":
		if argc != 4 {
			return nil, "usage: Tauth afid uname aname"
		}
		*f = styx.Fcall{Type: styx.Tauth, Afid: fid(av[1]), Uname: av[2], Aname: av[3]}
	case "Tflush":
		if argc != 2 {
			return nil, "usage: Tflush oldtag"
		}
		*f = styx.Fcall{Type: styx.Tflush, Oldtag: tag(av[1])}
	case "Tattach":
		if argc != 5 {
			return nil, "usage: Tattach fid afid uname aname"
		}
		*f = styx.Fcall{Type: styx.Tattach, Fid: fid(av[1]), Afid: fid(av[2]), Uname: av[3], Aname: av[4]}
	case "Twalk":
		if argc < 3 {
			return nil, "usage: Twalk fid newfid [name...]"
		}
		*f = styx.Fcall{Type: styx.Twalk, Fid: fid(av[1]), Newfid: fid(av[2]), Wname: av[3:]}
	case "Topen":
		if argc != 3 {
			return nil, "usage: Topen fid mode"
		}
		*f = styx.Fcall{Type: styx.Topen, Fid: fid(av[1]), Mode: uint8(tonum(av[2]))}
	case "Tcreate":
		if argc != 5 {
			return nil, "usage: Tcreate fid name perm mode"
		}
		*f = styx.Fcall{Type: styx.Tcreate, Fid: fid(av[1]), Name: av[2], Perm: uint32(tonum(av[3])), Mode: uint8(tonum(av[4]))}
	case "Tread":
		if argc != 4 {
			return nil, "usage: Tread fid offset count"
		}
		*f = styx.Fcall{Type: styx.Tread, Fid: fid(av[1]), Offset: uint64(tonum(av[2])), Count: uint32(tonum(av[3]))}
	case "Twrite":
		if argc != 4 {
			return nil, "usage: Twrite fid offset data"
		}
		*f = styx.Fcall{Type: styx.Twrite, Fid: fid(av[1]), Offset: uint64(tonum(av[2])), Data: []byte(av[3])}
	case "Tclunk", "Tremove", "Tstat":
		if argc != 2 {
			return nil, "usage: " + av[0] + " fid"
		}
		t := map[string]uint8{"Tclunk": styx.Tclunk, "Tremove": styx.Tremove, "Tstat": styx.Tstat}[av[0]]
		*f = styx.Fcall{Type: t, Fid: fid(av[1])}
	case "Twstat":
		if argc != 8 {
			return nil, "usage: Twstat fid name uid gid mode mtime length"
		}
		d := styx.Dir{Name: av[2], UID: av[3], GID: av[4], Mode: uint32(val(av[5])),
			Mtime: uint32(val(av[6])), Length: uint64(val(av[7]))}
		*f = styx.Fcall{Type: styx.Twstat, Fid: fid(av[1]), Stat: styx.MarshalDir(nil, &d)}
	case "nexttag":
		if argc < 2 {
			return nil, fmt.Sprintf("next tag is %d", nexttag)
		}
		nexttag = int(tag(av[1]))
		return nil, ""
	case "dump":
		verbose++
		return nil, ""
	default:
		return nil, "unknown message type"
	}
	return f, ""
}

func qid(s string) styx.Qid {
	var q styx.Qid
	fl := strings.FieldsFunc(s, func(r rune) bool { return r == '.' })
	if len(fl) > 0 {
		q.Path = uint64(tonum(fl[0]))
	}
	if len(fl) > 1 {
		q.Vers = uint32(tonum(fl[1]))
	}
	if len(fl) > 2 {
		q.Type = uint8(mode(fl[2]))
	}
	return q
}

func mode(s string) int {
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		return int(tonum(s))
	}
	m := 0
	for _, c := range s {
		switch c {
		case 'd':
			m |= sys.QTDIR
		case 'a':
			m |= sys.QTAPPEND
		case 'u':
			m |= sys.QTAUTH
		case 'l':
			m |= sys.QTEXCL
		case 'f':
		default:
			os.Stderr.WriteString(fmt.Sprintf("styxchat: unknown mode character %c, ignoring\n", c))
		}
	}
	return m
}

func rdir(a []string) styx.Dir {
	q := qid(a[0])
	return styx.Dir{Qid: q, Mode: uint32(tonum(a[1])) | uint32(q.Type)<<24,
		Atime: uint32(tonum(a[2])), Mtime: uint32(tonum(a[3])), Length: uint64(tonum(a[4])),
		Name: a[5], UID: a[6], GID: a[7], MUID: a[8]}
}

func rparse(s string) (*styx.Fcall, string) {
	av := unquoted(s)
	if len(av) == 0 {
		return nil, ""
	}
	argc := len(av)
	var f styx.Fcall
	switch av[0] {
	case "Rversion":
		if argc != 4 {
			return nil, "usage: Rversion tag messagesize version"
		}
		f = styx.Fcall{Type: styx.Rversion, Tag: tag(av[1]), Msize: uint32(tonum(av[2])), Version: av[3]}
	case "Rauth":
		if argc != 3 {
			return nil, "usage: Rauth tag aqid"
		}
		f = styx.Fcall{Type: styx.Rauth, Tag: tag(av[1]), Qid: qid(av[2])}
	case "Rflush", "Rclunk", "Rremove":
		if argc != 2 {
			return nil, "usage: " + av[0] + " tag"
		}
		t := map[string]uint8{"Rflush": styx.Rflush, "Rclunk": styx.Rclunk, "Rremove": styx.Rremove}[av[0]]
		f = styx.Fcall{Type: t, Tag: tag(av[1])}
	case "Rattach":
		if argc != 3 {
			return nil, "usage: Rattach tag qid"
		}
		f = styx.Fcall{Type: styx.Rattach, Tag: tag(av[1]), Qid: qid(av[2])}
	case "Rwalk":
		if argc < 2 {
			return nil, "usage: Rwalk tag [qid ...]"
		}
		f = styx.Fcall{Type: styx.Rwalk, Tag: tag(av[1]), Wqid: []styx.Qid{}}
		for _, q := range av[2:] {
			f.Wqid = append(f.Wqid, qid(q))
		}
	case "Ropen", "Rcreate":
		if argc != 4 {
			return nil, "usage: " + av[0] + " tag qid iounit"
		}
		t := styx.Ropen
		if av[0] == "Rcreate" {
			t = styx.Rcreate
		}
		f = styx.Fcall{Type: t, Tag: tag(av[1]), Qid: qid(av[2]), Iounit: uint32(tonum(av[3]))}
	case "Rread":
		if argc != 3 {
			return nil, "usage: Rread tag data"
		}
		f = styx.Fcall{Type: styx.Rread, Tag: tag(av[1]), Data: []byte(av[2])}
	case "Rwrite":
		if argc != 3 {
			return nil, "usage: Rwrite tag count"
		}
		f = styx.Fcall{Type: styx.Rwrite, Tag: tag(av[1]), Count: uint32(tonum(av[2]))}
	case "Rstat":
		if argc != 11 {
			return nil, "usage: Rstat tag qid mode atime mtime length name uid gid muid"
		}
		d := rdir(av[2:])
		f = styx.Fcall{Type: styx.Rstat, Tag: tag(av[1]), Stat: styx.MarshalDir(nil, &d)}
	case "Rwstat":
		// Inferno wants eight words here, though only the tag is used.
		if argc != 8 {
			return nil, "usage: Rwstat tag"
		}
		f = styx.Fcall{Type: styx.Rwstat, Tag: tag(av[1])}
	case "Rerror":
		if argc != 3 {
			return nil, "usage: Rerror tag ename"
		}
		f = styx.Fcall{Type: styx.Rerror, Tag: tag(av[1]), Ename: av[2]}
	case "dump":
		verbose++
		return nil, ""
	default:
		return nil, "unknown message type"
	}
	return &f, ""
}
