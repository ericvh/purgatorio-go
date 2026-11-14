package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/tk"
)

// ui is what the editor asks of the world beyond its window: dialogs,
// the title and the snarf buffer.
type ui interface {
	Prompt(title, msg string, dflt int, labs []string) int
	GetString(msg string) string
	SetTitle(title string)
	SnarfGet() string
	SnarfPut(s string)
}

const errico = "error -fg red"

// The editor's window, as edit.b builds it, with Goto line added to
// the Search menu.
var edconfig = []string{
	"frame .m -relief raised",
	"frame .b",
	"menubutton .m.file -text File -menu .m.file.menu",
	"menubutton .m.edit -text Edit -menu .m.edit.menu",
	"menubutton .m.search -text Search -menu .m.search.menu",
	"menubutton .m.options -text Options -menu .m.options.menu",
	"pack .m.file .m.edit .m.search .m.options -side left",
	"menu .m.file.menu",
	".m.file.menu add command -label New -command {send c new}",
	".m.file.menu add command -label Open... -command {send c open}",
	".m.file.menu add separator",
	".m.file.menu add command -label Save -command {send c save}",
	".m.file.menu add command -label {Save As...} -command {send c saveas}",
	".m.file.menu add separator",
	".m.file.menu add command -label {Exit} -command {send c exit}",
	"menu .m.edit.menu",
	".m.edit.menu add command -label Cut -command {send c cut}",
	".m.edit.menu add command -label Copy -command {send c copy}",
	".m.edit.menu add command -label Paste -command {send c paste}",
	"menu .m.search.menu",
	".m.search.menu add command -label {Find ...} -command {send c searchf}",
	".m.search.menu add command -label {Replace with...} -command {send c replacew}",
	".m.search.menu add command -label {Find Again} -command {send c search}",
	".m.search.menu add command -label {Find and Replace} -command {send c replace}",
	".m.search.menu add command -label {Find and Replace All} -command {send c replaceall}",
	".m.search.menu add command -label {Goto Line ...} -command {send c gotoline}",
	"menu .m.options.menu",
	".m.options.menu add checkbutton -text Limbo -command {send c limbo}",
	".m.options.menu add command -label Indent -command {send c indent}",
	"text .b.t -yscrollcommand {.b.s set} -bg white",
	"bind .b.t <Button-2> {.m.edit.menu post %X %Y}",
	"bind .b.t <Key> +{send c dirtied {%A}}",
	"bind .b.t <ButtonRelease-1> +{send c reindent}",
	"scrollbar .b.s -command {.b.t yview}",
	"pack .m -fill x",
	"pack .b.s -fill y -side left",
	"pack .b.t -fill both -expand 1",
	"pack .b -fill both -expand 1",
	"focus .b.t",
	"pack propagate . 0",
	".b.t tag configure keyword -fg #0000ff",
	".b.t tag configure comment -fg #008800",
	"update",
}

// Kinds of search, as edit.b numbers them.
const (
	search = iota
	searchFor
	replace
	replaceWith
	replaceAll
	noSee
)

// Kinds of indent.
const (
	block = iota
	temp
)

var keywords = []string{
	"adt", "alt", "array", "big", "break",
	"byte", "case", "chan", "con", "continue",
	"cyclic", "do", "else", "exit", "fn",
	"for", "hd", "if", "implement", "import",
	"include", "int", "len", "list", "load",
	"module", "nil", "of", "or", "pick",
	"real", "ref", "return", "self", "spawn",
	"string", "tagof", "tl", "to", "type",
	"while",
}

// blockKeyword marks the keywords after which a line is indented once:
// do, else, for, if and while.
const blockKeyword = int64(1)<<40 | 1<<17 | 1<<15 | 1<<12 | 1<<11

// editor is wm/edit (Inferno's appl/wm/edit.b).
type editor struct {
	top         *tk.Toplevel
	ui          ui
	dirty       bool
	curfile     string
	searchfor   string
	replacewith string
	path        string
	log         func(string)

	// Limbo mode.
	isLimbo       bool
	thisWord      string
	inComment     bool
	firstChar     bool
	indent        []int
	lastKwIsBlock bool
	tab           string
	tabs          [9]string
}

func newEditor(top *tk.Toplevel, u ui) *editor {
	e := &editor{top: top, ui: u, curfile: "(New)", path: ".", firstChar: true, tab: "\t",
		log: func(s string) { fmt.Print(s) }}
	for i := range e.tabs {
		e.tabs[i] = strings.Repeat("\t", i)
	}
	return e
}

// cmd runs a Tk command, reporting errors as edit.b does.
func (e *editor) cmd(s string) string {
	r := tk.Cmd(e.top, s)
	if strings.HasPrefix(r, "!") {
		e.log(fmt.Sprintf("wm/edit: error executing '%s': %s\n", s, r))
	}
	return r
}

func (e *editor) setup() {
	for _, c := range edconfig {
		e.cmd(c)
	}
}

func isErr(s string) bool { return strings.HasPrefix(s, "!") }

// settitle names the window after the file.
func (e *editor) settitle() {
	t := "Edit " + e.curfile
	if e.dirty {
		t += " (dirty)"
	}
	e.ui.SetTitle(t)
}

// do carries out a command the menus or bindings sent; it returns false
// when the editor should exit.
func (e *editor) do(s string) bool {
	if strings.HasPrefix(s, "dirtied") && len(s) > 7 {
		e.setDirty()
		e.limboCheck(s)
		return true
	}
	switch s {
	case "exit":
		if e.checkDirty() {
			e.setClean()
			return false
		}
	case "dirtied":
		e.setDirty()
	case "new":
		if e.checkDirty() {
			e.setClean()
			e.doNew()
		}
	case "open":
		if e.checkDirty() && e.doOpen() {
			e.setClean()
		}
	case "save":
		e.doSave(false)
	case "saveas":
		e.doSave(true)
	case "cut":
		e.doSnarf(true)
		e.setDirty()
	case "copy":
		e.doSnarf(false)
	case "paste":
		e.doPaste()
		e.setDirty()
	case "search":
		e.doSearch(search)
	case "searchf":
		e.doSearch(searchFor)
	case "replace":
		e.doReplace(replace)
	case "replacew":
		e.doReplace(replaceWith)
	case "replaceall":
		e.doReplaceAll()
	case "gotoline":
		e.doGoto()
	case "limbo":
		e.doLimbo()
	case "indent":
		e.doIndent()
	case "reindent":
		e.reIndent()
	}
	e.cmd("focus .b.t")
	return true
}

// checkDirty asks before changes are thrown away; it reports whether
// they may be.
func (e *editor) checkDirty() bool {
	if !e.dirty {
		return true
	}
	return e.ui.Prompt("Confirm", "File was changed.\nDiscard changes?", 0, []string{"Yes", "No"}) == 0
}

func (e *editor) setDirty() {
	if !e.dirty {
		e.dirty = true
		e.settitle()
		e.cmd("update")
	}
}

func (e *editor) setClean() {
	if e.dirty {
		e.dirty = false
		e.settitle()
		e.cmd("update")
	}
}

func (e *editor) doNew() {
	e.cmd(".b.t delete 1.0 end")
	e.curfile = "(New)"
	e.settitle()
}

func (e *editor) doOpen() bool {
	for {
		fname := e.ui.GetString("Open file")
		if fname == "" {
			return false
		}
		if fname[0] != '/' && e.path != "" && e.path != "." {
			fname = e.path + "/" + fname
		}
		e.cmd(".b.t delete 1.0 end")
		msg := e.loadtfile(fname)
		if msg == "" {
			e.basepath(fname)
			return true
		}
		if e.ui.Prompt("Open file", msg, 0, []string{"Cancel", "Open another file"}) == 0 {
			return false
		}
	}
}

func (e *editor) basepath(file string) {
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		e.path = file[:i]
	}
}

func (e *editor) doSave(prompt bool) {
	fname := e.curfile
	contents := tk.Cmd(e.top, ".b.t get 1.0 end")
	for {
		if prompt || e.curfile == "(New)" {
			fname = e.ui.GetString("File")
			if fname == "" {
				return
			}
			if fname[0] != '/' && e.path != "" {
				fname = e.path + "/" + fname
			}
		}
		err := e.savetfile(fname, contents)
		if err == nil {
			e.setClean()
			return
		}
		msg := fmt.Sprintf("Trying to write file \"%s\"\n%v", fname, err)
		if e.ui.Prompt("Save file", msg, 0, []string{"Cancel", "Try another file"}) == 0 {
			return
		}
		prompt = true
	}
}

// doSnarf copies the selection to the snarf buffer, and with del cuts
// it.
func (e *editor) doSnarf(del bool) {
	r := e.cmd(".b.t tag nextrange sel 1.0")
	if r == "" || isErr(r) {
		return
	}
	s := tk.Cmd(e.top, ".b.t get "+r)
	if del {
		e.cmd(".b.t delete " + r)
	}
	e.ui.SnarfPut(s)
}

func (e *editor) doPaste() {
	s := e.ui.SnarfGet()
	if s == "" {
		return
	}
	e.cmd(".b.t insert insert " + tk.Quote(s))
}

func (e *editor) doSearch(prompt int) bool {
	if prompt == searchFor {
		e.searchfor = e.ui.GetString("Search For")
	}
	if e.searchfor == "" {
		return false
	}
	ix := e.cmd(".b.t search -- " + tk.Quote(e.searchfor) + " insert+1c")
	if ix == "" || isErr(ix) {
		return false
	}
	n := strconv.Itoa(utf8.RuneCountInString(e.searchfor))
	e.cmd(".b.t tag remove sel 0.0 end")
	e.cmd(".b.t mark set anchor " + ix)
	e.cmd(".b.t mark set insert " + ix)
	e.cmd(".b.t tag add sel " + ix + " " + ix + "+" + n + "c")
	if prompt != noSee {
		e.cmd(".b.t see " + ix)
	}
	return true
}

func (e *editor) doReplace(prompt int) {
	r := ""
	if prompt == replaceWith {
		e.replacewith = e.ui.GetString("Replacement String")
		r = e.cmd(".b.t tag nextrange sel 1.0")
		if r == "" || isErr(r) {
			return
		}
	}
	if r != "" {
		e.cmd(".b.t mark set insert sel.first")
	} else if e.searchfor == "" {
		if !e.doSearch(searchFor) {
			return
		}
	} else if !e.doSearch(search) {
		return
	}
	e.cmd(".b.t delete sel.first sel.last")
	e.cmd(".b.t insert insert " + tk.Quote(e.replacewith))
	e.setDirty()
}

func (e *editor) doReplaceAll() {
	cur := e.cmd(".b.t index insert")
	if cur == "" || isErr(cur) {
		return
	}
	if e.searchfor == "" {
		e.searchfor = e.ui.GetString("Search For")
	}
	if e.searchfor == "" {
		return
	}
	srch, repl := tk.Quote(e.searchfor), tk.Quote(e.replacewith)
	ns := strconv.Itoa(utf8.RuneCountInString(e.searchfor))
	nr := strconv.Itoa(utf8.RuneCountInString(e.replacewith))
	dirt := 0
	for ix := "1.0"; ix != "" && !isErr(ix); {
		ix = e.cmd(".b.t search -- " + srch + " " + ix + " end")
		if ix == "" || isErr(ix) {
			break
		}
		e.cmd(".b.t delete " + ix + " " + ix + "+" + ns + "c")
		if e.replacewith != "" {
			e.cmd(".b.t insert " + ix + " " + repl)
			ix = e.cmd(".b.t index " + ix + "+" + nr + "c")
		}
		dirt++
	}
	e.cmd(".b.t mark set insert " + cur)
	if dirt > 0 {
		e.setDirty()
	}
}

// doGoto asks for a line number, selects that line and shows it.
func (e *editor) doGoto() {
	s := strings.TrimSpace(e.ui.GetString("Line"))
	n, err := strconv.Atoi(s)
	if s == "" || err != nil || n < 1 {
		return
	}
	ix := strconv.Itoa(n) + ".0"
	e.cmd(".b.t tag remove sel 1.0 end")
	e.cmd(".b.t mark set insert " + ix)
	e.cmd(".b.t mark set anchor " + ix)
	e.cmd(".b.t tag add sel {" + ix + " linestart} {" + ix + " lineend}")
	e.cmd(".b.t see insert")
}

// loadtfile reads path into the text and returns an error message, or
// "" if all went well.
func (e *editor) loadtfile(path string) string {
	if path != "" && path[0] == '/' {
		e.basepath(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "Can't open " + path + ", the error was:\n" + errstr(err)
	}
	defer f.Close()
	d, err := f.Stat()
	if err != nil {
		return "Can't stat " + path + ", the error was:\n" + errstr(err)
	}
	if d.IsDir() {
		return path + " is a directory"
	}
	buf := make([]byte, 8192+utf8.UTFMax)
	inset := 0
	for {
		n, err := f.Read(buf[inset : inset+8192])
		if n <= 0 || err != nil && n == 0 {
			break
		}
		n += inset
		// Keep a partial rune for the next read.
		nutf := n
		if i := lastStart(buf[:n]); !utf8.FullRune(buf[i:n]) {
			nutf = i
		}
		e.cmd(".b.t insert end " + tk.Quote(string(buf[:nutf])))
		inset = copy(buf, buf[nutf:n])
	}
	if e.isLimbo {
		e.markKeywComm()
	}
	e.curfile = path
	e.settitle()
	e.cmd("update")
	return ""
}

// lastStart is where the last rune in b starts.
func lastStart(b []byte) int {
	i := len(b) - 1
	for i > 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	return max(i, 0)
}

// errstr is an error as Inferno's %r shows it.
func errstr(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

func (e *editor) savetfile(path, contents string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
	if err != nil {
		return fmt.Errorf("%s", errstr(err))
	}
	defer f.Close()
	if _, err := f.Write([]byte(contents)); err != nil {
		return fmt.Errorf("%s", errstr(err))
	}
	e.curfile = path
	e.settitle()
	return nil
}

func (e *editor) doLimbo() {
	e.isLimbo = !e.isLimbo
	if e.isLimbo {
		e.markKeywComm()
	} else {
		e.cmd(".b.t tag remove comment 1.0 end")
		e.cmd(".b.t tag remove keyword 1.0 end")
	}
}

// limboCheck follows typing in Limbo mode: it tags keywords and
// comments and indents new lines (do_limbo_check). s is "dirtied {c}".
func (e *editor) limboCheck(s string) {
	if !e.isLimbo || len(s) < 11 {
		return
	}
	rs := []rune(s)
	c := rs[9]
	if c == '\\' && len(rs) > 10 {
		c = rs[10]
	}
	switch {
	case c == ' ' || c == '\t':
		if !e.inComment {
			e.lookKeyword(e.thisWord)
		}
		e.thisWord = ""
	case c == '\n':
		if e.inComment {
			e.cmd(".b.t tag remove comment insert-1chars")
			e.inComment = false
		} else {
			e.lookKeyword(e.thisWord)
		}
		e.thisWord = ""
		if e.lastKwIsBlock {
			e.indent = append([]int{temp}, e.indent...)
		} else {
			for len(e.indent) > 0 && e.indent[0] == temp {
				e.indent = e.indent[1:]
			}
		}
		e.lastKwIsBlock = false
		e.addIndent()
		e.firstChar = true
		return
	case c == '{':
		e.indent = append([]int{block}, e.indent...)
		e.lastKwIsBlock = false
	case c == '}':
		if len(e.indent) > 0 {
			e.indent = e.indent[1:]
		}
		e.lastKwIsBlock = false
		// A line of just indentation and } loses one indent.
		if e.firstChar {
			cur := e.cmd(".b.t index insert")
			line := cur
			if i := strings.IndexByte(cur, '.'); i >= 0 {
				line = cur[:i]
			}
			e.cmd(".b.t delete " + line + ".0 insert")
			e.addIndent()
			e.cmd(".b.t insert insert '}")
		}
	case c == '#':
		e.inComment = true
	case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_':
		if !e.inComment {
			e.thisWord += string(c)
		}
	default:
		if !e.inComment {
			e.lookKeyword(e.thisWord)
		}
		e.thisWord = ""
	}
	if e.inComment {
		e.cmd(".b.t tag add comment insert-1chars")
	}
	e.firstChar = false
}

func (e *editor) lookKeyword(word string) {
	if e.isKeyword(word) {
		e.cmd(".b.t tag add keyword insert-" + strconv.Itoa(len(e.thisWord)+1) + "chars insert-1chars")
	}
}

func (e *editor) isKeyword(word string) bool {
	for i, k := range keywords {
		if word == k {
			if i != 26 { // not for nil
				e.lastKwIsBlock = blockKeyword>>i&1 != 0
			}
			return true
		}
	}
	return false
}

// markKeywComm tags the keywords and comments of the whole text.
func (e *editor) markKeywComm() {
	last, _ := strconv.Atoi(strings.SplitN(e.cmd(".b.t index end"), ".", 2)[0])
	for i := 1; i <= last; i++ {
		var quote rune
		word := ""
		start := 0
		notkey := false
		line := []rune(tk.Cmd(e.top, ".b.t get "+strconv.Itoa(i)+".0 "+strconv.Itoa(i+1)+".0"))
	scan:
		for j := 0; j < len(line); j++ {
			c := line[j]
			if quote != 0 && c != quote {
				continue
			}
			switch {
			case c == '#':
				e.cmd(fmt.Sprintf(".b.t tag add comment %d.%d %d.%d", i, j, i, len(line)))
				break scan
			case c == '\'' || c == '"':
				if j != 0 && line[j-1] == '\\' {
					break
				}
				if c == quote {
					quote = 0
				} else {
					quote = c
				}
				word = ""
			case c >= 'a' && c <= 'z':
				if word == "" {
					start = j
				}
				word += string(c)
			case c >= 'A' && c <= 'Z' || c == '_':
				notkey = true
			default:
				if !notkey && e.isKeyword(word) {
					e.cmd(fmt.Sprintf(".b.t tag add keyword %d.%d %d.%d", i, start, i, j))
				}
				word = ""
				notkey = false
			}
		}
	}
}

func (e *editor) doIndent() {
	e.tab = e.ui.GetString("single indent")
	for i := 1; i <= 8; i++ {
		e.tabs[i] = collapse(strings.Repeat(e.tab, i))
	}
}

// collapse turns runs of eight spaces into tabs.
func collapse(s string) string {
	if strings.HasPrefix(s, "        ") {
		return "\t" + collapse(s[8:])
	}
	return s
}

func (e *editor) addIndent() {
	i := len(e.indent)
	for ; i >= 8; i -= 8 {
		e.cmd(".b.t insert insert '" + e.tabs[8])
	}
	e.cmd(".b.t insert insert '" + e.tabs[i])
}

// reIndent works out the indent and comment state after a click.
func (e *editor) reIndent() {
	pos := e.cmd(".b.t index insert")
	f := strings.SplitN(pos, ".", 2)
	if len(f) < 2 {
		return
	}
	init := tk.Cmd(e.top, ".b.t get "+f[0]+".0 insert")
	i := 8
	for ; i > 0; i-- {
		if strings.HasPrefix(init, e.tabs[i]) {
			break
		}
	}
	e.indent = make([]int, i)
	e.inComment = strings.Contains(init[len(e.tabs[i]):], "#")
}
