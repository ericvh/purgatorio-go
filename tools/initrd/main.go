// Command initrd packs the kernel's root file system: the named files and
// directories under root/ go into kernel/initrd/root.tar.gz, which the
// kernel embeds and serves as #/.
//
//	go run ./tools/initrd [-init cmd] [-engine spec] [-aot] [-cmds a,b] [-files a,b] [path ...]
//
// With no paths it packs lib and the commands the kernel's tests use; a
// Go guest is about 2 MB, so packing all of bin is left to the caller.
// -engine writes lib/kernel/engine, which chooses the kernel's
// WebAssembly engine (kernel/engine.go). -aot compiles every module it
// packs ahead of time (go run ./tools/aot, on an arm64 host) and packs
// each image beside its module.
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	initCmd := flag.String("init", "", "first command for the kernel, written as lib/kernel/init")
	extra := flag.String("cmds", "", "more commands to pack with the default ones, separated by commas")
	extraFiles := flag.String("files", "", "more files under root to pack, separated by commas")
	engine := flag.String("engine", "", "the kernel's engine, written as lib/kernel/engine")
	withAOT := flag.Bool("aot", false, "compile the modules ahead of time and pack their images")
	flag.Parse()
	paths := flag.Args()
	if len(paths) == 0 {
		paths = []string{"lib"}
		cmds := defaultCmds
		if *extra != "" {
			cmds = append(cmds, strings.Split(*extra, ",")...)
		}
		for _, c := range cmds {
			paths = append(paths, "bin/"+c+".wasm")
		}
		if *extraFiles != "" {
			paths = append(paths, strings.Split(*extraFiles, ",")...)
		}
	}
	check(os.MkdirAll("kernel/initrd", 0o755))
	f, err := os.Create("kernel/initrd/root.tar.gz")
	check(err)
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	w := tar.NewWriter(zw)
	// Parent directories of single files come first.
	for _, p := range paths {
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			check(w.WriteHeader(&tar.Header{Name: filepath.ToSlash(d) + "/", Typeflag: tar.TypeDir, Mode: 0o555,
				ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}))
		}
	}
	var files []string
	for _, p := range paths {
		check(filepath.WalkDir(filepath.Join("root", p), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !strings.HasSuffix(path, ".aot") { // images go in with -aot only
				files = append(files, path)
			}
			return nil
		}))
	}
	sort.Strings(files)
	if *withAOT {
		files = addImages(files)
	}
	for _, path := range files {
		fi, err := os.Stat(path)
		check(err)
		name, _ := filepath.Rel("root", path)
		h := &tar.Header{Name: filepath.ToSlash(name), Mode: 0o444, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if fi.IsDir() {
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, h.Name+"/", 0o555
			check(w.WriteHeader(h))
			continue
		}
		b, err := os.ReadFile(path)
		check(err)
		h.Typeflag, h.Size = tar.TypeReg, int64(len(b))
		check(w.WriteHeader(h))
		_, err = w.Write(b)
		check(err)
	}
	if *initCmd != "" || *engine != "" {
		check(w.WriteHeader(&tar.Header{Name: "lib/kernel/", Typeflag: tar.TypeDir, Mode: 0o555,
			ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}))
	}
	for _, f := range []struct{ name, text string }{{"init", *initCmd}, {"engine", *engine}} {
		if f.text == "" {
			continue
		}
		b := []byte(f.text)
		check(w.WriteHeader(&tar.Header{Name: "lib/kernel/" + f.name, Typeflag: tar.TypeReg, Mode: 0o444,
			Size: int64(len(b)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}))
		_, err := w.Write(b)
		check(err)
	}
	check(w.Close())
	check(zw.Close())
	check(f.Close())
	fi, _ := os.Stat("kernel/initrd/root.tar.gz")
	fmt.Printf("kernel/initrd/root.tar.gz: %d files, %d bytes\n", len(files), fi.Size())
}

// addImages compiles the modules among files ahead of time and adds
// their images after each.
func addImages(files []string) []string {
	var mods []string
	for _, f := range files {
		if strings.HasSuffix(f, ".wasm") {
			rel, _ := filepath.Rel("root", f)
			mods = append(mods, rel)
		}
	}
	aot := exec.Command("go", append([]string{"run", "./tools/aot"}, mods...)...)
	aot.Stdout, aot.Stderr = os.Stdout, os.Stderr
	check(aot.Run())
	var out []string
	for _, f := range files {
		out = append(out, f)
		if strings.HasSuffix(f, ".wasm") {
			out = append(out, strings.TrimSuffix(f, ".wasm")+".aot")
		}
	}
	return out
}

// defaultCmds are the commands the kernel's script tests need.
var defaultCmds = []string{"emuinit", "sh", "echo", "cat", "ls", "ps", "wc", "bind", "ns",
	"mkdir", "rm", "date", "sleep", "kill", "pwd", "ramfile", "memfs", "test", "tr", "grep"}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "initrd:", err)
		os.Exit(1)
	}
}
