// Command wasmdump prints what is in a wasm module. It has the role
// Inferno's disdump has for Dis files, at the level of the module's
// structure: function bodies are measured, not disassembled. The output
// is the Rust sibling's.
//
//	wasmdump [-hsiemcf] file ...
//
// With no option it prints everything but the function list: -h the
// size, digest and ABI version; -s the sections; -i the imports by
// module; -e the exports; -m the tables, memories, globals and start
// function; -c the custom sections; -f every function with its index,
// body size, type and name.
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wasmobj"
)

type parts struct {
	header, sections, imports, exports, items, custom, funcs bool
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("wasmdump [-hsiemcf] file ...")
	var p parts
	chosen := false
	for c := a.Opt(); c != 0; c = a.Opt() {
		chosen = true
		switch c {
		case 'h':
			p.header = true
		case 's':
			p.sections = true
		case 'i':
			p.imports = true
		case 'e':
			p.exports = true
		case 'm':
			p.items = true
		case 'c':
			p.custom = true
		case 'f':
			p.funcs = true
		default:
			a.Usage()
		}
	}
	if !chosen {
		p = parts{true, true, true, true, true, true, false}
	}
	files := a.Argv()
	if len(files) == 0 {
		a.Usage()
	}
	failed := false
	for _, f := range files {
		if err := dump(f, p); err != "" {
			fmt.Fprintf(os.Stderr, "wasmdump: %s: %s\n", f, err)
			failed = true
		}
	}
	if failed {
		sys.Exits("fail:errors")
	}
}

func dump(file string, p parts) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return sys.Errstr()
	}
	m, err := wasmobj.Parse(b)
	if err != nil {
		return err.Error()
	}
	fmt.Printf("module %s\n", file)
	if p.header {
		fmt.Printf("size %d\n", len(b))
		if info, err := modsig.Inspect(b); err == nil {
			fmt.Printf("digest %s\n", hex.EncodeToString(info.Digest[:]))
			if len(info.ABI) == 0 {
				fmt.Printf("abi -\n")
			} else {
				fmt.Printf("abi %s\n", info.ABI)
			}
		}
	}
	if p.sections {
		fmt.Printf("sections %d\n", len(m.Sections))
		for _, s := range m.Sections {
			l := fmt.Sprintf("\t%s offset %d size %d", s.Kind(), s.Offset, s.Size)
			if s.ID == wasmobj.SecCustom {
				l += " " + s.Name
			}
			fmt.Println(l)
		}
	}
	if p.imports {
		imports(m)
	}
	if p.exports {
		fmt.Printf("exports %d\n", len(m.Exports))
		for _, e := range m.Exports {
			l := fmt.Sprintf("\t%s %d %s", wasmobj.KindName(e.Kind), e.Index, e.Name)
			if e.Kind == wasmobj.KFunc {
				if t, ok := m.FuncType(e.Index); ok {
					l += " " + t.String()
				}
			}
			fmt.Println(l)
		}
	}
	if p.items {
		items(m)
	}
	if p.custom {
		custom(b, m)
	}
	if p.funcs {
		funcs(m)
	}
	return ""
}

// imports prints the imports under the module each comes from, in
// order of first use, each with its index among the items of its kind.
func imports(m *wasmobj.Module) {
	fmt.Printf("imports %d\n", len(m.Imports))
	var mods []string
	seen := map[string]bool{}
	for _, im := range m.Imports {
		if !seen[im.Module] {
			seen[im.Module] = true
			mods = append(mods, im.Module)
		}
	}
	for _, mod := range mods {
		fmt.Printf("\t%s\n", mod)
		next := map[byte]int{}
		for _, im := range m.Imports {
			index := next[im.Kind]
			next[im.Kind]++
			if im.Module != mod {
				continue
			}
			var ty string
			switch im.Kind {
			case wasmobj.KFunc:
				if int(im.Type) < len(m.Types) {
					ty = m.Types[im.Type].String()
				} else {
					ty = fmt.Sprintf("type %d", im.Type)
				}
			case wasmobj.KTable:
				ty = wasmobj.ValType(im.Table.Elem) + " " + im.Table.Limits.String()
			case wasmobj.KMemory:
				ty = im.Mem.String()
			case wasmobj.KGlobal:
				ty = im.Global.String()
			case wasmobj.KTag:
				ty = fmt.Sprintf("type %d", im.Type)
			}
			fmt.Printf("\t\t%s %d %s %s\n", wasmobj.KindName(im.Kind), index, im.Name, ty)
		}
	}
}

func items(m *wasmobj.Module) {
	fmt.Printf("functions %d imported %d\n", len(m.Funcs), m.Imported(wasmobj.KFunc))
	base := m.Imported(wasmobj.KTable)
	for i, t := range m.Tables {
		fmt.Printf("table %d %s %s\n", base+i, wasmobj.ValType(t.Elem), t.Limits)
	}
	base = m.Imported(wasmobj.KMemory)
	for i, l := range m.Mems {
		fmt.Printf("memory %d %s\n", base+i, l)
	}
	base = m.Imported(wasmobj.KGlobal)
	for i, g := range m.Globals {
		init := "-"
		if g.HasInit {
			init = fmt.Sprint(g.Init)
		}
		fmt.Printf("global %d %s %s\n", base+i, g, init)
	}
	if m.Start < 0 {
		fmt.Printf("start -\n")
	} else {
		fmt.Printf("start %d\n", m.Start)
	}
	fmt.Printf("elements %d\n", m.Elements)
	fmt.Printf("data %d\n", len(m.Datas))
}

func custom(b []byte, m *wasmobj.Module) {
	nsig := 0
	for _, s := range m.Sections {
		if s.ID != wasmobj.SecCustom {
			continue
		}
		switch s.Name {
		case abi.Section:
			fmt.Printf("custom %s %s\n", s.Name, s.Data)
		case "name":
			if n, ok := wasmobj.NameSection(s.Data); ok {
				fmt.Printf("custom %s functions %d\n", s.Name, len(n))
			} else {
				fmt.Printf("custom %s bad\n", s.Name)
			}
		case modsig.Section:
			nsig++
			es, ok := modsig.Parse(s.Data)
			if !ok {
				fmt.Printf("custom %s bad\n", s.Name)
				continue
			}
			fmt.Printf("custom %s signatures %d\n", s.Name, len(es))
			for _, e := range es {
				l := "\tsigner " + hex.EncodeToString(e.Key)
				for _, c := range e.Certs {
					l += " " + c.Name
				}
				fmt.Println(l)
			}
		default:
			fmt.Printf("custom %s size %d\n", s.Name, len(s.Data))
		}
	}
	// More than one signature section is refused by the loader.
	if nsig > 1 {
		fmt.Printf("custom %s repeated\n", modsig.Section)
	}
}

// funcs prints every function. A Go guest's are named from its own
// symbol table; the Go linker makes several wasm functions of some Go
// functions, which then share a name.
func funcs(m *wasmobj.Module) {
	syms := m.Syms()
	nimp := m.Imported(wasmobj.KFunc)
	var imps []wasmobj.Import
	for _, im := range m.Imports {
		if im.Kind == wasmobj.KFunc {
			imps = append(imps, im)
		}
	}
	for i := 0; i < nimp+len(m.Funcs); i++ {
		ty := "?"
		if t, ok := m.FuncType(uint32(i)); ok {
			ty = t.String()
		}
		size := "import"
		name := "-"
		if i < nimp {
			name = imps[i].Module + "." + imps[i].Name
		} else if j := i - nimp; j < len(m.Bodies) {
			size = fmt.Sprint(m.Bodies[j])
		} else {
			size = "-"
		}
		if f := syms.Lookup(uint32(i)); f != nil {
			name = f.Name
		}
		fmt.Printf("func %d %s %s %s\n", i, size, ty, name)
	}
}
