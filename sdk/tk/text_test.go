package tk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

func TestTextTags(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "text .t -width 200 -height 100 -bg white; pack .t")
	mustCmd(t, top, ".t insert end {alpha beta gamma\nsecond line\nthird}")
	mustCmd(t, top, ".t tag add kw 1.0 1.5 1.11 1.16; .t tag configure kw -fg blue")
	mustCmd(t, top, ".t tag add hi 2.0 2.6; .t tag configure hi -bg red -underline 1")
	for _, c := range []struct{ cmd, want string }{
		{".t tag ranges kw", "1.0 1.5 1.11 1.16"},
		{".t tag nextrange kw 1.3", "1.11 1.16"},
		{".t tag nextrange kw 1.0", "1.0 1.5"},
		{".t tag nextrange kw 1.12", ""},
		{".t tag prevrange kw end", "1.11 1.16"},
		{".t tag prevrange kw 1.11", "1.0 1.5"},
		{".t tag names", "hi kw sel"},
		{".t tag names 2.1", "hi"},
		{".t tag cget kw -fg", "#0000cc"},
		{".t tag cget hi -underline", "1"},
		{".t tag cget kw -bg", ""},
		{".t tag cget sel -bg", "#404040"},
		{".t tag configure 9x -fg red", "!bad/no matching tag"},
		{".t tag configure kw -wobble 1", "!bad option -wobble"},
		{".t tag configure kw -fg", "!arg requires option -fg"},
	} {
		if got := top.Cmd(c.cmd); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
	mustCmd(t, top, ".t tag raise kw; update")
	if v := mustCmd(t, top, ".t tag names"); v != "kw hi sel" {
		t.Errorf("after raise %q", v)
	}
	mustCmd(t, top, ".t tag lower kw hi")
	if v := mustCmd(t, top, ".t tag names"); v != "hi kw sel" {
		t.Errorf("after lower %q", v)
	}
	mustCmd(t, top, "update")
	// The background of hi, under "second", is red; underlined.
	o := top.Rect(".t", 0).Min
	bb := strings.Fields(mustCmd(t, top, ".t bbox 2.1"))
	if len(bb) != 4 {
		t.Fatalf("bbox %q", bb)
	}
	x, _ := atoi(bb[0])
	y, _ := atoi(bb[1])
	if c := pixel(t, top, o.Add(draw.Pt(x, y))); c != 0xFF0000 {
		t.Errorf("tagged background %06x", c)
	}
	// Characters of kw are blue: some pixel in "alpha" is.
	bb = strings.Fields(mustCmd(t, top, ".t bbox 1.0"))
	x, _ = atoi(bb[0])
	y, _ = atoi(bb[1])
	blue := false
	for dy := 0; dy < top.font.Height && !blue; dy++ {
		for dx := 0; dx < 4*top.font.Width("a"); dx++ {
			if pixel(t, top, o.Add(draw.Pt(x+dx, y+dy))) == 0x0000CC {
				blue = true
				break
			}
		}
	}
	if !blue {
		t.Error("no blue in the kw text")
	}
	// Text inserted inside a tagged range takes the tags around it;
	// with a tag list it takes only those.
	mustCmd(t, top, ".t insert 1.2 XX")
	if v := mustCmd(t, top, ".t tag ranges kw"); v != "1.0 1.7 1.13 1.18" {
		t.Errorf("after insert inside %q", v)
	}
	mustCmd(t, top, ".t insert 1.0 YY {}")
	if v := mustCmd(t, top, ".t tag ranges kw"); v != "1.2 1.9 1.15 1.20" {
		t.Errorf("after insert with no tags %q", v)
	}
	// Only one selection.
	mustCmd(t, top, ".t tag add sel 3.0 3.2")
	if v := top.Cmd(".t tag add sel 3.3 3.4"); v != "!selection already exists" {
		t.Errorf("second selection %q", v)
	}
	mustCmd(t, top, ".t tag delete kw")
	if v := mustCmd(t, top, ".t tag names"); v != "hi sel" {
		t.Errorf("after delete %q", v)
	}
}

func TestTextTagBind(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	cmd := make(chan string, 16)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "text .t -width 200 -height 100; pack .t")
	mustCmd(t, top, ".t insert end {plain link plain}; .t tag add link 1.6 1.10")
	mustCmd(t, top, ".t tag bind link <Button-1> {send cmd click %x}")
	mustCmd(t, top, ".t tag bind link <Enter> {send cmd enter}; .t tag bind link <Leave> {send cmd leave}")
	if v := mustCmd(t, top, ".t tag bind link"); v != "<Button-1> <Enter> <Leave>" {
		t.Errorf("tag bind list %q", v)
	}
	mustCmd(t, top, "update")
	o := top.Rect(".t", 0).Min
	pt := func(idx string) draw.Point {
		bb := strings.Fields(mustCmd(t, top, ".t bbox "+idx))
		x, _ := atoi(bb[0])
		y, _ := atoi(bb[1])
		return o.Add(draw.Pt(x+1, y+2))
	}
	top.Pointer(draw.Pointer{XY: pt("1.1")})
	top.Pointer(draw.Pointer{XY: pt("1.7")})
	click(top, pt("1.7"), 0)
	top.Pointer(draw.Pointer{XY: pt("1.12"), Msec: 10})
	click(top, pt("1.12"), 1000)
	var got []string
	for len(cmd) > 0 {
		got = append(got, <-cmd)
	}
	if s := strings.Join(got, "|"); !strings.HasPrefix(s, "enter|click ") || !strings.HasSuffix(s, "|leave") || len(got) != 3 {
		t.Errorf("tag events %q", s)
	}
}

func TestTextSearchMarks(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "text .t; .t insert end {one two three\nTwo again two\nend}")
	for _, c := range []struct{ cmd, want string }{
		{".t search two 1.0", "1.4"},
		{".t search two 1.5", "2.10"},
		{".t search -nocase two 1.5", "2.0"},
		{".t search -backwards two 2.10", "1.4"},
		{".t search two 2.11", "1.4"},
		{".t search two 2.11 end", ""},
		{".t search {e\nT} 1.0", "1.12"},
		{".t search -- -x 1.0", ""},
		{".t search nothing 1.0", ""},
		{".t index {1.5 wordstart}", "1.4"},
		{".t index {1.3 wordend}", "1.4"},
		{".t index {2.end -2c}", "2.11"},
		{".t index {3.1 -1 lines}", "2.1"},
		{".t index 0.0", "1.0"},
		{".t index {1.0+16c}", "2.2"},
		{".t index insert-1chars", "3.2"},
	} {
		if got := top.Cmd(c.cmd); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
	mustCmd(t, top, ".t mark set a 1.4; .t mark set b 2.0; .t mark set insert 1.0; .t mark set current 3.0")
	for _, c := range []struct{ cmd, want string }{
		{".t mark next 1.1", "a"},
		{".t mark next a", "b"},
		{".t mark previous 2.0", "a"},
		{".t mark previous a", "insert"},
		{".t mark next 3.1", ""},
		{".t mark names", "a b current insert"},
	} {
		if got := top.Cmd(c.cmd); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestTextKeysAndClicks(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "text .t -width 200 -height 100; pack .t; focus .t; focus -global 1; update")
	mustCmd(t, top, ".t insert end {hello (big) world}; .t mark set insert 1.5")
	top.Keyboard(0x0B) // ^K
	if v := mustCmd(t, top, ".t get 1.0 end"); v != "hello" {
		t.Errorf("after ^K %q", v)
	}
	mustCmd(t, top, ".t insert end { (big) world}; update")
	o := top.Rect(".t", 0).Min
	at := func(idx string) draw.Point {
		bb := strings.Fields(mustCmd(t, top, ".t bbox "+idx))
		x, _ := atoi(bb[0])
		y, _ := atoi(bb[1])
		return o.Add(draw.Pt(x+1, y+2))
	}
	// A double click after the bracket selects what it holds; on a
	// word, the word.
	click(top, at("1.7"), 0)
	click(top, at("1.7"), 100)
	if v := mustCmd(t, top, ".t get sel.first sel.last"); v != "big" {
		t.Errorf("bracket double click %q", v)
	}
	click(top, at("1.14"), 2000)
	click(top, at("1.14"), 2100)
	if v := mustCmd(t, top, ".t get sel.first sel.last"); v != "world" {
		t.Errorf("word double click %q", v)
	}
	// Typing replaces the selection holding the insert point.
	mustCmd(t, top, ".t mark set insert sel.first")
	top.Keyboard('W')
	if v := mustCmd(t, top, ".t get 1.0 end"); v != "hello (big) W" {
		t.Errorf("typing over the selection %q", v)
	}
}

// imagefile writes an uncompressed image(6) file.
func imagefile(t *testing.T, chans string, r draw.Rectangle, data []byte) string {
	t.Helper()
	f := func(s string) string { return strings.Repeat(" ", 11-len(s)) + s + " " }
	hdr := f(chans) + f(itoa(r.Min.X)) + f(itoa(r.Min.Y)) + f(itoa(r.Max.X)) + f(itoa(r.Max.Y))
	p := filepath.Join(t.TempDir(), "img")
	if err := os.WriteFile(p, append([]byte(hdr), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImages(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	// A 4x2 red image, and a grey one used as a bitmap.
	red := imagefile(t, "r8g8b8", draw.Rect(0, 0, 4, 2), []byte(strings.Repeat("\x00\x00\xff", 8)))
	grey := imagefile(t, "k8", draw.Rect(0, 0, 3, 3), []byte(strings.Repeat("\xff", 9)))
	if v := mustCmd(t, top, "image create bitmap -file @"+red); v != "image0" {
		t.Errorf("create %q", v)
	}
	mustCmd(t, top, "image create photo mask -file @"+grey)
	for _, c := range []struct{ cmd, want string }{
		{"image width image0", "4"},
		{"image height image0", "2"},
		{"image type mask", "bitmap"},
		{"image names", "image0 mask"},
		{"image width nosuch", "!bad value"},
		{"image create pixmap", "!bad value"},
		{"image create bitmap -file @/nonexistent", "!failed to load bitmap"},
		{"label .bad -image nosuch", "!bad value"},
		{"image bogus x", "!bad value"},
	} {
		if got := top.Cmd(c.cmd); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
	mustCmd(t, top, "label .l -image image0 -bg white; button .b -bitmap @"+grey+" -fg blue -bg white")
	mustCmd(t, top, "pack .l .b; update")
	if r := top.Rect(".l", Border|Required); r.Dx() != 4 || r.Dy() != 2 {
		t.Errorf("image label size %v", r)
	}
	if c := pixel(t, top, center(top, ".l")); c != 0xFF0000 {
		t.Errorf("image pixel %06x", c)
	}
	if c := pixel(t, top, center(top, ".b")); c != 0x0000CC {
		t.Errorf("bitmap pixel %06x", c)
	}
	if v := mustCmd(t, top, ".l cget -image"); v != "image0" {
		t.Errorf("cget -image %q", v)
	}
	mustCmd(t, top, "image delete image0 mask")
	if v := mustCmd(t, top, "image names"); v != "" {
		t.Errorf("names after delete %q", v)
	}
}

func TestBindAdd(t *testing.T) {
	top := toplevel(t, "")
	cmd := make(chan string, 4)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "frame .f; bind .f <Key> {send cmd one}; bind .f <Key> +{send cmd two {%A}}")
	if v := mustCmd(t, top, "bind .f <Key>"); v != "send cmd one;send cmd two {%A}" {
		t.Errorf("bind + %q", v)
	}
	mustCmd(t, top, "bind .f <Key> -{send cmd one;}")
	if v := mustCmd(t, top, "bind .f <Key>"); v != "send cmd two {%A}" {
		t.Errorf("bind - %q", v)
	}
}
