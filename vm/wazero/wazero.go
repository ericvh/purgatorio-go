// Package wazero runs guests on wazero, a pure-Go WebAssembly runtime, with
// the host functions from package vm.
package wazero

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/vm"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/sys"
)

// Mode selects wazero's execution mode.
type Mode string

const (
	Compiler    Mode = "compiler"    // native code (amd64, arm64)
	Interpreter Mode = "interpreter" // portable, no executable memory
)

type mem struct{ m api.Memory }

func (m mem) Read(off, n uint32) ([]byte, bool) { return m.m.Read(off, n) }
func (m mem) Write(off uint32, b []byte) bool   { return m.m.Write(off, b) }
func (m mem) Size() uint32                      { return m.m.Size() }
func (m mem) Max() (uint32, bool)               { return m.m.Definition().Max() }

func types(ts []vm.Type) []api.ValueType {
	out := make([]api.ValueType, len(ts))
	for i, t := range ts {
		if t == vm.I64 {
			out[i] = api.ValueTypeI64
		} else {
			out[i] = api.ValueTypeI32
		}
	}
	return out
}

type instKey struct{}

// allocator backs an instance's linear memories with buffers whose growth
// is charged through a vm.Meter, which is how the prog and domain memory
// limits hold memory.grow in both wazero modes: a refused grow returns -1
// to the guest.
type allocator struct{ m *vm.Meter }

func (a allocator) Allocate(capacity, _ uint64) experimental.LinearMemory {
	return &linmem{m: a.m, buf: make([]byte, 0, capacity)}
}

type linmem struct {
	m   *vm.Meter
	buf []byte
}

// Reallocate implements experimental.LinearMemory. The memory never
// shrinks, and the spare capacity of a Go slice is zero, so growth within
// it needs no clearing.
func (l *linmem) Reallocate(size uint64) []byte {
	cur := uint64(len(l.buf))
	if size <= cur {
		return l.buf[:size]
	}
	if !l.m.Charge(size - cur) {
		return nil
	}
	if size <= uint64(cap(l.buf)) {
		l.buf = l.buf[:size]
	} else {
		l.buf = append(l.buf, make([]byte, size-cur)...)
	}
	return l.buf
}

func (l *linmem) Free() { l.buf = nil }

// metered charges the module's initial memory to the prog of in and
// returns ctx with the allocator that charges its growth.
func metered(ctx context.Context, code []byte, in *vm.Instance) (context.Context, *vm.Meter, error) {
	m, err := vm.NewMeter(in.Prog, code)
	if err != nil {
		return nil, nil, err
	}
	return experimental.WithMemoryAllocator(ctx, allocator{m}), m, nil
}

// cacheMax is how many compiled modules an engine keeps. A compiled Go
// guest takes tens of megabytes, so the cache is bounded; the least
// recently used module is dropped once no instance is using it.
const cacheMax = 8

// key names a compiled module: its code's hash, and whether it was
// compiled with #P's hooks on every function.
type key struct {
	hash uint64
	prof bool
}

type cached struct {
	code  []byte         // the module, to tell apart two with the same hash
	r     wazero.Runtime // the runtime that compiled it
	cm    wazero.CompiledModule
	used  uint64 // last use, for LRU
	users int    // instances running it
}

// Engine is one wazero runtime shared by every prog. Compiled modules are
// cached by content, as Inferno caches loaded modules. The cache is keyed
// by a seeded hash of the module, which every spawn computes, and a hit
// is checked against the module's bytes: a cryptographic digest of a
// module of a few megabytes was a tenth of a spawn's time.
type Engine struct {
	r      wazero.Runtime
	interp wazero.Runtime // with images: for modules without one
	images Images
	nimg   int // modules compiled from images
	nint   int // modules on interp
	mu     sync.Mutex
	seed   maphash.Seed
	cache  map[key]*cached
	tick   uint64
	loads  sync.Map // api.Module of a loaded module -> its path
	// compiling holds the modules being compiled, each with a channel
	// closed when it is done. A module is compiled once at a time:
	// wazero shares the code of modules with the same content, counting
	// its users, but two compiles at once each store the code with one
	// user, so closing the second copy frees the first one's code.
	compiling map[key]chan struct{}
}

// New returns an engine in the given mode.
func New(mode Mode) (*Engine, error) { return NewWith(Options{Mode: mode}) }

// NewCached is New with compiled code kept in dir (when not empty), so a
// module dropped from the in-memory cache, or compiled by an earlier run,
// loads without being compiled again.
func NewCached(mode Mode, dir string) (*Engine, error) {
	return NewWith(Options{Mode: mode, CacheDir: dir})
}

// Images supplies modules compiled ahead of time (the kernel's AOT
// images) to an engine's compilation cache.
type Images interface {
	// Prepare puts the image of code, a module the prog of in loaded
	// from path, where the compiler's cache finds it, and returns a
	// function to call once the module is compiled, which forgets the
	// image and reports whether the compiler took the code from it. It
	// returns nil when the module has no image that may run.
	Prepare(path string, code []byte, in *vm.Instance) func() bool
}

// Options configure an engine.
type Options struct {
	Mode     Mode
	CacheDir string // compiled code kept on disk, when not empty
	// Cache, when not nil, is the compiler's compilation cache, in
	// place of CacheDir.
	Cache wazero.CompilationCache
	// Images, with Mode Compiler, runs a module as native code only
	// when Images has an image of it; the others run on wazero's
	// interpreter. Cache must be where the images are put.
	Images Images
}

// NewWith returns an engine configured by o.
func NewWith(o Options) (*Engine, error) {
	var cfg wazero.RuntimeConfig
	switch o.Mode {
	case Compiler:
		cfg = wazero.NewRuntimeConfigCompiler()
	case Interpreter:
		cfg = wazero.NewRuntimeConfigInterpreter()
	default:
		return nil, fmt.Errorf("unknown engine %q", o.Mode)
	}
	if o.Images != nil && (o.Mode != Compiler || o.Cache == nil) {
		return nil, errors.New("images need the compiler and a cache")
	}
	cfg = cfg.WithCloseOnContextDone(true)
	switch {
	case o.Cache != nil:
		cfg = cfg.WithCompilationCache(o.Cache)
	case o.CacheDir != "":
		cache, err := wazero.NewCompilationCacheWithDir(o.CacheDir)
		if err != nil {
			return nil, err
		}
		cfg = cfg.WithCompilationCache(cache)
	}
	e := &Engine{seed: maphash.MakeSeed(), cache: map[key]*cached{}, compiling: map[key]chan struct{}{}, images: o.Images}
	var err error
	if e.r, err = e.runtime(cfg); err != nil {
		return nil, err
	}
	if o.Images != nil {
		icfg := wazero.NewRuntimeConfigInterpreter().WithCloseOnContextDone(true)
		if e.interp, err = e.runtime(icfg); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Stats returns how many modules were compiled from images and how many
// ran on the interpreter for want of one, for an engine with images.
func (e *Engine) Stats() (images, interpreted int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nimg, e.nint
}

// runtime makes a runtime with the host modules of package vm.
func (e *Engine) runtime(cfg wazero.RuntimeConfig) (wazero.Runtime, error) {
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	builders := map[string]wazero.HostModuleBuilder{}
	blocking := map[string]bool{}
	for _, f := range vm.HostFuncs() {
		blocking[f.Module+"."+f.Name] = f.MayBlock()
		b, found := builders[f.Module]
		if !found {
			b = r.NewHostModuleBuilder(f.Module)
			builders[f.Module] = b
		}
		f := f
		np := len(f.Params)
		fn := api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			in := ctx.Value(instKey{}).(*vm.Instance)
			// An i32 argument's upper 32 bits are undefined (wazero
			// leaves them set on amd64): clear them so host functions
			// see the value the guest passed.
			for i, t := range f.Params {
				if t == vm.I32 {
					stack[i] &= 0xffffffff
				}
			}
			res, err := f.Call(in, mem{mod.Memory()}, stack[:np])
			var exit *vm.Exit
			if errors.As(err, &exit) {
				panic(sys.NewExitError(exit.Code()))
			}
			var ex *vm.Exec
			if errors.As(err, &ex) {
				in.Exec = ex
				panic(sys.NewExitError(0))
			}
			copy(stack, res)
		})
		b.NewFunctionBuilder().WithGoModuleFunction(fn, types(f.Params), types(f.Results)).Export(f.Name)
	}
	// Host calls that may block are reported to the prog with the
	// guest's stack, for /prog/n/stack and dbgctl.
	lctx := experimental.WithFunctionListenerFactory(ctx, experimental.FunctionListenerFactoryFunc(
		func(def api.FunctionDefinition) experimental.FunctionListener {
			if blocking[def.ModuleName()+"."+def.Name()] {
				return hostListener{e}
			}
			return nil
		}))
	for name, b := range builders {
		if _, err := b.Instantiate(lctx); err != nil {
			return nil, fmt.Errorf("host module %s: %w", name, err)
		}
	}
	return r, nil
}

// hostListener reports host calls to the calling prog.
type hostListener struct{ e *Engine }

func (l hostListener) Before(ctx context.Context, mod api.Module, def api.FunctionDefinition, _ []uint64, si experimental.StackIterator) {
	in, _ := ctx.Value(instKey{}).(*vm.Instance)
	if in == nil {
		return
	}
	if pi, _ := ctx.Value(profKey{}).(*core.ProfInstance); pi != nil {
		pi.Host(true)
	}
	in.Prog.EnterHost(mem{mod.Memory()}, func(fs []core.Frame, text string) []core.Frame {
		path := text
		if p, ok := l.e.loads.Load(mod); ok {
			path = p.(string)
		}
		first := true
		for si.Next() {
			f := si.Function()
			d := f.Definition()
			if d.GoFunction() != nil {
				fs = append(fs, core.Frame{Func: d.Index(), Path: "$" + d.ModuleName()})
				if !first {
					path = text // below a host call: the calling module
				}
			} else {
				fs = append(fs, core.Frame{Func: d.Index(), Offset: f.SourceOffsetForPC(si.ProgramCounter()), Path: path})
			}
			first = false
		}
		return fs
	})
}

func (hostListener) After(ctx context.Context, _ api.Module, _ api.FunctionDefinition, _ []uint64) {
	leaveHost(ctx)
}

func (hostListener) Abort(ctx context.Context, _ api.Module, _ api.FunctionDefinition, _ error) {
	leaveHost(ctx)
}

func leaveHost(ctx context.Context) {
	if in, _ := ctx.Value(instKey{}).(*vm.Instance); in != nil {
		if pi, _ := ctx.Value(profKey{}).(*core.ProfInstance); pi != nil {
			pi.Host(false)
		}
		in.Prog.LeaveHost()
	}
}

type profKey struct{}

// profListener is on every function of a module compiled for #P: it
// tells the profiled instance which function the guest is in.
type profListener struct{}

func (profListener) NewFunctionListener(api.FunctionDefinition) experimental.FunctionListener {
	return profListener{}
}

func (profListener) Before(ctx context.Context, mod api.Module, def api.FunctionDefinition, _ []uint64, _ experimental.StackIterator) {
	if pi, _ := ctx.Value(profKey{}).(*core.ProfInstance); pi != nil {
		pi.Enter(def.Index(), memsize(pi, mod))
	}
}

func (profListener) After(ctx context.Context, mod api.Module, _ api.FunctionDefinition, _ []uint64) {
	if pi, _ := ctx.Value(profKey{}).(*core.ProfInstance); pi != nil {
		pi.Leave(memsize(pi, mod))
	}
}

func (profListener) Abort(ctx context.Context, mod api.Module, _ api.FunctionDefinition, _ error) {
	if pi, _ := ctx.Value(profKey{}).(*core.ProfInstance); pi != nil {
		pi.Leave(memsize(pi, mod))
	}
}

// memsize is the size of the module's memory when the instance counts
// memory growth, else 0.
func memsize(pi *core.ProfInstance, mod api.Module) uint32 {
	if !pi.Mem() || mod == nil {
		return 0
	}
	if m := mod.Memory(); m != nil {
		return m.Size()
	}
	return 0
}

var trapPC = regexp.MustCompile(`\.\$([0-9]+)\(`)

// exception records a trap as the prog's last exception: the function
// index of the trapping frame, the module and wazero's error.
func exception(p *core.Prog, path string, err error) {
	var exit *sys.ExitError
	if err == nil || errors.As(err, &exit) {
		return
	}
	msg, trace, _ := strings.Cut(err.Error(), "\n")
	if i := strings.Index(msg, "wasm error: "); i >= 0 {
		msg = msg[i+len("wasm error: "):]
	}
	var pc uint64
	if m := trapPC.FindStringSubmatch(trace); m != nil {
		pc, _ = strconv.ParseUint(m[1], 10, 32)
	}
	p.SetException(uint32(pc), vm.ModuleName(path), msg)
}

// compile returns the compiled module for code, holding it until release
// is called.
func (e *Engine) compile(ctx context.Context, path string, code []byte, prof bool, in *vm.Instance) (wazero.Runtime, wazero.CompiledModule, func(), error) {
	key := key{maphash.Bytes(e.seed, code), prof}
	e.mu.Lock()
	for {
		if c, ok := e.cache[key]; ok && bytes.Equal(c.code, code) {
			e.tick++
			c.used, c.users = e.tick, c.users+1
			e.mu.Unlock()
			return c.r, c.cm, e.releaser(key), nil
		}
		done, ok := e.compiling[key]
		if !ok {
			break
		}
		e.mu.Unlock()
		<-done
		e.mu.Lock()
	}
	done := make(chan struct{})
	e.compiling[key] = done
	e.mu.Unlock()
	r, cm, err := e.compileNew(ctx, path, code, prof, in)
	e.mu.Lock()
	delete(e.compiling, key)
	close(done)
	if err != nil {
		e.mu.Unlock()
		return nil, nil, nil, err
	}
	if _, ok := e.cache[key]; ok {
		// Another module with the same hash: it goes uncached.
		e.mu.Unlock()
		return r, cm, func() { _ = cm.Close(context.Background()) }, nil
	}
	e.tick++
	e.cache[key] = &cached{code: code, r: r, cm: cm, used: e.tick, users: 1}
	e.evict()
	e.mu.Unlock()
	return r, cm, e.releaser(key), nil
}

// compileNew compiles code, with the profiling listener if prof, and
// returns it with the runtime that compiled it. With images, a module
// is native code only when it has an image the compiler took its code
// from; any other module, and any profiled one, runs on the
// interpreter.
func (e *Engine) compileNew(ctx context.Context, path string, code []byte, prof bool, in *vm.Instance) (wazero.Runtime, wazero.CompiledModule, error) {
	if prof {
		ctx = experimental.WithFunctionListenerFactory(ctx, profListener{})
	}
	if e.images == nil {
		cm, err := compileOn(ctx, e.r, code)
		return e.r, cm, err
	}
	if !prof && in != nil {
		if done := e.images.Prepare(path, code, in); done != nil {
			cm, err := compileOn(ctx, e.r, code)
			used := done()
			if err == nil && used {
				e.mu.Lock()
				e.nimg++
				e.mu.Unlock()
				return e.r, cm, nil
			}
			if err == nil {
				_ = cm.Close(ctx) // compiled here, not loaded from the image
			}
		}
	}
	e.mu.Lock()
	e.nint++
	e.mu.Unlock()
	cm, err := compileOn(ctx, e.interp, code)
	return e.interp, cm, err
}

// compileOn compiles code on r and checks its imports.
func compileOn(ctx context.Context, r wazero.Runtime, code []byte) (wazero.CompiledModule, error) {
	cm, err := r.CompileModule(ctx, code)
	if err != nil {
		return nil, err
	}
	for _, imp := range cm.ImportedFunctions() {
		mod, _, _ := imp.Import()
		if mod != abi.WASIModule && mod != abi.Module {
			_ = cm.Close(ctx)
			return nil, fmt.Errorf("import from unknown module %q", mod)
		}
	}
	return cm, nil
}

func (e *Engine) releaser(key key) func() {
	return func() {
		e.mu.Lock()
		if c, ok := e.cache[key]; ok {
			c.users--
		}
		e.evict()
		e.mu.Unlock()
	}
}

// evict drops idle modules, least recently used first, while the cache
// is over its bound. Called with e.mu held.
func (e *Engine) evict() {
	for len(e.cache) > cacheMax {
		var victim key
		var oldest uint64
		found := false
		for k, c := range e.cache {
			if c.users == 0 && (!found || c.used < oldest) {
				victim, oldest, found = k, c.used, true
			}
		}
		if !found {
			return
		}
		_ = e.cache[victim].cm.Close(context.Background())
		delete(e.cache, victim)
	}
}

// Run implements vm.Engine.
func (e *Engine) Run(ctx context.Context, path string, code []byte, in *vm.Instance) error {
	var pi *core.ProfInstance
	if pr := in.Prog.Kernel.Prof; pr != nil {
		pi = pr.Want(in.Prog, vm.ModuleName(path), path, code)
	}
	r, cm, release, err := e.compile(context.Background(), path, code, pi != nil, in)
	if err != nil {
		if pi != nil {
			pi.Done()
		}
		return err
	}
	defer release()
	if pi != nil {
		defer pi.Done()
	}
	ctx, meter, err := metered(context.WithValue(ctx, instKey{}, in), code, in)
	if err != nil {
		return err
	}
	defer meter.Release()
	if pi != nil {
		ctx = context.WithValue(ctx, profKey{}, pi)
	}
	// Each instance is anonymous, so many progs can run one module.
	mod, err := r.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName("").WithStartFunctions("_start"))
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	var exit *sys.ExitError
	if err != nil && !errors.As(err, &exit) {
		exception(in.Prog, path, err)
		return err
	}
	return nil
}

// Close releases the runtime.
func (e *Engine) Close() error {
	if e.interp != nil {
		_ = e.interp.Close(context.Background())
	}
	return e.r.Close(context.Background())
}

// loaded is a reactor instance in a prog.
type loaded struct {
	e       *Engine
	path    string
	mod     api.Module
	in      *vm.Instance
	call    api.Function
	allo    api.Function
	release func()
	meter   *vm.Meter
}

// Load implements vm.Engine.
func (e *Engine) Load(ctx context.Context, path string, code []byte, in *vm.Instance) (vm.Loaded, error) {
	r, cm, release, err := e.compile(context.Background(), path, code, false, in)
	if err != nil {
		return nil, err
	}
	ctx, meter, err := metered(context.WithValue(ctx, instKey{}, in), code, in)
	if err != nil {
		release()
		return nil, err
	}
	ctx = context.WithValue(ctx, profKey{}, (*core.ProfInstance)(nil))
	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize")
	mod, err := r.InstantiateModule(ctx, cm, cfg)
	if err != nil {
		exception(in.Prog, path, err)
		if mod != nil {
			_ = mod.Close(context.Background())
		}
		meter.Release()
		release()
		return nil, err
	}
	e.loads.Store(mod, path)
	l := &loaded{e: e, path: path, mod: mod, in: in, call: mod.ExportedFunction("purgatorio_call"),
		allo: mod.ExportedFunction("purgatorio_alloc"), release: release, meter: meter}
	if l.call == nil || l.allo == nil {
		l.Close()
		return nil, fmt.Errorf("not a loadable module")
	}
	return l, nil
}

func (l *loaded) put(ctx context.Context, b []byte) (uint32, error) {
	r, err := l.allo.Call(ctx, uint64(len(b)))
	if err != nil {
		return 0, err
	}
	p := uint32(r[0])
	if len(b) > 0 && !l.mod.Memory().Write(p, b) {
		return 0, fmt.Errorf("bad module memory")
	}
	return p, nil
}

// Call implements vm.Loaded: the name and argument are copied into the
// module, purgatorio_call returns ptr<<32|len of its result, whose first
// byte is 0 for success or 1 before an error string.
func (l *loaded) Call(ctx context.Context, name string, in []byte) ([]byte, error) {
	ctx = context.WithValue(ctx, instKey{}, l.in)
	ctx = context.WithValue(ctx, profKey{}, (*core.ProfInstance)(nil))
	np, err := l.put(ctx, []byte(name))
	if err != nil {
		return nil, err
	}
	ip, err := l.put(ctx, in)
	if err != nil {
		return nil, err
	}
	r, err := l.call.Call(ctx, uint64(np), uint64(len(name)), uint64(ip), uint64(len(in)))
	if err != nil {
		exception(l.in.Prog, l.path, err)
		return nil, err
	}
	p, n := uint32(r[0]>>32), uint32(r[0])
	b, ok := l.mod.Memory().Read(p, n)
	if !ok || n == 0 {
		return nil, fmt.Errorf("bad module result")
	}
	out := append([]byte(nil), b[1:]...)
	if b[0] != 0 {
		return nil, core.Error(string(out))
	}
	return out, nil
}

func (l *loaded) Close() {
	l.e.loads.Delete(l.mod)
	_ = l.mod.Close(context.Background())
	l.meter.Release()
	l.release()
}
