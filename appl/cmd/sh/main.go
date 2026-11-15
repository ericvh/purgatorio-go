// Command sh is Inferno's shell (sh(1)), with its std, string and expr
// builtin modules compiled in (sh-std(1), sh-string(1), sh-expr(1)).
//
// Inferno's sh runs braced blocks in pipelines, backgrounds and
// backquotes in new processes made with spawn. Here those run in a new
// sh prog that is given the context's modules, $0, $* and exported
// variables; external commands are spawned directly.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const libshellrc = "/lib/sh/profile"

func usage() {
	writefd(2, "usage: sh [-ilexn] [-c command] [file [arg...]]\n")
	raise("fail:usage")
}

func main() {
	if e, raised := catch(shmain); raised {
		sys.Exits(e)
	}
}

func shmain() {
	argv := os.Args
	lflag, nflag := false, false
	ctxtflags := 0
	carg := ""
	if len(argv) > 0 {
		if argv[0] != "" && argv[0][0] == '-' {
			lflag = true
		}
		argv = argv[1:]
	}
	interactive := 0
loop:
	for len(argv) > 0 && argv[0] != "" && argv[0][0] == '-' {
		a := argv[0]
		for i := 1; i < len(a); i++ {
			switch a[i] {
			case 'i':
				interactive = optINTERACTIVE
			case 'l':
				lflag = true
			case 'n':
				nflag = true
			case 'e':
				ctxtflags |= optERROREXIT
			case 'x':
				ctxtflags |= optEXECPRINT
			case 'c':
				var arg string
				if i < len(a)-1 {
					arg = a[i+1:]
				} else if len(argv) < 2 || argv[1] == "" {
					usage()
				} else {
					arg = argv[1]
					argv = argv[1:]
				}
				argv = argv[1:]
				carg = arg
				continue loop
			}
		}
		argv = argv[1:]
	}

	_, _ = sys.Pctl(sys.FORKFD, nil)
	inherited = openfds()
	if !nflag {
		_, _ = sys.Pctl(sys.FORKNS, nil)
	}
	c := newContext()
	c.setoptions(ctxtflags, true)
	if carg != "" {
		status := c.run(stringlist(append([]string{"{" + carg + "}"}, argv...)), interactive == 0)
		if interactive == 0 {
			if status != "" {
				raise("fail:" + status)
			}
			return
		}
		setstatus(c, status)
	}
	if lflag {
		runscript(c, libshellrc, nil, false)
	}
	if len(argv) == 0 {
		if isconsole(0) {
			interactive |= optINTERACTIVE
		}
		c.setoptions(interactive, true)
		runfile(c, 0, "stdin", nil)
	} else {
		c.setoptions(interactive, true)
		runscript(c, argv[0], stringlist(argv[1:]), true)
	}
}

// newContext makes a shell context as Sh->Context.new does: it forks
// the environment group and reads its variables.
func newContext() *Context {
	_, _ = sys.Pctl(sys.FORKENV, nil)
	if waitfd < 0 {
		waitfd = openwait()
	}
	c := &Context{
		env: &Environment{sbuiltins: &Builtins{}, builtins: &Builtins{}, localenv: newlocalenv(nil)},
		fds: newfdtab(),
	}
	myself.initbuiltin(c)
	c.env.localenv.flags = optVERBOSE
	for _, v := range c.get("autoload") {
		if v.cmd == nil && v.word != "" {
			loadmodule(c, v.word)
		}
	}
	return c
}

func isconsole(fd int) bool {
	d1, err1 := sys.Fstat(fd)
	d2, err2 := sys.Stat("/dev/cons")
	if err1 != nil || err2 != nil {
		return false
	}
	return d1.Type == d2.Type && d1.Qid.Path == d2.Qid.Path
}

// runscript runs the commands in the file path.
func runscript(c *Context, path string, args []*Listnode, reporterr bool) {
	e, raised := catch(func() {
		fd, err := sys.Open(path, sys.OREAD)
		if err != nil {
			if reporterr {
				c.fail("bad script path", "sh: cannot open "+path+": "+sys.Errstr())
			}
			return
		}
		defer closefd(fd)
		runfile(c, fd, path, args)
	})
	if raised {
		if !reporterr && isfail(e) {
			return
		}
		raise(e)
	}
}

// runfile runs commands read from fd, prompting for them when the shell
// is interactive.
func runfile(c *Context, fd int, path string, args []*Listnode) {
	c.push()
	e, raised := catch(func() {
		c.setlocal("0", stringlist([]string{path}))
		c.setlocal("*", args)
		lex := newFileLexer(fdreader(fd), path, c.eprint)
		prompt := []string{"", ""}
		laststatus := ""
		for !lex.eof {
			interactive := c.options()&optINTERACTIVE != 0
			if interactive {
				prompt = words(c.get("prompt"))
				if len(prompt) == 0 {
					prompt = []string{"; ", ""}
				}
				c.eprint(prompt[0])
				if len(prompt) == 1 {
					prompt = []string{prompt[0], ""}
				}
			}
			n, err := doparse(lex, prompt[1], !interactive)
			if err != "" {
				c.eprint("sh: " + err + "\n")
				if !interactive {
					raise("fail:parse error")
				}
			} else if n != nil {
				if interactive {
					e2, r2 := catch(func() { laststatus = walk(c, n, false) })
					if r2 {
						if !isfail(e2) {
							raise(e2)
						}
						laststatus = failurestatus(e2)
					}
				} else {
					laststatus = walk(c, n, false)
				}
				setstatus(c, laststatus)
				if c.options()&optERROREXIT != 0 && laststatus != "" {
					break
				}
			}
		}
		if laststatus != "" {
			raise("fail:" + laststatus)
		}
	})
	c.pop()
	if raised {
		raise(e)
	}
}
