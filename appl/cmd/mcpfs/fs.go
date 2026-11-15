package main

// The tree mcpfs serves, and what a read of each file asks the server.

import (
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	pathRoot      = 0
	pathServer    = 1
	pathTools     = 2
	pathResources = 3
	pathRList     = 4
	pathRRead     = 5
	pathPrompts   = 6
	pathPList     = 7
	pathPGet      = 8
	pathLog       = 9
	// pathTool is the first tool: tool i is pathTool+4i (its directory),
	// +1 (schema), +2 (call).
	pathTool = 16

	// maxLog is how much of the server's log the log file keeps: older
	// lines go first.
	maxLog = 64 * 1024
)

type tool struct {
	name   string
	schema []byte
	// gone: no longer listed by the server. The tool keeps its place,
	// so the others keep their qids, but cannot be walked to or listed.
	gone bool
}

// refresh is a tools/list made again after the server said its tools
// changed.
type refresh struct {
	id    int64 // the page request out
	pages int
	seen  []tool
}

// call is the state of one open of a file whose read asks the server.
type call struct {
	args     []byte
	end      uint64 // the offset after the last write
	done     bool
	res      []byte
	err      string
	inflight int64 // the request sent for it and not yet answered; 0 if none
}

type mcpfs struct {
	client  *client
	user    string
	server  []byte
	tools   []tool
	refresh *refresh
	// log is the server's log and progress notifications, one JSON line
	// each.
	log []byte
	// timeout bounds a call, in milliseconds; 0 is none.
	timeout int
}

func isDir(path uint64) bool {
	return path == pathRoot || path == pathTools || path == pathResources || path == pathPrompts ||
		path >= pathTool && (path-pathTool)%4 == 0
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
	if t >= len(fs.tools) || fs.tools[t].gone || i%4 > 2 {
		return 0, 0, false
	}
	return t, i % 4, true
}

// asks reports whether a read of path asks the server: a tool's call, or
// a file of resources or prompts.
func (fs *mcpfs) asks(path uint64) bool {
	if _, f, ok := fs.toolOf(path); ok && f == 2 {
		return true
	}
	return path == pathRList || path == pathRRead || path == pathPList || path == pathPGet
}

// takesWrites reports whether path takes writes: the arguments of a
// request.
func (fs *mcpfs) takesWrites(path uint64) bool {
	if _, f, ok := fs.toolOf(path); ok && f == 2 {
		return true
	}
	return path == pathRRead || path == pathPGet
}

func (fs *mcpfs) parent(path uint64) uint64 {
	if t, f, ok := fs.toolOf(path); ok {
		if f == 0 {
			return pathTools
		}
		return pathTool + 4*uint64(t)
	}
	switch path {
	case pathRList, pathRRead:
		return pathResources
	case pathPList, pathPGet:
		return pathPrompts
	}
	return pathRoot
}

func (fs *mcpfs) child(path uint64, name string) (uint64, bool) {
	var names map[string]uint64
	switch path {
	case pathRoot:
		names = map[string]uint64{"server": pathServer, "tools": pathTools, "resources": pathResources,
			"prompts": pathPrompts, "log": pathLog}
	case pathResources:
		names = map[string]uint64{"list": pathRList, "read": pathRRead}
	case pathPrompts:
		names = map[string]uint64{"list": pathPList, "get": pathPGet}
	case pathTools:
		for i, t := range fs.tools {
			if t.name == name && !t.gone {
				return pathTool + 4*uint64(i), true
			}
		}
		return 0, false
	default:
		if t, f, ok := fs.toolOf(path); ok && f == 0 {
			names = map[string]uint64{"schema": pathTool + 4*uint64(t) + 1, "call": pathTool + 4*uint64(t) + 2}
		}
	}
	p, ok := names[name]
	return p, ok
}

func (fs *mcpfs) children(path uint64) []uint64 {
	switch path {
	case pathRoot:
		return []uint64{pathServer, pathTools, pathResources, pathPrompts, pathLog}
	case pathResources:
		return []uint64{pathRList, pathRRead}
	case pathPrompts:
		return []uint64{pathPList, pathPGet}
	case pathTools:
		var l []uint64
		for i, t := range fs.tools {
			if !t.gone {
				l = append(l, pathTool+4*uint64(i))
			}
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
	case path == pathResources:
		d.Name, d.Mode = "resources", styx.DMDIR|0o555
	case path == pathPrompts:
		d.Name, d.Mode = "prompts", styx.DMDIR|0o555
	case path == pathRList || path == pathPList:
		d.Name, d.Mode = "list", 0o444
	case path == pathRRead:
		d.Name, d.Mode = "read", 0o666
	case path == pathPGet:
		d.Name, d.Mode = "get", 0o666
	case path == pathLog:
		d.Name, d.Mode, d.Length = "log", 0o444, uint64(len(fs.log))
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

// arguments is what was written to a call file, as the JSON object
// tools/call sends: nothing is {}.
func arguments(args []byte) (mcp.Value, error) {
	text := strings.TrimSpace(mcp.Lossy(args))
	if text == "" {
		return mcp.Obj{}, nil
	}
	v, err := mcp.Parse(text)
	if err != nil {
		return nil, sys.Error("the arguments are not JSON: " + err.Error())
	}
	if _, ok := v.(mcp.Obj); !ok {
		return nil, sys.Error("the arguments must be a JSON object")
	}
	return v, nil
}

// query is the request a read of path with args written makes: its
// method and parameters. Arguments that do not fit are the read's error.
func (fs *mcpfs) query(path uint64, args []byte) (string, mcp.Value, error) {
	if t, f, ok := fs.toolOf(path); ok && f == 2 {
		a, err := arguments(args)
		if err != nil {
			return "", nil, err
		}
		return "tools/call", mcp.O("name", fs.tools[t].name, "arguments", a), nil
	}
	switch path {
	case pathRList:
		return "resources/list", mcp.Obj{}, nil
	case pathRRead:
		uri := strings.TrimSpace(mcp.Lossy(args))
		if uri == "" {
			return "", nil, sys.Error("no resource URI written")
		}
		return "resources/read", mcp.O("uri", uri), nil
	case pathPList:
		return "prompts/list", mcp.Obj{}, nil
	case pathPGet:
		a, err := arguments(args)
		if err != nil {
			return "", nil, err
		}
		return "prompts/get", a, nil
	}
	return "", nil, styxservers.Eperm
}

// render is what a read of path returns for the server's result: the
// JSON, or for resources/read the contents themselves.
func render(path uint64, result mcp.Value) []byte {
	if path != pathRRead {
		return []byte(mcp.Encode(result) + "\n")
	}
	var out []byte
	l, _ := mcp.Get(result, "contents")
	contents, _ := l.([]mcp.Value)
	for _, c := range contents {
		if t, ok := mcp.Str(c, "text"); ok {
			out = append(out, t...)
		} else if b, ok := mcp.Str(c, "blob"); ok {
			d, _ := mcp.Unbase64(b)
			out = append(out, d...)
		}
	}
	return out
}

// toolsChanged asks for the tool list again, unless that is already
// under way.
func (fs *mcpfs) toolsChanged() error {
	if fs.refresh != nil {
		return nil
	}
	id, err := fs.client.start("tools/list", mcp.Obj{})
	if err != nil {
		return err
	}
	fs.refresh = &refresh{id: id, pages: 1}
	return nil
}

// refreshed takes a reply to request id if it is the refresh's: it adds
// its page and asks for the next or, at the last page, makes the new
// list the tree's. It reports whether the reply was the refresh's.
func (fs *mcpfs) refreshed(id int64, reply mcp.Value) (bool, error) {
	r := fs.refresh
	if r == nil || r.id != id {
		return false, nil
	}
	fs.refresh = nil
	// A failed list leaves the tools as they were.
	page, ok := mcp.Get(reply, "result")
	if !ok {
		return true, nil
	}
	var cursor mcp.Value
	r.seen, cursor = addPage(r.seen, page)
	if cursor != nil && r.pages < maxPages {
		id, err := fs.client.start("tools/list", mcp.O("cursor", cursor))
		if err != nil {
			return true, err
		}
		r.id = id
		r.pages++
		fs.refresh = r
		return true, nil
	}
	for i := range fs.tools {
		t := &fs.tools[i]
		t.gone = true
		for j, n := range r.seen {
			if n.name == t.name {
				t.schema, t.gone = n.schema, false
				r.seen = append(r.seen[:j], r.seen[j+1:]...)
				break
			}
		}
	}
	fs.tools = append(fs.tools, r.seen...)
	return true, nil
}

// note keeps a log or progress notification in log, dropping whole old
// lines past maxLog.
func (fs *mcpfs) note(v mcp.Value) {
	line := []byte(mcp.Encode(v))
	if len(line) > maxLog-1 {
		line = line[:maxLog-1]
	}
	fs.log = append(fs.log, line...)
	fs.log = append(fs.log, '\n')
	if over := len(fs.log) - maxLog; over > 0 {
		cut := len(fs.log)
		if i := strings.IndexByte(string(fs.log[over:]), '\n'); i >= 0 {
			cut = over + i + 1
		}
		fs.log = append([]byte(nil), fs.log[cut:]...)
	}
}
