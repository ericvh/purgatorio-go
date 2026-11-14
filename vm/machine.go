package vm

import (
	"context"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
)

// CommandPath maps a command name to its binary, as Inferno's sh does with
// /dis: a name that starts with /, ./, ../ or # is used as given; any
// other name is looked up as /bin/<name>.wasm.
func CommandPath(cmd string) string {
	if strings.HasPrefix(cmd, "/") || strings.HasPrefix(cmd, "#") ||
		strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, "../") {
		return cmd
	}
	return "/bin/" + cmd + ".wasm"
}

// ModuleName is the prog name in wait messages: the base name without
// .wasm.
func ModuleName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(path, ".wasm")
}

// Load reads the module at path through p's name space.
func (mc *Machine) Load(p *core.Prog, path string) ([]byte, error) {
	c, err := p.OpenChan(path, abi.OREAD)
	if err != nil {
		return nil, err
	}
	defer c.DecRef()
	if c.IsDir() {
		return nil, core.Error(abi.Eisdir)
	}
	// Read straight into a buffer of the file's length, when the device
	// gives one, rather than through a smaller one that is copied and
	// grown: a module is a few megabytes and is read at every spawn.
	size := 64 * 1024
	if d, err := c.Dev.Stat(p.Ctx, c); err == nil && d.Length > 0 && d.Length < 1<<30 {
		size = int(d.Length) + 1
	}
	out := make([]byte, 0, size)
	var off int64
	for {
		if len(out) == cap(out) {
			out = append(out, 0)[:len(out)]
		}
		n, err := c.Dev.Read(p.Ctx, c, out[len(out):cap(out)], off)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		out = out[:len(out)+n]
		off += int64(n)
	}
	if len(out) < 8 || string(out[:4]) != "\x00asm" {
		return nil, core.Error(abi.Ebadexec)
	}
	return out, nil
}

// InitFds opens the standard fds a first prog starts with: 0, 1 and 2 on
// /dev/cons.
func InitFds(p *core.Prog) error {
	for _, f := range []struct {
		path string
		mode int
	}{{"/dev/cons", abi.OREAD}, {"/dev/cons", abi.OWRITE}, {"/dev/cons", abi.OWRITE}} {
		if _, err := p.Open(f.path, f.mode); err != nil {
			return err
		}
	}
	return nil
}

// InitPreopens installs the WASI preopens, / at fd 3 and . at fd 4. These
// two numbers are reserved in every prog.
func InitPreopens(p *core.Prog) error {
	for _, x := range []struct {
		fd   int
		path string
	}{{abi.PreopenRootFD, "/"}, {abi.PreopenDotFD, "."}} {
		c, err := p.OpenChan(x.path, abi.OREAD)
		if err != nil {
			return err
		}
		if err := p.Fgrp().Install(x.fd, c); err != nil {
			c.DecRef()
			return err
		}
	}
	return nil
}

// Run runs the module at path in prog p until it exits and returns its
// Inferno status. The prog is not exited.
func (mc *Machine) Run(p *core.Prog, path string, argv []string) (string, error) {
	code, err := mc.Load(p, path)
	if err != nil {
		return "", core.Error(path + ": " + core.Errstr(err))
	}
	return mc.RunCode(p, path, code, argv)
}

// RunCode runs an already loaded module.
func (mc *Machine) RunCode(p *core.Prog, path string, code []byte, argv []string) (string, error) {
	if err := InitPreopens(p); err != nil {
		return "", err
	}
	p.SetName(ModuleName(path))
	p.SetState("ready")
	for {
		in := &Instance{M: mc, Prog: p, Args: argv}
		if p.Note.Killed() {
			return "killed", nil
		}
		err := mc.Engine.Run(p.Ctx, path, code, in)
		in.closeMods()
		if p.Note.Killed() {
			return "killed", nil
		}
		if ex := in.Exec; ex != nil {
			path, code, argv = ex.Path, ex.Code, ex.Argv
			p.SetName(ModuleName(path))
			continue
		}
		if err != nil {
			return "", core.Error(path + ": " + err.Error())
		}
		return in.Status, nil
	}
}

// Spawn starts path in a new prog on its own goroutine, sharing parent's
// groups except as flags say, and returns its pid. The module is loaded
// first, so a missing or bad module is the caller's error.
func (mc *Machine) Spawn(parent *core.Prog, path string, argv []string, flags uint32, fdmap [][2]int) (int, error) {
	for _, m := range fdmap {
		if m[0] == abi.PreopenRootFD || m[0] == abi.PreopenDotFD {
			return -1, core.Error(abi.Ebadarg)
		}
	}
	code, err := mc.Load(parent, path)
	if err != nil {
		return -1, err
	}
	child, err := parent.Spawn(flags, fdmap)
	if err != nil {
		return -1, err
	}
	child.SetName(ModuleName(path))
	go func() {
		status, err := mc.RunCode(child, path, code, argv)
		if err != nil {
			status = core.Errstr(err)
			msg := []byte(ModuleName(path) + ": " + status + "\n")
			_, _ = child.Write(2, msg)
		}
		child.Exit(status)
	}()
	return child.Pid, nil
}

var _ = context.Background
