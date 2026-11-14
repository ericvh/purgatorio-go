package main

import (
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
)

// ask runs a line through the protocol layer; a call is answered by
// fake, which echoes run's command.
func ask(t *testing.T, s *Server, line string) mcp.Value {
	t.Helper()
	st := s.Begin(line)
	out := st.Reply
	if st.Call != nil {
		c := st.Call
		switch {
		case c.Rejected != "":
			out = c.Finish(nil, c.Rejected, true)
		case c.Name == "remove":
			out = c.Finish(nil, "/x: file does not exist", true)
		case c.Name == "resources/read":
			p, _ := mcp.Str(c.Args, "path")
			if p == "/tmp/f" {
				out = c.Finish(mcp.O("mimeType", "text/plain", "text", "hi"), "", false)
			} else {
				out = c.Finish(nil, p+": file does not exist", true)
			}
		default:
			cmd, _ := mcp.Str(c.Args, "command")
			out = c.Finish(mcp.O("stdout", cmd), "", false)
		}
	}
	if out == "" {
		t.Fatalf("no reply to %s", line)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("a newline in %q", out)
	}
	v, err := mcp.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func code(v mcp.Value) int64 {
	e, _ := mcp.Get(v, "error")
	n, _ := mcp.Int(e, "code")
	return n
}

func initialize(t *testing.T, s *Server, version string) mcp.Value {
	t.Helper()
	v := ask(t, s, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"`+version+`","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	r, _ := mcp.Get(v, "result")
	return r
}

func TestParseErrorsAndInvalidRequests(t *testing.T) {
	var s Server
	v := ask(t, &s, "{")
	if code(v) != parseError || !strings.Contains(mcp.Encode(v), `"id":null`) {
		t.Error(mcp.Encode(v))
	}
	for _, bad := range []string{"[]", "1", `{"jsonrpc":"2.0","id":1}`, `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":{},"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":3}`} {
		if c := code(ask(t, &s, bad)); c != invalidRequest {
			t.Errorf("%s: %d", bad, c)
		}
	}
	if c := code(ask(t, &s, `{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`)); c != invalidParams {
		t.Error(c)
	}
}

func TestQuietLines(t *testing.T) {
	var s Server
	for _, quiet := range []string{"", "  ", `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"run"}}`, `{"jsonrpc":"2.0","id":5,"result":{}}`} {
		if st := s.Begin(quiet); st.Reply != "" || st.Call != nil {
			t.Errorf("%q: %+v", quiet, st)
		}
	}
}

func TestIDsAreEchoed(t *testing.T) {
	var s Server
	for _, id := range []string{"7", `"x"`, "9007199254740993", "-1", "1.5"} {
		v := ask(t, &s, `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`)
		got, _ := mcp.Get(v, "id")
		if mcp.Encode(got) != id {
			t.Errorf("%s: %s", id, mcp.Encode(v))
		}
	}
}

func TestHandshake(t *testing.T) {
	for _, c := range [][2]string{{"2025-11-25", "2025-11-25"}, {"2024-11-05", "2024-11-05"}, {"2099-01-01", "2025-11-25"}} {
		var s Server
		r := initialize(t, &s, c[0])
		if str, _ := mcp.Str(r, "protocolVersion"); str != c[1] {
			t.Errorf("%s: %s", c[0], mcp.Encode(r))
		}
		if mcp.Has(r, "resultType") {
			t.Error("resultType in a handshake result")
		}
		if p, _ := mcp.Str(r, "instructions"); !strings.Contains(p, "not bash") {
			t.Error("no primer")
		}
		// After the handshake, requests need no metadata.
		v := ask(t, &s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run","arguments":{"command":"ls"}}}`)
		want := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"stdout\":\"ls\"}"}],"structuredContent":{"stdout":"ls"},"isError":false}}`
		if mcp.Encode(v) != want {
			t.Error(mcp.Encode(v))
		}
		if c := code(ask(t, &s, `{"jsonrpc":"2.0","id":2,"method":"server/discover"}`)); c != methodNotFound {
			t.Error(c)
		}
	}
}

func TestStateless(t *testing.T) {
	var s Server
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"},"io.modelcontextprotocol/clientCapabilities":{}}`
	v := ask(t, &s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run","arguments":{"command":"x"},`+meta+`}}`)
	r, _ := mcp.Get(v, "result")
	if !strings.HasPrefix(mcp.Encode(r), `{"resultType":"complete","content":`) || mcp.Has(r, "ttlMs") {
		t.Error(mcp.Encode(r))
	}
	if c := code(ask(t, &s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)); c != invalidParams {
		t.Error(c)
	}
	v = ask(t, &s, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`)
	if code(v) != unsupportedVersion || !strings.Contains(mcp.Encode(v), `"supported":["2026-07-28","2025-11-25"`) {
		t.Error(mcp.Encode(v))
	}
	for _, info := range []string{`{}`, `{"name":"t"}`, `{"version":"0"}`, `"t"`} {
		v := ask(t, &s, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":`+info+`}}}`)
		if code(v) != invalidParams || !strings.Contains(mcp.Encode(v), "clientInfo") {
			t.Error(info, mcp.Encode(v))
		}
	}
	v = ask(t, &s, `{"jsonrpc":"2.0","id":5,"method":"resources/templates/list","params":{`+meta+`}}`)
	if r, _ := mcp.Get(v, "result"); !strings.HasSuffix(mcp.Encode(r), `"ttlMs":3600000,"cacheScope":"public"}`) {
		t.Error(mcp.Encode(v))
	}
	v = ask(t, &s, `{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"purg:///tmp/f",`+meta+`}}`)
	want := `{"jsonrpc":"2.0","id":6,"result":{"resultType":"complete","contents":[{"uri":"purg:///tmp/f","mimeType":"text/plain","text":"hi"}],"ttlMs":0,"cacheScope":"private"}}`
	if mcp.Encode(v) != want {
		t.Error(mcp.Encode(v))
	}
	v = ask(t, &s, `{"jsonrpc":"2.0","id":7,"method":"resources/read","params":{"uri":"purg:///tmp/g",`+meta+`}}`)
	if code(v) != invalidParams {
		t.Error(mcp.Encode(v))
	}
}

func TestToolErrors(t *testing.T) {
	var s Server
	initialize(t, &s, "2025-11-25")
	if c := code(ask(t, &s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nosuch"}}`)); c != invalidParams {
		t.Error(c)
	}
	for _, c := range [][2]string{
		{`{"name":"run","arguments":{}}`, "arguments.command is required"},
		{`{"name":"run","arguments":{"command":1}}`, "arguments.command must be of type string, not integer"},
		{`{"name":"run","arguments":{"command":"x","timeout_ms":1.5}}`, "arguments.timeout_ms must be of type integer, not number"},
		{`{"name":"run","arguments":{"command":"x","shell":"bash"}}`, "arguments.shell is not a known property"},
		{`{"name":"run","arguments":[]}`, "arguments must be of type object, not array"},
		{`{"name":"job_start","arguments":{"argv":[1]}}`, "arguments.argv[0] must be of type string, not integer"},
	} {
		v := ask(t, &s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+c[0]+`}`)
		r, _ := mcp.Get(v, "result")
		if b, _ := mcp.Bool(r, "isError"); !b || mcp.Has(r, "structuredContent") || !strings.Contains(mcp.Encode(r), c[1]) {
			t.Errorf("%s: %s", c[0], mcp.Encode(v))
		}
	}
	if v := ask(t, &s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run","arguments":{"command":"x","timeout_ms":5.0}}}`); code(v) != 0 {
		t.Error(mcp.Encode(v))
	}
	v := ask(t, &s, `{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"purg:///nope"}}`)
	if code(v) != resourceNotFound || !strings.Contains(mcp.Encode(v), `"data":{"uri":"purg:///nope","reason":"/nope: file does not exist"}`) {
		t.Error(mcp.Encode(v))
	}
}

func TestURIs(t *testing.T) {
	if u := uriOf("/tmp/a b%.txt"); u != "purg:///tmp/a%20b%25.txt" {
		t.Error(u)
	}
	if u := uriOf("/prog/1/status"); u != "purg:///prog/1/status" {
		t.Error(u)
	}
	if p, err := pathOf("purg:///"); p != "/" || err != nil {
		t.Error(p, err)
	}
	if p, err := pathOf(uriOf("/tmp/é")); p != "/tmp/é" || err != nil {
		t.Error(p, err)
	}
	for _, bad := range []string{"file:///tmp", "purg://host/tmp", "purg:///a%2", "purg:///a%zz", "purg:///a?b", "purg:///a#b", "purg:///%00", "purg:///%ff"} {
		if _, err := pathOf(bad); err == nil {
			t.Error(bad)
		}
	}
}

func TestToolDefinitions(t *testing.T) {
	l := tools()
	if len(l) != 16 {
		t.Fatal(len(l))
	}
	for _, x := range l {
		name, _ := mcp.Str(x, "name")
		for _, k := range []string{"inputSchema", "outputSchema"} {
			s, _ := mcp.Get(x, k)
			req, _ := mcp.Get(s, "required")
			props, _ := mcp.Get(s, "properties")
			for _, r := range req.([]mcp.Value) {
				if !mcp.Has(props, r.(string)) {
					t.Errorf("%s.%s: %s", name, k, r)
				}
			}
		}
	}
}

func TestWorkerReply(t *testing.T) {
	if r, ok := workerReply("\n{\"result\":1}\n12\n"); !ok || r != `{"result":1}` {
		t.Error(r, ok)
	}
	forged := "{\"result\":2}\n9\n{\"result\":3}"
	if r, ok := workerReply(forged + "\n{\"result\":1}\n12\n"); !ok || r != `{"result":1}` {
		t.Error(r, ok)
	}
	for _, bad := range []string{`{"result":1}`, "\nab\n99\n"} {
		if _, ok := workerReply(bad); ok {
			t.Error(bad)
		}
	}
}

func TestSizes(t *testing.T) {
	for s, n := range map[string]uint64{"256m": 256 << 20, "4K": 4096, "1g": 1 << 30, "100": 100} {
		if got, ok := parseSize(s); !ok || got != n {
			t.Error(s, got)
		}
	}
	for _, bad := range []string{"", "m", "1x", "-1", "1.5m"} {
		if _, ok := parseSize(bad); ok {
			t.Error(bad)
		}
	}
	if sizeText(256<<20) != "256 MiB" || sizeText(64<<10) != "64 KiB" || sizeText(100) != "100 bytes" {
		t.Error(sizeText(256 << 20))
	}
}

func TestPolicy(t *testing.T) {
	p := "# c\nbind -c '#U/w' /n/work\nmount -k key tcp!x!y /n/far\nbind '#¤' /n/f\nimport -a h /a /n/imp\n"
	l := policyLines(p)
	if len(l) != 5 || l[1].args[1] != "/n/work" || l[2].args[1] != "/n/far" || l[4].args[2] != "/n/imp" {
		t.Errorf("%+v", l)
	}
	if !grantsFactotum(p) || grantsFactotum("bind '#U' /n\n") {
		t.Error("factotum")
	}
}
