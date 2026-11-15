package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const ndbipDir = root + "/tmp/ndbip"

func ndbipSetup(t *testing.T, files map[string]string) {
	t.Helper()
	os.RemoveAll(ndbipDir)
	if err := os.MkdirAll(ndbipDir, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		os.MkdirAll(filepath.Dir(ndbipDir+"/"+name), 0o777)
		if err := os.WriteFile(ndbipDir+"/"+name, []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(ndbipDir) })
}

func ndbipCheck(t *testing.T, script, want string) {
	t.Helper()
	out, st := emu(t, "", "sh", "-c", script)
	if out != want || st != "" {
		t.Errorf("got:\n%s\nstatus %q\nwant:\n%s", out, st, want)
	}
}

// dnsName appends name to b in DNS form.
func dnsName(b []byte, name string) []byte {
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

type testRR struct {
	name  string
	rtype uint16
	data  []byte
}

func rrA(name string, a ...byte) testRR { return testRR{name, 1, a} }
func rrName(name string, t uint16, target string) testRR {
	return testRR{name, t, dnsName(nil, target)}
}

// ndbipDNS serves a few names under example.test on a loopback UDP
// port, as an authoritative server, and returns the port.
func ndbipDNS(t *testing.T) int {
	t.Helper()
	zone := map[string][]testRR{
		"www.example.test":   {rrA("www.example.test", 10, 1, 2, 3)},
		"mail.example.test":  {rrA("mail.example.test", 10, 1, 2, 4)},
		"alias.example.test": {rrName("alias.example.test", 5, "www.example.test"), rrA("www.example.test", 10, 1, 2, 3)},
		"example.test": {
			{"example.test", 15, append([]byte{0, 10}, dnsName(nil, "mail.example.test")...)},
			{"example.test", 13, []byte("\x05arm64\x07inferno")},
		},
		"3.2.1.10.in-addr.arpa": {rrName("3.2.1.10.in-addr.arpa", 12, "www.example.test")},
	}
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if len(q) < 12 {
				continue
			}
			// The question: name, type, class.
			o := 12
			var labels []string
			for o < len(q) && q[o] != 0 {
				l := int(q[o])
				labels = append(labels, strings.ToLower(string(q[o+1:o+1+l])))
				o += 1 + l
			}
			o++
			qtype := binary.BigEndian.Uint16(q[o:])
			question := q[12 : o+4]
			name := strings.Join(labels, ".")
			var an []testRR
			for _, r := range zone[name] {
				if r.rtype == qtype || qtype == 255 || r.rtype == 5 {
					an = append(an, r)
				}
			}
			flags := uint16(0x8400) // response, authoritative
			var ns []testRR
			if _, ok := zone[name]; !ok {
				flags |= 3 // name does not exist
				soa := dnsName(nil, "ns.example.test")
				soa = dnsName(soa, "admin.example.test")
				soa = binary.BigEndian.AppendUint32(soa, 1)
				for range 3 {
					soa = binary.BigEndian.AppendUint32(soa, 3600)
				}
				soa = binary.BigEndian.AppendUint32(soa, 60)
				ns = []testRR{{"example.test", 6, soa}}
			}
			r := append([]byte{}, q[0:2]...)
			r = binary.BigEndian.AppendUint16(r, flags)
			r = binary.BigEndian.AppendUint16(r, 1)
			r = binary.BigEndian.AppendUint16(r, uint16(len(an)))
			r = binary.BigEndian.AppendUint16(r, uint16(len(ns)))
			r = binary.BigEndian.AppendUint16(r, 0)
			r = append(r, question...)
			for _, x := range append(an, ns...) {
				r = dnsName(r, x.name)
				r = binary.BigEndian.AppendUint16(r, x.rtype)
				r = binary.BigEndian.AppendUint16(r, 1)
				r = binary.BigEndian.AppendUint32(r, 300)
				r = binary.BigEndian.AppendUint16(r, uint16(len(x.data)))
				r = append(r, x.data...)
			}
			c.WriteTo(r, from)
		}
	}()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// TestNdbDNS runs ndb/dns against a loopback DNS server, ndb/dnsquery
// and ndb/csquery through it, and ndb/cs with it.
func TestNdbDNS(t *testing.T) {
	port := ndbipDNS(t)
	ndbipSetup(t, map[string]string{
		"db": fmt.Sprintf("infernosite=\n\tdns=127.0.0.1!%d\nsys=here dom=here.example.test ip=10.9.9.9\n", port),
	})
	script := `ndb/dns -h -f /tmp/ndbip/db
ndb/dnsquery www.example.test alias.example.test 'example.test mx' 10.1.2.3 'example.test hinfo'
ndb/dnsquery nosuch.example.test 'www.example.test bogus' '10.1.2.3 ip'
echo 'mail.example.test
example.test all' | ndb/dnsquery
ndb/cs
ndb/csquery tcp!www.example.test!styx 'net!mail.example.test!9fs' tcp!nosuch.example.test!styx
ndb/dnsquery -s /nonexistent x; echo $status
ndb/csquery -x
echo $status
`
	want := "www.example.test ip\t10.1.2.3\n" +
		"www.example.test ip\t10.1.2.3\n" +
		"example.test mx\t10 mail.example.test\n" +
		"3.2.1.10.in-addr.arpa ptr\twww.example.test\n" +
		"example.test hinfo\tarm64 inferno\n" +
		"!dns: name does not exist\n" +
		"!dns: unknown type\n" +
		"!dns: invalid host name\n" +
		"> mail.example.test ip\t10.1.2.4\n" +
		"> example.test mx\t10 mail.example.test\n" +
		"example.test hinfo\tarm64 inferno\n" +
		"example.test soa\tns.example.test admin.example.test 1 3600 3600 3600 60\n" +
		"> /net/tcp/clone 10.1.2.3!6666\n" +
		"/net/tcp/clone 10.1.2.4!564\n" +
		"tcp!nosuch.example.test!styx: dns: name does not exist\n" +
		"dnsquery: can't open /nonexistent: file does not exist\nopen\n" +
		"usage: csquery [-x /net] [-s server] [address ...]\nusage\n"
	ndbipCheck(t, script, want)
}

// TestNdbRegistry mounts ndb/registry and registers, finds and removes
// services, with ndb/regquery.
func TestNdbRegistry(t *testing.T) {
	ndbipSetup(t, map[string]string{
		"reg.db": "service=tcp!fs!styx proto=styx auth=none\nservice=bad/name x=y\n",
	})
	script := `r=/tmp/ndbip/reg
mkdir -p $r
mount -A {ndb/registry -f /tmp/ndbip/reg.db} $r
ls $r
cat $r/index
{echo 'tcp!a!1 proto styx owner ''me too'''; sleep 2} > $r/new &
sleep 1
echo 'tcp!b!2 proto http persist 1' > $r/new
ndb/regquery -m $r proto styx
ndb/regquery -m $r -n proto '*'
echo 'colour red' > $r/tcp!b!2
cat $r/tcp!b!2
echo 'tcp!a!1 x y' > $r/new
sleep 2
echo after close
cat $r/index
echo 'proto http' | ndb/regquery -m $r -n
rm $r/tcp!b!2
{sleep 1; echo 'tcp!c!3 persist 1' > $r/new} &
read < $r/event
ls $r
echo 'a b c' > $r/tcp!c!3
ndb/regquery
ndb/registry -f /nonexistent
echo $status
`
	want := `/tmp/ndbip/reg/event
/tmp/ndbip/reg/find
/tmp/ndbip/reg/index
/tmp/ndbip/reg/new
/tmp/ndbip/reg/tcp!fs!styx
tcp!fs!styx auth none proto styx
tcp!fs!styx auth none proto styx
tcp!a!1 owner 'me too' proto styx
tcp!fs!styx
tcp!a!1
tcp!b!2
tcp!b!2 colour red persist 1 proto http
echo: write error: duplicate service name
after close
tcp!fs!styx auth none proto styx
tcp!b!2 colour red persist 1 proto http
> tcp!b!2
> 00000006
/tmp/ndbip/reg/event
/tmp/ndbip/reg/find
/tmp/ndbip/reg/index
/tmp/ndbip/reg/new
/tmp/ndbip/reg/tcp!c!3
/tmp/ndbip/reg/tcp!fs!styx
echo: write error: odd attribute/value pairs
> registry: can't open /nonexistent: file does not exist
error
`
	ndbipCheck(t, script, want)
}

// TestIPIcmp pings 127.0.0.1 through /net/icmp, when the host lets
// users open ICMP sockets (on Linux, net.ipv4.ping_group_range), and
// writes and reads /net/iproute.
func TestIPIcmp(t *testing.T) {
	script := `ls /net
echo announce 1 > /net/icmp/clone
ip/ping
{
	echo tag rip
	echo add 10.1.0.0 255.255.0.0 10.0.0.1
	echo add 192.168.0.0 /120 10.0.0.2 stat
} > /net/iproute
echo add 10.2.0.0 255.255.0.0 10.0.0.3 > /net/iproute
echo add 0.0.0.0 0.0.0.0 10.0.0.254 > /net/iproute
cat /net/iproute
echo remove 10.2.0.0 255.255.0.0 > /net/iproute
echo flush rip > /net/iproute
cat /net/iproute
echo flush > /net/iproute
cat /net/iproute
`
	want := `/net/arp
/net/icmp
/net/iproute
/net/ndb
/net/tcp
/net/udp
echo: write error: protocol not supported
usage: ip/ping [-alq] [-s msgsize] [-i millisecs] [-n #pings] destination
0.0.0.0         /96  10.0.0.254      4    none   -
10.1.0.0        /112 10.0.0.1        4     rip   -
10.2.0.0        /112 10.0.0.3        4    none   -
192.168.0.0     /120 10.0.0.2        4    stat   -
0.0.0.0         /96  10.0.0.254      4    none   -
192.168.0.0     /120 10.0.0.2        4    stat   -
`
	if fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_ICMP); err != nil {
		t.Logf("no ICMP socket on this host (%v): ip/ping not run", err)
	} else {
		unix.Close(fd)
		script += `ip/ping -n 3 -i 10 127.0.0.1 | sed 's/[0-9]+ µs/N µs/g'
ip/ping -a -n 1 -i 10 127.0.0.1 | sed 's/[0-9]+ µs/N µs/g'
ip/ping -q -s 1000 -n 2 -i 10 127.0.0.1
echo $status
ip/ping -n 1 192.0.2.1x
echo $status
`
		want += `sending 3 64 byte messages 10 ms apart
0: rtt N µs, avg rtt N µs, ttl = 64
1: rtt N µs, avg rtt N µs, ttl = 64
2: rtt N µs, avg rtt N µs, ttl = 64
sending 1 64 byte messages 10 ms apart
0: 127.0.0.1->127.0.0.1 rtt N µs, avg rtt N µs, ttl = 64
sending 2 1000 byte messages 10 ms apart

ip/ping: can't dial icmp!192.0.2.1x!1: invalid IP address
dial
`
	}
	ndbipCheck(t, script, want)
}

// TestIPUdp runs ip/sntp against a loopback time server and asks
// ip/virgild a question.
func TestIPUdp(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 48 || buf[0]&7 != 3 {
				continue
			}
			r := make([]byte, 48)
			r[0] = 1<<3 | 4 // version 1, server
			r[1] = 1        // stratum 1
			copy(r[12:], "GPS")
			copy(r[24:32], buf[40:48])
			binary.BigEndian.PutUint32(r[40:], 1700000000+2208988800)
			c.WriteTo(r, from)
		}
	}()
	port := c.LocalAddr().(*net.UDPAddr).Port

	// The virgil client: it asks until virgild answers.
	ndbipSetup(t, map[string]string{"db": "database=\n\tfile=/tmp/ndbip/db\n\tfile=/lib/ndb/local\nsys=here ip=10.9.9.9\n"})
	vc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vc.Close() })
	answers := make(chan string, 10)
	stop := make(chan struct{})
	go func() {
		to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2202}
		buf := make([]byte, 512)
		for {
			select {
			case <-stop:
				return
			default:
			}
			vc.WriteTo([]byte("me?here"), to)
			vc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			if n, _, err := vc.ReadFrom(buf); err == nil {
				answers <- string(buf[:n])
				time.Sleep(time.Second)
			}
		}
	}()
	script := fmt.Sprintf(`ip/sntp -d -i udp!127.0.0.1!%d
echo $status
ip/sntp a b
ip/sntp -i udp!127.0.0.1!1
echo $status
ndb/cs -f /tmp/ndbip/db
ip/virgild &
sleep 2
`, port)
	want := `LI = 0, version = 1, mode = 4
stratum = 1 (GPS), poll = 0, prec = 0
rootdelay = 0, dispersion = 0
1700000000

usage: sntp [-d] [server]
read error
`
	ndbipCheck(t, script, want)
	close(stop)
	select {
	case a := <-answers:
		if a != "me?here=10.9.9.9" {
			t.Errorf("virgild answered %q", a)
		}
	default:
		t.Errorf("virgild did not answer")
	}
}

// freeUDPPort returns a UDP port on the loopback interface that was free
// a moment ago.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// ndbipRounds runs script, which starts a server, waits for the file
// /tmp/ndbip/done, stops the server and removes the file. client talks
// to the server once for each wazero mode; ndbipRounds checks that the
// emu's output and the client's findings are as wanted for each.
func ndbipRounds(t *testing.T, script, want string, client func() []string, wantClient []string) {
	t.Helper()
	done := ndbipDir + "/done"
	stop := make(chan struct{})
	var rounds [][]string
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			r := client()
			select {
			case <-stop:
				return
			default:
			}
			rounds = append(rounds, r)
			os.WriteFile(done, nil, 0o666)
			for {
				if _, err := os.Stat(done); err != nil {
					break
				}
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
	}()
	out, st := emu(t, "", "sh", "-c", "load std\n"+script)
	close(stop)
	<-finished
	// The shell reports the server it killed, with its pid.
	var lines []string
	for _, l := range strings.SplitAfter(out, "\n") {
		if !strings.HasSuffix(l, ":killed\n") {
			lines = append(lines, l)
		}
	}
	out = strings.Join(lines, "")
	if out != want {
		t.Errorf("got:\n%s\nstatus %q\nwant:\n%s", out, st, want)
	}
	if len(rounds) != len(testModes()) {
		t.Fatalf("%d client rounds for %d modes: %q", len(rounds), len(testModes()), rounds)
	}
	for _, r := range rounds {
		if strings.Join(r, "\n") != strings.Join(wantClient, "\n") {
			t.Errorf("client got:\n%s\nwant:\n%s", strings.Join(r, "\n"), strings.Join(wantClient, "\n"))
		}
	}
}

// udpAsk sends req to port on the loopback interface until an answer
// comes or stop is closed, and returns the conversation, the answer and
// where it came from.
func udpAsk(port int, req []byte, stop <-chan struct{}) (net.PacketConn, net.Addr, []byte) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil
	}
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	buf := make([]byte, 2048)
	giveup := time.After(5 * time.Second)
	for {
		select {
		case <-stop:
			c.Close()
			return nil, nil, nil
		case <-giveup:
			return c, nil, nil
		default:
		}
		c.WriteTo(req, to)
		c.SetReadDeadline(time.Now().Add(time.Second))
		if n, from, err := c.ReadFrom(buf); err == nil {
			c.SetReadDeadline(time.Time{})
			return c, from, buf[:n]
		}
	}
}

// TestIPTftpd reads and writes files with ip/tftpd.
func TestIPTftpd(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 64) // 1024 bytes: 3 blocks, the last empty
	ndbipSetup(t, map[string]string{"tftp/small": "hello tftp\n", "tftp/big": big})
	port := freeUDPPort(t)
	never := make(chan struct{})
	client := func() []string {
		var res []string
		for _, name := range []string{"big", "small", "nosuch", "../db", "/etc/passwd"} {
			c, from, b := udpAsk(port, append([]byte{0, 1}, name+"\x00octet\x00"...), never)
			var data []byte
			block := 1
			for {
				if len(b) < 4 {
					res = append(res, fmt.Sprintf("%s: short reply", name))
					break
				}
				if op := binary.BigEndian.Uint16(b); op == 5 {
					res = append(res, fmt.Sprintf("%s: error %d %q", name, b[3], strings.TrimRight(string(b[4:]), "\x00")))
					break
				}
				if int(binary.BigEndian.Uint16(b[2:])) == block {
					data = append(data, b[4:]...)
					block++
				}
				c.WriteTo([]byte{0, 4, b[2], b[3]}, from)
				if len(b) < 4+512 {
					res = append(res, fmt.Sprintf("%s: %d blocks, %q...", name, block-1, data[:min(len(data), 16)]))
					break
				}
				buf := make([]byte, 600)
				c.SetReadDeadline(time.Now().Add(5 * time.Second))
				n, _, err := c.ReadFrom(buf)
				if err != nil {
					res = append(res, name+": "+err.Error())
					break
				}
				b = buf[:n]
			}
			c.Close()
		}
		// Write a file of 700 bytes.
		up := strings.Repeat("x", 700)
		c, from, b := udpAsk(port, []byte("\x00\x02up\x00octet\x00"), never)
		res = append(res, fmt.Sprintf("write: % x", b))
		for i, blk := range []string{up[:512], up[512:]} {
			c.WriteTo(append([]byte{0, 3, 0, byte(i + 1)}, blk...), from)
			buf := make([]byte, 100)
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				res = append(res, fmt.Sprintf("write: %v", err))
				break
			}
			res = append(res, fmt.Sprintf("write: % x", buf[:n]))
		}
		c.Close()
		return res
	}
	script := fmt.Sprintf(`ip/tftpd -r -p %d -h /tmp/ndbip/tftp &
p=$apid
while {! test -e /tmp/ndbip/done} {sleep 1}
kill $p
rm /tmp/ndbip/done
sleep 1
wc -c /tmp/ndbip/tftp/up
rm /tmp/ndbip/tftp/up
ip/tftpd -h /nonexistent
ip/tftpd -x
`, port)
	want := `nak: file does not exist
nak: Permission denied
nak: Permission denied
    700 /tmp/ndbip/tftp/up
tftpd: can't chdir to /nonexistent: file does not exist
usage: tftpd [-dr] [-p port] [-h homedir] [-x network-dir]
`
	wantClient := []string{
		`big: 3 blocks, "0123456789abcdef"...`,
		`small: 1 blocks, "hello tftp\n"...`,
		`nosuch: error 0 "file does not exist"`,
		`../db: error 4 "Permission denied"`,
		`/etc/passwd: error 4 "Permission denied"`,
		"write: 00 04 00 00",
		"write: 00 04 00 01",
		"write: 00 04 00 02",
	}
	ndbipRounds(t, script, want, client, wantClient)
}

// TestIPBootpd answers BOOTP requests with ip/bootpd.
func TestIPBootpd(t *testing.T) {
	ndbipSetup(t, map[string]string{
		"db": "database=\n\tfile=/tmp/ndbip/db\n\tfile=/lib/ndb/local\n" +
			"sys=here ip=127.0.0.1\n" +
			"ipnet=lab ip=10.1.0.0 ipmask=255.255.0.0 ipgw=10.1.0.1 fs=10.1.0.2 auth=here bootf=/boot/kernel\n" +
			"sys=client ether=0102030405a6 ip=10.1.2.3\n",
	})
	port := freeUDPPort(t)
	never := make(chan struct{})
	client := func() []string {
		req := make([]byte, 300)
		req[0], req[1], req[2] = 1, 1, 6
		binary.BigEndian.PutUint32(req[4:], 0x12345678)
		copy(req[28:], []byte{1, 2, 3, 4, 5, 0xa6})
		c, _, r := udpAsk(port, req, never)
		if c != nil {
			c.Close()
		}
		if len(r) < 300 {
			return []string{fmt.Sprintf("short reply % x", r)}
		}
		return []string{fmt.Sprintf("op %d xid %x yiaddr %v siaddr %v sname %q file %q vend %q", r[0], r[4:8], net.IP(r[16:20]), net.IP(r[20:24]),
			strings.TrimRight(string(r[44:108]), "\x00"), strings.TrimRight(string(r[108:236]), "\x00"), strings.TrimRight(string(r[236:]), "\x00"))}
	}
	script := fmt.Sprintf(`ndb/cs -f /tmp/ndbip/db
echo -n here > /dev/sysname
ip/bootpd -a udp!127.0.0.1!%d -f /tmp/ndbip/db &
p=$apid
while {! test -e /tmp/ndbip/done} {sleep 1}
kill $p
rm /tmp/ndbip/done
ip/bootpd -f /nonexistent
`, port)
	want := "bootpd: 0102030405a6 -> client 10.1.2.3\nbootpd: cannot open /nonexistent: file does not exist\n"
	ndbipRounds(t, script, want, client, []string{`op 2 xid 12345678 yiaddr 10.1.2.3 siaddr 127.0.0.1 sname "here" file "/boot/kernel" vend "p9  255.255.0.0 10.1.0.2 127.0.0.1 10.1.0.1"`})
}
