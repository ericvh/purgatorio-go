// Package aot runs modules in the kernel as native code compiled ahead
// of time: wazero's compiler made each image on the build host
// (tools/aot), and the kernel's engine loads its code into executable
// memory (kernel/codemem) instead of interpreting the module.
//
// For each module a prog runs, the engine looks for <path>.aot beside
// <path>.wasm, read through the prog's name space. The image is used
// only if it parses, is wazero's arm64 code for this wazero, names the
// digest of the module the prog loaded, and is signed as the image
// policy requires. Otherwise the module runs on wazero's interpreter, as
// it would without images. docs/perf.md and kernel/README.md have the
// details.
package aot

import (
	"bytes"
	"errors"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/vm"
	"github.com/ericvh/purgatorio-go/vm/image"
	"github.com/tetratelabs/wazero"
)

// Policy says which images may run.
type Policy int

const (
	// Signed images only (not yet: no image verifies until module
	// signing is in the kernel).
	Signed Policy = iota
	// Unsigned accepts images with no signature too. The images are
	// then trusted as the kernel's root is.
	Unsigned
)

// Images implements vm/wazero.Images and holds the images being loaded
// for the compiler's cache (wazero.CacheStore).
type Images struct {
	policy  Policy
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

	// Time spent on the images loaded: reading them, checking them
	// (the module's digest and the signatures), and loading the code
	// (wazero's compile from the image, the copy into code memory).
	Read, Check, Load time.Duration
}

// New returns the images of a kernel booted with policy p.
func New(p Policy) *Images {
	return &Images{policy: p, version: wazeroVersion(), pending: map[[32]byte]*entry{}}
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
	c, err := im.check(b, code)
	if err != nil {
		im.count(func(s *Stats) { s.Refused++ })
		return nil
	}
	t2 := time.Now()
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
			im.stats.Check += t2.Sub(t1)
			im.stats.Load += time.Since(t2)
		} else {
			im.stats.Unloaded++
		}
		return ok
	}
}

var errWazero = errors.New("image for another wazero")

// check returns the code of the image b if it may run as the module
// code.
func (im *Images) check(b, code []byte) ([]byte, error) {
	m, err := image.Parse(b)
	if err != nil {
		return nil, err
	}
	if m.Kind != image.KindWazeroArm64 {
		return nil, image.ErrFormat
	}
	d, err := image.Digest(code)
	if err != nil || d != m.Module {
		return nil, image.ErrFormat
	}
	if err := im.verify(m); err != nil {
		return nil, err
	}
	// wazero's entry starts "WAZEVO", the length of its version and the
	// version; one for another wazero would be compiled again here.
	v := m.Code
	if len(v) < 7 || string(v[:6]) != "WAZEVO" || len(v) < 7+int(v[6]) || string(v[7:7+int(v[6])]) != im.version {
		return nil, errWazero
	}
	return m.Code, nil
}

var errUnsigned = errors.New("image not signed")

// verify checks the image's signatures against the policy.
func (im *Images) verify(m *image.Image) error {
	if len(m.Sig) == 0 {
		if im.policy == Unsigned {
			return nil
		}
		return errUnsigned
	}
	return errors.New("image signatures not supported yet")
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
