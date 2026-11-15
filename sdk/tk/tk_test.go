package tk

import (
	"context"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/sdk/draw"
)

type plat struct{ scr *platform.Headless }

func (*plat) Now() int64                 { return 0 }
func (*plat) Entropy([]byte)             {}
func (*plat) Console() platform.Console  { return nil }
func (*plat) User() string               { return "inferno" }
func (*plat) Sysname() string            { return "test" }
func (p *plat) Display() platform.Screen { return p.scr }

type fdrw struct {
	p  *core.Prog
	fd int
}

func (f fdrw) Read(b []byte) (int, error)  { return f.p.Read(f.fd, b) }
func (f fdrw) Write(b []byte) (int, error) { return f.p.Write(f.fd, b) }

// display connects a Display to a draw device in a kernel of its own.
func display(t *testing.T) *draw.Display {
	t.Helper()
	k := &core.Kernel{Platform: &plat{platform.NewHeadless(320, 240, "x8r8g8b8")}, Progs: core.NewProgs(), Owner: "inferno"}
	root := core.NewRoot("inferno")
	root.AddDir("dev")
	k.DevTab = core.NewDevTab(root, core.NewDevDraw(k))
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	p := core.NewProg(k, core.NewPgrp(k.DevTab, rc), "inferno")
	if _, err := p.Bind("#i", "/dev", abi.MBEFORE); err != nil {
		t.Fatal(err)
	}
	ctl, err := p.Open("/dev/draw/new", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	info := make([]byte, 144)
	if _, err := p.Read(ctl, info); err != nil {
		t.Fatal(err)
	}
	data, err := p.Open("/dev/draw/1/data", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	d, err := draw.NewDisplay(fdrw{p, ctl}, fdrw{p, data}, info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetDefaultFont(); err != nil {
		t.Fatal(err)
	}
	return d
}

// toplevel makes a toplevel drawn on the whole screen at 0,0.
func toplevel(t *testing.T, arg string) *Toplevel {
	d := display(t)
	top := New(d, arg)
	return top
}

func mustCmd(t *testing.T, top *Toplevel, s string) string {
	t.Helper()
	v := top.Cmd(s)
	if strings.HasPrefix(v, "!") {
		t.Fatalf("%q: %s", s, v)
	}
	return v
}

func TestSplitAndWords(t *testing.T) {
	top := &Toplevel{}
	cmds, err := splitCmds("a b; c {d; e} [f;g]; h 'i; j")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a b", " c {d; e} [f;g]", " h 'i; j"}
	if strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Errorf("splitCmds: %q", cmds)
	}
	ws, err := top.words(`label .l -text {hello {nested} \} world} -fg 'rest of it`)
	if err != nil {
		t.Fatal(err)
	}
	wantw := []string{"label", ".l", "-text", "hello {nested} } world", "-fg", "rest of it"}
	if strings.Join(ws, "|") != strings.Join(wantw, "|") {
		t.Errorf("words: %q", ws)
	}
	if _, err := splitCmds("a {b"); err == nil || err.Error() != errSyntx {
		t.Errorf("unbalanced: %v", err)
	}
	if q := Quote(`a{b}\c`); q != `{a\{b\}\\c}` {
		t.Errorf("Quote: %s", q)
	}
	ws, _ = top.words(Quote(`a{b}\c d`))
	if len(ws) != 1 || ws[0] != `a{b}\c d` {
		t.Errorf("Quote round trip: %q", ws)
	}
}

func TestValues(t *testing.T) {
	top := toplevel(t, "")
	for _, c := range []struct {
		s    string
		want int
	}{{"10", 10}, {"1i", 100}, {"2.5", 3}, {"-4", -4}, {"72p", 100}, {"3w", 15}, {"2h", 16}, {"1c", 39}} {
		got, err := top.dist(c.s, nil)
		if err != nil || got != c.want {
			t.Errorf("dist %q = %d %v, want %d", c.s, got, err, c.want)
		}
	}
	if _, err := top.dist("x", nil); err == nil {
		t.Error("dist x")
	}
	for _, c := range []struct {
		s    string
		want uint32
	}{{"red", 0xFF0000FF}, {"#123", 0x112233FF}, {"#abcdef", 0xABCDEFFF}, {"#11223344", 0x11223344},
		{"white*#80", 0xFFFFFF80}, {"black*0.5", 0x00000080}} {
		if got := Color(c.s); got != c.want {
			t.Errorf("Color %q = %#x, want %#x", c.s, got, c.want)
		}
	}
	if Color("nosuch") != DNotacolor {
		t.Error("bad colour")
	}
}

func TestErrors(t *testing.T) {
	top := toplevel(t, "")
	for _, c := range []struct{ cmd, want string }{
		{"nosuch", "!bad command nosuch"},
		{".x configure -text a", "!bad window path .x"},
		{"label .l -nosuch 1", "!bad option -nosuch"},
		{"label .a.b", "!bad window path .a.b"},
		{"label .m; label .m", "!window path already exists .m"},
		{"label .n -text", "!arg requires option -text"},
		{"frame .f -relief wavy", "!bad value wavy"},
		{"frame .g; bind .g <Bogus> {}", "!bad event sequence <Bogus>"},
		{"pack .", "!can't pack top level"},
	} {
		if got := top.Cmd(c.cmd); got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
	if v := top.Cmd("variable lasterror"); !strings.Contains(v, "bad option") && !strings.Contains(v, "can't pack") {
		t.Errorf("lasterror %q", v)
	}
}

func TestPack(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "frame .f -width 100 -height 50; frame .g -width 30 -height 20; pack .f .g -side left")
	if r := top.Rect(".", Border|Required); r.Dx() != 130 || r.Dy() != 50 {
		t.Errorf(". wants %v", r)
	}
	if r := top.Rect(".g", Border|Local); r != draw.Rect(100, 15, 130, 35) {
		t.Errorf(".g at %v", r)
	}
	// Top and bottom with fill and expand in a fixed master.
	top = toplevel(t, "")
	mustCmd(t, top, ". configure -width 200 -height 100")
	mustCmd(t, top, "frame .a -height 10; frame .b -height 10; frame .c -width 20")
	mustCmd(t, top, "pack .a -side top -fill x; pack .b -side bottom -fill x; pack .c -fill both -expand 1 -padx 5 -pady 5")
	for _, c := range []struct {
		n string
		r draw.Rectangle
	}{{".a", draw.Rect(0, 0, 200, 10)}, {".b", draw.Rect(0, 90, 200, 100)}, {".c", draw.Rect(5, 15, 195, 85)}} {
		if r := top.Rect(c.n, Border|Local); r != c.r {
			t.Errorf("%s at %v, want %v", c.n, r, c.r)
		}
	}
	if s := mustCmd(t, top, "pack slaves ."); s != ".a .b .c" {
		t.Errorf("slaves %q", s)
	}
	mustCmd(t, top, "pack forget .b")
	if r := top.Rect(".c", Border|Local); r != draw.Rect(5, 15, 195, 95) {
		t.Errorf(".c after forget %v", r)
	}
	if s := mustCmd(t, top, "pack info .a"); !strings.Contains(s, "-side top") || !strings.Contains(s, "-fill x") {
		t.Errorf("info %q", s)
	}
	// -before puts a slave ahead of another.
	mustCmd(t, top, "frame .d -height 5; pack .d -before .a -fill x")
	if s := mustCmd(t, top, "pack slaves ."); s != ".d .a .c" {
		t.Errorf("slaves with before %q", s)
	}
	if v := top.Cmd("pack .a -in .a.x"); !strings.HasPrefix(v, "!bad window path") {
		t.Errorf("bad -in: %q", v)
	}
}

func TestLabelSize(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "label .l -text hello; button .b -text hi; pack .l .b")
	f := top.font
	if r := top.Rect(".l", Border|Required); r.Dx() != f.Width("hello")+2*textpadx || r.Dy() != f.Height {
		t.Errorf("label %v", r)
	}
	if r := top.Rect(".b", Border|Required); r.Dx() != f.Width("hi")+2*textpadx+2 || r.Dy() != f.Height+2 {
		t.Errorf("button %v", r)
	}
	if v := mustCmd(t, top, ".l cget -text"); v != "hello" {
		t.Errorf("cget %q", v)
	}
	mustCmd(t, top, ".l configure -text {two words} -fg red")
	if v := mustCmd(t, top, ".l cget -fg"); v != "#ff0000" {
		t.Errorf("fg %q", v)
	}
	if v := mustCmd(t, top, "winfo class .b"); v != "button" {
		t.Errorf("class %q", v)
	}
}

// screen gives a toplevel the display's image, as tkclient does
// without a window manager.
func screen(t *testing.T, top *Toplevel) {
	t.Helper()
	if e := top.PutImage(".", top.Display.Image, nil); e != "" {
		t.Fatal(e)
	}
}

func pixel(t *testing.T, top *Toplevel, p draw.Point) uint32 {
	t.Helper()
	b, err := top.Display.Image.Unload(draw.Rect(p.X, p.Y, p.X+1, p.Y+1))
	if err != nil {
		t.Fatal(err)
	}
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

func click(top *Toplevel, p draw.Point, msec int) {
	top.Pointer(draw.Pointer{XY: p, Msec: msec})
	top.Pointer(draw.Pointer{Buttons: 1, XY: p, Msec: msec})
	top.Pointer(draw.Pointer{XY: p, Msec: msec + 1})
}

func center(top *Toplevel, name string) draw.Point {
	r := top.Rect(name, Border)
	return r.Min.Add(r.Max).Div(2)
}

func TestButtonAndDraw(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	cmd := make(chan string, 8)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "frame .f -bg red -width 40 -height 20; button .b -text Press -command {send cmd pressed now}")
	mustCmd(t, top, "pack .f .b; update")
	if c := pixel(t, top, center(top, ".f")); c != 0xFF0000 {
		t.Errorf("frame pixel %06x", c)
	}
	if c := pixel(t, top, draw.Pt(300, 200)); c != 0xDDDDDD {
		t.Errorf("background pixel %06x", c)
	}
	click(top, center(top, ".b"), 0)
	select {
	case s := <-cmd:
		if s != "pressed now" {
			t.Errorf("sent %q", s)
		}
	default:
		t.Fatal("button sent nothing")
	}
	// A press that leaves the button does not invoke it.
	p := center(top, ".b")
	top.Pointer(draw.Pointer{Buttons: 1, XY: p, Msec: 1000})
	top.Pointer(draw.Pointer{Buttons: 1, XY: draw.Pt(310, 230), Msec: 1001})
	top.Pointer(draw.Pointer{XY: draw.Pt(310, 230), Msec: 1002})
	select {
	case s := <-cmd:
		t.Errorf("sent %q after leaving", s)
	default:
	}
	mustCmd(t, top, ".b invoke")
	if s := <-cmd; s != "pressed now" {
		t.Errorf("invoke sent %q", s)
	}
}

func TestBindings(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	cmd := make(chan string, 16)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "frame .f -width 50 -height 50; pack .f")
	mustCmd(t, top, "bind .f <Button-1> {send cmd b1 %x %y}; bind .f <Double-Button-1> {send cmd double}")
	mustCmd(t, top, "bind .f <ButtonRelease-1> {send cmd up %b}; bind . <Key> {send cmd key %A}")
	mustCmd(t, top, "bind .f <Motion-Button-1> {send cmd drag}; bind . <Key-q> {send cmd quit}")
	if v := mustCmd(t, top, "bind .f <Button-1>"); v != "send cmd b1 %x %y" {
		t.Errorf("bind query %q", v)
	}
	o := top.Rect(".f", Border).Min
	click(top, o.Add(draw.Pt(3, 4)), 10)
	top.Pointer(draw.Pointer{Buttons: 1, XY: o.Add(draw.Pt(3, 4)), Msec: 100})
	top.Pointer(draw.Pointer{Buttons: 1, XY: o.Add(draw.Pt(5, 5)), Msec: 110})
	top.Pointer(draw.Pointer{XY: o.Add(draw.Pt(5, 5)), Msec: 120})
	top.Keyboard('z')
	top.Keyboard('q')
	var got []string
	for len(cmd) > 0 {
		got = append(got, <-cmd)
	}
	want := "b1 3 4|up 1|double|drag|up 1|key z|quit"
	if strings.Join(got, "|") != want {
		t.Errorf("got %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestEntry(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "entry .e; pack .e; focus .e; focus -global 1")
	for _, k := range "hello worx" {
		top.Keyboard(int(k))
	}
	top.Keyboard('\b')
	top.Keyboard('l')
	top.Keyboard('d')
	if v := mustCmd(t, top, ".e get"); v != "hello world" {
		t.Errorf("get %q", v)
	}
	mustCmd(t, top, ".e delete 0 6; .e insert 0 {big }; .e icursor end")
	if v := mustCmd(t, top, ".e get"); v != "big world" {
		t.Errorf("after edit %q", v)
	}
	if v := mustCmd(t, top, ".e index insert"); v != "9" {
		t.Errorf("index %q", v)
	}
	top.Keyboard(0x17) // ^W
	if v := mustCmd(t, top, ".e get"); v != "big " {
		t.Errorf("after ^W %q", v)
	}
	mustCmd(t, top, ".e selection range 0 3")
	top.Keyboard('B')
	if v := mustCmd(t, top, ".e get"); v != "B " {
		t.Errorf("typing over selection %q", v)
	}
	// A click moves the cursor.
	r := top.Rect(".e", 0)
	click(top, draw.Pt(r.Min.X+1, r.Min.Y+2), 0)
	if v := mustCmd(t, top, ".e index insert"); v != "0" {
		t.Errorf("click index %q", v)
	}
}

func TestText(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "text .t -width 100 -height 40; pack .t")
	mustCmd(t, top, ".t insert end {line one\nline two\nthree}")
	for _, c := range []struct{ idx, want string }{
		{"end", "3.5"}, {"1.0", "1.0"}, {"2.end", "2.8"}, {"1.2 + 3 chars", "1.5"}, {"1.7+2c", "2.0"},
		{"3.1 -1 lines", "2.1"}, {"2.3 linestart", "2.0"}, {"1.6 wordstart", "1.5"}, {"1.1 wordend", "1.4"},
		{"9.9", "3.5"},
	} {
		if v := mustCmd(t, top, ".t index {"+c.idx+"}"); v != c.want {
			t.Errorf("index %q = %q, want %q", c.idx, v, c.want)
		}
	}
	if v := mustCmd(t, top, ".t get 1.5 2.4"); v != "one\nline" {
		t.Errorf("get %q", v)
	}
	mustCmd(t, top, ".t mark set m 2.0; .t insert 1.0 {zero\n}")
	if v := mustCmd(t, top, ".t index m"); v != "3.0" {
		t.Errorf("mark moved to %q", v)
	}
	mustCmd(t, top, ".t delete 1.0 2.0")
	if v := mustCmd(t, top, ".t get 1.0 end"); v != "line one\nline two\nthree" {
		t.Errorf("after delete %q", v)
	}
	mustCmd(t, top, ".t tag add sel 1.0 1.4")
	if v := mustCmd(t, top, ".t tag ranges sel"); v != "1.0 1.4" {
		t.Errorf("sel %q", v)
	}
	if v := mustCmd(t, top, ".t get sel.first sel.last"); v != "line" {
		t.Errorf("sel text %q", v)
	}
	if v := mustCmd(t, top, ".t compare 1.0 < end"); v != "1" {
		t.Errorf("compare %q", v)
	}
	// Typing goes in at the insert mark.
	mustCmd(t, top, "focus .t; .t mark set insert end; .t tag remove sel 1.0 end")
	for _, k := range "\nfour" {
		top.Keyboard(int(k))
	}
	if v := mustCmd(t, top, ".t get 4.0 end"); v != "four" {
		t.Errorf("typed %q", v)
	}
	// Many lines scroll; see brings the end into view.
	for i := 0; i < 20; i++ {
		mustCmd(t, top, ".t insert end {\nmore}")
	}
	mustCmd(t, top, ".t see end; update")
	if v := mustCmd(t, top, ".t yview"); strings.HasPrefix(v, "0.0000") || !strings.HasSuffix(v, "1.0000") {
		t.Errorf("yview after see %q", v)
	}
}

func TestListboxScrollbar(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "scrollbar .s -command {.l yview}; listbox .l -yscrollcommand {.s set} -height 40")
	mustCmd(t, top, "pack .s -side left -fill y; pack .l -side left")
	for i := 0; i < 20; i++ {
		mustCmd(t, top, ".l insert end item"+itoa(i))
	}
	mustCmd(t, top, "update")
	if v := mustCmd(t, top, ".l size"); v != "20" {
		t.Errorf("size %q", v)
	}
	if v := mustCmd(t, top, ".s get"); v != "0.0000 0.2500" {
		t.Errorf("scrollbar %q", v)
	}
	// Click the bottom arrow: the list scrolls one line.
	r := top.Rect(".s", 0)
	click(top, draw.Pt(r.Min.X+2, r.Max.Y-2), 0)
	if v := mustCmd(t, top, ".l yview"); v != "0.0500 0.3000" {
		t.Errorf("yview after arrow %q", v)
	}
	if v := mustCmd(t, top, ".s get"); v != "0.0500 0.3000" {
		t.Errorf("scrollbar after arrow %q", v)
	}
	// Click the second visible item.
	lr := top.Rect(".l", 0)
	click(top, draw.Pt(lr.Min.X+5, lr.Min.Y+top.font.Height+1), 1000)
	if v := mustCmd(t, top, ".l curselection"); v != "2" {
		t.Errorf("curselection %q", v)
	}
	if v := mustCmd(t, top, ".l get 2"); v != "item2" {
		t.Errorf("get %q", v)
	}
	mustCmd(t, top, ".l delete 0 1")
	if v := mustCmd(t, top, ".l curselection"); v != "0" {
		t.Errorf("curselection after delete %q", v)
	}
}

func TestCheckRadio(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "checkbutton .c -text check -variable v; radiobutton .r1 -text a -variable r -value a")
	mustCmd(t, top, "radiobutton .r2 -text b -variable r -value b; pack .c .r1 .r2; update")
	click(top, center(top, ".c"), 0)
	click(top, center(top, ".r2"), 1000)
	if v := mustCmd(t, top, "variable v"); v != "1" {
		t.Errorf("v = %q", v)
	}
	if v := mustCmd(t, top, "variable r"); v != "b" {
		t.Errorf("r = %q", v)
	}
	mustCmd(t, top, ".r1 select; .c toggle")
	if v := mustCmd(t, top, "variable r"); v != "a" {
		t.Errorf("r = %q after select", v)
	}
	if v := mustCmd(t, top, "variable v"); v != "0" {
		t.Errorf("v = %q after toggle", v)
	}
}

func TestMenu(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	cmd := make(chan string, 8)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "menubutton .mb -text File -menu .mb.m; menu .mb.m")
	mustCmd(t, top, ".mb.m add command -label Open -command {send cmd open}")
	mustCmd(t, top, ".mb.m add separator; .mb.m add command -label Quit -command {send cmd quit}")
	mustCmd(t, top, "pack .mb -anchor w; update")
	if v := mustCmd(t, top, ".mb.m index Quit"); v != "2" {
		t.Errorf("index %q", v)
	}
	// Press on the menubutton, drag to Quit, release.
	p := center(top, ".mb")
	top.Pointer(draw.Pointer{Buttons: 1, XY: p})
	if top.menu == nil {
		t.Fatal("menu not posted")
	}
	mr := top.menu.rect()
	y := mr.Min.Y + 1 + 2*(top.font.Height+2*menupad) + sepht/2 + 2
	top.Pointer(draw.Pointer{Buttons: 1, XY: draw.Pt(mr.Min.X+5, y)})
	top.Pointer(draw.Pointer{XY: draw.Pt(mr.Min.X+5, y)})
	if top.menu != nil {
		t.Error("menu still posted")
	}
	select {
	case s := <-cmd:
		if s != "quit" {
			t.Errorf("menu sent %q", s)
		}
	default:
		t.Error("menu sent nothing")
	}
	mustCmd(t, top, ".mb.m invoke 0")
	if s := <-cmd; s != "open" {
		t.Errorf("invoke sent %q", s)
	}
}

func TestCanvas(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	cmd := make(chan string, 8)
	NameChan(top, cmd, "cmd")
	mustCmd(t, top, "canvas .c -width 100 -height 100 -bg white; pack .c")
	id := mustCmd(t, top, ".c create rectangle 10 10 30 30 -fill blue -tags box")
	if id != "1" {
		t.Errorf("id %q", id)
	}
	mustCmd(t, top, ".c create line 0 50 99 50 -fill red; .c create text 50 80 -text hi")
	mustCmd(t, top, "update")
	o := top.Rect(".c", 0).Min
	if c := pixel(t, top, o.Add(draw.Pt(20, 20))); c != 0x0000CC {
		t.Errorf("rectangle pixel %06x", c)
	}
	if c := pixel(t, top, o.Add(draw.Pt(70, 50))); c != 0xFF0000 {
		t.Errorf("line pixel %06x", c)
	}
	mustCmd(t, top, ".c move box 50 0; update")
	if v := mustCmd(t, top, ".c coords box"); v != "60 10 80 30" {
		t.Errorf("coords %q", v)
	}
	if c := pixel(t, top, o.Add(draw.Pt(20, 20))); c != 0xFFFFFF {
		t.Errorf("old rectangle pixel %06x", c)
	}
	mustCmd(t, top, ".c bind box <Button-1> {send cmd hit}")
	click(top, o.Add(draw.Pt(70, 20)), 0)
	select {
	case s := <-cmd:
		if s != "hit" {
			t.Errorf("sent %q", s)
		}
	default:
		t.Error("item binding did not fire")
	}
	if v := mustCmd(t, top, ".c find withtag box"); v != "1" {
		t.Errorf("find %q", v)
	}
	mustCmd(t, top, ".c delete box")
	if v := mustCmd(t, top, ".c find all"); v != "2 3" {
		t.Errorf("find all %q", v)
	}
}

func TestDestroyAndFocus(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "frame .f; entry .f.e; pack .f; pack .f.e; focus .f.e")
	if v := mustCmd(t, top, "focus"); v != ".f.e" {
		t.Errorf("focus %q", v)
	}
	mustCmd(t, top, "destroy .f")
	if v := mustCmd(t, top, "focus"); v != "" {
		t.Errorf("focus after destroy %q", v)
	}
	if n := strings.Join(top.sortedNames(), " "); n != "." {
		t.Errorf("widgets left: %s", n)
	}
}

func TestBracketsAndVariables(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "label .l -text [variable x]; variable x 42; label .m -text [variable x]")
	if v := mustCmd(t, top, ".m cget -text"); v != "42" {
		t.Errorf("bracket %q", v)
	}
	if v := mustCmd(t, top, "variable x"); v != "42" {
		t.Errorf("variable %q", v)
	}
}

func TestWreq(t *testing.T) {
	top := toplevel(t, "")
	screen(t, top)
	mustCmd(t, top, "label .l -text a; pack .l; update")
	mustCmd(t, top, ".l configure -text {a much longer text}; update")
	select {
	case s := <-top.Wreq:
		if !strings.HasPrefix(s, "!reshape . -1 0 0 ") {
			t.Errorf("wreq %q", s)
		}
	default:
		t.Error("no reshape request")
	}
}

func TestMarkGravity(t *testing.T) {
	top := toplevel(t, "")
	mustCmd(t, top, "text .t; .t insert end {out}; .t mark set o end; .t mark gravity o left")
	mustCmd(t, top, ".t mark set insert end; .t insert insert typed")
	if v := mustCmd(t, top, ".t get o end"); v != "typed" {
		t.Errorf("after o: %q", v)
	}
	if v := mustCmd(t, top, ".t mark gravity o"); v != "left" {
		t.Errorf("gravity %q", v)
	}
}
