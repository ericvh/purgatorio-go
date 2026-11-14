package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// The window manager's menu: a press on the background shows the
// programs that /lib/wmsetup's menu lines name, and releasing on one
// starts it in a process group of its own, as wmsetup's wmrun does.
// Inferno has this menu on wm/toolbar's start button instead.

const wmsetup = "/lib/wmsetup"

type mentry struct {
	label string
	cmd   string
}

type wmmenu struct {
	entries []mentry
	img     *draw.Image
	r       draw.Rectangle
	sel     int
	eh      int // entry height
}

// menuEntries reads the menu lines of /lib/wmsetup: menu label {cmd},
// or menu section label {cmd}, which shows as section/label.
func menuEntries() []mentry {
	b, err := os.ReadFile(wmsetup)
	if err != nil {
		return []mentry{{"Shell", "wm/sh"}}
	}
	var es []mentry
	for _, line := range strings.Split(string(b), "\n") {
		f := wmwords(line)
		if len(f) < 3 || f[0] != "menu" {
			continue
		}
		label := f[1]
		if len(f) > 3 {
			label = f[1] + "/" + f[2]
		}
		c := strings.TrimSpace(f[len(f)-1])
		c = strings.TrimSpace(strings.TrimPrefix(c, "wmrun "))
		if label == "" || c == "" {
			continue
		}
		es = append(es, mentry{label, c})
	}
	return es
}

// wmwords splits a line of sh into words: quoted '...', braced {...}
// or plain, stopping at a comment.
func wmwords(s string) []string {
	var ws []string
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" || s[0] == '#' {
			return ws
		}
		switch s[0] {
		case '\'':
			i := strings.IndexByte(s[1:], '\'')
			if i < 0 {
				return append(ws, s[1:])
			}
			ws = append(ws, s[1:i+1])
			s = s[i+2:]
		case '{':
			depth, i := 0, 0
			for ; i < len(s); i++ {
				if s[i] == '{' {
					depth++
				} else if s[i] == '}' {
					if depth--; depth == 0 {
						break
					}
				}
			}
			if i >= len(s) {
				return append(ws, s[1:])
			}
			ws = append(ws, s[1:i])
			s = s[i+1:]
		default:
			i := strings.IndexAny(s, " \t")
			if i < 0 {
				return append(ws, s)
			}
			ws = append(ws, s[:i])
			s = s[i:]
		}
	}
}

// showMenu puts the menu up with its top left near p.
func (w *wm) showMenu(p draw.Point) {
	es := menuEntries()
	if len(es) == 0 {
		return
	}
	f, err := w.d.GetDefaultFont()
	if err != nil {
		return
	}
	wd := 0
	for _, e := range es {
		wd = max(wd, f.Width(e.label))
	}
	m := &wmmenu{entries: es, sel: -1, eh: f.Height + 4}
	sz := draw.Pt(wd+16, len(es)*m.eh+4)
	scr := w.d.Image.R
	p.X = max(scr.Min.X, min(p.X-sz.X/2, scr.Max.X-sz.X))
	p.Y = max(scr.Min.Y, min(p.Y-m.eh/2, scr.Max.Y-sz.Y))
	m.r = draw.Rectangle{Min: p, Max: p.Add(sz)}
	img, err := w.screen.NewWindow(m.r, draw.Refbackup, 0xEEEEEEFF)
	if err != nil {
		return
	}
	m.img = img
	w.menu = m
	w.drawMenu()
}

func (w *wm) drawMenu() {
	m := w.menu
	black, _ := w.d.Color(0x000000FF)
	white, _ := w.d.Color(0xFFFFFFFF)
	bg, _ := w.d.Color(0xEEEEEEFF)
	m.img.Draw(m.r, bg, nil, draw.ZP)
	m.img.Border(m.r, 1, black, draw.ZP)
	f, _ := w.d.GetDefaultFont()
	for i, e := range m.entries {
		er := draw.Rect(m.r.Min.X+2, m.r.Min.Y+2+i*m.eh, m.r.Max.X-2, m.r.Min.Y+2+(i+1)*m.eh)
		fg := black
		if i == m.sel {
			m.img.Draw(er, black, nil, draw.ZP)
			fg = white
		}
		x := er.Min.X + (er.Dx()-f.Width(e.label))/2
		_, _ = m.img.String(draw.Pt(x, er.Min.Y+2), fg, draw.ZP, f, e.label)
	}
	_ = w.d.Flush(true)
}

// menuPointer follows the pointer while the menu is up; a release ends
// it and starts the program chosen.
func (w *wm) menuPointer(p draw.Pointer) {
	m := w.menu
	sel := -1
	if p.XY.In(m.r) {
		sel = (p.XY.Y - m.r.Min.Y - 2) / m.eh
		if sel < 0 || sel >= len(m.entries) {
			sel = -1
		}
	}
	if sel != m.sel {
		m.sel = sel
		w.drawMenu()
	}
	if p.Buttons != 0 {
		return
	}
	_ = m.img.Free()
	w.menu = nil
	_ = w.d.Flush(true)
	if sel >= 0 {
		w.run(m.entries[sel].cmd)
	}
}

// run starts a command in a process group of its own.
func (w *wm) run(cmd string) {
	_, _ = sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", cmd}, sys.NEWPGRP, nil)
}
