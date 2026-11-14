package wasmobj

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Frame is one line of /prog/n/stack: Inferno's six fields, for a wasm
// guest (docs/plan.md 5.1).
type Frame struct {
	N      uint64 // frame number counted from the bottom
	Func   uint32 // wasm function index, the pc
	Offset uint64 // code section offset of the pc, or 0
	Path   string // module path, or $module for a host function
}

// Host reports whether the frame is a host function's.
func (f *Frame) Host() bool { return strings.HasPrefix(f.Path, "$") }

// ParseStack reads the text of a stack file, top first. Lines that are
// not frames are skipped, as Inferno's debug module skips them.
func ParseStack(s string) []Frame {
	var fs []Frame
	for _, l := range strings.Split(s, "\n") {
		f := strings.SplitN(l, " ", 6)
		if len(f) != 6 {
			continue
		}
		n, err1 := strconv.ParseUint(f[0], 16, 64)
		fn, err2 := strconv.ParseUint(f[1], 16, 32)
		off, err3 := strconv.ParseUint(f[2], 16, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		fs = append(fs, Frame{N: n, Func: uint32(fn), Offset: off, Path: f[5]})
	}
	return fs
}

// Namer names frames, reading each module's symbols once.
type Namer struct {
	syms map[string]*Syms
}

// Syms returns the symbols of the module at path, or nil.
func (n *Namer) Syms(path string) *Syms {
	if n.syms == nil {
		n.syms = map[string]*Syms{}
	}
	if s, ok := n.syms[path]; ok {
		return s
	}
	var s *Syms
	if b, err := os.ReadFile(path); err == nil {
		if m, err := Parse(b); err == nil {
			s = m.Syms()
		}
	}
	n.syms[path] = s
	return s
}

// Func returns the function of a frame, or nil.
func (n *Namer) Func(f *Frame) *Func {
	if f.Host() {
		return nil
	}
	return n.Syms(f.Path).Lookup(f.Func)
}

// Line is a frame as Inferno's stack prints it: the function and its
// source, or "unknown fn" and the module and pc. A Go function's
// source is the file and the line it starts at: the guest carries no
// table from a wasm pc to a line.
func (n *Namer) Line(f *Frame) string {
	fn := n.Func(f)
	switch {
	case fn == nil:
		return fmt.Sprintf("unknown fn() Module %s PC %d", f.Path, f.Func)
	case fn.File == "":
		return fmt.Sprintf("%s() Module %s PC %d", fn.Name, f.Path, f.Func)
	}
	return fmt.Sprintf("%s() %s:%d", fn.Name, fn.File, fn.Line)
}
