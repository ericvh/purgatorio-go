package main

import (
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

func newText(s string) *text {
	t := &text{}
	t.output(s)
	return t
}

func typeAll(t *text, s string) {
	for _, c := range s {
		t.typed(c)
	}
}

func read(t *testing.T, x *text, n int) string {
	t.Helper()
	b, ok := x.read(n)
	if !ok {
		t.Fatalf("read %d would wait", n)
	}
	return string(b)
}

func TestOutputGoesBeforeTyping(t *testing.T) {
	x := newText("; ")
	typeAll(x, "echo hi\n")
	x.output("x")
	if x.all() != "; xecho hi\n" {
		t.Errorf("text %q", x.all())
	}
	if s := read(t, x, 100); s != "echo hi\n" {
		t.Errorf("read %q", s)
	}
	x.output("hi\n; ")
	if x.all() != "; xecho hi\nhi\n; " {
		t.Errorf("text %q", x.all())
	}
	if _, ok := x.read(100); ok {
		t.Error("read did not wait")
	}
}

func TestReadsALine(t *testing.T) {
	x := newText("; ")
	typeAll(x, "one\ntwo\n")
	if s := read(t, x, 100); s != "one\n" {
		t.Errorf("read %q", s)
	}
	if s := read(t, x, 100); s != "two\n" {
		t.Errorf("read %q", s)
	}
}

func TestErasingAndEndOfFile(t *testing.T) {
	x := newText("; ")
	typeAll(x, "ab\bc\n")
	x.typed('\b') // the line is sent: backspace cannot reach it
	if x.all() != "; ac\n" {
		t.Errorf("text %q", x.all())
	}
	typeAll(x, "one two\x17\x17x\x15y")
	if x.all() != "; ac\ny" {
		t.Errorf("text %q", x.all())
	}
	if s := read(t, x, 1); s != "a" {
		t.Errorf("read %q", s)
	}
	if s := read(t, x, 100); s != "c\n" {
		t.Errorf("read %q", s)
	}
	x.typed(4)
	if s := read(t, x, 100); s != "y" {
		t.Errorf("read %q", s)
	}
	x.typed(4)
	if s := read(t, x, 100); s != "" {
		t.Errorf("read %q, want end of file", s)
	}
	if _, ok := x.read(100); ok {
		t.Error("read did not wait")
	}
}

func TestTypingAtTheSelection(t *testing.T) {
	x := newText("; ")
	typeAll(x, "echo hi")
	x.sel(7, 9)
	if x.selected() != "hi" {
		t.Errorf("selected %q", x.selected())
	}
	x.typed('x')
	x.output("out\n")
	if x.all() != "; out\necho x" || x.q0 != 12 || x.q1 != 12 {
		t.Errorf("text %q, selection %d %d", x.all(), x.q0, x.q1)
	}
	// A selection in the output: typing goes at the end.
	x.sel(2, 5)
	typeAll(x, "!\n")
	if s := read(t, x, 100); s != "echo x!\n" {
		t.Errorf("read %q", s)
	}
}

func TestPasteAndSend(t *testing.T) {
	x := newText("; hello\n; ")
	x.send("echo hi")
	if s := read(t, x, 100); s != "echo hi\n" {
		t.Errorf("send read %q", s)
	}
	x.paste("ls\nx")
	if x.selected() != "ls\nx" {
		t.Errorf("selected %q", x.selected())
	}
	if s := read(t, x, 100); s != "ls\n" {
		t.Errorf("paste read %q", s)
	}
	x.flushInput()
	if x.all() != "; hello\n; echo hi\nls\n" || x.readable() {
		t.Errorf("after flush %q", x.all())
	}
}

func TestDoubleClick(t *testing.T) {
	x := newText("; echo hello_there\nnext\n")
	for _, c := range []struct{ q, a, b int }{{10, 7, 18}, {0, 0, 19}, {18, 0, 19}, {19, 19, 24}} {
		if a, b := x.double(c.q); a != c.a || b != c.b {
			t.Errorf("double(%d) = %d %d, want %d %d", c.q, a, b, c.a, c.b)
		}
	}
}

func TestLayout(t *testing.T) {
	f := &widths{known: map[rune]int{}, tab: 32}
	x := newText("abcdefghij\nxy\n")
	// Eight pixels a rune: 40 pixels wrap the first line in two.
	lines := x.layout(0, 10, 40, f)
	want := []span{{0, 5}, {5, 11}, {11, 14}, {14, 14}}
	if len(lines) != len(want) {
		t.Fatalf("lines %v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d %v, want %v", i, lines[i], want[i])
		}
	}
	if org := x.orgForEnd(2, 40, f); org != 11 {
		t.Errorf("orgForEnd %d", org)
	}
	if q := x.atX(lines[1], 17, f); q != 7 {
		t.Errorf("atX %d", q)
	}
}

func TestWctl(t *testing.T) {
	cur := draw.Rect(100, 100, 300, 200)
	c, e := parseWctl("new -r 10 20 300 400 echo  'a  b'")
	if e != "" || c.verb != "new" || c.r == nil || c.rect(cur) != draw.Rect(10, 20, 300, 400) || c.cmd != "echo  'a  b'" {
		t.Errorf("new: %+v %q", c, e)
	}
	c, e = parseWctl("new -hide -noscroll -cd /tmp -pid 7 -dx 50")
	if e != "" || !c.hide || c.scroll == nil || *c.scroll || c.dir != "/tmp" || c.cmd != "" ||
		c.rect(cur) != draw.Rect(100, 100, 150, 200) {
		t.Errorf("new options: %+v %q", c, e)
	}
	c, e = parseWctl("move -minx 5 -miny +6")
	if e != "" || c.rect(cur) != draw.Rect(5, 106, 205, 206) {
		t.Errorf("move: %+v %q", c, e)
	}
	c, e = parseWctl("resize -dx 10 -dy -20")
	if e != "" || c.rect(cur) != draw.Rect(100, 100, 110, 180) {
		t.Errorf("resize: %+v %q", c, e)
	}
	c, e = parseWctl("resize -r -10 0 +10 400")
	if e != "" || c.rect(cur) != draw.Rect(90, 0, 310, 400) {
		t.Errorf("resize -r: %+v %q", c, e)
	}
	for s, want := range map[string]string{"resize -r 1 2": ebadrect, "top now": ebadwctl, "": ebadwctl,
		"resize -hide": ebadwctl, "new -cd": ebadwctl} {
		if _, e := parseWctl(s); e != want {
			t.Errorf("%q: %q, want %q", s, e, want)
		}
	}
}

func TestScale(t *testing.T) {
	from, to := draw.Rect(0, 0, 800, 600), draw.Rect(0, 0, 400, 300)
	for _, c := range []struct{ r, want draw.Rectangle }{
		{draw.Rect(100, 100, 500, 400), draw.Rect(50, 50, 250, 200)},
		// Never smaller than minsize, and on the screen.
		{draw.Rect(780, 580, 800, 600), draw.Rect(350, 250, 400, 300)},
	} {
		if got := scale(c.r, from, to); got != c.want {
			t.Errorf("scale %v = %v, want %v", c.r, got, c.want)
		}
	}
	if got := scale(draw.Rect(60, 60, 460, 300), draw.Rect(0, 0, 640, 480), draw.Rect(0, 0, 800, 600)); got != draw.Rect(75, 75, 575, 375) {
		t.Errorf("scale up %v", got)
	}
}

// TestHold checks hold mode: newlines typed do not make typing
// readable until it ends, and lines typed can be erased till then.
func TestHold(t *testing.T) {
	x := newText("; ")
	x.setHold(true)
	typeAll(x, "echo a\nech\b\bcho b\nfoo")
	if _, ok := x.read(100); ok {
		t.Fatal("read while holding")
	}
	typeAll(x, "\x15\x15")
	x.paste("x\n")
	x.send("y")
	if _, ok := x.read(100); ok {
		t.Fatal("read after paste and send while holding")
	}
	x.setHold(false)
	for _, want := range []string{"echo a\n", "echo b\n", "x\n", "y\n"} {
		if s := read(t, x, 100); s != want {
			t.Errorf("read %q, want %q", s, want)
		}
	}
	if _, ok := x.read(100); ok {
		t.Error("read past the last newline")
	}
}

func TestTakeRunes(t *testing.T) {

	q := []byte("aé")
	if b := takeRunes(&q, 2); string(b) != "a" {
		t.Errorf("first %q", b)
	}
	if b := takeRunes(&q, 2); string(b) != "é" {
		t.Errorf("second %q", b)
	}
}
