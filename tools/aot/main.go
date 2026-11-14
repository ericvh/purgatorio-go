// Command aot compiles modules ahead of time for the kernel: for each
// module root/bin/x.wasm it writes the image root/bin/x.aot beside it,
// which holds the native code wazero's compiler makes from the module,
// signed (docs/signing.md, "Ahead-of-time images").
//
//	go run ./tools/aot [-j n] [-sums file] [-key seedfile | -classical] [-anchors file] [-o dir] [path ...]
//
// Paths are under root/ (bin/sh.wasm); with none it compiles every module
// under root/bin but Go's own tools (root/bin/go). wazero compiles only
// for the host's architecture, and the kernel is arm64, so this runs on
// an arm64 host (Linux or macOS). The images are the same on every such
// host for the same wazero; -sums writes their SHA-256 sums, which CI
// compares between its arm64 runners.
//
// An image is the Rust sibling's signed image file with a kind of its own,
// 2, for wazero's compiled module (its compilation cache entry) for
// arm64. It is signed with the key in -key, a seed file (32 bytes for an
// Ed25519 key, 64 for a hybrid Ed25519 + ML-DSA-65 one), or else with a
// public test key, hybrid unless -classical. Both signatures are
// deterministic, so the images stay the same on every host. -anchors
// writes the signing key's line for /lib/keys/aot. -o writes the images
// under dir instead of beside the modules.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/tetratelabs/wazero"
)

func main() {
	jobs := flag.Int("j", runtime.NumCPU(), "modules to compile at once")
	sums := flag.String("sums", "", "write the images' SHA-256 sums to this file")
	keyFile := flag.String("key", "", "sign with the key in this seed file (default: the test key)")
	classical := flag.Bool("classical", false, "sign with the classical (Ed25519) test key")
	anchors := flag.String("anchors", "", "write the signing key's line for /lib/keys/aot to this file")
	outDir = flag.String("o", "", "write the images under this directory instead of beside the modules")
	flag.Parse()
	var err error
	if key, err = signingKey(*keyFile, *classical); err != nil {
		fail(err)
	}
	if *anchors != "" {
		line := key.Public().ID().String() + " aot image signer\n"
		if err := os.WriteFile(*anchors, []byte(line), 0o644); err != nil {
			fail(err)
		}
	}
	if runtime.GOARCH != "arm64" {
		fail(fmt.Errorf("wazero compiles only for the host, and this host is %s: run on an arm64 host", runtime.GOARCH))
	}
	paths := flag.Args()
	if len(paths) == 0 {
		err = filepath.WalkDir("root/bin", func(p string, d fs.DirEntry, err error) error {
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
			sum, err := build(p, filepath.Join(tmp, fmt.Sprint(i)))
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

// Kind is the image kind of wazero's compiled module for arm64. The Rust
// sibling uses 0 (wasmtime, aarch64) and 1 (Pulley).
const Kind = 2

var (
	key    *pgauth.PrivateKey
	outDir *string
)

// signingKey returns the key in the seed file, or a test key: anyone can
// make it, so only a test system trusts it.
func signingKey(file string, classical bool) (*pgauth.PrivateKey, error) {
	if file != "" {
		seed, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return pgauth.NewPrivateKey(seed)
	}
	seed := sha512.Sum512([]byte("purgatorio-go public AOT test key"))
	if classical {
		return pgauth.NewPrivateKey(seed[:pgauth.SeedSize])
	}
	return pgauth.NewPrivateKey(seed[:pgauth.HybridSeedSize])
}

// build compiles the module at rel (under root/) with a compilation cache
// in dir, whose one entry becomes the image, and returns the image
// file's SHA-256.
func build(rel, dir string) ([32]byte, error) {
	var zero [32]byte
	path := filepath.Join("root", rel)
	code, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	digest, err := modsig.Digest(code)
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
	b, err := modsig.BuildImage(Kind, digest, entry, key, nil)
	if err != nil {
		return zero, err
	}
	out := strings.TrimSuffix(path, ".wasm") + ".aot"
	if *outDir != "" {
		out = filepath.Join(*outDir, strings.TrimSuffix(rel, ".wasm")+".aot")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return zero, err
		}
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return zero, err
	}
	return sha256.Sum256(b), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "aot:", err)
	os.Exit(1)
}
