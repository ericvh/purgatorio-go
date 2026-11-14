package main

import (
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// internal is sh's own builtins module (sh.y's myselfbuiltin).
type internal struct{}

var myself Module = &internal{}

// modules are the builtin modules compiled into sh, which load finds
// by name instead of loading /dis/sh/<name>.dis.
var modules = map[string]Module{
	"std":    stdmod,
	"string": stringmod,
	"expr":   exprmod,
}

const builtinpath = "/dis/sh"

func (*internal) initbuiltin(c *Context) string {
	for _, n := range []string{"load", "unload", "loaded", "builtin", "syncenv", "whatis", "run", "exit", "@"} {
		c.addbuiltin(n, myself)
	}
	for _, n := range []string{"loaded", "quote", "bquote", "unquote", "builtin"} {
		c.addsbuiltin(n, myself)
	}
	return ""
}

func (*internal) whatis(*Context, string, int) string { return "" }

func (*internal) runsbuiltin(c *Context, argv []*Listnode) []*Listnode {
	switch argv[0].word {
	case "loaded":
		var v []*Listnode
		for _, b := range c.env.bmods {
			v = append([]*Listnode{wordnode(b.name)}, v...)
		}
		return v
	case "bquote":
		return []*Listnode{wordnode(quoted(argv[1:], false))}
	case "quote":
		return []*Listnode{wordnode(quoted(argv[1:], true))}
	case "unquote":
		argv = argv[1:]
		if len(argv) != 1 {
			builtinusage(c, "unquote arg")
		}
		return stringlist(unquoted(word(argv[0])))
	case "builtin":
		if len(argv) < 2 {
			builtinusage(c, "builtin command [args ...]")
		}
		name := argv[1].word
		_, mods := c.env.sbuiltins.find(name)
		for _, m := range mods {
			if m == myself {
				return m.runsbuiltin(c, argv[1:])
			}
		}
		c.fail("builtin not found", "sh: builtin "+name+" not found")
	}
	return nil
}

func (*internal) runbuiltin(c *Context, args []*Listnode, last bool) string {
	switch args[0].word {
	case "load":
		return builtinLoad(c, args)
	case "loaded":
		for _, e := range c.env.builtins.ba {
			c.print(e.name + "\t" + modname(c, e.mods[0]) + "\n")
		}
		for _, e := range c.env.sbuiltins.ba {
			c.print("${" + e.name + "}\t" + modname(c, e.mods[0]) + "\n")
		}
	case "unload":
		if len(args) < 2 {
			builtinusage(c, "unload path...")
		}
		status := ""
		for _, a := range args[1:] {
			if s := unloadmodule(c, a.word); s != "" {
				status = s
			}
		}
		return status
	case "builtin":
		return builtinBuiltin(c, args, last)
	case "whatis":
		if len(args) < 2 {
			builtinusage(c, "whatis name ...")
		}
		err := ""
		for _, a := range args[1:] {
			if e := whatisit(c, a); e != "" {
				err = e
			}
		}
		return err
	case "run":
		return builtinRun(c, args)
	case "exit":
		sys.Exits("")
	case "syncenv":
		export(c.env.localenv)
	case "@":
		return builtinSubsh(c, args)
	}
	return ""
}

func builtinusage(c *Context, s string) {
	c.fail("usage", "sh: usage: "+s)
}

// load raises an exception on failure, as most scripts need the module
// they load.
func builtinLoad(c *Context, args []*Listnode) string {
	if len(args) < 2 || args[1].word == "" {
		builtinusage(c, "load path...")
	}
	for _, a := range args[1:] {
		if s := loadmodule(c, a.word); s != "" {
			raise("fail:" + s)
		}
	}
	return ""
}

func builtinRun(c *Context, args []*Listnode) (status string) {
	if len(args) < 2 || args[1].word == "" {
		builtinusage(c, "run path")
	}
	c.push()
	e, raised := catch(func() {
		c.setoptions(optINTERACTIVE, false)
		runscript(c, args[1].word, args[2:], true)
	})
	c.pop()
	if raised {
		if !isfail(e) {
			raise(e)
		}
		return failurestatus(e)
	}
	return ""
}

func builtinSubsh(c *Context, args []*Listnode) string {
	if len(args) < 2 {
		return ""
	}
	j := runasync(c, args[1:], &Redirlist{})
	status := waitfor(c, []job{j})
	if j.exc != "" {
		raise(j.exc)
	}
	return status
}

// builtinBuiltin runs a command ignoring definitions by loaded modules.
func builtinBuiltin(c *Context, args []*Listnode, last bool) string {
	if len(args) < 2 {
		builtinusage(c, "builtin command [args ...]")
	}
	name := args[1].word
	if name == "" || name[0] == '{' {
		c.diagnostic(name + " not found")
		return "not found"
	}
	_, mods := c.env.builtins.find(name)
	for _, m := range mods {
		if m == myself {
			return m.runbuiltin(c, args[1:], last)
		}
	}
	if c.options()&optEXECPRINT != 0 {
		c.eprint(quoted(args[1:], false) + "\n")
	}
	return runexternal(c, args[1:])
}

func whatisit(c *Context, el *Listnode) string {
	if el.cmd != nil {
		c.print(cmd2string(el.cmd) + "\n")
		return ""
	}
	name := el.word
	if name != "" && name[0] == '{' {
		c.print(name + "\n")
		return ""
	}
	if name == "" {
		return ""
	}
	w := ""
	if val := c.get(name); val != nil {
		w += quote(name, false) + "=" + quoted(val, false) + "\n"
	}
	if _, mods := c.env.sbuiltins.find(name); mods != nil {
		mod := mods[0]
		if mod == myself {
			w += "${builtin " + name + "}\n"
		} else {
			mw := mod.whatis(c, name, wSBUILTIN)
			if mw == "" {
				mw = "${" + name + "}"
			}
			w += "load " + modname(c, mod) + "; " + mw + "\n"
		}
	}
	if _, mods := c.env.builtins.find(name); mods != nil {
		mod := mods[0]
		if mod == myself {
			c.print("builtin " + name + "\n")
		} else {
			mw := mod.whatis(c, name, wBUILTIN)
			if mw == "" {
				mw = name
			}
			w += "load " + modname(c, mod) + "; " + mw + "\n"
		}
	} else {
		ismod := strings.HasSuffix(name, modsuffix)
		var pathlist []string
		if len(name) >= 2 && (name[0] == '/' || name[:2] == "./") {
			pathlist = []string{""}
		} else if pl := c.get("path"); pl != nil {
			pathlist = words(pl)
		} else {
			pathlist = []string{"/bin", "."}
		}
		for _, dir := range pathlist {
			path := name
			if dir != "" {
				path = dir + "/" + name
			}
			if !ismod {
				if d, err := sys.Stat(path); err == nil && executable(d, 0o111) {
					w += path + "\n"
					break
				}
				path += modsuffix
			}
			if d, err := sys.Stat(path); err == nil && executable(d, 0o444) {
				w += path + "\n"
				break
			}
		}
	}
	for _, b := range c.env.bmods {
		if mw := b.mod.whatis(c, name, wOTHER); mw != "" {
			w += "load " + b.name + "; " + mw + "\n"
		}
	}
	if w == "" {
		c.eprint(name + ": not found\n")
		return "not found"
	}
	c.print(w)
	return ""
}

func executable(d *sys.Dir, mode uint32) bool {
	return d.Mode&sys.DMDIR == 0 && d.Mode&mode != 0
}

// loadmodule adds the builtins of the module name. Loading a module
// that is already loaded does nothing.
func loadmodule(c *Context, name string) string {
	for _, b := range c.env.bmods {
		if b.name == name {
			return ""
		}
	}
	path := name
	if !strings.HasSuffix(path, ".dis") {
		path += ".dis"
	}
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "./") {
		path = builtinpath + "/" + path
	}
	base := strings.TrimSuffix(path[strings.LastIndexByte(path, '/')+1:], ".dis")
	mod := modules[base]
	if mod == nil || path != builtinpath+"/"+base+".dis" {
		err := "file does not exist"
		if _, e := sys.Stat(path); e == nil {
			err = "exec header invalid"
		} else if s := sys.Errstr(); s != "" {
			err = s
		}
		c.diagnostic("load: cannot load " + path + ": " + err)
		return "bad module"
	}
	s := mod.initbuiltin(c)
	c.env.bmods = append([]bmod{{name, mod}}, c.env.bmods...)
	if s != "" {
		unloadmodule(c, name)
		c.diagnostic("load: module init failed: " + s)
	}
	return s
}

func unloadmodule(c *Context, name string) string {
	var mod Module
	var bl []bmod
	for _, b := range c.env.bmods {
		if b.name == name {
			mod = b.mod
		} else {
			bl = append(bl, b)
		}
	}
	if mod == nil {
		c.diagnostic("module " + name + " not found")
		return "not found"
	}
	c.env.bmods = bl
	c.env.builtins.removemod(mod)
	c.env.sbuiltins.removemod(mod)
	return ""
}
