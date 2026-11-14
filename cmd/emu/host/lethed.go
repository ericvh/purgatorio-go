package host

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/lethe"
	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/styx"
)

// LetheUsage is lethed's usage line.
const LetheUsage = "usage: lethed [-v] [-A] [-p proto] [-a addr] [-u owner] [-t ticketkey] [-e lifetime] [-w window] [-k keyfile ...] [-U userdir] [-s signers] [-r revoked] root"

// LetheOptions is what lethed's command line says.
type LetheOptions struct {
	Verbose bool
	// Clear is -A: answer clear requests too, as none.
	Clear bool
	// Proto is -p: the ticket exchanges taken, hybrid and or classical.
	Proto     string
	Addr      string
	Owner     string
	TicketKey string
	Config    lethe.Config
	// Keys are public key files trusted for any user.
	Keys []string
	// UserKeys is a directory of <uname>/key.pub files.
	UserKeys string
	Signers  string
	Revoked  string
	Root     string
}

// ParseLethed parses lethed's arguments after the command name.
func ParseLethed(args []string) (*LetheOptions, error) {
	o := &LetheOptions{Addr: "127.0.0.1:5640", Config: lethe.DefaultConfig()}
	usage := errors.New(LetheUsage)
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", usage
			}
			i++
			return args[i], nil
		}
		secs := func() (uint64, error) {
			v, err := val()
			if err != nil {
				return 0, err
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0, usage
			}
			return n, nil
		}
		var err error
		switch a {
		case "-v":
			o.Verbose = true
		case "-A":
			o.Clear = true
		case "-a":
			o.Addr, err = val()
		case "-p":
			o.Proto, err = val()
		case "-u":
			o.Owner, err = val()
		case "-t":
			o.TicketKey, err = val()
		case "-e":
			o.Config.Lifetime, err = secs()
		case "-w":
			o.Config.Window, err = secs()
		case "-k":
			var k string
			k, err = val()
			o.Keys = append(o.Keys, k)
		case "-U":
			o.UserKeys, err = val()
		case "-s":
			o.Signers, err = val()
		case "-r":
			o.Revoked, err = val()
		default:
			if strings.HasPrefix(a, "-") || o.Root != "" {
				return nil, usage
			}
			o.Root = a
		}
		if err != nil {
			return nil, err
		}
	}
	if o.Root == "" {
		return nil, usage
	}
	// Fail closed: nothing is served unless someone can authenticate or
	// the clear mode was asked for by name.
	if !o.Clear && len(o.Keys) == 0 && o.Signers == "" && o.UserKeys == "" {
		return nil, errors.New("lethed: no client can authenticate: give -k, -U or -s, or -A to serve without authentication")
	}
	return o, nil
}

// softLife is how long lethed's soft caches trust what they hold. A file
// renamed over, or a link changed, is seen at most this late; nothing is
// lost when they are dropped.
const softLife = time.Second

type softEntry[V any] struct {
	v  V
	at time.Time
}

// soft is a small map whose entries are trusted for softLife.
type soft[K comparable, V any] struct {
	mu  sync.Mutex
	m   map[K]softEntry[V]
	max int
}

func (s *soft[K, V]) get(k K) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[k]
	if !ok || time.Since(e.at) >= softLife {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (s *soft[K, V]) put(k K, v V) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[K]softEntry[V]{}
	}
	if len(s.m) >= s.max {
		for k, e := range s.m {
			if time.Since(e.at) >= softLife {
				delete(s.m, k)
			}
		}
		if len(s.m) >= s.max {
			clear(s.m)
		}
	}
	s.m[k] = softEntry[V]{v, time.Now()}
}

type names struct {
	qid   styx.Qid
	names []string
}

// LetheTree is host files under a root, read only, with #U's host
// semantics: Inferno error strings, owner names, qids from inodes, names
// that are case-exact on macOS, and hidden entries left out.
//
// A stateless server answers each request from the start. The soft
// caches keep the last of each for a second: resolved paths and
// directory listings (by the directory's qid).
type LetheTree struct {
	root     string
	owner    string
	caseFold bool
	resolved soft[string, string]
	lists    soft[string, names]
}

// NewLetheTree serves root; owner, if not empty, owns every file.
func NewLetheTree(root, owner string) (*LetheTree, error) {
	abs, err := filepath.Abs(root)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v", root, oserror(err))
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", root, oserror(err))
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: %s", root, abi.Enotdir)
	}
	return &LetheTree{root: abs, owner: owner, caseFold: runtime.GOOS == "darwin",
		resolved: soft[string, string]{max: 8192}, lists: soft[string, names]{max: 16}}, nil
}

func hiddenEntry(name string) bool {
	return runtime.GOOS == "darwin" && (name == ".DS_Store" || strings.HasPrefix(name, "._"))
}

// inside resolves p's links and checks the result is in the root. A
// name that leads out is answered as one that does not exist.
func (t *LetheTree) inside(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", oserror(err)
	}
	if real != t.root && !strings.HasPrefix(real, t.root+string(filepath.Separator)) {
		return "", lethe.Error(abi.Enonexist)
	}
	return real, nil
}

// resolve gives the host path of a Lethe path: each name exactly as
// given, and the whole path, links resolved, inside the root.
func (t *LetheTree) resolve(p string) (string, error) {
	if p == "/" {
		return t.root, nil
	}
	if real, ok := t.resolved.get(p); ok {
		return real, nil
	}
	i := strings.LastIndexByte(p, '/')
	parent, name := p[:i], p[i+1:]
	if parent == "" {
		parent = "/"
	}
	dir, err := t.resolve(parent)
	if err != nil {
		return "", err
	}
	if hiddenEntry(name) || (t.caseFold && !exactName(dir, name)) {
		return "", lethe.Error(abi.Enonexist)
	}
	cand := filepath.Join(dir, name)
	fi, err := os.Lstat(cand)
	if err != nil {
		return "", oserror(err)
	}
	real := cand
	if fi.Mode()&fs.ModeSymlink != 0 {
		if real, err = t.inside(cand); err != nil {
			return "", err
		}
	}
	t.resolved.put(p, real)
	return real, nil
}

// letheQid is the qid lethed gives a host file: path from the device and
// inode, as #U, and the version from the modification time with its
// nanoseconds and the size, so a rewrite within a second is seen.
func letheQid(fi fs.FileInfo) styx.Qid {
	q := fsqid(fi)
	v := uint64(fi.ModTime().UnixNano())
	sz := uint64(fi.Size())
	q.Vers = uint32(v) ^ uint32(v>>32) ^ uint32(sz) ^ uint32(sz>>32)*0x9e3779b9
	return q
}

func (t *LetheTree) entry(name string, fi fs.FileInfo) styx.Dir {
	d := (&DevFS{}).dirconv(name, fi, 0)
	d.Type, d.Dev = 0, 0
	d.Qid = letheQid(fi)
	if d.MUID == "" {
		d.MUID = d.UID
	}
	if t.owner != "" {
		d.UID, d.GID, d.MUID = t.owner, t.owner, t.owner
	}
	return d
}

func lastElem(p string) string {
	if p == "/" {
		return "/"
	}
	return p[strings.LastIndexByte(p, '/')+1:]
}

func (t *LetheTree) Stat(p string) (styx.Dir, error) {
	real, err := t.resolve(p)
	if err != nil {
		return styx.Dir{}, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return styx.Dir{}, oserror(err)
	}
	return t.entry(lastElem(p), fi), nil
}

func (t *LetheTree) Read(p string, off uint64, buf []byte) (styx.Qid, int, error) {
	real, err := t.resolve(p)
	if err != nil {
		return styx.Qid{}, 0, err
	}
	f, err := os.Open(real)
	if err != nil {
		return styx.Qid{}, 0, oserror(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return styx.Qid{}, 0, oserror(err)
	}
	if off > 1<<62 {
		return letheQid(fi), 0, nil
	}
	n, err := f.ReadAt(buf, int64(off))
	if err != nil && err != io.EOF {
		return styx.Qid{}, 0, oserror(err)
	}
	return letheQid(fi), n, nil
}

// names are the names in directory real, sorted as bytes, for its qid q.
func (t *LetheTree) names(real string, q styx.Qid) ([]string, error) {
	if n, ok := t.lists.get(real); ok && n.qid == q {
		return n.names, nil
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil, oserror(err)
	}
	var out []string
	for _, e := range ents {
		if !hiddenEntry(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out) // Go strings compare as bytes
	t.lists.put(real, names{q, out})
	return out, nil
}

func (t *LetheTree) List(p string, after []byte, limit int) (styx.Qid, []styx.Dir, error) {
	real, err := t.resolve(p)
	if err != nil {
		return styx.Qid{}, nil, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return styx.Qid{}, nil, oserror(err)
	}
	q := letheQid(fi)
	all, err := t.names(real, q)
	if err != nil {
		return styx.Qid{}, nil, err
	}
	from := sort.Search(len(all), func(i int) bool { return bytes.Compare([]byte(all[i]), after) > 0 })
	var out []styx.Dir
	for _, name := range all[from:] {
		if len(out) == limit {
			break
		}
		// Links out of the tree and dangling links are left out.
		cand := filepath.Join(real, name)
		fi, err := os.Lstat(cand)
		if err != nil {
			continue
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			r, err := t.inside(cand)
			if err != nil {
				continue
			}
			if fi, err = os.Stat(r); err != nil {
				continue
			}
		}
		out = append(out, t.entry(name, fi))
	}
	return q, out, nil
}

func readKey(p string) ([32]byte, bool) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(b), true
}

// LethedServer is a running lethed: its options, engine and tree.
type LethedServer struct {
	Opts   *LetheOptions
	Server *lethe.Server
	Tree   *LetheTree
	ln     net.Listener
}

// NewLethed makes the server the options describe and listens.
func NewLethed(o *LetheOptions) (*LethedServer, error) {
	tree, err := NewLetheTree(o.Root, o.Owner)
	if err != nil {
		return nil, err
	}
	s := &lethe.Server{Config: o.Config, Clear: o.Clear}
	if err := setupTickets(o, s); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", o.Addr, oserror(err))
	}
	return &LethedServer{Opts: o, Server: s, Tree: tree, ln: ln}, nil
}

// Addr is the address it listens on.
func (l *LethedServer) Addr() string { return l.ln.Addr().String() }

// Port is the port it listens on.
func (l *LethedServer) Port() int { return l.ln.Addr().(*net.TCPAddr).Port }

// Close stops listening.
func (l *LethedServer) Close() error { return l.ln.Close() }

// Serve answers connections until the listener is closed.
func (l *LethedServer) Serve() error {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		go l.serve(c)
	}
}

func (l *LethedServer) env() *lethe.Env {
	return &lethe.Env{
		Now:    uint64(time.Now().Unix()),
		Random: func(b []byte) { (&Host{}).Entropy(b) },
		Trust:  func(u string) pgauth.Policy { return letheTrust(l.Opts, u) },
	}
}

func (l *LethedServer) serve(c net.Conn) {
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	for {
		f, err := lethe.ReadFrame(c)
		if err != nil {
			return
		}
		reply, ev := l.Server.Handle(l.Tree, l.env(), f)
		if l.Opts.Verbose {
			fmt.Fprintf(os.Stderr, "lethed: %s\n", ev)
		}
		if _, err := c.Write(reply); err != nil {
			return
		}
	}
}

// RunLethed is lethed's main: it returns the exit status.
func RunLethed(args []string) int {
	o, err := ParseLethed(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	l, err := NewLethed(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Printf("lethed: listening on %s\n", l.Addr())
	if err := l.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "lethed:", err)
		return 1
	}
	return 0
}
