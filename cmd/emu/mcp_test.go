package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/cmd/emu/host"
	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

// The acceptance tests of mcpd (docs/mcp.md): an emu runs mcpd on the
// console, and the test is its MCP client, sending JSON-RPC lines on the
// console's input and reading the replies from its output. Each test
// runs on both wazero modes. The sh corpus (mcp*.sh) covers mcpd over a
// pipe inside one emu, the limits and mcpfs.

// mcpReply is longer than any single call should take, even on the
// interpreter.
const mcpReply = 2 * time.Minute

// mcpClient is an MCP client of an emu running mcpd.
type mcpClient struct {
	t     *testing.T
	mode  wazero.Mode
	in    *io.PipeWriter
	lines chan string
	done  chan string
	sys   *host.System
	next  int64
	// early holds replies read while waiting for another.
	early map[int64]mcp.Value
	init  bool
}

// lineWriter is the emu's console: it hands each line to a channel.
type lineWriter struct {
	mu    sync.Mutex
	buf   []byte
	lines chan string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			return len(p), nil
		}
		w.lines <- string(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
}

// startMcp boots an emu that runs argv (mcpd and its options, or another
// command) on its console, without the handshake.
func launchMcp(t *testing.T, m wazero.Mode, argv ...string) *mcpClient {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "bin", "mcpd.wasm")); err != nil {
		t.Fatalf("%v (run go run ./tools/build first)", err)
	}
	r, w := io.Pipe()
	lw := &lineWriter{lines: make(chan string, 1024)}
	h := &host.Host{In: r, Out: lw}
	sys, err := host.Boot(h, root, getEngines(t)[m])
	if err != nil {
		t.Fatal(err)
	}
	c := &mcpClient{t: t, mode: m, in: w, lines: lw.lines, done: make(chan string, 1), sys: sys,
		next: 1, early: map[int64]mcp.Value{}}
	go func() {
		st, err := sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", append([]string{"emuinit"}, argv...))
		if err != nil {
			st = "error: " + err.Error()
		}
		sys.Init.Exit(st)
		c.done <- st
	}()
	t.Cleanup(c.kill)
	return c
}

// kill ends every prog of the emu, for a test that failed midway.
func (c *mcpClient) kill() {
	c.in.Close()
	select {
	case <-c.done:
		c.done <- ""
		return
	case <-time.After(10 * time.Second):
	}
	for _, pid := range c.sys.Kernel.Progs.Pids() {
		if p := c.sys.Kernel.Progs.Get(pid); p != nil {
			p.Kill()
		}
	}
}

// startMcp runs mcpd with args and makes the 2025-11-25 handshake.
func startMcp(t *testing.T, m wazero.Mode, args ...string) *mcpClient {
	t.Helper()
	c := launchMcp(t, m, append([]string{"mcpd"}, args...)...)
	r := c.request("initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"0"}}`)
	if v, _ := mcp.Str(r, "protocolVersion"); v != "2025-11-25" {
		t.Fatalf("%s: initialize: %s", m, mcp.Encode(r))
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return c
}

func (c *mcpClient) send(line string) {
	c.t.Helper()
	if _, err := c.in.Write([]byte(line + "\n")); err != nil {
		c.t.Fatalf("%s: send: %v", c.mode, err)
	}
}

// recv reads the next line, as JSON.
func (c *mcpClient) recv() mcp.Value {
	c.t.Helper()
	select {
	case l := <-c.lines:
		v, err := mcp.Parse(l)
		if err != nil {
			c.t.Fatalf("%s: a line that is not JSON: %q", c.mode, l)
		}
		return v
	case st := <-c.done:
		c.done <- st
		c.t.Fatalf("%s: the emu exited (%q) before replying", c.mode, st)
	case <-time.After(mcpReply):
		c.t.Fatalf("%s: no reply in %v", c.mode, mcpReply)
	}
	return nil
}

// sendReq sends a request and returns its id.
func (c *mcpClient) sendReq(method, params string) int64 {
	c.t.Helper()
	id := c.next
	c.next++
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
	return id
}

// recvID reads until the reply to id, keeping others for later.
func (c *mcpClient) recvID(id int64) mcp.Value {
	c.t.Helper()
	if v, ok := c.early[id]; ok {
		delete(c.early, id)
		return v
	}
	for {
		v := c.recv()
		got, ok := mcp.Int(v, "id")
		if ok && got == id {
			return v
		}
		if !ok {
			c.t.Fatalf("%s: a reply without an id: %s", c.mode, mcp.Encode(v))
		}
		c.early[got] = v
	}
}

// exchange sends a request and returns the whole reply.
func (c *mcpClient) exchange(method, params string) mcp.Value {
	c.t.Helper()
	return c.recvID(c.sendReq(method, params))
}

// request sends a request and returns its result.
func (c *mcpClient) request(method, params string) mcp.Value {
	c.t.Helper()
	v := c.exchange(method, params)
	r, ok := mcp.Get(v, "result")
	if !ok {
		c.t.Fatalf("%s: %s %s: %s", c.mode, method, params, mcp.Encode(v))
	}
	return r
}

func errorCode(v mcp.Value) int64 {
	e, _ := mcp.Get(v, "error")
	n, _ := mcp.Int(e, "code")
	return n
}

func (c *mcpClient) sendTool(name, args string) int64 {
	c.t.Helper()
	return c.sendReq("tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, args))
}

// toolResult is a tool call's outcome: isError and the structured
// result, or the error text.
func (c *mcpClient) toolResult(id int64) (bool, mcp.Value) {
	c.t.Helper()
	v := c.recvID(id)
	r, ok := mcp.Get(v, "result")
	if !ok {
		c.t.Fatalf("%s: a tool call failed: %s", c.mode, mcp.Encode(v))
	}
	failed, _ := mcp.Bool(r, "isError")
	content, _ := mcp.Get(r, "content")
	l, _ := content.([]mcp.Value)
	if len(l) != 1 {
		c.t.Fatalf("%s: content: %s", c.mode, mcp.Encode(r))
	}
	text, _ := mcp.Str(l[0], "text")
	if failed {
		return true, text
	}
	s, _ := mcp.Get(r, "structuredContent")
	if mcp.Encode(s) != text {
		c.t.Fatalf("%s: the text is not the structured result: %s", c.mode, mcp.Encode(r))
	}
	return false, s
}

func (c *mcpClient) tool(name, args string) (bool, mcp.Value) {
	c.t.Helper()
	return c.toolResult(c.sendTool(name, args))
}

// ok calls a tool that must succeed.
func (c *mcpClient) ok(name, args string) mcp.Value {
	c.t.Helper()
	failed, v := c.tool(name, args)
	if failed {
		c.t.Fatalf("%s: %s %s: %v", c.mode, name, args, v)
	}
	return v
}

// fails calls a tool that must fail and returns its error text.
func (c *mcpClient) fails(name, args string) string {
	c.t.Helper()
	failed, v := c.tool(name, args)
	if !failed {
		c.t.Fatalf("%s: %s %s succeeded: %s", c.mode, name, args, mcp.Encode(v))
	}
	return v.(string)
}

type runResult struct {
	stdout, stderr, status, note             string
	timedOut, stdoutTrunc, stderrTrunc, okay bool
}

func (c *mcpClient) runWith(cmd, extra string) runResult {
	c.t.Helper()
	args := fmt.Sprintf(`{"command":%s`, mcp.Encode(cmd))
	if extra != "" {
		args += "," + extra
	}
	v := c.ok("run", args+"}")
	s := func(k string) string { x, _ := mcp.Str(v, k); return x }
	b := func(k string) bool { x, _ := mcp.Bool(v, k); return x }
	return runResult{s("stdout"), s("stderr"), s("status"), s("note"),
		b("timed_out"), b("stdout_truncated"), b("stderr_truncated"), b("ok")}
}

func (c *mcpClient) run(cmd string) runResult {
	c.t.Helper()
	return c.runWith(cmd, "")
}

// finish closes the input; mcpd must then exit, and the emu with it.
func (c *mcpClient) finish() {
	c.t.Helper()
	c.in.Close()
	select {
	case st := <-c.done:
		c.done <- st
		if st != "" {
			c.t.Errorf("%s: mcpd exited with %q", c.mode, st)
		}
	case <-time.After(mcpReply):
		c.t.Fatalf("%s: mcpd did not exit at the end of its input", c.mode)
	}
	for l := range len(c.lines) {
		_ = l
		c.t.Errorf("%s: a stray line: %q", c.mode, <-c.lines)
	}
}

func str(v mcp.Value, k string) string { s, _ := mcp.Str(v, k); return s }
func num(v mcp.Value, k string) int64  { n, _ := mcp.Int(v, k); return n }

// hostTemp is a file or directory under the host's root/tmp, which the
// emu sees as /tmp/<name> and a session must not.
func hostTemp(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	n := fmt.Sprintf("mcptest-%s-%d", name, time.Now().UnixNano())
	p := filepath.Join(dir, n)
	t.Cleanup(func() { os.RemoveAll(p) })
	return p, n
}

func readPrimer(t *testing.T) string {
	b, err := os.ReadFile("../../appl/cmd/mcpd/primer.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMcpHandshakeAndTools(t *testing.T) {
	primer := readPrimer(t)
	for _, m := range testModes() {
		c := launchMcp(t, m, "mcpd")
		r := c.request("initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`)
		if str(r, "protocolVersion") != "2025-06-18" {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		si, _ := mcp.Get(r, "serverInfo")
		if str(si, "name") != "mcpd" {
			t.Errorf("%s: %s", m, mcp.Encode(si))
		}
		caps, _ := mcp.Get(r, "capabilities")
		if mcp.Encode(caps) != `{"tools":{"listChanged":false},"resources":{"subscribe":false,"listChanged":false},"prompts":{"listChanged":false}}` {
			t.Errorf("%s: %s", m, mcp.Encode(caps))
		}
		if str(r, "instructions") != primer {
			t.Errorf("%s: the instructions are not the primer", m)
		}
		// A notification gets no reply: the next line answers the ping.
		c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		if v := c.request("ping", "{}"); mcp.Encode(v) != "{}" {
			t.Errorf("%s: ping: %s", m, mcp.Encode(v))
		}
		r = c.request("tools/list", "{}")
		tl, _ := mcp.Get(r, "tools")
		tools, _ := tl.([]mcp.Value)
		var names []string
		for _, x := range tools {
			names = append(names, str(x, "name"))
			if len(str(x, "description")) <= 40 {
				t.Errorf("%s: %s: short description", m, str(x, "name"))
			}
			for _, k := range []string{"inputSchema", "outputSchema"} {
				s, _ := mcp.Get(x, k)
				if str(s, "type") != "object" {
					t.Errorf("%s: %s %s", m, str(x, "name"), k)
				}
			}
		}
		want := "run read_file write_file list_dir stat remove job_start job_read job_write job_kill job_list ns bind mount unmount reset"
		if strings.Join(names, " ") != want {
			t.Errorf("%s: tools %q", m, names)
		}
		c.finish()

		// A client of the current, stateless revision: no handshake.
		meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"},"io.modelcontextprotocol/clientCapabilities":{}}`
		c = launchMcp(t, m, "mcpd")
		r = c.request("server/discover", "{"+meta+"}")
		sv, _ := mcp.Get(r, "supportedVersions")
		if str(r, "resultType") != "complete" || !strings.HasPrefix(mcp.Encode(sv), `["2026-07-28",`) ||
			str(r, "instructions") != primer {
			t.Errorf("%s: discover: %.200s", m, mcp.Encode(r))
		}
		r = c.request("tools/list", "{"+meta+"}")
		tl, _ = mcp.Get(r, "tools")
		if l, _ := tl.([]mcp.Value); len(l) != 16 || str(r, "resultType") != "complete" ||
			num(r, "ttlMs") != 3600000 || str(r, "cacheScope") != "public" {
			t.Errorf("%s: tools/list: %d tools", m, len(l))
		}
		r = c.request("tools/call", `{"name":"run","arguments":{"command":"echo stateless"},`+meta+"}")
		sc, _ := mcp.Get(r, "structuredContent")
		if str(r, "resultType") != "complete" || str(sc, "stdout") != "stateless\n" {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		// Without the handshake or the metadata a request is malformed.
		if v := c.exchange("tools/list", "{}"); errorCode(v) != -32602 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		v := c.exchange("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}`)
		if errorCode(v) != -32022 || !strings.Contains(mcp.Encode(v), `"requested":"1999-01-01"`) {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		c.finish()
	}
}

func TestMcpRun(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		r := c.run("echo out; echo err >[1=2]")
		if r.stdout != "out\n" || r.stderr != "err\n" || r.status != "" || !r.okay || r.timedOut ||
			r.stdoutTrunc || r.stderrTrunc {
			t.Errorf("%s: %+v", m, r)
		}
		// A failing command: Inferno's error text and status string.
		r = c.run("echo before; cat /nonexistent")
		if r.stdout != "before\n" || r.stderr != "cat: cannot open /nonexistent: file does not exist\n" ||
			r.status != "fail:bad open" || r.okay {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.run("load std; raise 'my status'"); r.status != "fail:my status" {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.run("nosuchcommand"); r.stderr != "sh: nosuchcommand: file does not exist\n" {
			t.Errorf("%s: %+v", m, r)
		}
		// Standard input, the working directory, and what persists.
		if r := c.runWith("cat; pwd", `"stdin":"line 1\nline 2\n","cwd":"/lib"`); r.stdout != "line 1\nline 2\n/lib\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.run("pwd; cat"); r.stdout != "/tmp\n" {
			t.Errorf("%s: empty stdin, cwd reset: %+v", m, r)
		}
		// The namespace persists; sh copies its environment, as Inferno's
		// does, so variables do not.
		if r := c.run("cd /lib; x=kept; bind /lib /n; pwd"); r.stdout != "/lib\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.run("pwd; echo $#x $home; ls /n/hello.txt"); r.stdout != "/tmp\n0 /tmp\n/n/hello.txt\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if e := c.fails("run", `{"command":"pwd","cwd":"/nonexistent"}`); e != "/nonexistent: file does not exist" {
			t.Errorf("%s: %q", m, e)
		}
		// More input than a pipe holds, to a command that writes before
		// it has read it all.
		big := strings.Repeat("0123456789abcdef", 16*1024)
		if r := c.runWith("wc -c", `"stdin":"`+big+`","max_output_bytes":1000`); strings.TrimSpace(r.stdout) != strconv.Itoa(len(big)) {
			t.Errorf("%s: %+v", m, r)
		}
		// Output caps: the default and a given one, on each stream.
		r = c.run("cat /bin/sh.wasm /bin/sh.wasm")
		if !r.stdoutTrunc || r.stderrTrunc || len(r.stdout) < 60000 || r.status != "" {
			t.Errorf("%s: %d %v %v %q", m, len(r.stdout), r.stdoutTrunc, r.stderrTrunc, r.status)
		}
		r = c.runWith("echo 0123456789; echo abcdefghij >[1=2]", `"max_output_bytes":4`)
		if r.stdout != "0123" || r.stderr != "abcd" || !r.stdoutTrunc || !r.stderrTrunc {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.runWith("echo 0123", `"max_output_bytes":5`); r.stdout != "0123\n" || r.stdoutTrunc {
			t.Errorf("%s: %+v", m, r)
		}
		// A timeout kills a spinning shell, and what a command started.
		r = c.runWith("echo started; load std; while {} {}", `"timeout_ms":1500`)
		if !r.timedOut || r.stdout != "started\n" || r.status == "" || r.okay {
			t.Errorf("%s: %+v", m, r)
		}
		r = c.runWith("sleep 300 & sleep 300; echo not reached", `"timeout_ms":1000`)
		if !r.timedOut || r.stdout != "" {
			t.Errorf("%s: %+v", m, r)
		}
		ps := c.run("ps").stdout
		if strings.Contains(ps, "sleep") || strings.Count(ps, " sh\n") != 1 {
			t.Errorf("%s: the group was not killed:\n%s", m, ps)
		}
		// A background prog does not hold the call open, and lives on.
		if r := c.run("sleep 300 > /dev/null >[2] /dev/null & echo bg"); r.stdout != "bg\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if !strings.Contains(c.run("ps").stdout, "sleep") {
			t.Errorf("%s: the background sleep is gone", m)
		}
		c.finish()
	}
}

func TestMcpFileTools(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		// Every byte value, written and read back as base64.
		all := make([]byte, 1000)
		for i := range all {
			all[i] = byte(i)
		}
		w := c.ok("write_file", `{"path":"bin.dat","content":"`+mcp.Base64(all)+`","encoding":"base64"}`)
		if num(w, "bytes") != 1000 || num(w, "size") != 1000 {
			t.Errorf("%s: %s", m, mcp.Encode(w))
		}
		r := c.ok("read_file", `{"path":"/tmp/bin.dat"}`)
		got, _ := mcp.Unbase64(str(r, "content"))
		if str(r, "encoding") != "base64" || string(got) != string(all) || num(r, "bytes") != 1000 ||
			num(r, "size") != 1000 || !strings.Contains(mcp.Encode(r), `"eof":true`) {
			t.Errorf("%s: %.200s", m, mcp.Encode(r))
		}
		if r := c.run("wc -c < bin.dat"); strings.TrimSpace(r.stdout) != "1000" {
			t.Errorf("%s: %+v", m, r)
		}
		r = c.ok("read_file", `{"path":"bin.dat","offset":254,"length":4}`)
		if got, _ := mcp.Unbase64(str(r, "content")); string(got) != "\xfe\xff\x00\x01" || !strings.Contains(mcp.Encode(r), `"eof":false`) {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		r = c.ok("read_file", `{"path":"bin.dat","offset":2000}`)
		if num(r, "bytes") != 0 || !strings.Contains(mcp.Encode(r), `"eof":true`) {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		// Text comes back as text; a read never splits a character.
		c.ok("write_file", `{"path":"t.txt","content":"héllo\nwörld\n"}`)
		r = c.ok("read_file", `{"path":"t.txt"}`)
		if str(r, "encoding") != "utf8" || str(r, "content") != "héllo\nwörld\n" || num(r, "bytes") != 14 {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		if r := c.ok("read_file", `{"path":"t.txt","length":2}`); str(r, "content") != "h" || num(r, "bytes") != 1 {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		if r := c.ok("read_file", `{"path":"t.txt","offset":1,"length":3}`); str(r, "content") != "él" || num(r, "bytes") != 3 {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		if r := c.ok("read_file", `{"path":"t.txt","encoding":"base64","length":3}`); str(r, "content") != mcp.Base64([]byte("hé")) {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		// create, truncate and append.
		if e := c.fails("write_file", `{"path":"t.txt","content":"x","mode":"create"}`); e != "t.txt: file already exists" {
			t.Errorf("%s: %q", m, e)
		}
		c.ok("write_file", `{"path":"n.txt","content":"one\n","mode":"create"}`)
		if w := c.ok("write_file", `{"path":"n.txt","content":"two\n","mode":"append"}`); num(w, "bytes") != 4 || num(w, "size") != 8 {
			t.Errorf("%s: %s", m, mcp.Encode(w))
		}
		c.ok("write_file", `{"path":"new.txt","content":"a","mode":"append"}`)
		if r := c.run("cat n.txt new.txt"); r.stdout != "one\ntwo\na" {
			t.Errorf("%s: %+v", m, r)
		}
		c.ok("write_file", `{"path":"n.txt","content":"short"}`)
		if r := c.run("cat n.txt"); r.stdout != "short" {
			t.Errorf("%s: %+v", m, r)
		}
		if e := c.fails("write_file", `{"path":"nodir/x","content":""}`); e != "nodir/x: file does not exist" {
			t.Errorf("%s: %q", m, e)
		}
		if e := c.fails("write_file", `{"path":"x","content":"!!","encoding":"base64"}`); !strings.Contains(e, "base64") {
			t.Errorf("%s: %q", m, e)
		}
		// list_dir, stat and remove.
		c.run("mkdir -p d/sub; echo hi > d/f; echo x > d/sub/g; chmod 600 d/f")
		l := c.ok("list_dir", `{"path":"d"}`)
		want := `{"entries":[{"name":"f","type":"file","size":3,"mode":"600","append_only":false,"mtime":`
		if !strings.HasPrefix(mcp.Encode(l), want) || !strings.Contains(mcp.Encode(l), `{"name":"sub","type":"dir",`) ||
			!strings.HasSuffix(mcp.Encode(l), `"truncated":false}`) {
			t.Errorf("%s: %s", m, mcp.Encode(l))
		}
		s := c.ok("stat", `{"path":"d/f"}`)
		if str(s, "name") != "f" || str(s, "type") != "file" || num(s, "size") != 3 || str(s, "path") != "/tmp/d/f" || str(s, "uid") == "" {
			t.Errorf("%s: %s", m, mcp.Encode(s))
		}
		if s := c.ok("stat", `{"path":"/bin"}`); str(s, "type") != "dir" {
			t.Errorf("%s: %s", m, mcp.Encode(s))
		}
		for _, x := range [][3]string{
			{"stat", `{"path":"/tmp/none"}`, "/tmp/none: file does not exist"},
			{"read_file", `{"path":"none"}`, "none: file does not exist"},
			{"read_file", `{"path":"d"}`, "d: file is a directory"},
			{"list_dir", `{"path":"none"}`, "none: file does not exist"},
			{"list_dir", `{"path":"d/f"}`, "d/f: not a directory"},
			{"remove", `{"path":"none"}`, "none: file does not exist"},
			{"remove", `{"path":"d"}`, "d: directory not empty"},
			{"read_file", `{"path":""}`, "arguments.path must not be empty"},
		} {
			if e := c.fails(x[0], x[1]); e != x[2] {
				t.Errorf("%s: %s %s: %q", m, x[0], x[1], e)
			}
		}
		if r := c.ok("remove", `{"path":"d/f"}`); num(r, "removed") != 1 {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		if r := c.ok("remove", `{"path":"d","recursive":true}`); num(r, "removed") != 3 {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		c.finish()
	}
}

func TestMcpConfined(t *testing.T) {
	for _, m := range testModes() {
		hp, name := hostTemp(t, "secret")
		if err := os.WriteFile(hp, []byte("host only\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		c := startMcp(t, m)
		// The root holds mount points only, and /tmp is private and empty.
		if r := c.run("ls /; ls /tmp /net /chan /n /mnt"); r.stdout != "/bin\n/chan\n/dev\n/env\n/fd\n/lib\n/mnt\n/n\n/net\n/prog\n/tmp\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if e := c.fails("read_file", `{"path":"/tmp/`+name+`"}`); e != "/tmp/"+name+": file does not exist" {
			t.Errorf("%s: %q", m, e)
		}
		// No device can be attached once the namespace is built: not the
		// host tree, host commands or the network, by sh or by a tool.
		for _, dev := range []string{"#U", "#C", "#I", "#c", "#p", "#¤"} {
			r := c.run("bind '" + dev + "' /n; ls /n")
			if r.stdout != "" || !strings.Contains(r.stderr, "mount/attach disallowed") {
				t.Errorf("%s %s: %+v", m, dev, r)
			}
			if e := c.fails("read_file", `{"path":"`+dev+`/tmp/`+name+`"}`); e != dev+"/tmp/"+name+": mount/attach disallowed" {
				t.Errorf("%s: %q", m, e)
			}
			if e := c.fails("list_dir", `{"path":"`+dev+`"}`); e != dev+": mount/attach disallowed" {
				t.Errorf("%s: %q", m, e)
			}
		}
		r := c.run("ls /cmd; cat /net/tcp/clone; os echo escaped")
		if r.stdout != "" || !strings.Contains(r.stderr, "/net/tcp/clone: file does not exist") ||
			!strings.Contains(r.stderr, "/cmd: file does not exist") {
			t.Errorf("%s: %+v", m, r)
		}
		// The console is not the protocol stream: writes vanish and reads
		// see end of file. (finish fails on a stray line.)
		if r := c.run("echo stray > /dev/cons; cat /dev/cons; echo done"); r.stdout != "done\n" || r.status != "" {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.run("ls /dev"); strings.Count(r.stdout, "\n") != 8 {
			t.Errorf("%s: %+v", m, r)
		}
		// /bin and /lib can be read and run but not changed.
		r = c.run("@{echo x > /bin/new}; @{echo x > /lib/new}; @{echo x > /bin/cat.wasm}; " +
			"@{echo x >> /lib/hello.txt}; rm /bin/cat.wasm; mkdir /lib/d; " +
			"mv /bin/cat.wasm /bin/c.wasm; chmod 777 /bin/cat.wasm; cat /lib/hello.txt")
		if r.stdout != "hello from the root\n" {
			t.Errorf("%s: %+v", m, r)
		}
		errs := strings.Split(strings.TrimSpace(r.stderr), "\n")
		if len(errs) != 8 {
			t.Errorf("%s: %q", m, errs)
		}
		for _, e := range errs {
			if !strings.HasSuffix(e, "file system read only") && !strings.HasSuffix(e, "mounted directory forbids creation") &&
				!strings.HasSuffix(e, "permission denied") {
				t.Errorf("%s: %q", m, e)
			}
		}
		for _, x := range [][2]string{
			{"write_file", `{"path":"/bin/cat.wasm","content":"x"}`},
			{"write_file", `{"path":"/lib/hello.txt","content":"x","mode":"append"}`},
			{"write_file", `{"path":"/bin/new","content":"x"}`},
			{"remove", `{"path":"/lib/hello.txt"}`},
			{"remove", `{"path":"/lib/ndb","recursive":true}`},
		} {
			e := c.fails(x[0], x[1])
			if !strings.HasSuffix(e, "file system read only") && !strings.HasSuffix(e, "mounted directory forbids creation") {
				t.Errorf("%s: %s %s: %q", m, x[0], x[1], e)
			}
		}
		if b, err := os.ReadFile(filepath.Join(root, "lib", "hello.txt")); err != nil || string(b) != "hello from the root\n" {
			t.Errorf("%s: /lib/hello.txt changed: %q %v", m, b, err)
		}
		if st, err := os.Stat(filepath.Join(root, "bin", "cat.wasm")); err != nil || st.Size() < 1000 {
			t.Errorf("%s: /bin/cat.wasm changed", m)
		}
		for _, f := range []string{"bin/new", "lib/new", "lib/d"} {
			if _, err := os.Stat(filepath.Join(root, f)); err == nil {
				t.Errorf("%s: %s made", m, f)
			}
		}
		c.run("echo mine > /tmp/session-file")
		if _, err := os.Stat(filepath.Join(root, "tmp", "session-file")); err == nil {
			t.Errorf("%s: the session wrote the host's /tmp", m)
		}
		if b, _ := os.ReadFile(hp); string(b) != "host only\n" {
			t.Errorf("%s: %q", m, b)
		}
		c.finish()
	}
}

func TestMcpProgs(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		// The session prog leads its group and sees itself; its parent,
		// the front end, and the emu's first prog do not exist for it.
		job := num(c.ok("job_start", `{"argv":["sleep","1000"]}`), "pid")
		ls := c.run("ls /prog").stdout
		var pids []int64
		for _, l := range strings.Fields(ls) {
			n, _ := strconv.ParseInt(strings.TrimPrefix(l, "/prog/"), 10, 64)
			pids = append(pids, n)
		}
		has := func(p int64) bool {
			for _, x := range pids {
				if x == p {
					return true
				}
			}
			return false
		}
		if !has(job) || has(1) {
			t.Errorf("%s: job %d, %s", m, job, ls)
		}
		for _, pid := range []int64{1, 2} {
			if has(pid) {
				continue
			}
			p := strconv.FormatInt(pid, 10)
			r := c.run("ls /prog/" + p + "; @{echo kill > /prog/" + p + "/ctl}; kill " + p)
			want := "ls: /prog/" + p + ": file does not exist\nsh: cannot open /prog/" + p +
				"/ctl: file does not exist\nkill: cannot open /prog/" + p + "/ctl: file does not exist\n"
			if r.stderr != want {
				t.Errorf("%s: %q", m, r.stderr)
			}
			if e := c.fails("read_file", `{"path":"/prog/`+p+`/status"}`); e != "/prog/"+p+"/status: file does not exist" {
				t.Errorf("%s: %q", m, e)
			}
		}
		if r := c.run(fmt.Sprintf("load std; kill %d && echo killed", job)); r.stdout != "killed\n" {
			t.Errorf("%s: %+v", m, r)
		}
		// A file tool that writes /fd/1 writes into its own worker, so it
		// cannot forge a reply.
		c.ok("write_file", `{"path":"/fd/1","content":"{\"jsonrpc\":\"2.0\",\"id\":999,\"result\":{}}\n{\"tag\":99,\"result\":{}}\n"}`)
		if r := c.run("echo still serving"); r.stdout != "still serving\n" {
			t.Errorf("%s: %+v", m, r)
		}
		c.finish()
	}
}

func TestMcpBlockingFileTool(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m, "-t", "2000")
		c.ok("write_file", `{"path":"keep","content":"kept"}`)
		job := num(c.ok("job_start", `{"argv":["sleep","100000"]}`), "pid")
		// A prog's wait file blocks until a child exits; sleep has none.
		start := time.Now()
		e := c.fails("read_file", fmt.Sprintf(`{"path":"/prog/%d/wait"}`, job))
		if !strings.HasPrefix(e, "timed out after 2000 ms") || time.Since(start) > 30*time.Second {
			t.Errorf("%s: %q", m, e)
		}
		// The session goes on: its files and its job are still there.
		if v := c.ok("read_file", `{"path":"keep"}`); str(v, "content") != "kept" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if v := c.ok("job_list", "{}"); !strings.Contains(mcp.Encode(v), `"running":true`) {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// Calls sent behind it wait for it, then go on.
		a := c.sendTool("read_file", fmt.Sprintf(`{"path":"/prog/%d/wait"}`, job))
		b := c.sendTool("stat", `{"path":"keep"}`)
		if failed, v := c.toolResult(a); !failed || !strings.Contains(v.(string), "timed out after") {
			t.Errorf("%s: %v", m, v)
		}
		if _, v := c.toolResult(b); num(v, "size") != 4 {
			t.Errorf("%s: %v", m, v)
		}
		c.finish()
	}
}

func TestMcpNamespaceFile(t *testing.T) {
	for _, m := range testModes() {
		wp, wname := hostTemp(t, "work")
		if err := os.MkdirAll(wp, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wp, "in.txt"), []byte("from the host\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pp, pname := hostTemp(t, "policy")
		policy := "# grant one host directory\nbind -c '#U/tmp/" + wname + "' /n/work\n" +
			"# a mount point, bound whole\nbind /lib /n/lib\nbind -a $home /mnt/home/x\n"
		if err := os.WriteFile(pp, []byte(policy), 0o644); err != nil {
			t.Fatal(err)
		}
		c := startMcp(t, m, "-n", "/tmp/"+pname)
		if r := c.run("cat /n/work/in.txt; echo out > /n/work/out.txt; ls /n"); r.stdout != "from the host\n/n/lib\n/n/work\n" {
			t.Errorf("%s: %+v", m, r)
		}
		names := func(dir string) string {
			v := c.ok("list_dir", `{"path":"`+dir+`"}`)
			e, _ := mcp.Get(v, "entries")
			return mcp.Encode(e)
		}
		lib := names("/lib")
		if !strings.Contains(lib, `"name":"hello.txt"`) || names("/n/lib") != lib {
			t.Errorf("%s: %s", m, lib)
		}
		if r := c.run("ls -d /mnt/home/x"); r.stdout != "/mnt/home/x\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if b, _ := os.ReadFile(filepath.Join(wp, "out.txt")); string(b) != "out\n" {
			t.Errorf("%s: %q", m, b)
		}
		if r := c.run("bind '#U' /mnt"); !strings.Contains(r.stderr, "mount/attach disallowed") {
			t.Errorf("%s: %+v", m, r)
		}
		c.ok("reset", "{}")
		if r := c.run("cat /n/work/out.txt"); r.stdout != "out\n" {
			t.Errorf("%s: granted again: %+v", m, r)
		}
		c.finish()

		// A policy that cannot be applied is reported by every call.
		if err := os.WriteFile(pp, []byte("bind /nonexistent /n/work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		c = startMcp(t, m, "-n", "/tmp/"+pname)
		if e := c.fails("run", `{"command":"echo hi"}`); !strings.Contains(e, "namespace file") {
			t.Errorf("%s: %q", m, e)
		}
		c.finish()

		// The factotum only when the policy binds it.
		if err := os.WriteFile(pp, []byte("bind '#¤' /n/factotum\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		c = startMcp(t, m, "-n", "/tmp/"+pname)
		if r := c.run("ls '#¤' /n/factotum"); r.stderr != "" {
			t.Errorf("%s: %+v", m, r)
		}
		c.finish()
	}
}

func TestMcpReset(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		c.run("echo data > f; v=set; mkdir /tmp/u; bind /tmp/u /n; sleep 300 > /dev/null &")
		if r := c.run("cat f; echo $v; ns | grep '/tmp/u /n'; ps | grep sleep | wc -l"); r.stdout != "data\n\nbind /tmp/u /n\n      1\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if r := c.ok("reset", "{}"); mcp.Encode(r) != `{"reset":true}` {
			t.Errorf("%s: %s", m, mcp.Encode(r))
		}
		if r := c.run("ls /tmp; echo $#v; ns | grep '/tmp/u /n'; ps | grep sleep | wc -l"); r.stdout != "0\n      0\n" {
			t.Errorf("%s: %+v", m, r)
		}
		// The session sees only its own progs: the session prog, two
		// memfs, two exports, and this command and its launcher.
		ps := c.run("ps").stdout
		if strings.Count(ps, "mcpd\n") != 2 || strings.Count(ps, "memfs\n") != 2 || strings.Count(ps, "export\n") != 2 {
			t.Errorf("%s:\n%s", m, ps)
		}
		// A command can kill the session's own server prog. The call says
		// so, and the next one finds a new session.
		c.run("echo x > survivor")
		e := c.fails("run", `{"command":"load std; l=`+"`"+`{ps | grep ' mcpd$'}; p=${hd $l}; echo kill > /prog/$p/ctl; sleep 30"}`)
		if !strings.Contains(e, "the session ended") {
			t.Errorf("%s: %q", m, e)
		}
		if r := c.run("ls /tmp; echo fresh"); r.stdout != "fresh\n" {
			t.Errorf("%s: %+v", m, r)
		}
		c.finish()
	}
}

func TestMcpMalformed(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		c.send("{this is not json")
		if v := c.recv(); errorCode(v) != -32700 || !strings.Contains(mcp.Encode(v), `"id":null`) {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		c.send("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"\xff\xfe\"}")
		if v := c.recv(); errorCode(v) != -32700 {
			t.Errorf("%s: invalid UTF-8: %s", m, mcp.Encode(v))
		}
		c.send("[]")
		if v := c.recv(); errorCode(v) != -32600 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		c.send(`{"jsonrpc":"2.0","id":null,"method":"ping"}`)
		if v := c.recv(); errorCode(v) != -32600 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// Blank lines and unknown notifications are passed over.
		c.send("")
		c.send(`{"jsonrpc":"2.0","method":"notifications/nosuch"}`)
		c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`)
		for _, method := range []string{"completion/complete", "resources/subscribe", "nosuch"} {
			if v := c.exchange(method, "{}"); errorCode(v) != -32601 {
				t.Errorf("%s: %s: %s", m, method, mcp.Encode(v))
			}
		}
		if v := c.exchange("tools/call", `{"name":"nosuch","arguments":{}}`); errorCode(v) != -32602 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// Bad arguments are for the model to see and fix.
		for _, x := range [][3]string{
			{"run", `{"cmd":"echo hi"}`, "arguments.command is required"},
			{"run", `{"command":"echo hi","timeout_ms":"soon"}`, "arguments.timeout_ms must be of type integer, not string"},
			{"run", `{"command":"x","timeout_ms":600001}`, "arguments.timeout_ms must be at most 600000"},
			{"write_file", `{"path":"a","content":"","mode":"w"}`, `arguments.mode must be one of "truncate", "create", "append"`},
			{"job_start", `{}`, "arguments.command or arguments.argv is required"},
			{"job_start", `{"command":"x","argv":["x"]}`, "give arguments.command or arguments.argv, not both"},
			{"job_start", `{"argv":[]}`, "arguments.argv must not be empty"},
		} {
			if e := c.fails(x[0], x[1]); e != x[2] {
				t.Errorf("%s: %s %s: %q", m, x[0], x[1], e)
			}
		}
		c.send(`{"jsonrpc":"2.0","id":"last","method":"ping"}`)
		if v := c.recv(); str(v, "id") != "last" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if r := c.run("echo still here"); r.stdout != "still here\n" {
			t.Errorf("%s: %+v", m, r)
		}
		c.finish()
	}
}

// TestMcpPrimer checks that every example in the primer the server sends
// to models does what the primer says.
func TestMcpPrimer(t *testing.T) {
	primer := readPrimer(t)
	type example struct{ cmd, want string }
	var examples []example
	open := false
	for _, l := range strings.Split(primer, "\n") {
		if cmd, ok := strings.CutPrefix(l, "; "); ok {
			examples = append(examples, example{cmd, ""})
			open = true
		} else if out, ok := strings.CutPrefix(l, "  "); ok && open {
			examples[len(examples)-1].want += out + "\n"
		} else {
			open = false
		}
	}
	if len(examples) < 12 {
		t.Fatalf("%d examples", len(examples))
	}
	for _, m := range testModes() {
		c := startMcp(t, m)
		for _, e := range examples {
			r := c.run(e.cmd)
			if got := r.stderr + r.stdout; got != e.want {
				t.Errorf("%s: %s:\ngot  %q\nwant %q", m, e.cmd, got, e.want)
			}
		}
		for _, cmd := range []string{"awk", "find", "head", "python", "git"} {
			if e := c.fails("stat", `{"path":"/bin/`+cmd+`.wasm"}`); !strings.HasSuffix(e, "file does not exist") {
				t.Errorf("%s: %q", m, e)
			}
		}
		_, rest, _ := strings.Cut(primer, "Among them: ")
		list, _, _ := strings.Cut(rest, ".")
		for _, cmd := range strings.Fields(list) {
			c.ok("stat", `{"path":"/bin/`+cmd+`.wasm"}`)
		}
		c.finish()
	}
}

// readToExit reads job id until it has exited; it returns the last
// result and all of stdout and stderr read on the way.
func (c *mcpClient) readToExit(id int64) (mcp.Value, string, string) {
	c.t.Helper()
	var out, errs string
	for {
		v := c.ok("job_read", fmt.Sprintf(`{"id":%d,"wait_ms":20000}`, id))
		out += str(v, "stdout")
		errs += str(v, "stderr")
		if running, _ := mcp.Bool(v, "running"); !running {
			return v, out, errs
		}
	}
}

func TestMcpJobs(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		job := c.ok("job_start", `{"argv":["cat"]}`)
		id := num(job, "id")
		if num(job, "pid") <= 0 {
			t.Errorf("%s: %s", m, mcp.Encode(job))
		}
		// A read that waits for output does not hold up other calls, nor
		// does the job, blocked reading its input.
		waiting := c.sendTool("job_read", fmt.Sprintf(`{"id":%d,"wait_ms":60000}`, id))
		if r := c.run("echo meanwhile"); r.stdout != "meanwhile\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if f := c.ok("read_file", `{"path":"/lib/hello.txt"}`); str(f, "content") != "hello from the root\n" {
			t.Errorf("%s: %s", m, mcp.Encode(f))
		}
		if l := c.ok("job_list", "{}"); !strings.Contains(mcp.Encode(l), `"command":"cat","running":true`) {
			t.Errorf("%s: %s", m, mcp.Encode(l))
		}
		if _, ok := c.early[waiting]; ok {
			t.Errorf("%s: the waiting read was answered early", m)
		}
		if w := c.ok("job_write", fmt.Sprintf(`{"id":%d,"data":"hello\n"}`, id)); num(w, "queued") != 6 {
			t.Errorf("%s: %s", m, mcp.Encode(w))
		}
		_, v := c.toolResult(waiting)
		if str(v, "stdout") != "hello\n" || num(v, "offset") != 0 || num(v, "next_offset") != 6 ||
			!strings.Contains(mcp.Encode(v), `"running":true`) {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// The next read starts where that one ended; offsets read again.
		if v := c.ok("job_read", fmt.Sprintf(`{"id":%d}`, id)); str(v, "stdout") != "" || num(v, "offset") != 6 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if w := c.ok("job_write", fmt.Sprintf(`{"id":%d,"data":"Ynll","encoding":"base64","eof":true}`, id)); !strings.Contains(mcp.Encode(w), `"eof":true`) {
			t.Errorf("%s: %s", m, mcp.Encode(w))
		}
		v, out, _ := c.readToExit(id)
		if out != "bye" || str(v, "status") != "" || !strings.Contains(mcp.Encode(v), `"ok":true`) {
			t.Errorf("%s: %q %s", m, out, mcp.Encode(v))
		}
		if v := c.ok("job_read", fmt.Sprintf(`{"id":%d,"offset":0}`, id)); str(v, "stdout") != "hello\nbye" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if e := c.fails("job_write", fmt.Sprintf(`{"id":%d,"data":"x"}`, id)); e != fmt.Sprintf("job %d has exited", id) {
			t.Errorf("%s: %q", m, e)
		}
		if e := c.fails("job_read", `{"id":999}`); e != "job 999: no such job" {
			t.Errorf("%s: %q", m, e)
		}
		// An sh command line, with stderr and a failing status.
		j := c.ok("job_start", `{"command":"echo out; echo err >[1=2]; cat /nonexistent","stdin":"unused"}`)
		v, out, errs := c.readToExit(num(j, "id"))
		if out != "out\n" || errs != "err\ncat: cannot open /nonexistent: file does not exist\n" ||
			str(v, "status") == "" || !strings.Contains(mcp.Encode(v), `"ok":false`) {
			t.Errorf("%s: %q %q %s", m, out, errs, mcp.Encode(v))
		}
		// Input given at the start, then end of file.
		j = c.ok("job_start", `{"argv":["wc","-c"],"stdin":"abc"}`)
		c.ok("job_write", fmt.Sprintf(`{"id":%d,"eof":true}`, num(j, "id")))
		if _, out, _ := c.readToExit(num(j, "id")); strings.TrimSpace(out) != "3" {
			t.Errorf("%s: %q", m, out)
		}
		// A program that cannot be found ends the job at once.
		j = c.ok("job_start", `{"argv":["nosuch"]}`)
		v, _, errs = c.readToExit(num(j, "id"))
		if !strings.HasPrefix(str(v, "status"), "fail:nosuch: ") || !strings.Contains(errs, "file does not exist") {
			t.Errorf("%s: %q %s", m, errs, mcp.Encode(v))
		}
		// A killed job reports it, and everything it started is gone.
		j = c.ok("job_start", `{"command":"sleep 300 & sleep 300"}`)
		kid := num(j, "id")
		start := time.Now()
		for c.run("ps | grep sleep | wc -l").stdout != "      2\n" {
			if time.Since(start) > 30*time.Second {
				t.Fatalf("%s: the sleeps did not start", m)
			}
		}
		if k := c.ok("job_kill", fmt.Sprintf(`{"id":%d}`, kid)); mcp.Encode(k) != `{"killed":true}` {
			t.Errorf("%s: %s", m, mcp.Encode(k))
		}
		if v, _, _ := c.readToExit(kid); str(v, "status") != "killed" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if k := c.ok("job_kill", fmt.Sprintf(`{"id":%d}`, kid)); mcp.Encode(k) != `{"killed":false}` {
			t.Errorf("%s: %s", m, mcp.Encode(k))
		}
		if r := c.run("ps | grep sleep | wc -l"); r.stdout != "      0\n" {
			t.Errorf("%s: %+v", m, r)
		}
		// A job past its timeout is killed and says so.
		j = c.ok("job_start", `{"argv":["sleep","100"],"timeout_ms":200}`)
		if v, _, _ := c.readToExit(num(j, "id")); str(v, "note") != "timed out: killed at its timeout_ms" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// Output past the bound: the oldest is dropped and counted, and
		// the job is not held up.
		j = c.ok("job_start", `{"command":"cat /bin/sh.wasm /bin/sh.wasm"}`)
		bid := num(j, "id")
		for {
			v := c.ok("job_read", fmt.Sprintf(`{"id":%d,"wait_ms":20000,"max_bytes":0}`, bid))
			if running, _ := mcp.Bool(v, "running"); !running {
				break
			}
		}
		var total int64
		l := c.ok("job_list", "{}")
		jl, _ := mcp.Get(l, "jobs")
		for _, x := range jl.([]mcp.Value) {
			if num(x, "id") == bid {
				total = num(x, "stdout_bytes")
			}
		}
		if total < 2<<20 {
			t.Errorf("%s: %d bytes", m, total)
		}
		v = c.ok("job_read", fmt.Sprintf(`{"id":%d,"offset":0,"max_bytes":10}`, bid))
		if num(v, "lost") != total-(1<<20) || num(v, "offset") != total-(1<<20) || num(v, "next_offset") > num(v, "offset")+10 {
			t.Errorf("%s: %d %s", m, total, mcp.Encode(v))
		}
		// Jobs end with reset, and with the session.
		c.ok("job_start", `{"command":"sleep 300"}`)
		start = time.Now()
		for c.run("ps | grep sleep | wc -l").stdout != "      1\n" {
			if time.Since(start) > 30*time.Second {
				t.Fatalf("%s: the sleep did not start", m)
			}
		}
		c.ok("reset", "{}")
		if r := c.run("ps | grep sleep | wc -l"); r.stdout != "      0\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if l := c.ok("job_list", "{}"); mcp.Encode(l) != `{"jobs":[]}` {
			t.Errorf("%s: %s", m, mcp.Encode(l))
		}
		c.ok("job_start", `{"argv":["cat"]}`)
		c.finish()
	}
}

func TestMcpNamespaceTools(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		ns := str(c.ok("ns", "{}"), "ns")
		if ns != c.run("ns").stdout || !strings.Contains(ns, " /tmp\n") {
			t.Errorf("%s: %q", m, ns)
		}
		c.run("mkdir /tmp/u /tmp/v; echo u > /tmp/u/f; echo v > /tmp/v/g")
		c.ok("bind", `{"from":"/tmp/u","to":"/n"}`)
		c.ok("bind", `{"from":"/tmp/v","to":"/n","order":"after","create":true}`)
		if r := c.run("ls /n"); r.stdout != "/n/f\n/n/g\n" {
			t.Errorf("%s: %+v", m, r)
		}
		if ns := str(c.ok("ns", "{}"), "ns"); !strings.Contains(ns, " -ac /tmp/v /n") {
			t.Errorf("%s: %q", m, ns)
		}
		c.ok("unmount", `{"from":"/tmp/u","to":"/n"}`)
		if r := c.run("ls /n"); r.stdout != "/n/g\n" {
			t.Errorf("%s: %+v", m, r)
		}
		c.ok("unmount", `{"to":"/n"}`)
		if r := c.run("ls /n"); r.stdout != "" {
			t.Errorf("%s: %+v", m, r)
		}
		if e := c.fails("unmount", `{"to":"/n"}`); !strings.HasPrefix(e, "unmount: /n: ") {
			t.Errorf("%s: %q", m, e)
		}
		if e := c.fails("bind", `{"from":"/nonexistent","to":"/n"}`); e != "bind: cannot bind /nonexistent on /n: file does not exist" {
			t.Errorf("%s: %q", m, e)
		}
		// NODEVS holds for the tools too.
		for _, x := range [][2]string{{"#C", "/cmd"}, {"#C", "/n"}, {"#U", "/n"}, {"#I", "/net"}} {
			if e := c.fails("bind", `{"from":"`+x[0]+`","to":"`+x[1]+`"}`); !strings.Contains(e, "mount/attach disallowed") {
				t.Errorf("%s: %q", m, e)
			}
		}
		if e := c.fails("mount", `{"source":"tcp!127.0.0.1!564","to":"/n"}`); !strings.HasPrefix(e, "mount: ") {
			t.Errorf("%s: %q", m, e)
		}
		// A file server started by the mount.
		c.ok("mount", `{"source":"{memfs -s}","to":"/mnt","create":true,"authenticate":false}`)
		c.ok("write_file", `{"path":"/mnt/x","content":"in memfs"}`)
		if r := c.run("cat /mnt/x"); r.stdout != "in memfs" {
			t.Errorf("%s: %+v", m, r)
		}
		c.ok("reset", "{}")
		if r := c.run("ls /mnt /n"); r.stdout != "" {
			t.Errorf("%s: %+v", m, r)
		}
		c.finish()
	}
}

func TestMcpResources(t *testing.T) {
	for _, m := range testModes() {
		c := startMcp(t, m)
		c.ok("write_file", `{"path":"a b.txt","content":"hello"}`)
		c.run("mkdir d")
		c.ok("write_file", `{"path":"d/c.bin","content":"AAEC/w==","encoding":"base64"}`)
		r := c.request("resources/list", "{}")
		res, _ := mcp.Get(r, "resources")
		if mcp.Encode(res) != `[{"uri":"purg:///tmp/a%20b.txt","name":"/tmp/a b.txt","size":5},{"uri":"purg:///tmp/d/c.bin","name":"/tmp/d/c.bin","size":4}]` {
			t.Errorf("%s: %s", m, mcp.Encode(res))
		}
		read := func(uri string) mcp.Value {
			r := c.request("resources/read", `{"uri":"`+uri+`"}`)
			cs, _ := mcp.Get(r, "contents")
			l, _ := cs.([]mcp.Value)
			if len(l) != 1 {
				t.Fatalf("%s: %s", m, mcp.Encode(r))
			}
			return l[0]
		}
		if v := read("purg:///tmp/a%20b.txt"); mcp.Encode(v) != `{"uri":"purg:///tmp/a%20b.txt","mimeType":"text/plain","text":"hello"}` {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if v := read("purg:///tmp/d/c.bin"); str(v, "blob") != "AAEC/w==" || str(v, "mimeType") != "application/octet-stream" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if v := read("purg:///tmp"); str(v, "text") != "a b.txt\nd/\n" || str(v, "mimeType") != "inode/directory" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if v := read("purg:///lib/hello.txt"); str(v, "text") != "hello from the root\n" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if v := read("purg:///dev/sysname"); str(v, "text") == "" {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if tl := c.request("resources/templates/list", "{}"); !strings.Contains(mcp.Encode(tl), "purg:///{+path}") {
			t.Errorf("%s: %s", m, mcp.Encode(tl))
		}
		if v := c.exchange("resources/read", `{"uri":"purg:///tmp/nope"}`); errorCode(v) != -32002 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		if v := c.exchange("resources/read", `{"uri":"/tmp/a"}`); errorCode(v) != -32602 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// A URI names a file from the root, never a device.
		if v := c.exchange("resources/read", `{"uri":"purg:///%23U/tmp"}`); errorCode(v) != -32002 {
			t.Errorf("%s: %s", m, mcp.Encode(v))
		}
		// Too big for a resource.
		c.run("cat /bin/sh.wasm > big")
		if v := c.exchange("resources/read", `{"uri":"purg:///tmp/big"}`); errorCode(v) != -32603 || !strings.Contains(mcp.Encode(v), "read_file") {
			t.Errorf("%s: %.300s", m, mcp.Encode(v))
		}
		c.finish()
	}
}

func TestMcpAuditLog(t *testing.T) {
	for _, m := range testModes() {
		lp, lname := hostTemp(t, "log")
		c := startMcp(t, m, "-l", "/tmp/"+lname)
		c.run("echo hi")
		c.fails("read_file", `{"path":"/tmp/nope"}`)
		c.fails("run", `{"command":"x","timeout_ms":0}`)
		c.ok("write_file", `{"path":"f","content":"`+strings.Repeat("y", 5000)+`"}`)
		c.request("resources/read", `{"uri":"purg:///tmp/f"}`)
		// The session cannot reach the log: not by name, nor as its fd 2.
		c.ok("write_file", `{"path":"/fd/2","content":"forged\n"}`)
		if e := c.fails("read_file", `{"path":"#U/tmp/`+lname+`"}`); !strings.Contains(e, "mount/attach disallowed") {
			t.Errorf("%s: %q", m, e)
		}
		c.ok("reset", "{}")
		c.finish()
		b, err := os.ReadFile(lp)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		var shape []string
		for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
			if strings.HasPrefix(l, "forged") {
				t.Errorf("%s: the session wrote the log", m)
			}
			secs, what, _ := strings.Cut(l, " ")
			if n, err := strconv.ParseInt(secs, 10, 64); err != nil || n < 1_600_000_000 {
				t.Errorf("%s: %q", m, l)
			}
			f := strings.SplitN(what, " ", 5)
			switch {
			case len(f) == 3 && f[0] == "session" && f[1] == "start":
				shape = append(shape, "session start")
			case len(f) == 5:
				if !strings.HasSuffix(f[3], "ms") || !strings.HasPrefix(f[4], "{") {
					t.Errorf("%s: %q", m, l)
				}
				shape = append(shape, strings.Join(f[:3], " "))
			default:
				shape = append(shape, what)
			}
			if len(l) >= 600 {
				t.Errorf("%s: a long line: %d", m, len(l))
			}
		}
		want := []string{"session start", "tool run ok", "tool read_file error", "tool run invalid",
			"tool write_file ok", "resource resources/read ok", "tool write_file ok", "tool read_file error",
			"session reset", "session start", "tool reset ok", "session end"}
		if strings.Join(shape, "|") != strings.Join(want, "|") {
			t.Errorf("%s:\n%s", m, text)
		}
		if !strings.Contains(text, `{"command":"echo hi"}`) || !strings.Contains(text, "...(5000 bytes)") {
			t.Errorf("%s:\n%s", m, text)
		}
	}
}

// TestMcpRemote is stage 3: the stdio server run on another system with
// rcmd, here over loopback in one emu. rcmd carries the console: the
// remote mcpd reads and writes this emu's /dev/cons through the mount
// of /n/client. Go's rcmd has no keyring authentication yet, so this is
// rcmd -A against listen -A.
func TestMcpRemote(t *testing.T) {
	// The session runs authenticated with the test keys (pgauth's hybrid
	// protocol, encrypted after the proof), and again with -A, no proof.
	starts := map[string]string{
		"auth": "bind /lib/test/keys /lib/keys; listen 'tcp!127.0.0.1!0' auxi/rstyxd >[2] /dev/null; " +
			"port=`{cat /net/tcp/*/local}; rcmd -S tcp!$port mcpd",
		"noauth": "listen -A 'tcp!127.0.0.1!0' auxi/rstyxd >[2] /dev/null; " +
			"port=`{cat /net/tcp/*/local}; rcmd -A tcp!$port mcpd",
	}
	for _, m := range testModes() {
		for _, kind := range []string{"auth", "noauth"} {
			mcpRemote(t, fmt.Sprintf("%s/%s", m, kind), launchMcp(t, m, "sh", "-c", starts[kind]))
		}
	}
}

func mcpRemote(t *testing.T, m string, c *mcpClient) {
	r := c.request("initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"far","version":"0"}}`)
	if str(r, "protocolVersion") != "2025-11-25" {
		t.Errorf("%s: %s", m, mcp.Encode(r))
	}
	if r := c.run("echo remote; ls /n"); r.stdout != "remote\n" || r.status != "" {
		t.Errorf("%s: %+v", m, r)
	}
	// The caller's namespace on /n/client is not in the session.
	if e := c.fails("stat", `{"path":"/n/client/lib/hello.txt"}`); e != "/n/client/lib/hello.txt: file does not exist" {
		t.Errorf("%s: %q", m, e)
	}
	c.ok("write_file", `{"path":"f","content":"far"}`)
	if v := c.ok("read_file", `{"path":"f"}`); str(v, "content") != "far" {
		t.Errorf("%s: %s", m, mcp.Encode(v))
	}
	c.in.Close()
	select {
	case <-c.done:
		c.done <- ""
	case <-time.After(mcpReply):
		t.Fatalf("%s: rcmd did not end with its input", m)
	}
}

// TestMcpScripts runs each script in root/lib/test/mcp, mcpd over a pipe
// inside one emu, and compares its output with the .out file beside it
// (go test -shupdate rewrites them). They are not in the sh corpus,
// which the kernel runs too: mcpd needs commands and memory its initrd
// does not have.
func TestMcpScripts(t *testing.T) {
	scripts, err := filepath.Glob(filepath.Join(root, "lib", "test", "mcp", "*.sh"))
	if err != nil || len(scripts) == 0 {
		t.Fatal("no mcp scripts", err)
	}
	for _, s := range scripts {
		name := strings.TrimSuffix(filepath.Base(s), ".sh")
		t.Run(name, func(t *testing.T) {
			shtmp(t)
			out, _ := emu(t, "", "sh", "/lib/test/mcp/"+name+".sh")
			golden := strings.TrimSuffix(s, ".sh") + ".out"
			if *shupdate {
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("%s: output differs\ngot:\n%s\nwant:\n%s", name, out, want)
			}
		})
	}
}
