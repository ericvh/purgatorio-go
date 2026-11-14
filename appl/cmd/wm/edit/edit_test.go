package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tk/tktest"
)

// fakeui answers dialogs from a script and keeps the title and snarf.
type fakeui struct {
	answers []string // for GetString
	prompts []int    // for Prompt
	asked   []string
	title   string
	snarf   string
}

func (u *fakeui) Prompt(title, msg string, dflt int, labs []string) int {
	u.asked = append(u.asked, "prompt "+title+": "+msg)
	if len(u.prompts) == 0 {
		return dflt
	}
	n := u.prompts[0]
	u.prompts = u.prompts[1:]
	return n
}

func (u *fakeui) GetString(msg string) string {
	u.asked = append(u.asked, "getstring "+msg)
	if len(u.answers) == 0 {
		return ""
	}
	s := u.answers[0]
	u.answers = u.answers[1:]
	return s
}

func (u *fakeui) SetTitle(t string) { u.title = t }
func (u *fakeui) SnarfGet() string  { return u.snarf }
func (u *fakeui) SnarfPut(s string) { u.snarf = s }

type rig struct {
	t   *testing.T
	top *tk.Toplevel
	e   *editor
	u   *fakeui
	c   chan string
}

func newRig(t *testing.T) *rig {
	d := tktest.Display(t, 640, 480)
	top := tk.New(d, "")
	top.PutImage(".", d.Image, nil)
	u := &fakeui{}
	r := &rig{t: t, top: top, u: u, c: make(chan string, 64)}
	tk.NameChan(top, r.c, "c")
	r.e = newEditor(top, u)
	r.e.log = func(s string) { t.Log(s) }
	r.e.setup()
	tk.Cmd(top, "focus -global 1")
	return r
}

// drain carries out what the window has sent, as main's loop does.
func (r *rig) drain() bool {
	for {
		select {
		case s := <-r.c:
			if !r.e.do(s) {
				return false
			}
		default:
			r.e.cmd("update")
			return true
		}
	}
}

func (r *rig) menu(m, entry string) bool {
	r.e.cmd(m + " invoke {" + entry + "}")
	return r.drain()
}

func (r *rig) typing(s string) {
	for _, c := range s {
		r.top.Keyboard(int(c))
	}
	r.drain()
}

func (r *rig) text() string { return tk.Cmd(r.top, ".b.t get 1.0 end") }

func TestEditFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("first line\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	if msg := r.e.loadtfile(f); msg != "" {
		t.Fatal(msg)
	}
	if r.u.title != "Edit "+f || r.text() != "first line\nsecond line\n" {
		t.Errorf("after load: title %q text %q", r.u.title, r.text())
	}
	// Typing marks the file dirty; Save writes it back.
	r.e.cmd(".b.t mark set insert 1.0")
	r.typing("new ")
	if r.u.title != "Edit "+f+" (dirty)" {
		t.Errorf("title after typing %q", r.u.title)
	}
	r.menu(".m.file.menu", "Save")
	if b, _ := os.ReadFile(f); string(b) != "new first line\nsecond line\n" {
		t.Errorf("saved %q", b)
	}
	if r.u.title != "Edit "+f {
		t.Errorf("title after save %q", r.u.title)
	}
	// Save As asks for a name, relative to the file's directory.
	r.u.answers = []string{"b.txt"}
	r.menu(".m.file.menu", "Save As...")
	if b, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(b) != "new first line\nsecond line\n" {
		t.Errorf("saved as %q", b)
	}
	// Exit with changes asks first; No keeps the editor going.
	r.typing("x")
	r.u.prompts = []int{1}
	if !r.menu(".m.file.menu", "Exit") {
		t.Error("exited after No")
	}
	r.u.prompts = []int{0}
	if r.menu(".m.file.menu", "Exit") {
		t.Error("did not exit after Yes")
	}
	// A missing file gives Inferno's message.
	if msg := r.e.loadtfile(filepath.Join(dir, "none")); !strings.Contains(msg, "Can't open") ||
		!strings.Contains(msg, "no such file") {
		t.Errorf("missing file %q", msg)
	}
	if msg := r.e.loadtfile(dir); msg != dir+" is a directory" {
		t.Errorf("directory %q", msg)
	}
}

func TestEditSnarfAndSearch(t *testing.T) {
	r := newRig(t)
	r.e.cmd(".b.t insert end {one two three\ntwo four two\nend}")
	r.e.cmd(".b.t tag add sel 1.4 1.8")
	r.menu(".m.edit.menu", "Copy")
	if r.u.snarf != "two " {
		t.Errorf("copied %q", r.u.snarf)
	}
	r.menu(".m.edit.menu", "Cut")
	if r.text() != "one three\ntwo four two\nend" {
		t.Errorf("after cut %q", r.text())
	}
	r.e.cmd(".b.t mark set insert 3.0")
	r.menu(".m.edit.menu", "Paste")
	if r.text() != "one three\ntwo four two\ntwo end" {
		t.Errorf("after paste %q", r.text())
	}
	// Find selects the next match after the insert point.
	r.e.cmd(".b.t mark set insert 1.0")
	r.u.answers = []string{"two"}
	r.menu(".m.search.menu", "Find ...")
	if v := tk.Cmd(r.top, ".b.t tag ranges sel"); v != "2.0 2.3" {
		t.Errorf("find selected %q", v)
	}
	r.menu(".m.search.menu", "Find Again")
	if v := tk.Cmd(r.top, ".b.t tag ranges sel"); v != "2.9 2.12" {
		t.Errorf("find again selected %q", v)
	}
	// Replace with replaces the selection.
	r.u.answers = []string{"2"}
	r.menu(".m.search.menu", "Replace with...")
	if r.text() != "one three\ntwo four 2\ntwo end" {
		t.Errorf("after replace %q", r.text())
	}
	r.menu(".m.search.menu", "Find and Replace All")
	if r.text() != "one three\n2 four 2\n2 end" {
		t.Errorf("after replace all %q", r.text())
	}
	// Goto line selects the line.
	r.u.answers = []string{"2"}
	r.menu(".m.search.menu", "Goto Line ...")
	if v := tk.Cmd(r.top, ".b.t index insert"); v != "2.0" {
		t.Errorf("goto insert %q", v)
	}
	if v := tk.Cmd(r.top, ".b.t get sel.first sel.last"); v != "2 four 2" {
		t.Errorf("goto selection %q", v)
	}
}

func TestEditLimbo(t *testing.T) {
	r := newRig(t)
	r.e.cmd(".b.t insert end {if(x) return nil; # done\n}")
	r.menu(".m.options.menu", "Limbo")
	if v := tk.Cmd(r.top, ".b.t tag ranges keyword"); v != "1.0 1.2 1.6 1.12 1.13 1.16" {
		t.Errorf("keywords %q", v)
	}
	if v := tk.Cmd(r.top, ".b.t tag ranges comment"); v != "1.18 1.24" {
		t.Errorf("comments %q", v)
	}
	// Typing a block keyword and a newline indents the next line.
	r.e.cmd(".b.t mark set insert end")
	r.typing("while ")
	r.typing("\n")
	if r.text() != "if(x) return nil; # done\nwhile \n\t" {
		t.Errorf("after while %q", r.text())
	}
}

func TestEditMenuByPointer(t *testing.T) {
	r := newRig(t)
	r.e.cmd("update")
	// Press on Search, drag down to its first entry, release.
	mb := r.top.Rect(".m.search", tk.Border)
	p := mb.Min.Add(mb.Max).Div(2)
	r.top.Pointer(draw.Pointer{Buttons: 1, XY: p})
	mr := r.top.Rect(".m.search.menu", tk.Border)
	q := draw.Pt(mr.Min.X+10, mr.Min.Y+5)
	r.top.Pointer(draw.Pointer{Buttons: 1, XY: q})
	r.top.Pointer(draw.Pointer{XY: q})
	r.u.answers = []string{"zz"}
	r.drain()
	if len(r.u.asked) != 1 || r.u.asked[0] != "getstring Search For" {
		t.Errorf("asked %q", r.u.asked)
	}
}
