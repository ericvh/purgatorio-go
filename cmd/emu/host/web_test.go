package host

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/platform"
)

func newWeb(t *testing.T) *WebScreen {
	t.Helper()
	s, err := NewWebScreen("127.0.0.1:0", 64, 32)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func get(t *testing.T, u string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestWebPage(t *testing.T) {
	s := newWeb(t)
	resp, body := get(t, s.URL())
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<canvas") {
		t.Fatalf("page: %d %q", resp.StatusCode, body)
	}
	base := strings.Split(s.URL(), "?")[0]
	for _, u := range []string{base, base + "?t=wrong", base + "screen.png", base + "ws"} {
		if resp, _ := get(t, u); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", u, resp.StatusCode)
		}
	}
	if resp, _ := get(t, base+"nothing?t="+s.token); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: %d", resp.StatusCode)
	}
	// A plain GET of the WebSocket path is not an upgrade.
	if resp, _ := get(t, base+"ws?t="+s.token); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("ws without upgrade: %d", resp.StatusCode)
	}
}

func TestWebScreenPNG(t *testing.T) {
	s := newWeb(t)
	// Paint a red pixel at 3,2 in x8r8g8b8.
	pix := make([]byte, 4*64*32)
	pix[4*(2*64+3)+2] = 0xFF
	s.Flush(0, 0, 64, 32, pix, 4*64)
	resp, body := get(t, strings.Replace(s.URL(), "/?", "/screen.png?", 1))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Fatalf("size %v", b)
	}
	if r, g, b, _ := img.At(3, 2).RGBA(); r>>8 != 0xFF || g != 0 || b != 0 {
		t.Errorf("pixel 3,2 = %x %x %x", r, g, b)
	}
	if r, _, _, _ := img.At(4, 2).RGBA(); r != 0 {
		t.Errorf("pixel 4,2 = %x", r)
	}
}

// wsConn is a minimal WebSocket client.
type wsConn struct {
	c net.Conn
	r *bufio.Reader
}

func dialWS(t *testing.T, s *WebScreen, origin string) (*wsConn, string) {
	t.Helper()
	host := strings.TrimPrefix(strings.Split(s.URL(), "/?")[0], "http://")
	c, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	var k [16]byte
	rand.Read(k[:])
	fmt.Fprintf(c, "GET /ws?t=%s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nOrigin: %s\r\n\r\n",
		s.token, host, base64.StdEncoding.EncodeToString(k[:]), origin)
	r := bufio.NewReader(c)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &wsConn{c, r}, resp.Status
}

func (w *wsConn) read(t *testing.T) (byte, []byte) {
	t.Helper()
	w.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(w.r, h[:]); err != nil {
		t.Fatal(err)
	}
	n := int(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(w.r, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(w.r, b[:])
		n = int(binary.BigEndian.Uint64(b[:]))
	}
	d := make([]byte, n)
	if _, err := io.ReadFull(w.r, d); err != nil {
		t.Fatal(err)
	}
	return h[0] & 0x0F, d
}

func (w *wsConn) send(t *testing.T, data []byte) {
	t.Helper()
	mask := []byte{1, 2, 3, 4}
	f := []byte{0x82, 0x80 | byte(len(data))}
	f = append(f, mask...)
	for i, b := range data {
		f = append(f, b^mask[i%4])
	}
	if _, err := w.c.Write(f); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocket(t *testing.T) {
	s := newWeb(t)
	host := strings.TrimPrefix(strings.Split(s.URL(), "/?")[0], "http://")
	if _, st := dialWS(t, s, "http://evil.example"); !strings.HasPrefix(st, "403") {
		t.Fatalf("foreign origin: %s", st)
	}
	w, st := dialWS(t, s, "http://"+host)
	if !strings.HasPrefix(st, "101") {
		t.Fatalf("upgrade: %s", st)
	}
	op, d := w.read(t)
	if op != 1 || string(d) != `{"width":64,"height":32,"input":true}` {
		t.Fatalf("hello: %d %q", op, d)
	}
	// The first rectangle is the whole screen, white.
	op, d = w.read(t)
	if op != 2 || d[0] != 1 {
		t.Fatalf("rect: %d % x", op, d[:min(len(d), 17)])
	}
	x, y, rw, rh := binary.LittleEndian.Uint32(d[1:]), binary.LittleEndian.Uint32(d[5:]),
		binary.LittleEndian.Uint32(d[9:]), binary.LittleEndian.Uint32(d[13:])
	if x != 0 || y != 0 || rw != 64 || rh != 32 {
		t.Fatalf("rect %d %d %d %d", x, y, rw, rh)
	}
	z, err := zlib.NewReader(bytes.NewReader(d[17:]))
	if err != nil {
		t.Fatal(err)
	}
	px, _ := io.ReadAll(z)
	if len(px) != 64*32*4 || px[0] != 0xFF || px[3] != 0xFF {
		t.Fatalf("pixels: %d bytes, % x", len(px), px[:4])
	}

	// A flush sends the damaged rectangle.
	pix := make([]byte, 4*64*32)
	s.Flush(10, 5, 12, 6, pix, 4*64)
	_, d = w.read(t)
	if binary.LittleEndian.Uint32(d[1:]) != 10 || binary.LittleEndian.Uint32(d[9:]) != 2 {
		t.Errorf("damage % x", d[:17])
	}

	// Pointer and key messages come out as events.
	m := []byte{'m'}
	m = binary.LittleEndian.AppendUint32(m, 7)
	m = binary.LittleEndian.AppendUint32(m, 9)
	m = binary.LittleEndian.AppendUint32(m, 1)
	w.send(t, m)
	select {
	case e := <-s.Pointer():
		if e != (platform.PointerEvent{X: 7, Y: 9, Buttons: 1}) {
			t.Errorf("pointer %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no pointer event")
	}
	for _, k := range []struct {
		mod  byte
		name string
		want rune
	}{{0, "a", 'a'}, {1, "c", 3}, {0, "Enter", '\n'}, {0, "ArrowUp", 0xE012}, {0, "F2", 0xE042}, {0, "é", 'é'}} {
		w.send(t, append([]byte{'k', k.mod}, k.name...))
		select {
		case r := <-s.Keyboard():
			if r != k.want {
				t.Errorf("key %q: %#x, want %#x", k.name, r, k.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no key for %q", k.name)
		}
	}

	// A second page watches without input until the first goes.
	w2, _ := dialWS(t, s, "")
	if _, d := w2.read(t); string(d) != `{"width":64,"height":32,"input":false}` {
		t.Errorf("second hello %q", d)
	}
	w2.read(t) // the screen
	w.c.Close()
	if _, d := w2.read(t); string(d) != `{"input":true}` {
		t.Errorf("handover %q", d)
	}
}
