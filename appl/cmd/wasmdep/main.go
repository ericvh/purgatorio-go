// Command wasmdep prints what a wasm module needs from the system that
// runs it, as Inferno's disdep lists the modules a Dis file depends on.
// The output is the Rust sibling's.
//
//	wasmdep file ...
//
// For each file it prints the ABI version the module was built for (abi
// - when it has no purgatorio.abi section), then "import module name"
// for each function it imports and "import module name kind" for
// anything else. A module that imports purgatorio's load, spawn_prog or
// exec can start other modules: each path ending in .wasm found in its
// data that is an absolute path is printed as "refers path". That is a
// guess from the module's strings; names a command builds at run time,
// as sh does, are not found. With more than one file each block starts with "module file".
// Inferno's options follow a tree of Dis modules; a wasm module's
// imports are host functions, so there is no tree and no options.
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

func main() {
	a := arg.New(os.Args)
	a.SetUsage("wasmdep file ...")
	if a.Opt() != 0 {
		a.Usage()
	}
	files := a.Argv()
	if len(files) == 0 {
		a.Usage()
	}
	failed := false
	for _, f := range files {
		if len(files) > 1 {
			fmt.Printf("module %s\n", f)
		}
		if err := deps(f); err != "" {
			fmt.Fprintf(os.Stderr, "wasmdep: %s: %s\n", f, err)
			failed = true
		}
	}
	if failed {
		sys.Exits("fail:errors")
	}
}

func deps(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return sys.Errstr()
	}
	m, err := wasmobj.Parse(b)
	if err != nil {
		return err.Error()
	}
	if v, ok := m.Custom(abi.Section); ok {
		fmt.Printf("abi %s\n", v)
	} else {
		fmt.Printf("abi -\n")
	}
	loads := false
	for _, im := range m.Imports {
		if im.Kind == wasmobj.KFunc {
			fmt.Printf("import %s %s\n", im.Module, im.Name)
		} else {
			fmt.Printf("import %s %s %s\n", im.Module, im.Name, wasmobj.KindName(im.Kind))
		}
		if im.Module == "purgatorio" && (im.Name == "load" || im.Name == "spawn_prog" || im.Name == "exec") {
			loads = true
		}
	}
	if loads {
		for _, p := range paths(m.Memory()) {
			fmt.Printf("refers %s\n", p)
		}
	}
	return ""
}

func pathchar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '/' || c == '.' || c == '_' || c == '-' || c == '#'
}

// paths finds the strings ending in .wasm in a module's data, in order
// of first appearance.
func paths(mem []byte) []string {
	var out []string
	seen := map[string]bool{}
	suf := []byte(".wasm")
	for i := 0; ; {
		j := bytes.Index(mem[i:], suf)
		if j < 0 {
			return out
		}
		end := i + j + len(suf)
		s := i + j
		for s > 0 && pathchar(mem[s-1]) {
			s--
		}
		i = end
		// Go packs its strings with nothing between them, so a name
		// may run on from the string before it: keep absolute paths,
		// from their first slash.
		k := bytes.IndexByte(mem[s:end], '/')
		if k < 0 {
			continue
		}
		s += k
		if p := string(mem[s:end]); len(p) > len(suf) && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
}
