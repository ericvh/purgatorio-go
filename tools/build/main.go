// Command build compiles every guest for wasip1 and installs it with the
// purgatorio.abi custom section: appl/cmd/<name> as root/bin/<name>.wasm and
// appl/test/<name> as root/bin/test/<name>.wasm, and appl/lib/<name> as a
// loadable module, root/bin/lib/<name>.wasm. A directory under appl/cmd
// with no Go files of its own holds a group of commands, as Inferno's
// /dis/ndb and /dis/auxi: appl/cmd/ndb/query is root/bin/ndb/query.wasm.
//
//	go run ./tools/build
//	go run ./tools/build -tinygo [-o root-tinygo] [-flags '-opt=z -no-debug'] [-j 4] [-keep] [-fill] [-only dir]
//
// Guest builds must be byte-identical on every build host: the flags below
// strip paths, VCS stamps, build IDs and symbols.
//
// -tinygo is an experiment (docs/tinygo.md): it builds the guests with
// TinyGo into a second root, a copy of root without bin, tmp and mnt, so
// both sets exist side by side. A guest TinyGo cannot build is skipped
// and its error is written to <root>/tinygo-failed.txt; -fill installs
// root's standard Go module in its place, for a root with every command.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
)

var (
	tinygo    = flag.Bool("tinygo", false, "build with TinyGo into the root named by -o")
	tinyOut   = flag.String("o", "root-tinygo", "with -tinygo, the root to make")
	tinyFlags = flag.String("flags", "-opt=z -no-debug", "with -tinygo, more flags for tinygo build")
	tinyJobs  = flag.Int("j", 4, "with -tinygo, builds to run at once")
	tinyOnly  = flag.String("only", "", "with -tinygo, build only the guests whose directory has this prefix (appl/cmd/sh)")
	tinyKeep  = flag.Bool("keep", false, "with -tinygo, do not rebuild guests the root already has")
	tinyFill  = flag.Bool("fill", false, "with -tinygo, install root's standard Go module for a guest TinyGo cannot build")
)

func main() {
	flag.Parse()
	if *tinygo {
		tinyBuild()
		return
	}
	build("appl/cmd/*", "root/bin", false)
	build("appl/test/*", "root/bin/test", false)
	build("appl/lib/*", "root/bin/lib", true)
}

// A guest is one package directory and the module it is installed as.
type guest struct {
	dir, out string
	reactor  bool
}

// guests lists the package directories matching pattern as build does.
func guests(pattern, bin string, reactor bool) []guest {
	dirs, err := filepath.Glob(pattern)
	check(err)
	var gs []guest
	for _, d := range dirs {
		name := filepath.Base(d)
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		if gos, _ := filepath.Glob(filepath.Join(d, "*.go")); len(gos) == 0 && !reactor {
			gs = append(gs, guests(filepath.Join(d, "*"), filepath.Join(bin, name), false)...)
			continue
		}
		gs = append(gs, guest{d, filepath.Join(bin, name+".wasm"), reactor})
	}
	return gs
}

// tinyBuild makes the TinyGo root: root's files other than its guests,
// and every guest TinyGo can build.
func tinyBuild() {
	root := *tinyOut
	check(filepath.WalkDir("root", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("root", path)
		switch rel {
		case "bin", "tmp", "mnt", filepath.Join("lib", "go"):
			return filepath.SkipDir
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}))
	gs := guests("appl/cmd/*", filepath.Join(root, "bin"), false)
	gs = append(gs, guests("appl/test/*", filepath.Join(root, "bin", "test"), false)...)
	gs = append(gs, guests("appl/lib/*", filepath.Join(root, "bin", "lib"), true)...)
	var (
		mu     sync.Mutex
		failed = map[string]string{}
		built  int
		wg     sync.WaitGroup
		work   = make(chan guest)
	)
	for range *tinyJobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range work {
				msg, err := tinyOne(g)
				mu.Lock()
				if err != nil {
					failed[g.dir] = msg
					fmt.Println("FAIL", g.dir)
					if *tinyFill {
						rel, _ := filepath.Rel(root, g.out)
						b, err := os.ReadFile(filepath.Join("root", rel))
						check(err)
						check(os.WriteFile(g.out, b, 0o644))
					}
				} else {
					built++
					fmt.Println(g.out)
				}
				mu.Unlock()
			}
		}()
	}
	for _, g := range gs {
		if strings.HasPrefix(g.dir, *tinyOnly) {
			work <- g
		}
	}
	close(work)
	wg.Wait()
	var names []string
	for n := range failed {
		names = append(names, n)
	}
	sort.Strings(names)
	var log strings.Builder
	for _, n := range names {
		fmt.Fprintf(&log, "== %s\n%s\n", n, failed[n])
	}
	list := filepath.Join(root, "tinygo-failed.txt")
	check(os.WriteFile(list, []byte(log.String()), 0o644))
	fmt.Printf("tinygo: %d built, %d failed (%s)\n", built, len(failed), list)
}

// tinyOne builds one guest with TinyGo and returns the compiler's output
// if it fails.
func tinyOne(g guest) (string, error) {
	if _, err := os.Stat(g.out); err == nil && *tinyKeep {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(g.out), 0o755); err != nil {
		return err.Error(), err
	}
	args := []string{"build", "-target=wasip1"}
	args = append(args, strings.Fields(*tinyFlags)...)
	if g.reactor {
		args = append(args, "-buildmode=c-shared")
	}
	cmd := exec.Command("tinygo", append(args, "-o", g.out, "./"+g.dir)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(g.out)
		return string(out), err
	}
	b, err := os.ReadFile(g.out)
	if err != nil {
		return err.Error(), err
	}
	err = os.WriteFile(g.out, appendCustom(b, abi.Section, []byte(abi.Version)), 0o644)
	return "", err
}

// build compiles each package directory matching pattern into bin;
// loadable modules (appl/lib) are built as reactors.
func build(pattern, bin string, reactor bool) {
	dirs, err := filepath.Glob(pattern)
	check(err)
	check(os.MkdirAll(bin, 0o755))
	for _, d := range dirs {
		name := filepath.Base(d)
		out := filepath.Join(bin, name+".wasm")
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		if gos, _ := filepath.Glob(filepath.Join(d, "*.go")); len(gos) == 0 && !reactor {
			build(filepath.Join(d, "*"), filepath.Join(bin, name), false)
			continue
		}
		args := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid="}
		if reactor {
			args = append(args, "-buildmode=c-shared")
		}
		cmd := exec.Command("go", append(args, "-o", out, "./"+d)...)
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		check(cmd.Run())
		b, err := os.ReadFile(out)
		check(err)
		check(os.WriteFile(out, appendCustom(b, abi.Section, []byte(abi.Version)), 0o644))
		fmt.Println(out)
	}
}

// appendCustom adds a WebAssembly custom section (id 0) to a module.
func appendCustom(mod []byte, name string, payload []byte) []byte {
	body := append(uleb(uint32(len(name))), name...)
	body = append(body, payload...)
	mod = append(mod, 0)
	mod = append(mod, uleb(uint32(len(body)))...)
	return append(mod, body...)
}

func uleb(v uint32) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if v == 0 {
			return b
		}
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}
