// Command mcpfs [-m mntpt] command [arg ...] mounts the tools of a Model
// Context Protocol server as files on mntpt (default /n/mcp). It is a
// port of the Rust sibling's mcpfs; docs/mcp.md describes it.
//
// command is started with pipes for its standard input and output and
// must speak MCP's stdio transport on them: mcpd for a local sandbox,
// rcmd host mcpd for a remote one, or any other guest program that does.
// mcpfs makes the initialize handshake (revision 2025-11-25), lists the
// tools, and serves:
//
//	mntpt/server             the server's initialize result, as JSON
//	mntpt/tools/NAME/schema  the tool's definition from tools/list, as JSON
//	mntpt/tools/NAME/call    write the arguments (a JSON object), then read
//	                         the result
//
// Each open of call is one call. What is written to it is the arguments;
// nothing written is {}. The first read makes the call and returns the
// CallToolResult as JSON (content, structuredContent, isError), followed
// by a newline. A read that starts where the writes ended, or at 0, gets
// its start, so {echo $args >[1=0]; cat} <> call works as well as a seek
// to 0. A write after a read starts a new call on the same open file. A
// request the server answers with a JSON-RPC error fails the read with
// the error's message.
//
// The file server is one prog that answers one request at a time, so a
// call holds up every other request to the mount until the server
// answers. Resources, prompts, notifications (such as a changed tool
// list) and progress are not served yet.
package main

import (
	"os"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	usage = "mcpfs [-m mntpt] command [arg ...]"
	self  = "/bin/mcpfs.wasm"
	// protocol is the revision mcpfs asks for. mcpd and most servers
	// answer it.
	protocol = "2025-11-25"
	// maxLine is the longest line taken from the server.
	maxLine = 16 << 20
	// maxPages is the pages of tools/list followed before giving up.
	maxPages = 100

	pathRoot   = 0
	pathServer = 1
	pathTools  = 2
	// pathTool is the first tool: tool i is pathTool+4i (its directory),
	// +1 (schema), +2 (call).
	pathTool = 16
)

// client is the client end of an MCP stdio connection.
type client struct {
	to, from int
	buf      []byte
	next     int64
}

func (c *client) send(v mcp.Value) error {
	b := []byte(mcp.Encode(v) + "\n")
	for len(b) > 0 {
		n, err := syscall.Write(c.to, b)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil || n <= 0 {
			return sys.Error("mcp server: " + sys.Errstr())
		}
		b = b[n:]
	}
	return nil
}

// line reads one line from the server.
func (c *client) line() (string, error) {
	tmp := make([]byte, 65536)
	for {
		if i := strings.IndexByte(string(c.buf), '\n'); i >= 0 {
			l := string(c.buf[:i+1])
			c.buf = c.buf[i+1:]
			return l, nil
		}
		if len(c.buf) > maxLine {
			return "", sys.Error("mcp server: message too long")
		}
		n, err := syscall.Read(c.from, tmp)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return "", sys.Error("mcp server: " + sys.Errstr())
		}
		if n == 0 {
			if len(c.buf) > 0 {
				l := string(c.buf)
				c.buf = nil
				return l, nil
			}
			return "", sys.Error("mcp server: hungup")
		}
		c.buf = append(c.buf, tmp[:n]...)
	}
}

// request sends a request and returns its result, or the error's
// message. Requests from the server meanwhile are answered (ping) or
// refused; notifications are dropped.
func (c *client) request(method string, params mcp.Value) (mcp.Value, error) {
	id := c.next
	c.next++
	if err := c.send(mcp.O("jsonrpc", "2.0", "id", id, "method", method, "params", params)); err != nil {
		return nil, err
	}
	for {
		l, err := c.line()
		if err != nil {
			return nil, err
		}
		v, err := mcp.Parse(strings.TrimSpace(mcp.Lossy([]byte(l))))
		if err != nil {
			continue
		}
		if mcp.Has(v, "method") {
			if rid, ok := mcp.Get(v, "id"); ok {
				if err := c.answer(v, rid); err != nil {
					return nil, err
				}
			}
			continue
		}
		if got, ok := mcp.Int(v, "id"); !ok || got != id {
			continue
		}
		if e, ok := mcp.Get(v, "error"); ok {
			msg, ok := mcp.Str(e, "message")
			if !ok {
				msg = "error"
			}
			return nil, sys.Error(msg)
		}
		r, ok := mcp.Get(v, "result")
		if !ok {
			return nil, sys.Error("mcp server: reply without a result")
		}
		return r, nil
	}
}

// answer answers a request the server sent: ping, and nothing else.
func (c *client) answer(req mcp.Value, id mcp.Value) error {
	if m, _ := mcp.Str(req, "method"); m == "ping" {
		return c.send(mcp.O("jsonrpc", "2.0", "id", id, "result", mcp.Obj{}))
	}
	return c.send(mcp.O("jsonrpc", "2.0", "id", id,
		"error", mcp.O("code", int64(-32601), "message", "method not found")))
}

func (c *client) notify(method string) error {
	return c.send(mcp.O("jsonrpc", "2.0", "method", method))
}

type tool struct {
	name   string
	schema []byte
}

// call is the state of one open of a call file.
type call struct {
	args []byte
	end  uint64 // the offset after the last write
	done bool
	res  []byte
	err  string
}

type mcpfs struct {
	client *client
	user   string
	server []byte
	tools  []tool
}

func isDir(path uint64) bool {
	return path == pathRoot || path == pathTools || path >= pathTool && (path-pathTool)%4 == 0
}

func qid(path uint64) styx.Qid {
	q := styx.Qid{Path: path, Type: styx.QTFILE}
	if isDir(path) {
		q.Type = styx.QTDIR
	}
	return q
}

// toolOf is the tool a path belongs to, and which of its files it is (0
// the directory, 1 schema, 2 call).
func (fs *mcpfs) toolOf(path uint64) (int, uint64, bool) {
	if path < pathTool {
		return 0, 0, false
	}
	i := path - pathTool
	t := int(i / 4)
	if t >= len(fs.tools) || i%4 > 2 {
		return 0, 0, false
	}
	return t, i % 4, true
}

func (fs *mcpfs) parent(path uint64) uint64 {
	if t, f, ok := fs.toolOf(path); ok {
		if f == 0 {
			return pathTools
		}
		return pathTool + 4*uint64(t)
	}
	return pathRoot
}

func (fs *mcpfs) child(path uint64, name string) (uint64, bool) {
	switch path {
	case pathRoot:
		switch name {
		case "server":
			return pathServer, true
		case "tools":
			return pathTools, true
		}
		return 0, false
	case pathTools:
		for i, t := range fs.tools {
			if t.name == name {
				return pathTool + 4*uint64(i), true
			}
		}
		return 0, false
	}
	if t, f, ok := fs.toolOf(path); ok && f == 0 {
		switch name {
		case "schema":
			return pathTool + 4*uint64(t) + 1, true
		case "call":
			return pathTool + 4*uint64(t) + 2, true
		}
	}
	return 0, false
}

func (fs *mcpfs) children(path uint64) []uint64 {
	switch path {
	case pathRoot:
		return []uint64{pathServer, pathTools}
	case pathTools:
		var l []uint64
		for i := range fs.tools {
			l = append(l, pathTool+4*uint64(i))
		}
		return l
	}
	if t, f, ok := fs.toolOf(path); ok && f == 0 {
		b := pathTool + 4*uint64(t)
		return []uint64{b + 1, b + 2}
	}
	return nil
}

func (fs *mcpfs) dir(path uint64) *styx.Dir {
	d := &styx.Dir{Qid: qid(path), UID: fs.user, GID: fs.user, MUID: fs.user}
	t, f, isTool := fs.toolOf(path)
	switch {
	case path == pathRoot:
		d.Name, d.Mode = "/", styx.DMDIR|0o555
	case path == pathServer:
		d.Name, d.Mode, d.Length = "server", 0o444, uint64(len(fs.server))
	case path == pathTools:
		d.Name, d.Mode = "tools", styx.DMDIR|0o555
	case isTool && f == 0:
		d.Name, d.Mode = fs.tools[t].name, styx.DMDIR|0o555
	case isTool && f == 1:
		d.Name, d.Mode, d.Length = "schema", 0o444, uint64(len(fs.tools[t].schema))
	case isTool && f == 2:
		d.Name, d.Mode = "call", 0o666
	default:
		return nil
	}
	return d
}

func (fs *mcpfs) Stat(path uint64) (*styx.Dir, error) {
	if d := fs.dir(path); d != nil {
		return d, nil
	}
	return nil, styxservers.Enotfound
}

func (fs *mcpfs) Walk(parent uint64, name string) (*styx.Dir, error) {
	if name == ".." {
		return fs.dir(fs.parent(parent)), nil
	}
	p, ok := fs.child(parent, name)
	if !ok {
		return nil, styxservers.Enotfound
	}
	return fs.dir(p), nil
}

func (fs *mcpfs) Readdir(path uint64, offset, count int) []*styx.Dir {
	kids := fs.children(path)
	var out []*styx.Dir
	for i := offset; i < len(kids) && len(out) < count; i++ {
		out = append(out, fs.dir(kids[i]))
	}
	return out
}

// doCall makes the call whose arguments a call file holds.
func (fs *mcpfs) doCall(t int, args []byte) ([]byte, error) {
	text := strings.TrimSpace(mcp.Lossy(args))
	var arguments mcp.Value = mcp.Obj{}
	if text != "" {
		v, err := mcp.Parse(text)
		if err != nil {
			return nil, sys.Error("the arguments are not JSON: " + err.Error())
		}
		if _, ok := v.(mcp.Obj); !ok {
			return nil, sys.Error("the arguments must be a JSON object")
		}
		arguments = v
	}
	r, err := fs.client.request("tools/call", mcp.O("name", fs.tools[t].name, "arguments", arguments))
	if err != nil {
		return nil, err
	}
	return []byte(mcp.Encode(r) + "\n"), nil
}

func (fs *mcpfs) serve(srv *styxservers.Server) {
	for {
		t, err := srv.Next()
		if err != nil {
			return
		}
		switch t.Type {
		case styx.Topen:
			c := srv.GetFid(t.Fid)
			if c != nil {
				if _, f, ok := fs.toolOf(c.Path); ok && f == 2 {
					// The call file is opened for writing; anything
					// else is read-only.
					if t.Mode&3 == sys.OEXEC || t.Mode&sys.ORCLOSE != 0 {
						srv.Error(t, string(styxservers.Eperm))
						continue
					}
					c.Open(int(t.Mode&3), qid(c.Path))
					c.Data = &call{}
					srv.Reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: qid(c.Path), Iounit: srv.Iounit()})
					continue
				}
				if t.Mode&3 != sys.OREAD || t.Mode&(sys.OTRUNC|sys.ORCLOSE) != 0 {
					srv.Error(t, string(styxservers.Eperm))
					continue
				}
			}
			srv.Open(t)
		case styx.Tread:
			c, err := srv.CanRead(t)
			if c == nil {
				srv.Error(t, err.Error())
				continue
			}
			if c.Qtype&styx.QTDIR != 0 {
				srv.Read(t)
				continue
			}
			ti, f, isTool := fs.toolOf(c.Path)
			switch {
			case c.Path == pathServer:
				srv.Reply(styxservers.ReadBytes(t, fs.server))
			case isTool && f == 1:
				srv.Reply(styxservers.ReadBytes(t, fs.tools[ti].schema))
			case isTool && f == 2:
				cl, _ := c.Data.(*call)
				if cl == nil {
					cl = &call{}
					c.Data = cl
				}
				if !cl.done {
					res, err := fs.doCall(ti, cl.args)
					cl.done, cl.res = true, res
					if err != nil {
						cl.err = err.Error()
					}
				}
				if cl.err != "" {
					srv.Error(t, cl.err)
					continue
				}
				off := t.Offset
				if off >= cl.end {
					off -= cl.end
				}
				srv.Reply(styxservers.ReadBytes(&styx.Fcall{Tag: t.Tag, Offset: off, Count: t.Count}, cl.res))
			default:
				srv.Error(t, string(styxservers.Eperm))
			}
		case styx.Twrite:
			c, err := srv.CanWrite(t)
			if c == nil {
				srv.Error(t, err.Error())
				continue
			}
			if _, f, ok := fs.toolOf(c.Path); !ok || f != 2 {
				srv.Error(t, string(styxservers.Eperm))
				continue
			}
			cl, _ := c.Data.(*call)
			if cl == nil || cl.done {
				cl = &call{}
				c.Data = cl
			}
			cl.args = append(cl.args, t.Data...)
			cl.end = t.Offset + uint64(len(t.Data))
			srv.Reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(len(t.Data))})
		case styx.Tremove:
			if c := srv.GetFid(t.Fid); c != nil {
				srv.DelFid(c)
			}
			srv.Error(t, string(styxservers.Eperm))
		default:
			srv.Default(t)
		}
	}
}

// connect starts argv with pipes for its standard input and output, and
// makes the handshake and the tool list.
func connect(argv []string) (*mcpfs, error) {
	req, err := sys.Pipe()
	if err != nil {
		return nil, sys.Error("pipe: " + err.Error())
	}
	resp, err := sys.Pipe()
	if err != nil {
		return nil, sys.Error("pipe: " + err.Error())
	}
	path := commandPath(argv[0])
	_, err = sys.Spawn(path, argv, sys.NEWFD, [][2]int{{0, req[0]}, {1, resp[1]}, {2, 2}})
	syscall.Close(req[0])
	syscall.Close(resp[1])
	if err != nil {
		return nil, sys.Error(argv[0] + ": " + path + ": " + err.Error())
	}
	c := &client{to: req[1], from: resp[0], next: 1}
	init, err := c.request("initialize", mcp.O(
		"protocolVersion", protocol,
		"capabilities", mcp.Obj{},
		"clientInfo", mcp.O("name", "mcpfs", "version", "0")))
	if err != nil {
		return nil, err
	}
	if err := c.notify("notifications/initialized"); err != nil {
		return nil, err
	}
	var tools []tool
	var cursor mcp.Value
	for range maxPages {
		params := mcp.Obj{}
		if cursor != nil {
			params = mcp.O("cursor", cursor)
			cursor = nil
		}
		page, err := c.request("tools/list", params)
		if err != nil {
			return nil, err
		}
		list, _ := mcp.Get(page, "tools")
		l, _ := list.([]mcp.Value)
	next:
		for _, t := range l {
			name, ok := mcp.Str(t, "name")
			// A name that cannot be a file name is left out.
			if !ok || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
				continue
			}
			for _, x := range tools {
				if x.name == name {
					continue next
				}
			}
			tools = append(tools, tool{name, []byte(mcp.Encode(t) + "\n")})
		}
		nc, _ := mcp.Get(page, "nextCursor")
		if s, ok := nc.(string); ok {
			cursor = s
		} else {
			break
		}
	}
	user := "none"
	if fd, err := sys.Open("/dev/user", sys.OREAD); err == nil {
		b := make([]byte, 256)
		if n, err := syscall.Read(fd, b); err == nil && n > 0 {
			user = strings.TrimSpace(string(b[:n]))
		}
		syscall.Close(fd)
	}
	return &mcpfs{client: c, user: user, server: []byte(mcp.Encode(init) + "\n"), tools: tools}, nil
}

// commandPath finds a command as sh does.
func commandPath(cmd string) string {
	if strings.HasPrefix(cmd, "/") || strings.HasPrefix(cmd, "#") ||
		strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, "../") {
		return cmd
	}
	return "/bin/" + cmd + ".wasm"
}

// serveMain is mcpfs --serve command...: the file server. 9P comes on fd
// 0; fd 1 gets ok or why the server could not be reached, then is
// closed.
func serveMain(argv []string) {
	status := os.NewFile(1, "status")
	fs, err := connect(argv)
	if err != nil {
		status.WriteString(err.Error() + "\n")
		return
	}
	status.WriteString("ok\n")
	status.Close()
	fs.serve(styxservers.New(os.NewFile(0, "styx"), fs, pathRoot))
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--serve" {
		serveMain(os.Args[2:])
		return
	}
	a := arg.New(os.Args)
	a.SetUsage(usage)
	mntpt := "/n/mcp"
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mntpt = a.EArg()
		default:
			a.Usage()
		}
	}
	argv := a.Argv()
	if len(argv) == 0 {
		a.Usage()
	}
	die := func(e string) {
		os.Stderr.WriteString("mcpfs: " + e + "\n")
		sys.Exits("fail:" + e)
	}
	fsp, err := sys.Pipe()
	if err != nil {
		die(err.Error())
	}
	st, err := sys.Pipe()
	if err != nil {
		die(err.Error())
	}
	_, err = sys.Spawn(self, append([]string{"mcpfs", "--serve"}, argv...), sys.NEWFD,
		[][2]int{{0, fsp[0]}, {1, st[1]}, {2, 2}})
	syscall.Close(fsp[0])
	syscall.Close(st[1])
	if err != nil {
		die(err.Error())
	}
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := syscall.Read(st[0], b)
		if err != nil || n == 0 || b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	syscall.Close(st[0])
	switch l := strings.TrimSpace(string(line)); l {
	case "ok":
	case "":
		die("the file server exited")
	default:
		die(l)
	}
	if _, err := sys.Mount(fsp[1], -1, mntpt, sys.MREPL, ""); err != nil {
		die("mount " + mntpt + ": " + err.Error())
	}
}
