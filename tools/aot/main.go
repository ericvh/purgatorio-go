// Command aot compiles modules ahead of time for the kernel: for each
// module root/bin/x.wasm it writes the image root/bin/x.aot beside it
// (vm/image), which holds the native code wazero's compiler makes from
// the module.
//
//	go run ./tools/aot [-j n] [-sums file] [path ...]
//
// Paths are under root/ (bin/sh.wasm); with none it compiles every module
// under root/bin but Go's own tools (root/bin/go). wazero compiles only
// for the host's architecture, and the kernel is arm64, so this runs on
// an arm64 host (Linux or macOS). The images are the same on every such
// host for the same wazero; -sums writes their SHA-256 sums, which CI
// compares between its arm64 runners.
//
// The images are unsigned: the kernel loads them only when booted to
// accept unsigned images (tools/kernel -aot).
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/vm/image"
	"github.com/tetratelabs/wazero"
)

func main() {
	jobs := flag.Int("j", runtime.NumCPU(), "modules to compile at once")
	sums := flag.String("sums", "", "write the images' SHA-256 sums to this file")
	flag.Parse()
	if runtime.GOARCH != "arm64" {
		fail(fmt.Errorf("wazero compiles only for the host, and this host is %s: run on an arm64 host", runtime.GOARCH))
	}
	paths := flag.Args()
	if len(paths) == 0 {
		err := filepath.WalkDir("root/bin", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && p == "root/bin/go" {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(p, ".wasm") {
				rel, _ := filepath.Rel("root", p)
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			fail(err)
		}
	}
	sort.Strings(paths)
	tmp, err := os.MkdirTemp("", "aot")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(tmp)
	sumOf := make([]string, len(paths))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, max(*jobs, 1))
	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			sum, err := build(filepath.Join("root", p), filepath.Join(tmp, fmt.Sprint(i)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p, err)
			}
			sumOf[i] = fmt.Sprintf("%x  %s", sum, strings.TrimSuffix(p, ".wasm")+".aot")
		}()
	}
	wg.Wait()
	if firstErr != nil {
		fail(firstErr)
	}
	fmt.Printf("aot: %d images\n", len(paths))
	if *sums != "" {
		if err := os.WriteFile(*sums, []byte(strings.Join(sumOf, "\n")+"\n"), 0o644); err != nil {
			fail(err)
		}
	}
}

// build compiles the module at path with a compilation cache in dir,
// whose one entry becomes the image, and returns the image's SHA-256.
func build(path, dir string) ([32]byte, error) {
	var zero [32]byte
	code, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	digest, err := image.Digest(code)
	if err != nil {
		return zero, err
	}
	ctx := context.Background()
	cache, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		return zero, err
	}
	// The kernel's engine closes modules when their prog is killed
	// (WithCloseOnContextDone), which is part of the cache key.
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler().
		WithCloseOnContextDone(true).WithCompilationCache(cache))
	defer r.Close(ctx)
	if _, err := r.CompileModule(ctx, code); err != nil {
		return zero, err
	}
	var entries []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			entries = append(entries, p)
		}
		return err
	})
	if err != nil {
		return zero, err
	}
	if len(entries) != 1 {
		return zero, fmt.Errorf("wazero wrote %d cache entries", len(entries))
	}
	entry, err := os.ReadFile(entries[0])
	if err != nil {
		return zero, err
	}
	b := image.Encode(&image.Image{Kind: image.KindWazeroArm64, Module: digest, Code: entry})
	out := strings.TrimSuffix(path, ".wasm") + ".aot"
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return zero, err
	}
	return sha256.Sum256(b), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "aot:", err)
	os.Exit(1)
}
