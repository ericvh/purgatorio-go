// Command keyfs serves the signer's password database (Inferno
// keyfs(4)) on /mnt/keys: a directory per user holding secret, log,
// status and expire. The database is kept in /keydb/keys, encrypted
// with AES under a key derived from a password asked for on the
// console, or read from the file -n names; its format is Inferno's, so
// a database moves between the two systems.
//
// The file server runs as a second prog, started with -S, since a guest
// cannot serve the mount it is itself waiting on.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	qroot = iota
	quser
	qsecret
	qlog
	qstatus
	qexpire
)

var files = []struct {
	t    int
	name string
}{{qsecret, "secret"}, {qlog, "log"}, {qstatus, "status"}, {qexpire, "expire"}}

const (
	maxsecret = 255
	maxname   = 255
	maxfail   = 50
	never     = 0
	sok       = 0
	sdisabled = 1

	checkpat = "XXXXXXXXXXXXXXXX" // as Plan 9's aescbc
	hdrlen   = 1 + 1 + 4

	eremoved  = "user has been removed"
	enotfound = "file does not exist"
	eperm     = "permission denied"
)

var status = []string{sok: "ok", sdisabled: "disabled"}

type user struct {
	x      int
	name   string
	secret []byte
	expire int
	status int
	failed int
	path   uint64
}

var (
	users   []*user
	pathgen uint64
	owner   string
	now     int
	thekey  []byte
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "-S" {
		server(os.Args[2])
		return
	}
	a := arg.New(os.Args)
	a.SetUsage("keyfs [-m mntpt] [-D] [-n nvramfile] [keyfile]")
	mountpt := "/mnt/keys"
	keyfile := "/keydb/keys"
	nvram := ""
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mountpt = a.EArg()
		case 'D':
		case 'n':
			nvram = a.EArg()
		default:
			a.Usage()
		}
	}
	if args := a.Argv(); len(args) > 0 {
		keyfile = args[0]
	}

	pwd := ""
	if nvram != "" {
		b, err := os.ReadFile(nvram)
		if err != nil {
			fatal(fmt.Sprintf("can't read %s: %s", nvram, sys.Errstr()))
		}
		pwd = string(b)
	}
	if pwd == "" {
		var err string
		pwd, err = readconsline("Key: ", true)
		if pwd == "" || err == "exit" {
			sys.Exits("")
		}
		if err != "" {
			fatal("couldn't get key: " + err)
		}
		if d, serr := sys.Stat(keyfile); serr != nil || d.Length == 0 {
			pwd0 := pwd
			pwd, err = readconsline("Confirm key: ", true)
			if pwd == "" || err == "exit" {
				sys.Exits("")
			}
			if pwd != pwd0 {
				fatal("key mismatch")
			}
		}
	}
	thekey = hashkey(pwd)
	readkeys(keyfile) // diagnose a wrong key here

	p, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe: " + sys.Errstr())
	}
	k, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe: " + sys.Errstr())
	}
	if _, err := sys.Spawn("/bin/keyfs.wasm", []string{"keyfs", "-S", keyfile}, sys.NEWFD|sys.NEWPGRP,
		[][2]int{{0, p[0]}, {1, k[0]}, {2, 2}}); err != nil {
		fatal("can't start server: " + sys.Errstr())
	}
	kf := os.NewFile(uintptr(k[1]), "key")
	kf.Write(thekey)
	kf.Close()
	os.NewFile(uintptr(k[0]), "key").Close()
	os.NewFile(uintptr(p[0]), "styx").Close()
	if _, err := sys.Mount(p[1], -1, mountpt, sys.MREPL|sys.MCREATE, ""); err != nil {
		fatal(fmt.Sprintf("mount on %s failed: %s", mountpt, sys.Errstr()))
	}
}

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "keyfs: %s\n", s)
	sys.Exits("fail:error")
}

func hashkey(s string) []byte {
	sk := make([]byte, keyring.SHA1dlen)
	keyring.SHA1([]byte(s), sk, keyring.SHA1([]byte("aescbc file"), nil, nil))
	return sk[:keyring.AESbsize]
}

func readconsline(prompt string, raw bool) (string, string) {
	cons, err := os.OpenFile("/dev/cons", os.O_RDWR, 0)
	if err != nil {
		return "", "can't open cons: " + sys.Errstr()
	}
	defer cons.Close()
	cons.Write([]byte(prompt))
	var ctl *os.File
	if raw {
		ctl, err = os.OpenFile("/dev/consctl", os.O_WRONLY, 0)
		if err != nil || func() error { _, e := ctl.Write([]byte("rawon")); return e }() != nil {
			return "", "can't open consctl: " + sys.Errstr()
		}
		defer func() { ctl.Write([]byte("rawoff")); ctl.Close() }()
	}
	var line []byte
	b := make([]byte, 1)
	e := ""
read:
	for {
		n, _ := cons.Read(b)
		if n <= 0 {
			break
		}
		switch c := b[0]; c {
		case 0x7f:
			e = "interrupt"
			break read
		case '\b':
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		case '\n', '\r', 4:
			break read
		default:
			if len(line) > 256 {
				e = "line too long"
				break read
			}
			line = append(line, c)
		}
	}
	cons.Write([]byte("\n"))
	if e != "" {
		return "", e
	}
	return string(line), ""
}

func corrupt(keyfile string) {
	fatal(keyfile + ": incorrect key or corrupt/damaged keyfile")
}

func readkeys(keyfile string) {
	users = nil
	buf, err := os.ReadFile(keyfile)
	if err != nil {
		fatal(fmt.Sprintf("can't open %s: %s", keyfile, sys.Errstr()))
	}
	n := len(buf)
	if n == 0 {
		return
	}
	if n < keyring.AESbsize+len(checkpat) {
		corrupt(keyfile)
	}
	state := keyring.AESsetup(thekey, buf[:keyring.AESbsize])
	keyring.AEScbc(state, buf[keyring.AESbsize:], keyring.Decrypt)
	if string(buf[n-len(checkpat):]) != checkpat {
		corrupt(keyfile)
	}
	n -= len(checkpat)
	for i := keyring.AESbsize; i < n; {
		u, k := unpack(buf[i:n])
		if u == nil {
			corrupt(keyfile)
		}
		newuser(u.name, u)
		i += k
	}
}

func writekeys(keyfile string) {
	var body []byte
	for _, u := range users {
		if u != nil {
			body = append(body, pack(u)...)
		}
	}
	if len(body) == 0 {
		// Leave it empty for clarity.
		if err := os.WriteFile(keyfile, nil, 0o600); err != nil {
			fatal(fmt.Sprintf("can't create %s: %s", keyfile, sys.Errstr()))
		}
		return
	}
	buf := make([]byte, keyring.AESbsize, keyring.AESbsize+len(body)+len(checkpat))
	rand.Read(buf)
	buf = append(buf, body...)
	buf = append(buf, checkpat...)
	state := keyring.AESsetup(thekey, buf[:keyring.AESbsize])
	keyring.AEScbc(state, buf[keyring.AESbsize:], keyring.Encrypt)
	fd, err := sys.Create(keyfile, sys.OWRITE, 0o600)
	if err != nil {
		fatal(fmt.Sprintf("can't create %s: %s", keyfile, sys.Errstr()))
	}
	f := os.NewFile(uintptr(fd), keyfile)
	defer f.Close()
	if n, err := f.Write(buf); err != nil || n != len(buf) {
		fatal(fmt.Sprintf("error writing to %s: %s", keyfile, sys.Errstr()))
	}
}

func pack(u *user) []byte {
	a := []byte{byte(u.status), byte(u.failed)}
	a = binary.LittleEndian.AppendUint32(a, uint32(u.expire))
	a = append(a, byte(len(u.name)))
	a = append(a, u.name...)
	a = append(a, byte(len(u.secret)))
	return append(a, u.secret...)
}

func unpack(a []byte) (*user, int) {
	if len(a) < hdrlen+2 {
		return nil, 0
	}
	u := &user{status: int(a[0]), failed: int(a[1]), expire: int(int32(binary.LittleEndian.Uint32(a[2:])))}
	n := int(a[6])
	j := 7 + n
	if j >= len(a) {
		return nil, 0
	}
	u.name = string(a[7:j])
	n = int(a[j])
	j++
	if j+n > len(a) {
		return nil, 0
	}
	if n > 0 {
		u.secret = append([]byte(nil), a[j:j+n]...)
	}
	return u, j + n
}

func newuser(name string, u *user) *user {
	i := 0
	for i < len(users) && users[i] != nil {
		i++
	}
	if i == len(users) {
		users = append(users, nil)
	}
	path := pathgen<<16 | uint64(i)<<4
	pathgen++
	if u == nil {
		u = &user{name: name, expire: never, status: sok}
	}
	u.x, u.path = i, path
	users[i] = u
	return u
}

func ftype(path uint64) int { return int(path & 0xf) }
func index(path uint64) int { return int(path&0xffff) >> 4 }

func finduserpath(path uint64) *user {
	i := index(path)
	if i >= len(users) || users[i] == nil || users[i].path != path&^0xf {
		return nil
	}
	return users[i]
}

func findusername(name string) *user {
	for _, u := range users {
		if u != nil && u.name == name {
			return u
		}
	}
	return nil
}

func readtime() int {
	b, err := os.ReadFile("/dev/time")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return int(n / 1000000)
}

func dir(q styx.Qid, name string, length uint64, perm uint32) styx.Dir {
	if q.Type&styx.QTDIR != 0 {
		perm |= styx.DMDIR
	}
	return styx.Dir{Qid: q, Mode: perm, Name: name, UID: owner, GID: owner, MUID: owner,
		Length: length, Atime: uint32(now), Mtime: uint32(now)}
}

// dirgen describes the file at path.
func dirgen(p uint64) (styx.Dir, string) {
	switch t := ftype(p); t {
	case qroot:
		return dir(styx.Qid{Type: styx.QTDIR, Path: qroot}, "/", 0, 0o755), ""
	case quser:
		u := finduserpath(p)
		if u == nil {
			return styx.Dir{}, enotfound
		}
		return dir(styx.Qid{Type: styx.QTDIR, Path: p}, u.name, 0, 0o500), ""
	default:
		u := finduserpath(p)
		if u == nil {
			return styx.Dir{}, eremoved
		}
		var l uint64
		if t == qsecret {
			l = uint64(len(u.secret))
		}
		for _, f := range files {
			if f.t == t {
				return dir(styx.Qid{Path: p}, f.name, l, 0o600), ""
			}
		}
	}
	return styx.Dir{}, enotfound
}

type fid struct {
	path uint64
	qt   uint8
	open bool
	mode uint8
	dirs []byte // packed directory being read
}

// server serves the database on fd 0, with the key from fd 1.
func server(keyfile string) {
	conn := os.NewFile(0, "styx")
	kf := os.NewFile(1, "key")
	thekey = make([]byte, keyring.AESbsize)
	if n, _ := kf.Read(thekey); n != len(thekey) {
		fatal("no key")
	}
	kf.Close()
	readkeys(keyfile)
	b, _ := os.ReadFile("/dev/user")
	owner = string(b)
	if owner == "" {
		owner = "keyfs"
	}
	s := &srv{io: conn, keyfile: keyfile, fids: map[uint32]*fid{}, msize: 8192}
	s.serve()
}

type srv struct {
	io      *os.File
	keyfile string
	fids    map[uint32]*fid
	msize   uint32
	buf     []byte
}

func (s *srv) read() (*styx.Fcall, error) {
	tmp := make([]byte, 65536)
	for {
		if n := styx.Size(s.buf); n != 0 && int(n) <= len(s.buf) {
			m := s.buf[:n]
			s.buf = append([]byte(nil), s.buf[n:]...)
			return styx.Unmarshal(m, 0)
		}
		k, err := s.io.Read(tmp)
		if k <= 0 {
			if err == nil {
				err = sys.Error("hungup")
			}
			return nil, err
		}
		s.buf = append(s.buf, tmp[:k]...)
	}
}

func (s *srv) reply(f *styx.Fcall) {
	b, err := styx.Marshal(nil, f)
	if err == nil {
		s.io.Write(b)
	}
}

func (s *srv) serve() {
	for {
		t, err := s.read()
		if err != nil {
			return
		}
		now = readtime()
		r, e := s.handle(t)
		if e != "" {
			r = &styx.Fcall{Type: styx.Rerror, Ename: e}
		}
		r.Tag = t.Tag
		s.reply(r)
	}
}

func (s *srv) qid(path uint64) styx.Qid {
	switch ftype(path) {
	case qroot, quser:
		return styx.Qid{Type: styx.QTDIR, Path: path}
	}
	return styx.Qid{Path: path}
}

func (s *srv) handle(t *styx.Fcall) (*styx.Fcall, string) {
	r := &styx.Fcall{Type: t.Type + 1}
	f := s.fids[t.Fid]
	switch t.Type {
	case styx.Tversion:
		s.msize = min(t.Msize, 65536)
		s.fids = map[uint32]*fid{}
		r.Msize, r.Version = s.msize, styx.Version
		if !strings.HasPrefix(t.Version, "9P2000") {
			r.Version = "unknown"
		}
		return r, ""
	case styx.Tauth:
		return nil, "authentication not required"
	case styx.Tflush:
		return r, ""
	case styx.Tattach:
		if s.fids[t.Fid] != nil {
			return nil, "fid in use"
		}
		s.fids[t.Fid] = &fid{path: qroot, qt: styx.QTDIR}
		r.Qid = s.qid(qroot)
		return r, ""
	}
	if f == nil {
		return nil, "unknown fid"
	}
	switch t.Type {
	case styx.Twalk:
		if f.open {
			return nil, "fid is open"
		}
		if t.Newfid != t.Fid && s.fids[t.Newfid] != nil {
			return nil, "fid in use"
		}
		p := f.path
		for i, name := range t.Wname {
			np, e := walk1(p, name)
			if e != "" {
				if i == 0 {
					return nil, e
				}
				return r, ""
			}
			p = np
			r.Wqid = append(r.Wqid, s.qid(p))
		}
		s.fids[t.Newfid] = &fid{path: p, qt: s.qid(p).Type}
		return r, ""
	case styx.Topen:
		if f.open {
			return nil, "fid is open"
		}
		d, e := dirgen(f.path)
		if e != "" {
			return nil, e
		}
		if e := access(d, t.Mode); e != "" {
			return nil, e
		}
		f.open, f.mode = true, t.Mode
		r.Qid, r.Iounit = d.Qid, s.msize-24
		return r, ""
	case styx.Tcreate:
		if ftype(f.path) != qroot {
			return nil, eperm
		}
		if t.Perm&styx.DMDIR == 0 {
			return nil, eperm
		}
		if findusername(t.Name) != nil {
			return nil, "user already exists"
		}
		if len(t.Name) > maxname {
			return nil, "user name too long"
		}
		u := newuser(t.Name, nil)
		f.path, f.qt, f.open, f.mode = u.path|quser, styx.QTDIR, true, t.Mode
		s.writekeys()
		r.Qid, r.Iounit = s.qid(f.path), s.msize-24
		return r, ""
	case styx.Tread:
		if !f.open {
			return nil, "fid not open"
		}
		if f.qt&styx.QTDIR != 0 {
			if t.Offset == 0 {
				f.dirs = s.readdir(f.path)
			}
			r.Data = dirslice(f.dirs, t.Offset, t.Count)
			return r, ""
		}
		data, e := s.readfile(f.path)
		if e != "" {
			return nil, e
		}
		if t.Offset < uint64(len(data)) {
			r.Data = data[t.Offset:min(uint64(len(data)), t.Offset+uint64(t.Count))]
		}
		return r, ""
	case styx.Twrite:
		if !f.open || f.mode&3 == sys.OREAD {
			return nil, "fid not open for write"
		}
		if e := s.writefile(f.path, t.Offset, t.Data); e != "" {
			return nil, e
		}
		r.Count = uint32(len(t.Data))
		return r, ""
	case styx.Tclunk:
		delete(s.fids, t.Fid)
		return r, ""
	case styx.Tremove:
		delete(s.fids, t.Fid)
		u := finduserpath(f.path)
		switch ftype(f.path) {
		case quser:
			if u == nil {
				return nil, eremoved
			}
			users[u.x] = nil
		case qsecret:
			if u == nil {
				return nil, eremoved
			}
			u.secret = nil
		default:
			return nil, eperm
		}
		s.writekeys()
		return r, ""
	case styx.Tstat:
		d, e := dirgen(f.path)
		if e != "" {
			return nil, e
		}
		r.Stat = styx.MarshalDir(nil, &d)
		return r, ""
	case styx.Twstat:
		d, err := styx.UnmarshalDir(t.Stat)
		if err != nil {
			return nil, "bad stat"
		}
		u := finduserpath(f.path)
		if ftype(f.path) != quser || d.Name == "" {
			return nil, eperm
		}
		if u == nil {
			return nil, eremoved
		}
		if d.Name == "." || d.Name == ".." {
			return nil, "illegal name"
		}
		if findusername(d.Name) != nil {
			return nil, "user already exists"
		}
		u.name = d.Name
		s.writekeys()
		return r, ""
	}
	return nil, "bad message"
}

func (s *srv) writekeys() { writekeys(s.keyfile) }

func access(d styx.Dir, mode uint8) string {
	if d.Mode&styx.DMDIR != 0 && mode&3 != sys.OREAD {
		return "is a directory"
	}
	need := [4]uint32{4, 2, 6, 1}[mode&3]
	if mode&sys.OTRUNC != 0 {
		need |= 2
	}
	if (d.Mode>>6)&need != need {
		return eperm
	}
	return ""
}

func walk1(p uint64, name string) (uint64, string) {
	switch ftype(p) {
	case qroot:
		if name == ".." {
			return p, ""
		}
		u := findusername(name)
		if u == nil {
			return 0, enotfound
		}
		return u.path | quser, ""
	case quser:
		if name == ".." {
			return qroot, ""
		}
		for _, f := range files {
			if f.name == name {
				return p&^0xf | uint64(f.t), ""
			}
		}
		return 0, enotfound
	}
	return 0, "not a directory"
}

func (s *srv) readdir(p uint64) []byte {
	var b []byte
	switch ftype(p) {
	case qroot:
		for _, u := range users {
			if u != nil {
				d, _ := dirgen(u.path | quser)
				b = styx.MarshalDir(b, &d)
			}
		}
	case quser:
		for _, f := range files {
			d, _ := dirgen(p&^0xf | uint64(f.t))
			b = styx.MarshalDir(b, &d)
		}
	}
	return b
}

// dirslice returns the whole directory entries of b that fit in count
// bytes from off.
func dirslice(b []byte, off uint64, count uint32) []byte {
	if off >= uint64(len(b)) {
		return nil
	}
	b = b[off:]
	n := 0
	for n+2 <= len(b) {
		sz := int(binary.LittleEndian.Uint16(b[n:])) + 2
		if n+sz > int(count) {
			break
		}
		n += sz
	}
	return b[:n]
}

func (s *srv) readfile(p uint64) ([]byte, string) {
	u := finduserpath(p)
	if u == nil {
		return nil, eremoved
	}
	switch ftype(p) {
	case qsecret:
		if u.status != sok {
			return nil, "user disabled"
		}
		if u.expire < now && u.expire != never {
			return nil, "user expired"
		}
		return u.secret, ""
	case qlog:
		return []byte(strconv.Itoa(u.failed)), ""
	case qstatus:
		st := status[u.status]
		if u.status == sok && u.expire != never && u.expire < now {
			st = "expired"
		}
		return []byte(st), ""
	case qexpire:
		if u.expire != never {
			return []byte(strconv.FormatUint(uint64(uint32(u.expire)), 10)), ""
		}
		return []byte("never"), ""
	}
	return nil, eperm
}

func trim(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func isnumeric(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func (s *srv) writefile(p uint64, off uint64, data []byte) string {
	u := finduserpath(p)
	if u == nil {
		return eremoved
	}
	switch ftype(p) {
	case qsecret:
		if off != 0 || len(data) > maxsecret {
			return "illegal write"
		}
		u.secret = append([]byte(nil), data...)
	case qexpire:
		v := trim(string(data))
		if v != "never" {
			if !isnumeric(v) {
				return "illegal expiry time"
			}
			n, _ := strconv.Atoi(v)
			u.expire = n
		} else {
			u.expire = never
		}
		u.failed = 0
	case qstatus:
		v := trim(string(data))
		i := 0
		for i < len(status) && status[i] != v {
			i++
		}
		if i == len(status) {
			return "unknown status"
		}
		u.status = i
		if i == sok {
			u.failed = 0
		}
	case qlog:
		if v := trim(string(data)); v != "good" && v != "ok" {
			if u.failed++; u.failed >= maxfail {
				u.status = sdisabled
			}
		} else {
			u.failed = 0
		}
	default:
		return eperm
	}
	s.writekeys()
	return ""
}
