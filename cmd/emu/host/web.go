package host

// The emu's screen in a web browser (emu -g WxH): a platform.Screen
// served over HTTP and a WebSocket to a page with a <canvas>.
//
// The emu listens on one TCP address (loopback unless the user names
// another) and prints a URL with a random token. GET /?t=<token> returns
// the page (one HTML file with its script inline), GET /screen.png?t=
// the screen as it is, and GET /ws?t=<token> upgrades to a WebSocket. A
// request without the token gets 403 Forbidden, as does a WebSocket
// request whose Origin is not the page's own, so other local users and
// other web pages cannot watch the screen or type into it.
//
// Server to page: a text message {"width":W,"height":H,"input":B} when
// the page connects, and {"input":B} when it gains or loses the input.
// Each damaged rectangle is a binary message: the byte 1, then x, y,
// width and height as little-endian uint32, then the rectangle's pixels
// as RGBA rows compressed with zlib (RFC 1950). A new page first gets
// the whole screen.
//
// Page to server: binary messages. 'm' with x, y (int32) and the
// buttons (uint32, pointer(3)'s bits) is a pointer event; 'k' with a
// byte of modifiers (bit 0: control) and the DOM's KeyboardEvent.key is
// a key, which becomes a character or a keyboard(6) code.
//
// Any number of pages may watch; only the one connected longest sends
// input, and the others' pointer and key messages are dropped. A flush
// only copies the pixels and adds the rectangle to each page's pending
// damage, so a slow page never holds up a prog: its writer sends the
// union of what was damaged since it last sent, read from the screen as
// it is then.
//
// The WebSocket (RFC 6455) is the little of it this needs: one upgrade,
// unfragmented frames out, fragments assembled on the way in.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/sha1"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/platform"
)

//go:embed web.html
var webPage string

// DefaultWebAddr is where the screen is served when no address is given:
// loopback, any port.
const DefaultWebAddr = "127.0.0.1:0"

const (
	wsGUID     = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxMessage = 4096 // the most a page's message may be
)

// WebScreen is a screen shown in web browsers.
type WebScreen struct {
	w, h  int
	token string
	url   string
	ln    net.Listener
	srv   *http.Server

	mu      sync.Mutex
	pix     []byte // x8r8g8b8, 4*w bytes a row
	clients []*webClient
	last    platform.PointerEvent

	ptr chan platform.PointerEvent
	kbd chan rune
}

var (
	_ platform.Screen        = (*WebScreen)(nil)
	_ platform.Pointerer     = (*WebScreen)(nil)
	_ platform.Keyboarder    = (*WebScreen)(nil)
	_ platform.PointerSetter = (*WebScreen)(nil)
)

// webClient is one connected page.
type webClient struct {
	s      *WebScreen
	conn   net.Conn
	mu     sync.Mutex
	cond   *sync.Cond
	damage image.Rectangle
	texts  []string
	pong   []byte
	closed bool
}

// NewWebScreen listens on addr and serves a w×h screen. The URL to open
// is s.URL().
func NewWebScreen(addr string, w, h int) (*WebScreen, error) {
	if addr == "" {
		addr = DefaultWebAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	var tok [16]byte
	_, _ = rand.Read(tok[:])
	s := &WebScreen{w: w, h: h, token: hex.EncodeToString(tok[:]), ln: ln,
		pix: make([]byte, 4*w*h), ptr: make(chan platform.PointerEvent, 256), kbd: make(chan rune, 256)}
	for i := range s.pix {
		s.pix[i] = 0xFF
	}
	host := ln.Addr().String()
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && ta.IP.IsUnspecified() {
		host = net.JoinHostPort("127.0.0.1", fmt.Sprint(ta.Port))
	}
	s.url = "http://" + host + "/?t=" + s.token
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// URL is the address of the page, with its token.
func (s *WebScreen) URL() string { return s.url }

// Close stops serving and disconnects the pages.
func (s *WebScreen) Close() error {
	s.mu.Lock()
	cs := append([]*webClient(nil), s.clients...)
	s.mu.Unlock()
	for _, c := range cs {
		c.close()
	}
	return s.srv.Close()
}

func (s *WebScreen) Info() (int, int, string) { return s.w, s.h, "x8r8g8b8" }

// Flush copies the rectangle and tells the pages it changed.
func (s *WebScreen) Flush(x0, y0, x1, y1 int, pix []byte, stride int) {
	r := image.Rect(x0, y0, x1, y1).Intersect(image.Rect(0, 0, s.w, s.h))
	if r.Empty() || stride < 4*s.w {
		return
	}
	s.mu.Lock()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := y * stride
		copy(s.pix[4*(y*s.w+r.Min.X):4*(y*s.w+r.Max.X)], pix[o+4*r.Min.X:o+4*r.Max.X])
	}
	cs := append([]*webClient(nil), s.clients...)
	s.mu.Unlock()
	for _, c := range cs {
		c.mu.Lock()
		c.damage = c.damage.Union(r)
		c.mu.Unlock()
		c.cond.Signal()
	}
}

func (s *WebScreen) Pointer() <-chan platform.PointerEvent { return s.ptr }
func (s *WebScreen) Keyboard() <-chan rune                 { return s.kbd }

// SetPointer moves the pointer, which reports the move as an event. The
// browser's own cursor does not move.
func (s *WebScreen) SetPointer(x, y int) {
	s.mu.Lock()
	b := s.last.Buttons
	s.mu.Unlock()
	s.track(platform.PointerEvent{X: x, Y: y, Buttons: b})
}

func (s *WebScreen) SetCursor(hotx, hoty, dx, dy int, clr, set []byte) {}

func (s *WebScreen) track(e platform.PointerEvent) {
	s.mu.Lock()
	s.last = e
	s.mu.Unlock()
	select {
	case s.ptr <- e:
	default:
	}
}

func (s *WebScreen) key(r rune) {
	select {
	case s.kbd <- r:
	default:
	}
}

// rgba returns r of the screen as RGBA rows.
func (s *WebScreen) rgba(r image.Rectangle) []byte {
	out := make([]byte, 0, 4*r.Dx()*r.Dy())
	s.mu.Lock()
	defer s.mu.Unlock()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := s.pix[4*(y*s.w+r.Min.X) : 4*(y*s.w+r.Max.X)]
		for x := 0; x < len(row); x += 4 {
			out = append(out, row[x+2], row[x+1], row[x], 0xFF)
		}
	}
	return out
}

// Snapshot returns the screen as an image.
func (s *WebScreen) Snapshot() *image.RGBA {
	r := image.Rect(0, 0, s.w, s.h)
	return &image.RGBA{Pix: s.rgba(r), Stride: 4 * s.w, Rect: r}
}

func (s *WebScreen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("t") != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, webPage)
	case "/screen.png":
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_ = png.Encode(w, s.Snapshot())
	case "/ws":
		s.serveWS(w, r)
	default:
		http.NotFound(w, r)
	}
}

// sameOrigin reports whether a WebSocket request comes from our page.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // not a browser
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, f := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), token) {
				return true
			}
		}
	}
	return false
}

func (s *WebScreen) serveWS(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Method != http.MethodGet || key == "" || !headerHas(r.Header, "Connection", "upgrade") ||
		!headerHas(r.Header, "Upgrade", "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot upgrade", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	if rw.Flush() != nil {
		conn.Close()
		return
	}
	c := &webClient{s: s, conn: conn, damage: image.Rect(0, 0, s.w, s.h)}
	c.cond = sync.NewCond(&c.mu)
	s.mu.Lock()
	first := len(s.clients) == 0
	s.clients = append(s.clients, c)
	s.mu.Unlock()
	c.text(fmt.Sprintf(`{"width":%d,"height":%d,"input":%v}`, s.w, s.h, first))
	go c.writer()
	c.reader(rw.Reader)
	c.close()
	s.mu.Lock()
	for i, x := range s.clients {
		if x == c {
			s.clients = append(s.clients[:i], s.clients[i+1:]...)
			if i == 0 && len(s.clients) > 0 {
				s.clients[0].text(`{"input":true}`)
			}
			break
		}
	}
	s.mu.Unlock()
}

func (c *webClient) text(m string) {
	c.mu.Lock()
	c.texts = append(c.texts, m)
	c.mu.Unlock()
	c.cond.Signal()
}

func (c *webClient) close() {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		_ = c.conn.SetReadDeadline(time.Now())
	}
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *webClient) hasInput() bool {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return len(c.s.clients) > 0 && c.s.clients[0] == c
}

// writer sends the page its texts and damage until the page goes.
func (c *webClient) writer() {
	defer c.conn.Close()
	for {
		c.mu.Lock()
		for !c.closed && len(c.texts) == 0 && c.damage.Empty() && c.pong == nil {
			c.cond.Wait()
		}
		if c.closed {
			c.mu.Unlock()
			_ = writeFrame(c.conn, 0x8, nil)
			return
		}
		texts, pong, dmg := c.texts, c.pong, c.damage
		c.texts, c.pong, c.damage = nil, nil, image.Rectangle{}
		c.mu.Unlock()
		var err error
		for _, t := range texts {
			if err == nil {
				err = writeFrame(c.conn, 0x1, []byte(t))
			}
		}
		if pong != nil && err == nil {
			err = writeFrame(c.conn, 0xA, pong)
		}
		if !dmg.Empty() && err == nil {
			err = writeFrame(c.conn, 0x2, c.s.rectMessage(dmg))
		}
		if err != nil {
			c.close()
		}
	}
}

// rectMessage encodes a damaged rectangle for the page.
func (s *WebScreen) rectMessage(r image.Rectangle) []byte {
	var b bytes.Buffer
	b.WriteByte(1)
	for _, v := range []int{r.Min.X, r.Min.Y, r.Dx(), r.Dy()} {
		_ = binary.Write(&b, binary.LittleEndian, uint32(v))
	}
	z, _ := zlib.NewWriterLevel(&b, zlib.BestSpeed)
	z.Write(s.rgba(r))
	z.Close()
	return b.Bytes()
}

// writeFrame writes one unmasked, unfragmented frame.
func writeFrame(w io.Writer, op byte, data []byte) error {
	h := []byte{0x80 | op}
	switch n := len(data); {
	case n < 126:
		h = append(h, byte(n))
	case n < 1<<16:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	if _, err := w.Write(append(h, data...)); err != nil {
		return err
	}
	return nil
}

var errFrame = errors.New("bad websocket frame")

// readMessage reads a whole message, answering pings on the way, and
// returns its opcode and data.
func (c *webClient) readMessage(r *bufio.Reader) (byte, []byte, error) {
	var msg []byte
	var op byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return 0, nil, err
		}
		fin, fop := h[0]&0x80 != 0, h[0]&0x0F
		if h[1]&0x80 == 0 {
			return 0, nil, errFrame // pages must mask
		}
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > maxMessage || uint64(len(msg))+n > maxMessage {
			return 0, nil, errFrame
		}
		var mask [4]byte
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return 0, nil, err
		}
		for i := range data {
			data[i] ^= mask[i%4]
		}
		switch fop {
		case 0x8:
			return 0x8, data, nil
		case 0x9:
			c.mu.Lock()
			c.pong = data
			c.mu.Unlock()
			c.cond.Signal()
			continue
		case 0xA:
			continue
		case 0x0:
		default:
			op = fop
		}
		msg = append(msg, data...)
		if fin {
			return op, msg, nil
		}
	}
}

// reader takes the page's messages until it goes.
func (c *webClient) reader(r *bufio.Reader) {
	for {
		op, m, err := c.readMessage(r)
		if err != nil || op == 0x8 {
			return
		}
		if op != 0x2 || len(m) == 0 || !c.hasInput() {
			continue
		}
		switch m[0] {
		case 'm':
			if len(m) >= 13 {
				c.s.track(platform.PointerEvent{
					X:       int(int32(binary.LittleEndian.Uint32(m[1:]))),
					Y:       int(int32(binary.LittleEndian.Uint32(m[5:]))),
					Buttons: int(binary.LittleEndian.Uint32(m[9:])),
				})
			}
		case 'k':
			if len(m) >= 3 {
				if k, ok := keyRune(string(m[2:]), m[1]&1 != 0); ok {
					c.s.key(k)
				}
			}
		}
	}
}

// Keyboard codes (keyboard(6)).
const (
	kSpec = 0xE000
	kView = kSpec | 0x10
	kKF   = kSpec | 0x40
)

var keyNames = map[string]rune{
	"Enter": '\n', "Backspace": '\b', "Tab": '\t', "Escape": 0x1b, "Delete": 0x7f,
	"Home": kView | 0, "End": kView | 1, "ArrowUp": kView | 2, "ArrowDown": kView | 3,
	"ArrowLeft": kView | 4, "ArrowRight": kView | 5, "PageUp": kView | 6, "PageDown": kView | 7,
	"Insert": kSpec | 0x63,
}

// keyRune turns a DOM key name into the rune Inferno's keyboard gives.
func keyRune(name string, ctrl bool) (rune, bool) {
	if r, n := utf8.DecodeRuneInString(name); n == len(name) && r != utf8.RuneError {
		if ctrl {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
				return r & 0x1F, true
			case r >= '@' && r <= '_':
				return r - '@', true
			}
		}
		return r, true
	}
	if r, ok := keyNames[name]; ok {
		return r, true
	}
	var f int
	if n, _ := fmt.Sscanf(name, "F%d", &f); n == 1 && f >= 1 && f <= 12 {
		return rune(kKF | f), true
	}
	return 0, false
}
