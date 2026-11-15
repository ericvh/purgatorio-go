// Command sendmail sends the mail on its standard input by SMTP (Inferno
// sendmail(1)), to the server $smtp in the network data base. The
// recipients are those of the To: line, or the arguments; the sender
// that of the From: line, or the user, with $DOMAIN or
// /usr/user/mail/domain added when it has no domain.
//
// -s server, not in Inferno, names the server to use instead.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const strmax = 512

func main() {
	args := os.Args[1:]
	server := ""
	if len(args) >= 2 && args[0] == "-s" {
		server, args = args[1], args[2:]
	}
	var from string
	var tos, cc []string
	msgl := readin()
	for _, msg := range msgl {
		for _, s := range strings.SplitAfter(msg, "\n") {
			if from == "" {
				if from = match(s, "from"); from != "" {
					from = extract(from)
				}
			}
			if tos == nil {
				tos = lmatch(s, "to")
			}
			if cc == nil {
				cc = lmatch(s, "cc")
			}
		}
	}
	if tos != nil && len(args) > 0 {
		fail("recipients specified on To: line and as args - aborted", true)
	}
	if from == "" {
		from = readfile("/dev/user")
	}
	from = adddom(from)
	if tos == nil {
		tos = args
	}
	if err := smtpopen(server); err != "" {
		smtpclose()
		fail("smtp open failed: "+err, true)
	}
	msgl = append([]string{"From " + from + "\t" + time.Now().Format("Mon Jan _2 15:04:05 MST 2006") + "\n"}, msgl...)
	if err := smtpsend(from, tos, cc, msgl); err != "" {
		smtpclose()
		fail("send failed : "+err, false)
	}
	smtpclose()
}

func readin() []string {
	var ls []string
	var m strings.Builder
	b := bufio.NewReader(os.Stdin)
	for {
		s, err := b.ReadString('\n')
		if s == "" && err != nil {
			break
		}
		if m.Len() > strmax {
			ls = append(ls, m.String())
			m.Reset()
		}
		m.WriteString(s)
	}
	if m.Len() > 0 {
		ls = append(ls, m.String())
	}
	return ls
}

// match returns the value of header pat (in any case) on line s.
func match(s, pat string) string {
	if len(s) < len(pat) || !strings.EqualFold(s[:len(pat)], pat) {
		return ""
	}
	i := len(pat)
	switch {
	case i < len(s) && s[i] == ':':
		i++
	case i < len(s)-1 && s[i] == ' ' && s[i+1] == ':':
		i += 2
	default:
		return ""
	}
	return strings.Trim(s[i:], " \t\n")
}

func lmatch(s, pat string) []string {
	if r := match(s, pat); r != "" {
		return strings.FieldsFunc(r, func(c rune) bool { return c == ' ' || c == ',' || c == '\t' })
	}
	return nil
}

func extract(s string) string {
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[i+1:]
		if j := strings.IndexByte(s, '>'); j >= 0 {
			s = s[:j]
		}
	}
	return s
}

func adddom(s string) string {
	if s == "" || strings.Contains(s, "@") {
		return s
	}
	if dom := strings.TrimSuffix(os.Getenv("DOMAIN"), "\n"); dom != "" {
		return s + "@" + dom
	}
	if d := strings.TrimSuffix(readfile("/usr/"+s+"/mail/domain"), "\n"); d != "" {
		return s + "@" + d
	}
	return s
}

func readfile(f string) string {
	fd, err := sys.Open(f, sys.OREAD)
	if err != nil {
		return ""
	}
	file := os.NewFile(uintptr(fd), f)
	defer file.Close()
	buf := make([]byte, 128)
	n, _ := file.Read(buf)
	if n < 0 {
		return ""
	}
	return string(buf[:n])
}

func fail(s string, exit bool) {
	fmt.Fprintf(os.Stderr, "sendmail: %s\n", s)
	if exit {
		sys.Exits("")
	}
}

//
// SMTP, as Inferno's smtp(2).
//

var (
	conn *dial.Connection
	ibuf *bufio.Reader
	obuf *bufio.Writer
)

func smtpopen(server string) string {
	if server == "" {
		server = "$smtp"
	}
	server = dial.Netmkaddr(server, "tcp", "25")
	c, err := dial.Dial(server, "")
	if err != nil {
		return "dialup failed"
	}
	conn = c
	ibuf = bufio.NewReader(c.Dfd)
	obuf = bufio.NewWriter(c.Dfd)
	return mread()
}

func smtpsend(from string, tos, cc []string, mlist []string) string {
	if conn == nil {
		return "connection is not open"
	}
	if err := mcmd("RSET"); err != "" {
		return err
	}
	user, dom, _ := strings.Cut(from, "@")
	if from == "" || user == "" {
		return "no 'from' name"
	}
	if tos == nil {
		return "no 'to' name"
	}
	if dom == "" {
		return "no domain name"
	}
	if err := mcmd("HELO " + dom); err != "" {
		return err
	}
	if err := mcmd("MAIL FROM:<" + from + ">"); err != "" {
		return err
	}
	t := ""
	for _, to := range append(append([]string(nil), tos...), cc...) {
		if err := mcmd("RCPT TO:<" + to + ">"); err != "" {
			t += " " + err
		}
	}
	if t != "" {
		return t
	}
	if err := mcmd("DATA"); err != "" {
		return err
	}
	for _, msg := range mlist {
		for _, line := range strings.SplitAfter(msg, "\n") {
			if line == "" {
				continue
			}
			if putline(strings.TrimSuffix(line, "\n")) != nil {
				return "write to server failed: " + sys.Errstr()
			}
		}
	}
	if err := mcmd("."); err != "" {
		return err
	}
	return ""
}

func putline(line string) error {
	line = strings.TrimSuffix(line, "\r")
	if strings.HasPrefix(line, ".") {
		obuf.WriteByte('.')
	}
	obuf.WriteString(line)
	_, err := obuf.WriteString("\r\n")
	return err
}

func smtpclose() {
	if conn == nil {
		return
	}
	mwrite("QUIT")
	conn.Dfd.Close()
	conn.Cfd.Close()
	conn = nil
}

// mread reads a reply: lines of continuation (ddd-) and a last line.
func mread() string {
	for {
		line, err := ibuf.ReadString('\n')
		if line == "" && err == io.EOF || err != nil && err != io.EOF {
			return "could not read response from server"
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if len(line) < 3 {
			return "short response from server"
		}
		if len(line) > 3 && line[3] == '-' {
			continue
		}
		if line[0] == '1' || line[0] == '2' || line[0] == '3' {
			return ""
		}
		return line[3:]
	}
}

func mwrite(s string) bool {
	if _, err := obuf.WriteString(s + "\r\n"); err != nil {
		return false
	}
	return obuf.Flush() == nil
}

func mcmd(s string) string {
	if !mwrite(s) {
		return errname(s) + " send failed"
	}
	if r := mread(); r != "" {
		return errname(s) + " receive failed (" + r + ")"
	}
	return ""
}

func errname(s string) string {
	if i := strings.IndexAny(s, " :"); i >= 0 {
		return s[:i]
	}
	return s
}
