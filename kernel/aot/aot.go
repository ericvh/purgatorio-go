// Package aot runs modules in the kernel as native code compiled ahead
// of time: wazero's compiler made each image on the build host
// (tools/aot), and the kernel's engine loads its code into executable
// memory (kernel/codemem) instead of interpreting the module.
//
// For each module a prog runs, the engine looks for <path>.aot beside
// <path>.wasm, read through the prog's name space. The image is used
// only if it parses, is wazero's arm64 code for this wazero, names the
// digest of the module the prog loaded, and one of its signatures is
// trusted under /lib/keys/aot and /lib/keys/revoked, as read at boot
// (docs/signing.md, "Ahead-of-time images"). Otherwise the module runs
// on wazero's interpreter, as it would without images. docs/perf.md and
// kernel/README.md have the details.
package aot

import (
	"bytes"
	"errors"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
	"github.com/ericvh/purgatorio-go/vm"
	"github.com/tetratelabs/wazero"
)

// Kind is the image kind of wazero's compiled module for arm64.
const Kind = 2

// Policy says which signatures an image may be trusted by.
type Policy int

const (
	// Hybrid takes hybrid (Ed25519 + ML-DSA-65) signatures only.
	Hybrid Policy = iota
	// Classical takes Ed25519 signatures too.
	Classical
)

// Trust reads the trust lists through read (the boot name space):
// /lib/keys/aot, the anchors, and /lib/keys/revoked. A revocation list
// that exists but does not parse refuses every image.
func Trust(p Policy, read func(string) ([]byte, error), now uint64) *modsig.Trust {
	t := &modsig.Trust{Now: now, Classical: p == Classical}
	if b, err := read("/lib/keys/aot"); err == nil {
		t.Anchors = pgauth.ParseListLenient(b)
	}
	if b, err := read("/lib/keys/revoked"); err == nil {
		var ok bool
		if t.Revoked, ok = pgauth.ParseList(b); !ok {
			t.Damaged = true
		}
	}
	return t
}

// Images implements vm/wazero.Images and holds the images being loaded
// for the compiler's cache (wazero.CacheStore).
type Images struct {
	trust   *modsig.Trust
	version string // wazero's, as its cache entries carry it

	mu      sync.Mutex
	pending map[[32]byte]*entry
	stats   Stats
}

type entry struct {
	code   []byte
	served bool // the compiler read it
	added  bool // the compiler compiled the module itself
}

// Stats count what happened to the modules the engine looked up.
type Stats struct {
	Loaded   int // images loaded
	Missing  int // modules with no image
	Refused  int // images that did not parse, verify or match
	Unloaded int // images the compiler did not take

	// Time spent on the images loaded: reading them, computing the
	// module's digest, verifying the signatures (with the image's
	// SHA-256), and loading the code (wazero's compile from the image,
	// the copy into code memory).
	Read, Digest, Verify, Load time.Duration
}

// New returns the images of a kernel that trusts t.
func New(t *modsig.Trust) *Images {
	return &Images{trust: t, version: wazeroVersion(), pending: map[[32]byte]*entry{}}
}

// Stats returns the counts so far.
func (im *Images) Stats() Stats {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.stats
}

func (im *Images) count(f func(*Stats)) {
	im.mu.Lock()
	f(&im.stats)
	im.mu.Unlock()
}

// Prepare implements vm/wazero.Images.
func (im *Images) Prepare(path string, code []byte, in *vm.Instance) func() bool {
	if !strings.HasSuffix(path, ".wasm") {
		im.count(func(s *Stats) { s.Missing++ })
		return nil
	}
	t0 := time.Now()
	b, err := vm.ReadFile(in.Prog, strings.TrimSuffix(path, ".wasm")+".aot")
	if err != nil {
		im.count(func(s *Stats) { s.Missing++ })
		return nil
	}
	t1 := time.Now()
	c, t2, err := im.check(b, code)
	if err != nil {
		im.count(func(s *Stats) { s.Refused++ })
		return nil
	}
	t3 := time.Now()
	key := wazero.CacheKey(code, true)
	e := &entry{code: c}
	im.mu.Lock()
	im.pending[key] = e
	im.mu.Unlock()
	return func() bool {
		im.mu.Lock()
		defer im.mu.Unlock()
		delete(im.pending, key)
		ok := e.served && !e.added
		if ok {
			im.stats.Loaded++
			im.stats.Read += t1.Sub(t0)
			im.stats.Digest += t2.Sub(t1)
			im.stats.Verify += t3.Sub(t2)
			im.stats.Load += time.Since(t3)
		} else {
			im.stats.Unloaded++
		}
		return ok
	}
}

var errWazero = errors.New("image for another wazero")

// check returns the code of the image b if it may run as the module
// code, and the time the module's digest was done.
func (im *Images) check(b, code []byte) ([]byte, time.Time, error) {
	m, err := modsig.ParseImage(b)
	if err != nil {
		return nil, time.Time{}, err
	}
	d, err := modsig.Digest(code)
	t := time.Now()
	if err != nil {
		return nil, t, err
	}
	c, err := m.Verify(Kind, d, im.trust)
	if err != nil {
		return nil, t, err
	}
	// wazero's entry starts "WAZEVO", the length of its version and the
	// version; one for another wazero would be compiled again here.
	if len(c) < 7 || string(c[:6]) != "WAZEVO" || len(c) < 7+int(c[6]) || string(c[7:7+int(c[6])]) != im.version {
		return nil, t, errWazero
	}
	return c, t, nil
}

// Get implements wazero.CacheStore.
func (im *Images) Get(key [32]byte) (io.ReadCloser, bool, error) {
	im.mu.Lock()
	defer im.mu.Unlock()
	e, ok := im.pending[key]
	if !ok {
		return nil, false, nil
	}
	e.served = true
	return io.NopCloser(bytes.NewReader(e.code)), true, nil
}

// Add implements wazero.CacheStore: the compiler compiled a module
// itself, so it did not take the image's code.
func (im *Images) Add(key [32]byte, _ io.Reader) error {
	im.mu.Lock()
	defer im.mu.Unlock()
	if e, ok := im.pending[key]; ok {
		e.added = true
	}
	return nil
}

// Delete implements wazero.CacheStore: the compiler found the entry
// stale.
func (im *Images) Delete(key [32]byte) error {
	return im.Add(key, nil)
}

// wazeroVersion is the version wazero writes in its cache entries: the
// module's, from the build information.
func wazeroVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Path == "github.com/tetratelabs/wazero" {
				return d.Version
			}
		}
	}
	return "dev"
}
