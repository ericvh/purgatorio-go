// Command ftpfs serves the files of an FTP server as a Styx file system
// (Inferno ftpfs(4)), mounted on /n/ftp unless -m says otherwise.
//
// ftpfs dials the FTP server and logs in, then starts the Styx server as
// a second prog (ftpfs.wasm with a hidden -S flag), which gets the FTP
// connection and one end of a pipe, and mounts the other end. A guest is
// single threaded and mount needs the server's answers before it
// returns, so the two cannot be one prog, as they are threads in
// ftpfs.b. The server waits for Styx messages with a ten second timeout,
// and keeps the FTP connection alive with NOOP after a hundred idle
// seconds, as ftpfs.b's heartbeat thread does.
//
// Remote files are cached in memory, not in /tmp/ftp.* files. The
// firewall proxy (proxy!host) and factotum are not supported.
package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Reply classes: the first digit of a reply code, or a failure.
const (
	syserr     = -2
	syntax     = -1
	shutdown   = 0
	extra      = 1
	success    = 2
	incomplete = 3
	tempfail   = 4
	permfail   = 5
	errclass   = 7
)

const (
	enosuchfile  = "file does not exist"
	eftpproto    = "ftp protocol error"
	eshutdown    = "remote shutdown"
	eioerror     = "io error"
	enotadir     = "not a directory"
	eisadir      = "is a directory"
	epermission  = "permission denied"
	ebadoffset   = "bad offset"
	ebadlength   = "bad length"
	enowstat     = "wstat not implemented"
	eauthnotreqd = "authentication not required"
)

var (
	ftpout   *os.File // the control connection
	ftpin    *bufio.Reader
	debug    int
	quiet    bool
	active   bool
	errstr   string
	activity bool // a request was sent since the last heartbeat
	transfer bool // a data transfer is going on
	netaddr  string
	stdout   = os.Stdout
	stderr   = os.Stderr
)

func print(s string) { stdout.WriteString(s) }

func main() {
	if len(os.Args) == 6 && os.Args[1] == "-S" {
		// The server prog: -S netdir remrootpath debug active.
		debug, _ = strconv.Atoi(os.Args[4])
		active = os.Args[5] == "1"
		ftpout = os.NewFile(5, "ftp")
		ftpin = bufio.NewReader(ftpout)
		netaddr, _ = getnet(os.Args[2])
		serve(os.Args[3])
		return
	}
	mountpoint := "/n/ftp"
	user, password := "", ""
	cdtoroot := false
	a := arg.New(os.Args)
	a.SetUsage("ftpfs [-/dpq] [-m mountpoint] [-a password] ftphost")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'd':
			debug++
		case '/':
			cdtoroot = true
		case 'p':
			active = true
		case 'q':
			quiet = true
		case 'm':
			mountpoint = a.EArg()
		case 'a':
			password = a.EArg()
			user = "anonymous"
		case 'k':
			a.EArg()
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) != 1 {
		a.Usage()
	}
	hostname := argv[0]
	if len(hostname) > 6 && hostname[:6] == "proxy!" {
		print("dial firewall service tcp!$proxy!402 failed: firewall proxy not supported\n")
		return
	}
	d := dial.Netmkaddr(hostname, "tcp", "ftp")
	c, err := dial.Dial(d, "")
	if err != nil {
		fatal("dial " + d + " failed: " + err.Error())
	}
	if debug > 0 {
		print("localdir " + c.Dir + "\n")
	}
	ftpout = c.Dfd
	ftpin = bufio.NewReader(ftpout)
	rv, l := getreply(!quiet)
	if rv != success {
		fail(rv, l)
	}
	stdin := bufio.NewReader(os.Stdin)
	if user == "" {
		user = prompt(stdin, "User", getuser(), true)
	}
	if rv = sendrequest("USER "+user, false); rv != success {
		sendfail(rv)
	}
	rv, code, l := getfullreply(!quiet)
	if rv != success {
		if rv != incomplete {
			fail(rv, l)
		}
		if code == 331 {
			if password == "" {
				password = prompt(stdin, "Password", "", false)
			}
			if rv = sendrequest2("PASS "+password, false, "PASS XXXX"); rv != success {
				sendfail(rv)
			}
			if rv, l = getreply(false); rv != success {
				fail(rv, l)
			}
		}
	}
	if cdtoroot {
		command("CWD /")
	}
	command("TYPE I")
	l = command("PWD")
	remrootpath := pwd(l)

	p, err := sys.Pipe()
	if err != nil {
		print("can't create pipe: " + sys.Errstr() + "\n")
		return
	}
	dbg, act := strconv.Itoa(debug), "0"
	if active {
		act = "1"
	}
	_, err = sys.Spawn("/bin/ftpfs.wasm", []string{"ftpfs", "-S", c.Dir, remrootpath, dbg, act},
		sys.NEWFD|sys.FORKNS, [][2]int{{0, p[1]}, {1, 1}, {2, 2}, {5, int(c.Dfd.Fd())}, {6, int(c.Cfd.Fd())}})
	if err != nil {
		fatal("cannot start server: " + sys.Errstr())
	}
	sys.Close(p[1])
	c.Dfd.Close()
	c.Cfd.Close()
	if _, err := sys.Mount(p[0], -1, mountpoint, sys.MREPL|sys.MCREATE, ""); err != nil {
		stderr.WriteString("ftpfs: mount " + mountpoint + " failed: " + sys.Errstr() + "\n")
		sys.Exits("fail:mount")
	}
}

func fatal(s string) {
	stderr.WriteString("ftpfs: " + s + "\n")
	sys.Exits("fail:" + s)
}

// command sends req and expects success; anything else ends ftpfs.
func command(req string) string {
	if rv := sendrequest(req, false); rv != success {
		sendfail(rv)
	}
	rv, l := getreply(false)
	if rv != success {
		fail(rv, l)
	}
	return l
}

func fail(s int, l string) {
	switch s {
	case syserr:
		print("read fail: " + sys.Errstr() + "\n")
	case syntax:
		print(eftpproto + "\n")
	case shutdown:
		print(eshutdown + "\n")
	default:
		print("unexpected response: " + l + "\n")
	}
	sys.Exits("")
}

func sendfail(s int) {
	switch s {
	case syserr:
		print("write fail: " + sys.Errstr() + "\n")
	case shutdown:
		print(eshutdown + "\n")
	default:
		print("internal error\n")
	}
	sys.Exits("")
}

func readfile(name string) string {
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

func getuser() string {
	name := "anon"
	b, err := os.ReadFile("/dev/user")
	switch {
	case err != nil:
		print("warning: could not open /dev/user: " + err.Error() + "\n")
	case len(b) == 0:
		print("warning: empty /dev/user\n")
	default:
		name = string(b)
	}
	return name
}

func raw(on bool) {
	fd, err := sys.Open("/dev/consctl", sys.OWRITE)
	if err != nil {
		stderr.WriteString("ftpfs: cannot open /dev/consctl: " + sys.Errstr() + "\n")
		return
	}
	f := os.NewFile(uintptr(fd), "consctl")
	if on {
		f.WriteString("rawon")
	} else {
		f.WriteString("rawoff")
	}
	f.Close()
}

func prompt(in *bufio.Reader, p, def string, echo bool) string {
	if def == "" {
		print(p + ": ")
	} else {
		print(p + "[" + def + "]: ")
	}
	if !echo {
		raw(true)
	}
	s, _ := in.ReadString('\n')
	if !echo {
		raw(false)
		print("\n")
	}
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return def
	}
	return s
}

// pwd takes the directory from a reply to PWD: the text between the
// first two double quotes.
func pwd(s string) string {
	i := strings.IndexByte(s, '"')
	if i < 0 || len(s)-i < 2 {
		return "/"
	}
	s = s[i+1:]
	if j := strings.IndexByte(s, '"'); j >= 0 {
		s = s[:j]
	}
	return s
}

// The FTP protocol.

func getfullreply(echo bool) (int, int, string) {
	reply := ""
	code := -1
	for {
		s, err := ftpin.ReadString('\n')
		if s == "" && err != nil {
			return shutdown, 0, ""
		}
		if len(s) >= 2 && s[len(s)-1] == '\n' {
			if s[len(s)-2] == '\r' {
				s = s[:len(s)-2]
			} else {
				s = s[:len(s)-1]
			}
		}
		if debug > 0 || echo {
			print(s + "\n")
		}
		reply += s
		if code < 0 {
			if len(s) < 3 {
				return syntax, 0, ""
			}
			code = atoi(s[:3])
			if len(s) < 4 || s[3] != '-' {
				break
			}
		}
		if len(s) >= 4 && atoi(s[:3]) == code && s[3] == ' ' {
			break
		}
	}
	if code < 100 {
		return syntax, 0, ""
	}
	return code / 100, code, reply
}

func getreply(echo bool) (int, string) {
	c, _, s := getfullreply(echo)
	return c, s
}

func sendrequest2(req string, echo bool, figleaf string) int {
	activity = true
	if debug > 0 || echo {
		print(figleaf + "\n")
	}
	b := []byte(req + "\r\n")
	n, err := ftpout.Write(b)
	if err != nil && n == 0 {
		return syserr
	}
	if n != len(b) {
		return shutdown
	}
	return success
}

func sendrequest(req string, echo bool) int { return sendrequest2(req, echo, req) }

// atoi is Limbo's string to int.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func tokenize(s, delim string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delim, r) })
}

// getnet returns the local address of the connection in dir.
func getnet(dir string) (string, int) {
	s := readfile(dir + "/local")
	if s == "" {
		if debug > 0 {
			stderr.WriteString("open " + dir + "/local: " + sys.Errstr() + "\n")
		}
		return "", 0
	}
	l := tokenize(s, "!")
	if len(l) != 2 {
		return "", 0
	}
	if debug > 0 {
		print("net is " + l[0] + "!" + strconv.Itoa(atoi(l[1])) + "\n")
	}
	return l[0], atoi(l[1])
}

func dataport(l []string) string {
	return "tcp!" + l[0] + "." + l[1] + "." + l[2] + "." + l[3] + "!" + strconv.Itoa(atoi(l[4])*256+atoi(l[5]))
}

// passive opens a data connection the server offers with PASV and
// sends cmd on it.
func passive(cmd string) *os.File {
	if sendrequest("PASV", false) != success {
		return nil
	}
	r, m := getreply(false)
	if r != success {
		return nil
	}
	p := m
	if i := strings.IndexByte(m, '('); i >= 0 {
		p = m[i+1:]
	}
	l := tokenize(p, ",")
	if len(l) < 6 {
		print("data: " + m + "\n")
		return nil
	}
	a := dataport(l)
	if debug > 0 {
		print("data dial " + a + "\n")
	}
	d, err := dial.Dial(a, "")
	if err != nil {
		return nil
	}
	d.Cfd.Close()
	if sendrequest(cmd, false) != success {
		d.Dfd.Close()
		return nil
	}
	if r, _ = getreply(false); r != extra {
		d.Dfd.Close()
		return nil
	}
	return d.Dfd
}

// activate announces a port, tells the server to call it with PORT,
// and sends cmd.
func activate(cmd string) *os.File {
	lc, err := dial.Announce("tcp!" + netaddr + "!0")
	if err != nil {
		return nil
	}
	defer lc.Cfd.Close()
	h, port := getnet(lc.Dir)
	t := "PORT " + strings.Join(tokenize(h, "."), ",") + "," + strconv.Itoa(port/256) + "," + strconv.Itoa(port&255)
	if sendrequest(t, false) != success {
		return nil
	}
	if r, _ := getreply(false); r != success {
		return nil
	}
	if sendrequest(cmd, false) != success {
		return nil
	}
	if r, _ := getreply(false); r != extra {
		return nil
	}
	dc, err := dial.Listen(lc)
	if err != nil {
		stderr.WriteString("activate: listen failed: " + err.Error() + "\n")
		return nil
	}
	defer dc.Cfd.Close()
	fd, err := sys.Open(dc.Dir+"/data", sys.ORDWR)
	if debug > 0 {
		print("activate: data connection on " + dc.Dir + "\n")
	}
	if err != nil {
		stderr.WriteString("activate: open of " + dc.Dir + " failed: " + sys.Errstr() + "\n")
		return nil
	}
	return os.NewFile(uintptr(fd), dc.Dir+"/data")
}

func data(cmd string) *os.File {
	if active {
		return activate(cmd)
	}
	return passive(cmd)
}

func seterr(e int, s string) int {
	switch e {
	case syserr:
		errstr = eioerror
	case syntax:
		errstr = eftpproto
	case shutdown:
		errstr = eshutdown
	default:
		errstr = s
	}
	return -1
}
