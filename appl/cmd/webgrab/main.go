// Command webgrab fetches a web page and the images and frames it refers
// to into local files (Inferno webgrab(1)).
//
// Unlike Inferno's, it can fetch https urls: crypto/tls runs over the
// connection's data file and checks the server against the certificates
// named by $SSL_CERT_FILE or $SSL_CERT_DIR.
package main

import (
	"crypto/tls"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const atomicio = 8192

var (
	stderr      = os.Stderr
	verbose     int
	postbody    string
	posting     bool
	httpproxy   *parsedURL
	noproxydoms []string
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("webgrab [-r] [-v[v]] [-p postbody] [-o stem] url")
	stem := ""
	rawflag := false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'r':
			rawflag = true
		case 'v':
			verbose++
		case 'o':
			stem = a.EArg()
		case 'p':
			postbody = a.EArg()
			posting = true
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if len(args) != 1 {
		a.Usage()
	}
	url := args[0]
	if !strings.Contains(url, "//") && !strings.Contains(url, ":") {
		url = "http://" + url
	}
	u := makeurl(url)
	if stem == "" {
		stem = getstem(u)
	}
	readconfig()
	grab(u, stem, rawflag)
}

func readconfig() {
	b, err := os.ReadFile("/services/webget/config")
	if err != nil {
		return
	}
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.IndexAny(line, " \t=")
		if i < 0 {
			continue
		}
		key, val := line[:i], strings.TrimLeft(line[i:], " \t=")
		if j := strings.IndexAny(val, "\r\n"); j >= 0 {
			val = val[:j]
		}
		if val == "" {
			continue
		}
		switch key {
		case "httpproxy":
			if val == "none" {
				continue
			}
			httpproxy = makeurl("http://" + val)
			if verbose > 0 {
				stderr.WriteString("Using http proxy " + httpproxy.String() + "\n")
			}
		case "noproxy", "noproxydoms":
			noproxydoms = strings.FieldsFunc(val, func(r rune) bool { return strings.ContainsRune(";, \t", r) })
		}
	}
}

// getstem makes the stem of the output files' names from u: the last
// component of its path, or its host, without its last extension, and
// then only the part after any remaining dot; "grabout" if that leaves
// nothing.
func getstem(u *parsedURL) string {
	stem := ""
	if u.path != "" {
		l, r := splitr(u.path, "/")
		if r == "" && l != "" {
			_, r = splitr(l[:len(l)-1], "/")
		}
		stem = r
	}
	if stem == "" {
		stem = u.host
	}
	if stem != "" {
		s, ext := splitr(stem, ".")
		if s == "" {
			stem = ext
		} else {
			stem = s[:len(s)-1]
		}
		_, stem = splitr(stem, ".")
	}
	if stem == "" {
		stem = "grabout"
	}
	return stem
}

// splitr splits s after the last occurrence of any char of cs.
func splitr(s, cs string) (string, string) {
	i := strings.LastIndexAny(s, cs) + 1
	return s[:i], s[i:]
}

func grab(u *parsedURL, stem string, rawflag bool) {
	contents, rest, actual, err := httpget(u)
	if err != "" {
		errorExit(err)
	}
	ish := ishtml(contents)
	if ish {
		contents = addfetchcomment(contents, u, actual)
	}
	if rawflag || !ish {
		writebytes(stem, contents, rest)
		return
	}
	contents, subs := subfix(contents, stem)
	writebytes(stem+".html", contents, rest)
	for _, s := range subs {
		subu := makeurl(s.url)
		subu.makeabsolute(actual)
		subcontents, subrest, _, suberr := httpget(subu)
		if suberr != "" {
			stderr.WriteString("webgrab: can't fetch subordinate " + s.file + " from " + subu.String() + ": " + suberr + "\n")
			continue
		}
		writebytes(s.file, subcontents, subrest)
	}
}

type sub struct{ file, url string }

// subfix replaces the urls in the src= and background= attributes of
// the tags in a with local names stem_1.suffix, stem_2.suffix, ..., and
// returns the new text and the files to fetch, in order.
func subfix(a []byte, stem string) ([]byte, []sub) {
	alen := len(a)
	if alen == 0 {
		return a, nil
	}
	var newa []byte
	var subs []sub
	j := 0
	intag, incom := false, false
	quote := 0
	i := 0
	for ; i < alen; i++ {
		c := int(a[i])
		if incom {
			if amatch(a, i, "-->") {
				incom = false
				i += 2
			}
		} else if intag {
			if quote == 0 && (amatch(a, i, "src") || amatch(a, i, "background")) {
				v := ""
				eqi := 0
				k := i + 10
				if amatch(a, i, "src") {
					k = i + 3
				}
				for k < alen && iswhite(int(a[k])) {
					k++
				}
				if k < alen && a[k] == '=' {
					eqi = k
					k++
					for k < alen && iswhite(int(a[k])) {
						k++
					}
					if k < alen {
						kstart := k
						c = int(a[k])
						if c == '\'' || c == '"' {
							quote = c
							k++
							for k < alen && int(a[k]) != quote {
								k++
							}
							v = string(a[kstart+1 : k])
							k++
						} else {
							for k < alen && !iswhite(int(a[k])) && a[k] != '>' {
								k++
							}
							v = string(a[kstart:k])
						}
					}
				}
				if v != "" {
					f := ""
					for _, s := range subs {
						if s.url == v {
							f = s.file
							break
						}
					}
					if f == "" {
						f = stem + "_" + strconv.Itoa(len(subs)+1) + getsuff(v)
						subs = append(subs, sub{f, v})
					}
					newa = append(newa, a[j:eqi+1]...)
					newa = append(newa, f...)
					j = min(k, alen)
				}
				i = k - 1
			}
			if c == '>' && quote == 0 {
				intag = false
			}
			if quote != 0 {
				if quote == c {
					quote = 0
				} else if c == '"' || c == '\'' {
					quote = c
				}
			}
		} else if c == '<' {
			intag = true
		}
	}
	if len(subs) == 0 {
		return a, nil
	}
	i = min(i, alen)
	if i > j {
		newa = append(newa, a[j:i]...)
	}
	return newa, subs
}

// amatch reports whether a at i starts with s, ignoring case in a.
func amatch(a []byte, i int, s string) bool {
	k := 0
	for ; i+k < len(a) && k < len(s); k++ {
		c := a[i+k]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[k] {
			break
		}
	}
	return k == len(s)
}

func getsuff(s string) string {
	p := makeurl(s).path
	for i := len(p) - 1; i >= 0; i-- {
		switch p[i] {
		case '.':
			return p[i:]
		case '/':
			return ""
		}
	}
	return ""
}

func iswhite(c int) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func addfetchcomment(a []byte, u, actu *parsedURL) []byte {
	ustr, actustr := u.String(), actu.String()
	c := "\n<!-- Fetched " + daytime.Text(daytime.Local(daytime.Now())) + " from " + ustr
	if ustr != actustr {
		c += ", redirected to " + actustr
	}
	c += " -->\n"
	return append(a[:len(a):len(a)], c...)
}

type connection interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
}

// httpget fetches u and returns the start of the body, the connection to
// read the rest from (nil when it has all been read), and the url it
// came from after redirections, or an error.
func httpget(u *parsedURL) ([]byte, connection, *parsedURL, string) {
	for redir := 0; redir < 10; redir++ {
		if u.port == "" {
			u.port = "80"
			if u.scheme == schHTTPS {
				u.port = "443"
			}
		}
		if verbose > 0 {
			stderr.WriteString("connecting to " + u.host + "\n")
		}
		dialhost, port := u.host, u.port
		proxied := httpproxy != nil && needproxy(u.host)
		if proxied {
			dialhost, port = httpproxy.host, httpproxy.port
		}
		dest := dial.Netmkaddr(dialhost, "tcp", port)
		c, err := dial.Dial(dest, "")
		if err != nil {
			return nil, nil, nil, "can't dial " + dest + ": " + err.Error()
		}
		var conn connection = c.Dfd
		if u.scheme == schHTTPS && !proxied {
			tc := tls.Client(&fileConn{c.Dfd}, &tls.Config{ServerName: u.host})
			if err := tc.Handshake(); err != nil {
				c.Dfd.Close()
				return nil, nil, nil, "can't dial " + dest + ": " + err.Error()
			}
			conn = tc
		}
		query := ""
		if u.query != "" {
			query = "?" + u.query
		}
		var req string
		switch {
		case posting:
			req = "POST /" + u.path + " HTTP/1.0\r\n" +
				"Host: " + u.host + "\r\n" +
				"Content-type: application/x-www-form-urlencoded\r\n" +
				"Content-length: " + strconv.Itoa(len(postbody)) + "\r\n" +
				"User-agent: Inferno/webgrab\r\n" +
				"\r\n" + postbody
		case proxied:
			req = "GET " + u.String() + " HTTP/1.0\r\n" + headers(u.host)
		default:
			req = "GET /" + u.path + query + " HTTP/1.0\r\n" + headers(u.host)
		}
		if verbose > 0 {
			stderr.WriteString("writing request: " + req + "\n")
		}
		if n, _ := conn.Write([]byte(req)); n != len(req) {
			return nil, nil, nil, "write problem: " + sys.Errstr()
		}
		ans, rest := readbytes(conn)
		status, body := stripline(ans)
		if verbose > 0 {
			stderr.WriteString("response: " + status + "\n")
		}
		vers, statusrest, _ := strings.Cut(status, " ")
		if !strings.HasPrefix(vers, "HTTP/") {
			return body, rest, nil, "bad reply status: " + status
		}
		code := atoi(statusrest)
		location := ""
		for {
			var hline string
			hline, body = stripline(body)
			if hline == "" {
				break
			}
			if verbose > 1 {
				stderr.WriteString(hline + "\n")
			}
			if iswhite(int(hline[0])) {
				continue
			}
			if hname, hrest, ok := strings.Cut(hline, ":"); ok && strings.ToLower(hname) == "location" {
				hval := strings.TrimLeft(hrest, ": \t")
				if i := strings.IndexAny(hval, " \t"); i >= 0 {
					hval = hval[:i]
				}
				location = hval
			}
		}
		if code != 200 {
			if (code == 300 || code == 301 || code == 302) && location != "" {
				if verbose > 0 {
					stderr.WriteString("redirect to " + location + "\n")
				}
				u = makeurl(location)
				continue
			}
			return body, rest, u, "status not ok: " + status
		}
		return body, rest, u, ""
	}
	return nil, nil, u, ""
}

func headers(host string) string {
	return "Host: " + host + "\r\n" +
		"User-agent: Inferno/webgrab\r\n" +
		"Cache-Control: no-cache\r\n" +
		"Pragma: no-cache\r\n\r\n"
}

// atoi is Limbo's string to int: leading blanks, a sign, then digits.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

func needproxy(h string) bool {
	if len(noproxydoms) == 0 {
		return true
	}
	for _, dom := range noproxydoms {
		if strings.HasSuffix(h, dom) {
			return false
		}
	}
	return true
}

// ishtml guesses that a is HTML when its first non-blank byte is '<'.
func ishtml(a []byte) bool {
	for _, c := range a {
		if !iswhite(int(c)) {
			return c == '<'
		}
	}
	return false
}

// readbytes reads up to atomicio bytes, and returns them and, if the
// connection may have more, the connection.
func readbytes(c connection) ([]byte, connection) {
	buf := make([]byte, atomicio)
	i := 0
	for i < len(buf) {
		n, _ := c.Read(buf[i:])
		if n <= 0 {
			return buf[:i], nil
		}
		i += n
	}
	return buf, c
}

func writebytes(f string, a []byte, c connection) {
	out := os.Stdout
	if f != "-" {
		fd, err := sys.Create(f, sys.OWRITE, 0o666)
		if err != nil {
			stderr.WriteString("webgrab: can't create " + f + ": " + err.Error() + "\n")
			return
		}
		out = os.NewFile(uintptr(fd), f)
		defer out.Close()
	}
	if _, err := out.Write(a); err != nil {
		stderr.WriteString("webgrab: write error: " + sys.Errstr() + "\n")
		return
	}
	clen := len(a)
	if c != nil {
		buf := make([]byte, atomicio)
		for {
			n, _ := c.Read(buf)
			if n <= 0 {
				break
			}
			if m, _ := out.Write(buf[:n]); m != n {
				stderr.WriteString("webgrab: write error: " + sys.Errstr() + "\n")
				return
			}
			clen += n
		}
	}
	if f != "-" {
		stderr.WriteString("created " + f + ", " + strconv.Itoa(clen) + " bytes\n")
	}
}

// stripline returns the text up to the first CRLF and what follows it,
// or nothing and b when there is no CRLF.
func stripline(b []byte) (string, []byte) {
	for i := 0; i < len(b)-1; i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return string(b[:i]), b[i+2:]
		}
	}
	return "", b
}

func errorExit(msg string) {
	stderr.WriteString(msg + "\n")
	sys.Exits("fail:error")
}

// fileConn makes a connection's data file a net.Conn for crypto/tls.
type fileConn struct{ f *os.File }

type addr string

func (a addr) Network() string { return "tcp" }
func (a addr) String() string  { return string(a) }

func (c *fileConn) Read(b []byte) (int, error)         { return c.f.Read(b) }
func (c *fileConn) Write(b []byte) (int, error)        { return c.f.Write(b) }
func (c *fileConn) Close() error                       { return c.f.Close() }
func (c *fileConn) LocalAddr() net.Addr                { return addr("local") }
func (c *fileConn) RemoteAddr() net.Addr               { return addr("remote") }
func (c *fileConn) SetDeadline(t time.Time) error      { return nil }
func (c *fileConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fileConn) SetWriteDeadline(t time.Time) error { return nil }
