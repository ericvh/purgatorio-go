// Command fs is Inferno's fs(1): it evaluates an expression whose values
// are file trees, lists of entries, gates, selectors, strings and shell
// commands. Each verb is a process; values flow between them on
// channels, as in Inferno's fs, whose verbs are all here.
//
// A shell command, @{...}, runs in a new sh each time, given $file and
// the other variables as assignments; Inferno's fs runs it in a shell
// context of its own, which keeps variables from one run to the next.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func init() {
	mods = map[string]*fsmod{
		"and":        {"pppp*", andrun},
		"bundle":     {"vx", bundlerun},
		"chstat":     {"xx-pp-ms-us-gs-ts-as-c", chstatrun},
		"compose":    {"ms-d", composerun},
		"depth":      {"ps", depthrun},
		"entries":    {"tx", entriesrun},
		"eval":       {evaltypes(), evalrun},
		"exec":       {"vct-ns-tc-p-P", execrun},
		"filter":     {"xpx-d", filterrun},
		"ls":         {"vt-u-m", lsrun},
		"match":      {"ps-a-r", matchrun},
		"merge":      {"xxxx*-1-cm", mergerun},
		"mergewrite": {"vmsx", mergewriterun},
		"mode":       {"ps", moderun},
		"not":        {"pp", notrun},
		"or":         {"pppp*", orrun},
		"path":       {"pss*-x", pathrun},
		"pipe":       {"vcx-1-p-P", piperun},
		"print":      {"vt", printrun},
		"proto":      {"xs-rs", protorun},
		"query":      {"pc-p-P", queryrun},
		"run":        {"sc", runrun},
		"select":     {"tpt", selectrun},
		"setroot":    {"xsx-c", setrootrun},
		"size":       {"vt", sizerun},
		"unbundle":   {"xs", unbundlerun},
		"void":       {"vv", voidrun},
		"walk":       {"xs-bs", walkrun},
		"write":      {"vsx", writerun},
	}
}

func main() {
	argv := os.Args[1:]
	if len(argv) == 0 {
		usage()
	}
	r := newReport()
	s := argv[0]
	if len(argv) == 1 && s != "" && s[0] == '{' && s[len(s)-1] == '}' {
		s = "void " + s
	} else {
		s = "void {" + s
		for _, a := range argv[1:] {
			if a == "" || a[0] != '{' {
				s += " " + quote.Quote(a)
			} else {
				s += " " + a
			}
		}
		s += "}"
	}
	m := mods["eval"]
	if !typecompat("as", m.types) {
		fmt.Fprintf(os.Stderr, "fs: eval module implements incompatible type (usage: %s)\n", cmdusage("eval", m.types))
		sys.Exits("fail:bad eval module")
	}
	fail := ""
	func() {
		defer func() {
			if x := recover(); x != nil {
				if _, ok := x.(typeerr); !ok {
					panic(x)
				}
				fail = "type conversion error"
			}
		}()
		v := m.run(r, nil, []value{vS{s}})
		if v == nil {
			fail = "error"
		} else {
			vv(v) <- 1
		}
	}()
	r.wait()
	if fail == "type conversion error" {
		sys.Exits(fail)
	}
	if fail != "" {
		sys.Exits("fail:" + fail)
	}
}

func usage() {
	w := os.Stderr
	fmt.Fprintf(w, "usage: fs expression\n")
	fmt.Fprintf(w, "verbs are:\n")
	var names []string
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "\t%s\n", cmdusage(n, mods[n].types))
	}
	fmt.Fprintf(w, "automatic conversions:\n")
	fmt.Fprintf(w, "\tstring -> fs {walk string}\n")
	fmt.Fprintf(w, "\tfs -> entries {entries fs}\n")
	fmt.Fprintf(w, "\tstring -> gate {match string}\n")
	fmt.Fprintf(w, "\tentries -> void {print entries}\n")
	fmt.Fprintf(w, "\tcommand -> string {run command}\n")
	sys.Exits("fail:usage")
}

// mtab and modes format permissions as ls -l does.
var mtab = []string{"---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"}

func modes(mode uint32) string {
	var s string
	switch {
	case mode&sys.DMDIR != 0:
		s = "d"
	case mode&sys.DMAPPEND != 0:
		s = "a"
	case mode&sys.DMAUTH != 0:
		s = "A"
	default:
		s = "-"
	}
	if mode&sys.DMEXCL != 0 {
		s += "l"
	} else {
		s += "-"
	}
	return s + mtab[mode>>6&7] + mtab[mode>>3&7] + mtab[mode&7]
}

func tokenize(s, delim string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delim, r) })
}
