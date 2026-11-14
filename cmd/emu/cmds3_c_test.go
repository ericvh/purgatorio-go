package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const cmds3cDir = root + "/tmp/cmds3c"

func cmds3cSetup(t *testing.T) {
	t.Helper()
	os.RemoveAll(cmds3cDir)
	for _, d := range []string{"p1", "p2", "mnt"} {
		if err := os.MkdirAll(cmds3cDir+"/"+d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(cmds3cDir) })
}

// cmds3cServe answers each connection on a new loopback listener with
// h, and returns the port.
func cmds3cServe(t *testing.T, h func(net.Conn)) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				h(c)
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// TestCmds3CNet runs whois, telnet and webgrab against servers on the
// loopback interface.
func TestCmds3CNet(t *testing.T) {
	cmds3cSetup(t)
	var mu sync.Mutex
	var got []string
	record := func(s string) {
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	}

	// whois: a server that refers to a second one.
	who2 := cmds3cServe(t, func(c net.Conn) {
		l, _ := bufio.NewReader(c).ReadString('\n')
		record(fmt.Sprintf("whois2 %q", l))
		io.WriteString(c, "domain: example.com\nowner: nobody\n")
	})
	who1 := cmds3cServe(t, func(c net.Conn) {
		bufio.NewReader(c).ReadString('\n')
		fmt.Fprintf(c, "%% IANA\nrefer: tcp!127.0.0.1!%d\n", who2)
	})

	// telnet: a server that negotiates and reports the answers, and one
	// that reports what it was sent.
	tn1 := cmds3cServe(t, func(c net.Conn) {
		io.WriteString(c, "Welcome\r\n\xff\xfb\x01\xff\xfd\x1f\xff\xffend\r\n")
		b := make([]byte, 6)
		io.ReadFull(c, b)
		record(fmt.Sprintf("telnet replies % x", b))
	})
	tn2 := cmds3cServe(t, func(c net.Conn) {
		b := make([]byte, 6)
		io.ReadFull(c, b)
		record(fmt.Sprintf("telnet sent %q", b))
	})

	// webgrab: an HTTP server, and an HTTPS one whose certificate is in
	// ca.pem.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	web := l.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/index.html", func(w http.ResponseWriter, r *http.Request) {
		record(fmt.Sprintf("http %s %s agent %q", r.Method, r.URL, r.UserAgent()))
		io.WriteString(w, `<html><body><img src="pics/a.gif"><IMG SRC=b.png alt='x'><img src="pics/a.gif"></body></html>`)
	})
	mux.HandleFunc("/pics/a.gif", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "GIF89a") })
	mux.HandleFunc("/b.png", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "PNG") })
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", fmt.Sprintf("http://127.0.0.1:%d/plain.txt", web))
		w.WriteHeader(302)
	})
	mux.HandleFunc("/plain.txt", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "plain text\n") })
	go http.Serve(l, mux)
	tlsport := cmds3cTLS(t)

	script := fmt.Sprintf(`whois -a tcp!127.0.0.1!%[1]d example.com
whois -n -a tcp!127.0.0.1!%[1]d example.com
whois -a tcp!127.0.0.1!1 x; echo whois $status
whois; echo whois $status
bind '#|' /tmp/cmds3c/p1
sleep 5 > /tmp/cmds3c/p1/data1 &
telnet 127.0.0.1 %[2]d < /tmp/cmds3c/p1/data | xd -c
bind '#|' /tmp/cmds3c/p2
{echo hello; sleep 5} > /tmp/cmds3c/p2/data1 &
telnet 127.0.0.1 %[3]d < /tmp/cmds3c/p2/data
echo telnet $status
telnet; echo telnet $status
webgrab -r -o - http://127.0.0.1:%[4]d/plain.txt
webgrab -o /tmp/cmds3c/index http://127.0.0.1:%[4]d/index.html
cat /tmp/cmds3c/index_1.gif /tmp/cmds3c/index_2.png; echo
webgrab -o /tmp/cmds3c/plain http://127.0.0.1:%[4]d/old
cat /tmp/cmds3c/plain
webgrab http://127.0.0.1:%[4]d/missing; echo webgrab $status
webgrab -r -o - https://127.0.0.1:%[5]d/s; echo webgrab $status
SSL_CERT_FILE=/tmp/cmds3c/ca.pem
webgrab -r -o - https://127.0.0.1:%[5]d/s
webgrab; echo webgrab $status
`, who1, tn1, tn2, web, tlsport)
	if err := os.WriteFile(cmds3cDir+"/net.sh", []byte(script), 0o666); err != nil {
		t.Fatal(err)
	}
	out, st := emu(t, "", "sh", "/tmp/cmds3c/net.sh")
	want := fmt.Sprintf(`[using server tcp!127.0.0.1!%[1]d]
[using server tcp!127.0.0.1!%[2]d]
domain: example.com
owner: nobody
[using server tcp!127.0.0.1!%[1]d]
%% IANA
refer: tcp!127.0.0.1!%[2]d
[using server tcp!127.0.0.1!1]
whois: dialing tcp!127.0.0.1!1: connection refused
whois errors
usage: whois [-a addr] [-n] host
whois usage
telnet: connected to 127.0.0.1
telnet: remote host closed connection
0000000 W  e  l  c  o  m  e  \r \n ff e  n  d  \r \n
000000f
telnet: connected to 127.0.0.1
hello
telnet: remote host closed connection
telnet 
usage: telnet host [port]
telnet usage
plain text
created /tmp/cmds3c/index.html, 219 bytes
created /tmp/cmds3c/index_1.gif, 6 bytes
created /tmp/cmds3c/index_2.png, 3 bytes
GIF89aPNG
created /tmp/cmds3c/plain, 11 bytes
plain text
status not ok: HTTP/1.0 404 Not Found
webgrab error
can't dial tcp!127.0.0.1!%[3]d: tls: failed to verify certificate: x509: certificate signed by unknown authority
webgrab error
secret
usage: webgrab [-r] [-v[v]] [-p postbody] [-o stem] url
webgrab usage
`, who1, who2, tlsport)
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
	html, _ := os.ReadFile(cmds3cDir + "/index.html")
	re := regexp.MustCompile(fmt.Sprintf(`^<html><body><img src=/tmp/cmds3c/index_1.gif><IMG SRC=/tmp/cmds3c/index_2.png alt='x'><img src=/tmp/cmds3c/index_1.gif></body></html>
<!-- Fetched [A-Z][a-z][a-z] [A-Z][a-z][a-z] \d\d \d\d:\d\d:\d\d [A-Z]+ \d{4} from http://127.0.0.1:%d/index.html -->
$`, web))
	if !re.Match(html) {
		t.Errorf("index.html: %q", html)
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(got)
	wantgot := []string{
		`http GET /index.html agent "Inferno/webgrab"`, `http GET /index.html agent "Inferno/webgrab"`,
		`telnet replies ff fd 01 ff fc 1f`, `telnet replies ff fd 01 ff fc 1f`,
		`telnet sent "hello\n"`, `telnet sent "hello\n"`,
		`whois2 "example.com\r\n"`, `whois2 "example.com\r\n"`,
	}
	if strings.Join(got, "\n") != strings.Join(wantgot, "\n") {
		t.Errorf("servers saw:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantgot, "\n"))
	}
}

// cmds3cTLS starts an HTTPS server with a new self-signed certificate,
// which it writes to ca.pem, and returns its port.
func cmds3cTLS(t *testing.T) int {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmds3cDir+"/ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o666); err != nil {
		t.Fatal(err)
	}
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secret\n") }))
	return l.Addr().(*net.TCPAddr).Port
}

// TestCmds3CFtpfs mounts a small FTP server with ftpfs, passive and
// active, and works on its files. The script leaves the server's files
// as it found them, for the second wazero mode.
func TestCmds3CFtpfs(t *testing.T) {
	cmds3cSetup(t)
	s := cmds3cNewFTP()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.serve(l)
	port := l.Addr().(*net.TCPAddr).Port
	script := fmt.Sprintf(`ftpfs -m /tmp/cmds3c/mnt -a me@ tcp!127.0.0.1!%[1]d
echo ftpfs $status
ls -l /tmp/cmds3c/mnt
cat /tmp/cmds3c/mnt/hello.txt /tmp/cmds3c/mnt/sub/a.txt
echo new file > /tmp/cmds3c/mnt/new.txt
cat /tmp/cmds3c/mnt/new.txt
mkdir /tmp/cmds3c/mnt/dir
rm /tmp/cmds3c/mnt/hello.txt
ls /tmp/cmds3c/mnt
ls -l /tmp/cmds3c/mnt/new.txt
cat /tmp/cmds3c/mnt/nothere
chmod 600 /tmp/cmds3c/mnt/new.txt
rm /tmp/cmds3c/mnt/new.txt /tmp/cmds3c/mnt/dir
echo 'hello, world' > /tmp/cmds3c/mnt/hello.txt
unmount /tmp/cmds3c/mnt
ftpfs -q -p -/ -m /tmp/cmds3c/mnt -a me@ tcp!127.0.0.1!%[1]d
ls /tmp/cmds3c/mnt /tmp/cmds3c/mnt/home/.flush.ftpfs
cat /tmp/cmds3c/mnt/home/hello.txt
unmount /tmp/cmds3c/mnt
ftpfs -m /tmp/cmds3c/nomnt -q -a x tcp!127.0.0.1!%[1]d; echo ftpfs $status
ftpfs -a x tcp!127.0.0.1!1; echo ftpfs $status
ftpfs; echo ftpfs $status
`, port)
	if err := os.WriteFile(cmds3cDir+"/ftp.sh", []byte(script), 0o666); err != nil {
		t.Fatal(err)
	}
	out, st := emu(t, "", "sh", "/tmp/cmds3c/ftp.sh")
	want := `220-Welcome to the stub
220 ready
331 password please
ftpfs 
--rw-r--r-- M 1 ftp ftp 13 Mar 04  2021 /tmp/cmds3c/mnt/hello.txt
d-rwxr-xr-x M 1 ftp ftp  0 Jan 02  2020 /tmp/cmds3c/mnt/sub
hello, world
in sub
new file
/tmp/cmds3c/mnt/dir
/tmp/cmds3c/mnt/new.txt
/tmp/cmds3c/mnt/sub
--rw-r--r-- M 1 ftp ftp 9 Mar 04  2021 /tmp/cmds3c/mnt/new.txt
cat: cannot open /tmp/cmds3c/mnt/nothere: file does not exist
chmod: cannot wstat /tmp/cmds3c/mnt/new.txt: wstat not implemented
/tmp/cmds3c/mnt/home
/tmp/cmds3c/mnt/pub
/tmp/cmds3c/mnt/home/.flush.ftpfs/hello.txt
/tmp/cmds3c/mnt/home/.flush.ftpfs/sub
hello, world
ftpfs: mount /tmp/cmds3c/nomnt failed: file does not exist
ftpfs mount
ftpfs: dial tcp!127.0.0.1!1 failed: connection refused
ftpfs dial tcp!127.0.0.1!1 failed: connection refused
usage: ftpfs [-/dpq] [-m mountpoint] [-a password] ftphost
ftpfs usage
`
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := strings.Join(s.log, "\n")
	for _, w := range []string{"USER anonymous\nPASS me@\nTYPE I\nPWD\n", "STOR new.txt", "MKD dir", "DELE hello.txt",
		"RMD dir", "CWD /\nTYPE I\nPWD\nPORT 127,0,0,1,"} {
		if !strings.Contains(log, w) {
			t.Errorf("server log lacks %q:\n%s", w, log)
		}
	}
}

// cmds3cFTP is a small FTP server for ftpfs: one user, files in memory.
type cmds3cFTP struct {
	mu    sync.Mutex
	files map[string][]byte // path -> contents; dirs end in /
	log   []string
}

func cmds3cNewFTP() *cmds3cFTP {
	return &cmds3cFTP{files: map[string][]byte{
		"/home/":          nil,
		"/home/hello.txt": []byte("hello, world\n"),
		"/home/sub/":      nil,
		"/home/sub/a.txt": []byte("in sub\n"),
		"/pub/":           nil,
	}}
}

func (s *cmds3cFTP) serve(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go s.session(c)
	}
}

func (s *cmds3cFTP) session(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	cwd := "/home"
	var pasv net.Listener
	var port string
	say := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	abs := func(p string) string {
		if !strings.HasPrefix(p, "/") {
			p = strings.TrimSuffix(cwd, "/") + "/" + p
		}
		return p
	}
	dataconn := func() net.Conn {
		if pasv != nil {
			d, err := pasv.Accept()
			pasv.Close()
			pasv = nil
			if err != nil {
				return nil
			}
			return d
		}
		if port != "" {
			d, err := net.Dial("tcp", port)
			port = ""
			if err == nil {
				return d
			}
		}
		return nil
	}
	say("220-Welcome to the stub")
	say("220 ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		s.mu.Lock()
		s.log = append(s.log, line)
		s.mu.Unlock()
		switch strings.ToUpper(cmd) {
		case "USER":
			say("331 password please")
		case "PASS":
			say("230 logged in")
		case "TYPE", "NOOP":
			say("200 ok")
		case "PWD":
			say("257 \"%s\" is cwd", cwd)
		case "CWD":
			p := abs(arg)
			if p != "/" {
				p = strings.TrimSuffix(p, "/")
			}
			s.mu.Lock()
			_, ok := s.files[strings.TrimSuffix(p, "/")+"/"]
			s.mu.Unlock()
			if ok || p == "/" {
				cwd = p
				say("250 ok")
			} else {
				say("550 no such directory")
			}
		case "PASV":
			pasv, _ = net.Listen("tcp", "127.0.0.1:0")
			pn := pasv.Addr().(*net.TCPAddr).Port
			say("227 Entering Passive Mode (127,0,0,1,%d,%d)", pn/256, pn%256)
		case "PORT":
			f := strings.Split(arg, ",")
			if len(f) == 6 {
				var a, b int
				fmt.Sscan(f[4], &a)
				fmt.Sscan(f[5], &b)
				port = fmt.Sprintf("%s.%s.%s.%s:%d", f[0], f[1], f[2], f[3], a*256+b)
				say("200 port ok")
			} else {
				say("501 bad port")
			}
		case "LIST":
			say("150 listing")
			d := dataconn()
			if d == nil {
				say("425 no data connection")
				continue
			}
			dir := strings.TrimSuffix(cwd, "/") + "/"
			s.mu.Lock()
			var names []string
			for p := range s.files {
				if strings.HasPrefix(p, dir) && p != dir {
					rest := p[len(dir):]
					if i := strings.IndexByte(rest, '/'); i < 0 || i == len(rest)-1 {
						names = append(names, rest)
					}
				}
			}
			sort.Strings(names)
			fmt.Fprintf(d, "total %d\r\n", len(names))
			fmt.Fprintf(d, "drwxr-xr-x 2 ftp ftp 0 Jan 02 2020 .\r\n")
			for _, n := range names {
				if strings.HasSuffix(n, "/") {
					fmt.Fprintf(d, "drwxr-xr-x 2 ftp ftp 0 Jan 02 2020 %s\r\n", strings.TrimSuffix(n, "/"))
				} else {
					fmt.Fprintf(d, "-rw-r--r-- 1 ftp ftp %d Mar 04 2021 %s\r\n", len(s.files[dir+n]), n)
				}
			}
			s.mu.Unlock()
			d.Close()
			say("226 done")
		case "RETR":
			s.mu.Lock()
			b, ok := s.files[abs(arg)]
			s.mu.Unlock()
			if !ok || strings.HasSuffix(abs(arg), "/") {
				if pasv != nil {
					pasv.Close()
					pasv = nil
				}
				say("550 no such file")
				continue
			}
			say("150 sending")
			d := dataconn()
			if d == nil {
				say("425 no data connection")
				continue
			}
			d.Write(b)
			d.Close()
			say("226 done")
		case "STOR":
			say("150 receiving")
			d := dataconn()
			if d == nil {
				say("425 no data connection")
				continue
			}
			b, _ := io.ReadAll(d)
			d.Close()
			s.mu.Lock()
			s.files[abs(arg)] = b
			s.mu.Unlock()
			say("226 stored")
		case "DELE", "RMD":
			p := abs(arg)
			if cmd == "RMD" {
				p += "/"
			}
			s.mu.Lock()
			_, ok := s.files[p]
			delete(s.files, p)
			s.mu.Unlock()
			if ok {
				say("250 deleted")
			} else {
				say("550 no such file")
			}
		case "MKD":
			s.mu.Lock()
			s.files[abs(arg)+"/"] = nil
			s.mu.Unlock()
			say("257 created")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}
