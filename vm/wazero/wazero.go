// Package wazero runs guests on wazero, a pure-Go WebAssembly runtime, with
// the host functions from package vm.
package wazero

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/vm"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
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

// cacheMax is how many compiled modules an engine keeps. A compiled Go
// guest takes tens of megabytes, so the cache is bounded; the least
// recently used module is dropped once no instance is using it.
const cacheMax = 8

type cached struct {
	cm    wazero.CompiledModule
	used  uint64 // last use, for LRU
	users int    // instances running it
}

// Engine is one wazero runtime shared by every prog. Compiled modules are
// cached by content, as Inferno caches loaded modules.
type Engine struct {
	r     wazero.Runtime
	mu    sync.Mutex
	cache map[[32]byte]*cached
	tick  uint64
}

// New returns an engine in the given mode.
func New(mode Mode) (*Engine, error) {
	var cfg wazero.RuntimeConfig
	switch mode {
	case Compiler:
		cfg = wazero.NewRuntimeConfigCompiler()
	case Interpreter:
		cfg = wazero.NewRuntimeConfigInterpreter()
	default:
		return nil, fmt.Errorf("unknown engine %q", mode)
	}
	cfg = cfg.WithCloseOnContextDone(true)
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	builders := map[string]wazero.HostModuleBuilder{}
	for _, f := range vm.HostFuncs() {
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
	for name, b := range builders {
		if _, err := b.Instantiate(ctx); err != nil {
			return nil, fmt.Errorf("host module %s: %w", name, err)
		}
	}
	return &Engine{r: r, cache: map[[32]byte]*cached{}}, nil
}

// compile returns the compiled module for code, holding it until release
// is called.
func (e *Engine) compile(ctx context.Context, code []byte) (wazero.CompiledModule, func(), error) {
	key := sha256.Sum256(code)
	e.mu.Lock()
	if c, ok := e.cache[key]; ok {
		e.tick++
		c.used, c.users = e.tick, c.users+1
		e.mu.Unlock()
		return c.cm, e.releaser(key), nil
	}
	e.mu.Unlock()
	cm, err := e.r.CompileModule(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	for _, imp := range cm.ImportedFunctions() {
		mod, _, _ := imp.Import()
		if mod != abi.WASIModule && mod != abi.Module {
			_ = cm.Close(ctx)
			return nil, nil, fmt.Errorf("import from unknown module %q", mod)
		}
	}
	e.mu.Lock()
	if c, ok := e.cache[key]; ok { // compiled meanwhile by another prog
		e.tick++
		c.used, c.users = e.tick, c.users+1
		e.mu.Unlock()
		_ = cm.Close(ctx)
		return c.cm, e.releaser(key), nil
	}
	e.tick++
	e.cache[key] = &cached{cm: cm, used: e.tick, users: 1}
	e.evict()
	e.mu.Unlock()
	return cm, e.releaser(key), nil
}

func (e *Engine) releaser(key [32]byte) func() {
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
		var victim [32]byte
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
	cm, release, err := e.compile(context.Background(), code)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, instKey{}, in)
	// Each instance is anonymous, so many progs can run one module.
	mod, err := e.r.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName("").WithStartFunctions("_start"))
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	var exit *sys.ExitError
	if err != nil && !errors.As(err, &exit) {
		return err
	}
	return nil
}

// Close releases the runtime.
func (e *Engine) Close() error { return e.r.Close(context.Background()) }

// loaded is a reactor instance in a prog.
type loaded struct {
	mod     api.Module
	in      *vm.Instance
	call    api.Function
	allo    api.Function
	release func()
}

// Load implements vm.Engine.
func (e *Engine) Load(ctx context.Context, path string, code []byte, in *vm.Instance) (vm.Loaded, error) {
	cm, release, err := e.compile(context.Background(), code)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, instKey{}, in)
	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize")
	mod, err := e.r.InstantiateModule(ctx, cm, cfg)
	if err != nil {
		release()
		return nil, err
	}
	l := &loaded{mod: mod, in: in, call: mod.ExportedFunction("purgatorio_call"),
		allo: mod.ExportedFunction("purgatorio_alloc"), release: release}
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
	_ = l.mod.Close(context.Background())
	l.release()
}
