package main

// The JSON-RPC 2.0 and MCP layer. A line from the client becomes a reply
// at once or a call for the session, whose reply is made when the session
// answers; calls can therefore finish out of order. Nothing here touches
// files or progs, so it is tested on the build host.
//
// Specification followed: Model Context Protocol revision 2026-07-28, the
// stdio transport. That revision has no handshake: every request carries
// its protocol version, the client's capabilities and its name and
// version (clientInfo) in params._meta, and server/discover describes
// the server. Clients of the earlier revisions (2025-11-25, 2025-06-18,
// 2025-03-26, 2024-11-05) open with initialize instead; the
// specification lets a server answer both, and this one does. ping exists
// only in the earlier revisions and is answered for any client.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
)

const (
	modern        = "2026-07-28"
	serverName    = "mcpd"
	serverTitle   = "Purgatorio sandbox"
	serverVersion = "0.1.0"

	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo   = "io.modelcontextprotocol/clientInfo"
	metaServerInfo   = "io.modelcontextprotocol/serverInfo"

	// ttlMs is how long a client may cache server/discover, tools/list
	// and resources/templates/list: they change only with the binary.
	ttlMs = 3_600_000

	parseError         = -32700
	invalidRequest     = -32600
	methodNotFound     = -32601
	invalidParams      = -32602
	internalError      = -32603
	unsupportedVersion = -32022
	// resourceNotFound is "Resource not found" before 2026-07-28, which
	// uses -32602.
	resourceNotFound = -32002
)

// legacy are the handshake revisions, newest first.
var legacy = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// What a call is.
type What int

const (
	Tool What = iota
	ListResources
	ReadResource
)

// Call is a request the session answers: a tool call or a resource
// request.
type Call struct {
	id     mcp.Value
	modern bool
	What   What
	// Name is the tool, or resources/list or resources/read.
	Name string
	// Args are the tool's arguments, or {"path": ...} for a resource read.
	Args mcp.Value
	uri  string
	// Rejected says why the arguments were refused, for a tool call that
	// failed its schema. Such a call is finished without running.
	Rejected string
}

// Step is what a line from the client comes to: nothing (Reply and Call
// empty), a reply ready to send, or work for the session.
type Step struct {
	Reply string
	Call  *Call
}

// Server is the protocol state: the revision agreed by initialize, for a
// handshake client.
type Server struct {
	legacy string
}

type rpcError struct {
	code    int64
	message string
	data    mcp.Value
}

func rerr(code int64, msg string) *rpcError { return &rpcError{code: code, message: msg} }

func response(id mcp.Value, result mcp.Value, e *rpcError) string {
	o := mcp.O("jsonrpc", "2.0", "id", id)
	if e == nil {
		o = append(o, mcp.Member{K: "result", V: result})
	} else {
		eo := mcp.O("code", e.code, "message", e.message)
		if e.data != nil {
			eo = append(eo, mcp.Member{K: "data", V: e.data})
		}
		o = append(o, mcp.Member{K: "error", V: eo})
	}
	return mcp.Encode(o)
}

// tooLong is the reply to a request line longer than the server holds.
func tooLong() string {
	return response(nil, nil, rerr(invalidRequest, "Invalid Request: message too long"))
}

func supported() []mcp.Value {
	v := []mcp.Value{modern}
	for _, s := range legacy {
		v = append(v, s)
	}
	return v
}

func serverInfo() mcp.Obj {
	return mcp.O("name", serverName, "title", serverTitle, "version", serverVersion)
}

// Caching hints of a 2026-07-28 result.
type cache int

const (
	cacheNone    cache = iota // a tool result
	cachePublic               // changes only with the binary
	cachePrivate              // depends on the session; stale at once
)

// complete adds the members every 2026-07-28 result carries.
func complete(result mcp.Value, isModern bool, c cache) mcp.Value {
	m, ok := result.(mcp.Obj)
	if !ok || !isModern {
		return result
	}
	m = append(mcp.Obj{{K: "resultType", V: "complete"}}, m...)
	switch c {
	case cachePublic:
		m = append(m, mcp.O("ttlMs", int64(ttlMs), "cacheScope", "public")...)
	case cachePrivate:
		m = append(m, mcp.O("ttlMs", int64(0), "cacheScope", "private")...)
	}
	return m
}

// uriOf is the URI of a file: purg:// and its absolute name, with every
// byte outside RFC 3986's unreserved and sub-delimiter sets (and /, :
// and @) percent-encoded.
func uriOf(path string) string {
	var b strings.Builder
	b.WriteString("purg://")
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("-._~/!$&'()*+,;=:@", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pathOf is the absolute file name a purg:///name URI stands for.
func pathOf(uri string) (string, error) {
	rest, ok := strings.CutPrefix(uri, "purg:///")
	if !ok {
		return "", fmt.Errorf("not a purg:/// URI: %s", uri)
	}
	b := []byte{'/'}
	for i := 0; i < len(rest); {
		switch c := rest[i]; c {
		case '%':
			if i+3 > len(rest) {
				return "", fmt.Errorf("bad percent escape in %s", uri)
			}
			n, err := strconv.ParseUint(rest[i+1:i+3], 16, 8)
			if err != nil || rest[i+1] == '+' || rest[i+1] == '-' {
				return "", fmt.Errorf("bad percent escape in %s", uri)
			}
			b = append(b, byte(n))
			i += 3
		case '?', '#':
			return "", fmt.Errorf("a query or fragment in %s", uri)
		default:
			b = append(b, c)
			i++
		}
	}
	p := string(b)
	if !validUTF8(b) || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("not a file name: %s", uri)
	}
	return p, nil
}

func validUTF8(b []byte) bool {
	n, _ := mcp.ValidUTF8Prefix(b)
	return n == len(b)
}

// templates is the one resource template: any file, by name.
func templates() []mcp.Value {
	return []mcp.Value{mcp.O(
		"uriTemplate", "purg:///{+path}",
		"name", "file",
		"title", "A file in the session",
		"description", "Any file in the session's namespace, by its absolute name without the leading "+
			"slash: purg:///tmp/notes.txt, purg:///prog/1/status. A directory reads as its "+
			"names, one per line, with / after each directory.",
	)}
}

func capabilities(handshake bool) mcp.Obj {
	if handshake {
		return mcp.O(
			"tools", mcp.O("listChanged", false),
			"resources", mcp.O("subscribe", false, "listChanged", false))
	}
	return mcp.O("tools", mcp.Obj{}, "resources", mcp.Obj{})
}

// toolResult is a tools/call result: the structured value and the same
// JSON as text, or a failure as text with isError.
func toolResult(v mcp.Value, failure string, failed bool) mcp.Value {
	text := failure
	if !failed {
		text = mcp.Encode(v)
	}
	o := mcp.O("content", []mcp.Value{mcp.O("type", "text", "text", text)})
	if !failed {
		o = append(o, mcp.Member{K: "structuredContent", V: v})
	}
	return append(o, mcp.Member{K: "isError", V: failed})
}

// Kind is tool or resource, for the log.
func (c *Call) Kind() string {
	if c.What == Tool {
		return "tool"
	}
	return "resource"
}

// Finish is the response to send, given the session's result or, when
// failed, its error text.
func (c *Call) Finish(v mcp.Value, e string, failed bool) string {
	switch c.What {
	case Tool:
		return response(c.id, complete(toolResult(v, e, failed), c.modern, cacheNone), nil)
	case ListResources:
		if failed {
			return response(c.id, nil, rerr(internalError, e))
		}
		return response(c.id, complete(v, c.modern, cachePrivate), nil)
	}
	if failed {
		if strings.HasSuffix(e, "file does not exist") {
			code := int64(resourceNotFound)
			if c.modern {
				code = invalidParams
			}
			return response(c.id, nil, &rpcError{code, "Resource not found",
				mcp.O("uri", c.uri, "reason", e)})
		}
		return response(c.id, nil, rerr(internalError, e))
	}
	item := mcp.O("uri", c.uri)
	if m, ok := v.(mcp.Obj); ok {
		item = append(item, m...)
	}
	return response(c.id, complete(mcp.O("contents", []mcp.Value{item}), c.modern, cachePrivate), nil)
}

// Begin reads one line from the client.
func (s *Server) Begin(line string) Step {
	line = strings.TrimSpace(line)
	if line == "" {
		return Step{}
	}
	msg, err := mcp.Parse(line)
	if err != nil {
		return Step{Reply: response(nil, nil, rerr(parseError, "Parse error: "+err.Error()))}
	}
	if _, ok := msg.([]mcp.Value); ok {
		return Step{Reply: response(nil, nil, rerr(invalidRequest, "Invalid Request: batches are not supported"))}
	}
	if _, ok := msg.(mcp.Obj); !ok {
		return Step{Reply: response(nil, nil, rerr(invalidRequest, "Invalid Request: a message must be an object"))}
	}
	id, hasID := mcp.Get(msg, "id")
	method, hasMethod := mcp.Get(msg, "method")
	if !hasMethod {
		// A response to a request. This server sends none, so there is
		// nothing to match it with; a message with neither is broken.
		if mcp.Has(msg, "result") || mcp.Has(msg, "error") {
			return Step{}
		}
		return Step{Reply: response(id, nil, rerr(invalidRequest, "Invalid Request: no method"))}
	}
	if !hasID {
		// A notification is never answered, whatever it says.
		return Step{}
	}
	var replyID mcp.Value
	switch id.(type) {
	case string, int64, float64:
		replyID = id
	}
	fail := func(code int64, text string) Step {
		return Step{Reply: response(replyID, nil, rerr(code, text))}
	}
	if replyID == nil {
		return fail(invalidRequest, "Invalid Request: id must be a string or a number")
	}
	if v, _ := mcp.Str(msg, "jsonrpc"); v != "2.0" {
		return fail(invalidRequest, `Invalid Request: jsonrpc must be "2.0"`)
	}
	m, ok := method.(string)
	if !ok {
		return fail(invalidRequest, "Invalid Request: method must be a string")
	}
	params := mcp.Value(mcp.Obj{})
	if p, ok := mcp.Get(msg, "params"); ok {
		if _, isObj := p.(mcp.Obj); !isObj {
			return fail(invalidParams, "Invalid params: params must be an object")
		}
		params = p
	}
	now, call, e := s.request(m, params)
	switch {
	case e != nil:
		return Step{Reply: response(replyID, nil, e)}
	case call != nil:
		call.id = replyID
		return Step{Call: call}
	}
	return Step{Reply: response(replyID, now, nil)}
}

// era decides which revision governs a request: true is 2026-07-28,
// false a client that did the initialize handshake.
func (s *Server) era(params mcp.Value) (bool, *rpcError) {
	meta, _ := mcp.Get(params, "_meta")
	v, ok := mcp.Get(meta, metaVersion)
	if !ok {
		if s.legacy != "" {
			return false, nil
		}
		return false, rerr(invalidParams, "Invalid params: _meta."+metaVersion+" is required "+
			"(revision "+modern+"), or send initialize first (revision "+legacy[0]+" and earlier)")
	}
	requested, ok := v.(string)
	if !ok {
		return false, rerr(invalidParams, "Invalid params: _meta."+metaVersion+" must be a string")
	}
	if requested != modern {
		// A handshake client may tag requests with the version it
		// negotiated; that is not a per-request version.
		if s.legacy == requested {
			return false, nil
		}
		return false, &rpcError{unsupportedVersion, "Unsupported protocol version",
			mcp.O("supported", supported(), "requested", requested)}
	}
	if c, _ := mcp.Get(meta, metaCapabilities); c == nil {
		return false, rerr(invalidParams, "Invalid params: _meta."+metaCapabilities+" is required")
	} else if _, ok := c.(mcp.Obj); !ok {
		return false, rerr(invalidParams, "Invalid params: _meta."+metaCapabilities+" is required")
	}
	// An Implementation: name and version are required.
	info, _ := mcp.Get(meta, metaClientInfo)
	_, n := mcp.Str(info, "name")
	_, ver := mcp.Str(info, "version")
	if !n || !ver {
		return false, rerr(invalidParams, "Invalid params: _meta."+metaClientInfo+" with a name and a version is required")
	}
	return true, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// request answers a request now, or makes it a call for the session.
func (s *Server) request(method string, params mcp.Value) (mcp.Value, *Call, *rpcError) {
	// Every list here fits one page, so any cursor is a bad one.
	noCursor := func() *rpcError {
		if c, ok := mcp.Get(params, "cursor"); ok && c != nil {
			return rerr(invalidParams, "Invalid params: unknown cursor")
		}
		return nil
	}
	switch method {
	case "initialize":
		requested, ok := mcp.Str(params, "protocolVersion")
		if !ok {
			return nil, nil, rerr(invalidParams, "Invalid params: protocolVersion must be a string")
		}
		// The client's version if this server speaks it, otherwise the
		// newest handshake revision; the client then decides.
		version := legacy[0]
		if contains(legacy, requested) {
			version = requested
		}
		s.legacy = version
		return mcp.O(
			"protocolVersion", version,
			"capabilities", capabilities(true),
			"serverInfo", serverInfo(),
			"instructions", primer), nil, nil
	case "ping":
		// Removed in 2026-07-28, but harmless to answer for anyone.
		meta, _ := mcp.Get(params, "_meta")
		return complete(mcp.Obj{}, mcp.Has(meta, metaVersion), cacheNone), nil, nil
	case "server/discover":
		isModern, e := s.era(params)
		if e != nil {
			return nil, nil, e
		}
		if !isModern {
			return nil, nil, rerr(methodNotFound, "Method not found: server/discover (not in the negotiated revision)")
		}
		return complete(mcp.O(
			"supportedVersions", supported(),
			"capabilities", capabilities(false),
			"instructions", primer,
			"_meta", mcp.O(metaServerInfo, serverInfo())), true, cachePublic), nil, nil
	case "tools/list":
		isModern, e := s.era(params)
		if e == nil {
			e = noCursor()
		}
		if e != nil {
			return nil, nil, e
		}
		return complete(mcp.O("tools", tools()), isModern, cachePublic), nil, nil
	case "tools/call":
		isModern, e := s.era(params)
		if e != nil {
			return nil, nil, e
		}
		name, ok := mcp.Str(params, "name")
		if !ok {
			return nil, nil, rerr(invalidParams, "Invalid params: name must be a string")
		}
		tool := findTool(name)
		if tool == nil {
			return nil, nil, rerr(invalidParams, "Unknown tool: "+name)
		}
		args, ok := mcp.Get(params, "arguments")
		if !ok || args == nil {
			args = mcp.Obj{}
		}
		// Bad arguments are reported as a tool failure, not a protocol
		// error, so that the model sees them and can correct the call.
		rejected := ""
		if schema, ok := mcp.Get(tool, "inputSchema"); ok {
			if err := validate(args, schema, "arguments"); err != nil {
				rejected = err.Error()
			} else if err := check(name, args); err != nil {
				rejected = err.Error()
			}
		}
		return nil, &Call{modern: isModern, What: Tool, Name: name, Args: args, Rejected: rejected}, nil
	case "resources/list":
		isModern, e := s.era(params)
		if e == nil {
			e = noCursor()
		}
		if e != nil {
			return nil, nil, e
		}
		return nil, &Call{modern: isModern, What: ListResources, Name: method, Args: mcp.Obj{}}, nil
	case "resources/templates/list":
		isModern, e := s.era(params)
		if e == nil {
			e = noCursor()
		}
		if e != nil {
			return nil, nil, e
		}
		return complete(mcp.O("resourceTemplates", templates()), isModern, cachePublic), nil, nil
	case "resources/read":
		isModern, e := s.era(params)
		if e != nil {
			return nil, nil, e
		}
		uri, ok := mcp.Str(params, "uri")
		if !ok {
			return nil, nil, rerr(invalidParams, "Invalid params: uri must be a string")
		}
		path, err := pathOf(uri)
		if err != nil {
			return nil, nil, rerr(invalidParams, "Invalid params: "+err.Error())
		}
		return nil, &Call{modern: isModern, What: ReadResource, Name: method,
			Args: mcp.O("path", path), uri: uri}, nil
	}
	return nil, nil, rerr(methodNotFound, "Method not found: "+method)
}
