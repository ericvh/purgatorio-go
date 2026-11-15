// Command mcpfs [-m mntpt] [-t ms] command [arg ...] mounts the tools,
// resources and prompts of a Model Context Protocol server as files on
// mntpt (default /n/mcp). It is a port of the Rust sibling's mcpfs;
// docs/mcp.md describes it.
//
// command is started with pipes for its standard input and output and
// must speak MCP's stdio transport on them: mcpd for a local sandbox,
// rcmd host mcpd for a remote one, or any other guest program that does.
// mcpfs asks server/discover under revision 2026-07-28 and, if the
// server speaks it, puts the client's _meta on every request; otherwise
// it makes the initialize handshake of 2025-11-25. Then it lists the
// tools, and serves:
//
//	mntpt/server             the server's server/discover (or initialize)
//	                         result, as JSON
//	mntpt/tools/NAME/schema  the tool's definition from tools/list, as JSON
//	mntpt/tools/NAME/call    write the arguments (a JSON object), then read
//	                         the result
//	mntpt/resources/list     the server's resources/list result, as JSON
//	mntpt/resources/read     write a resource's URI, then read its contents
//	mntpt/prompts/list       the server's prompts/list result, as JSON
//	mntpt/prompts/get        write the parameters ({"name": ...,
//	                         "arguments": {...}}), then read the prompt
//	mntpt/log                the server's log and progress notifications,
//	                         one JSON line each, as they came
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
// resources/read works the same way with a URI for the arguments
// (purg:///tmp/f for mcpd): the read returns the contents themselves,
// text as it is and a blob decoded, each content after the other.
//
// Calls do not hold up the mount (serve.go): a read that needs the
// server waits for its answer while other requests are served. With -t,
// a call not answered within ms milliseconds fails with "timed out". A
// changed tool list is fetched again. Log messages (notifications/message)
// and progress (notifications/progress) are kept in log, the last maxLog
// bytes of them.
package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	usage = "mcpfs [-m mntpt] [-t ms] command [arg ...]"
	self  = "/bin/mcpfs.wasm"
	// protocol is the revision spoken to a server without
	// server/discover.
	protocol = "2025-11-25"
	// modern is the stateless revision, spoken to a server that answers
	// server/discover with it among its versions.
	modern = "2026-07-28"
	// probeMs is how long server/discover waits for an answer before the
	// server is taken to be of the earlier revision.
	probeMs = 5000
	// maxLine is the longest line taken from the server.
	maxLine = 16 << 20
	// maxPages is the pages of tools/list followed before giving up.
	maxPages = 100
)

// client is the client end of an MCP stdio connection.
type client struct {
	to, from int
	buf      []byte
	eof      bool
	next     int64
	// modern: speaking 2026-07-28, every request carries the client's
	// _meta.
	modern bool
	// held are notifications that came while request waited, for the
	// file server to take when it starts.
	held []mcp.Value
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

// buffered reports whether take has a line to give without reading.
func (c *client) buffered() bool {
	return strings.IndexByte(string(c.buf), '\n') >= 0 || c.eof && len(c.buf) > 0
}

// take returns a whole line already read, or what is left at end of
// file.
func (c *client) take() (string, bool) {
	if i := strings.IndexByte(string(c.buf), '\n'); i >= 0 {
		l := string(c.buf[:i+1])
		c.buf = c.buf[i+1:]
		return l, true
	}
	if c.eof && len(c.buf) > 0 {
		l := string(c.buf)
		c.buf = nil
		return l, true
	}
	return "", false
}

// fill reads once from the server. At end of file it sets eof; with
// nothing left to take that is an error.
func (c *client) fill() error {
	if c.eof {
		return sys.Error("mcp server: hungup")
	}
	if len(c.buf) > maxLine {
		return sys.Error("mcp server: message too long")
	}
	tmp := make([]byte, 65536)
	for {
		n, err := syscall.Read(c.from, tmp)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return sys.Error("mcp server: " + sys.Errstr())
		}
		if n == 0 {
			c.eof = true
			if len(c.buf) == 0 {
				return sys.Error("mcp server: hungup")
			}
			return nil
		}
		c.buf = append(c.buf, tmp[:n]...)
		return nil
	}
}

// line reads one line from the server.
func (c *client) line() (string, error) {
	for {
		if l, ok := c.take(); ok {
			return l, nil
		}
		if err := c.fill(); err != nil {
			return "", err
		}
	}
}

// parse is a line from the server as JSON; ok is false for a line that
// is not.
func parse(l string) (mcp.Value, bool) {
	v, err := mcp.Parse(strings.TrimSpace(mcp.Lossy([]byte(l))))
	return v, err == nil
}

// withMeta is params with the _meta every 2026-07-28 request carries:
// the revision, the client and its (empty) capabilities, and a log level
// so that log messages still come. Members of a _meta params already
// has are kept.
func withMeta(params mcp.Value) mcp.Value {
	meta := mcp.O(
		"io.modelcontextprotocol/protocolVersion", modern,
		"io.modelcontextprotocol/clientInfo", mcp.O("name", "mcpfs", "version", "0"),
		"io.modelcontextprotocol/clientCapabilities", mcp.Obj{},
		"io.modelcontextprotocol/logLevel", "info")
	fields, _ := params.(mcp.Obj)
	var out mcp.Obj
	var m mcp.Obj
	for _, f := range fields {
		if f.K == "_meta" {
			m, _ = f.V.(mcp.Obj)
			continue
		}
		out = append(out, f)
	}
	m = append(mcp.Obj(nil), m...)
	for _, f := range meta {
		if !mcp.Has(m, f.K) {
			m = append(m, f)
		}
	}
	return append(out, mcp.Member{K: "_meta", V: m})
}

// meta is params as a request in the revision spoken carries them.
func (c *client) meta(params mcp.Value) mcp.Value {
	if !c.modern {
		return params
	}
	return withMeta(params)
}

// start sends a request without waiting for its answer, and returns its
// id.
func (c *client) start(method string, params mcp.Value) (int64, error) {
	id := c.next
	c.next++
	return id, c.send(mcp.O("jsonrpc", "2.0", "id", id, "method", method, "params", c.meta(params)))
}

// complete is a result, unless it asks for input (a 2026-07-28
// input_required result), which mcpfs cannot give.
func complete(r mcp.Value) (mcp.Value, error) {
	if t, _ := mcp.Str(r, "resultType"); t == "input_required" {
		return nil, sys.Error("mcp server: the request needs input, which mcpfs cannot give")
	}
	return r, nil
}

// replyResult is a reply's result, or its error's message.
func replyResult(v mcp.Value) (mcp.Value, error) {
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
	return complete(r)
}

// request sends a request and returns its result, or the error's
// message. Requests from the server meanwhile are answered (ping) or
// refused; notifications are held for the file server.
func (c *client) request(method string, params mcp.Value) (mcp.Value, error) {
	id, err := c.start(method, params)
	if err != nil {
		return nil, err
	}
	for {
		l, err := c.line()
		if err != nil {
			return nil, err
		}
		v, ok := parse(l)
		if !ok {
			continue
		}
		if mcp.Has(v, "method") {
			if rid, ok := mcp.Get(v, "id"); ok {
				if err := c.answer(v, rid); err != nil {
					return nil, err
				}
			} else {
				c.held = append(c.held, v)
			}
			continue
		}
		if got, ok := mcp.Int(v, "id"); !ok || got != id {
			continue
		}
		return replyResult(v)
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

// cancel tells the server to stop request id (notifications/cancelled).
func (c *client) cancel(id int64, reason string) {
	c.send(mcp.O("jsonrpc", "2.0", "method", "notifications/cancelled",
		"params", mcp.O("requestId", id, "reason", reason)))
}

// discover asks the server for server/discover under 2026-07-28. It
// returns the result if the server speaks that revision, and nil if it
// answers with another error, or not within probeMs, as a server of the
// earlier revision would. A 2026-07-28 server that does not take the
// revision is an error.
func discover(c *client) (mcp.Value, error) {
	id := c.next
	c.next++
	err := c.send(mcp.O("jsonrpc", "2.0", "id", id, "method", "server/discover", "params", withMeta(mcp.Obj{})))
	if err != nil {
		return nil, err
	}
	for {
		l, ok := c.take()
		if !ok {
			if i, err := sys.Wait([][2]int{{sys.WaitRead, c.from}}, probeMs); err != nil || i < 0 {
				return nil, nil
			}
			if err := c.fill(); err != nil {
				return nil, err
			}
			continue
		}
		v, ok := parse(l)
		if !ok || mcp.Has(v, "method") {
			continue
		}
		if got, ok := mcp.Int(v, "id"); !ok || got != id {
			continue
		}
		if e, ok := mcp.Get(v, "error"); ok {
			if code, _ := mcp.Int(e, "code"); code == -32022 {
				data, _ := mcp.Get(e, "data")
				s, _ := mcp.Get(data, "supported")
				supported := ""
				if s != nil {
					supported = mcp.Encode(s)
				}
				return nil, sys.Error("mcp server: does not speak " + modern + ": supports " + supported)
			}
			return nil, nil
		}
		r, ok := mcp.Get(v, "result")
		if !ok {
			return nil, nil
		}
		vs, _ := mcp.Get(r, "supportedVersions")
		l2, _ := vs.([]mcp.Value)
		for _, x := range l2 {
			if s, _ := x.(string); s == modern {
				return r, nil
			}
		}
		return nil, nil
	}
}

// addPage adds the tools of one tools/list page to tools, and returns
// them and the cursor of the next page, nil if there is none.
func addPage(tools []tool, page mcp.Value) ([]tool, mcp.Value) {
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
		tools = append(tools, tool{name: name, schema: []byte(mcp.Encode(t) + "\n")})
	}
	if s, ok := mcp.Str(page, "nextCursor"); ok {
		return tools, s
	}
	return tools, nil
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
	// 2026-07-28 first, as its stdio rules say: a server that answers
	// server/discover with that revision has no handshake; any other
	// answer, or none in time, is a server of the earlier revision.
	init, err := discover(c)
	if err != nil {
		return nil, err
	}
	if init != nil {
		c.modern = true
	} else {
		init, err = c.request("initialize", mcp.O(
			"protocolVersion", protocol,
			"capabilities", mcp.Obj{},
			"clientInfo", mcp.O("name", "mcpfs", "version", "0")))
		if err != nil {
			return nil, err
		}
		if err := c.notify("notifications/initialized"); err != nil {
			return nil, err
		}
	}
	// Under 2026-07-28 a changed tool list comes only on a subscription.
	caps, _ := mcp.Get(init, "capabilities")
	tcaps, _ := mcp.Get(caps, "tools")
	changes, _ := mcp.Bool(tcaps, "listChanged")
	listens := c.modern && changes
	var tools []tool
	var cursor mcp.Value
	for range maxPages {
		params := mcp.Obj{}
		if cursor != nil {
			params = mcp.O("cursor", cursor)
		}
		page, err := c.request("tools/list", params)
		if err != nil {
			return nil, err
		}
		if tools, cursor = addPage(tools, page); cursor == nil {
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
	fs := &mcpfs{client: c, user: user, server: []byte(mcp.Encode(init) + "\n"), tools: tools}
	if listens {
		params := mcp.O("notifications", mcp.O("toolsListChanged", true))
		if _, err := c.start("subscriptions/listen", params); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

// commandPath finds a command as sh does.
func commandPath(cmd string) string {
	if strings.HasPrefix(cmd, "/") || strings.HasPrefix(cmd, "#") ||
		strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, "../") {
		return cmd
	}
	return "/bin/" + cmd + ".wasm"
}

// serveMain is mcpfs --serve ms command...: the file server, with a call
// timeout of ms (0 for none). 9P comes on fd 0; fd 1 gets ok or why the
// server could not be reached, then is closed.
func serveMain(argv []string) {
	status := os.NewFile(1, "status")
	if len(argv) < 2 {
		status.WriteString("usage: " + usage + "\n")
		return
	}
	timeout, _ := strconv.ParseUint(argv[0], 10, 31)
	fs, err := connect(argv[1:])
	if err != nil {
		status.WriteString(err.Error() + "\n")
		return
	}
	status.WriteString("ok\n")
	status.Close()
	fs.timeout = int(timeout)
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
	timeout := "0"
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'm':
			mntpt = a.EArg()
		case 't':
			timeout = a.EArg()
			if _, err := strconv.ParseUint(timeout, 10, 31); err != nil {
				a.Usage()
			}
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
	_, err = sys.Spawn(self, append([]string{"mcpfs", "--serve", timeout}, argv...), sys.NEWFD,
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
	if err := sys.Mount(fsp[1], -1, mntpt, sys.MREPL, ""); err != nil {
		die("mount " + mntpt + ": " + err.Error())
	}
}
