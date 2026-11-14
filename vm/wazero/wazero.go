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

// Engine is one wazero runtime shared by every prog. Compiled modules are
// cached by content, as Inferno caches loaded modules.
type Engine struct {
	r     wazero.Runtime
	mu    sync.Mutex
	cache map[[32]byte]wazero.CompiledModule
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
	cfg = cfg.WithCloseOnContextDone(true).WithCompilationCache(wazero.NewCompilationCache())
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
	return &Engine{r: r, cache: map[[32]byte]wazero.CompiledModule{}}, nil
}

func (e *Engine) compile(ctx context.Context, code []byte) (wazero.CompiledModule, error) {
	key := sha256.Sum256(code)
	e.mu.Lock()
	cm, ok := e.cache[key]
	e.mu.Unlock()
	if ok {
		return cm, nil
	}
	cm, err := e.r.CompileModule(ctx, code)
	if err != nil {
		return nil, err
	}
	for _, imp := range cm.ImportedFunctions() {
		mod, _, _ := imp.Import()
		if mod != abi.WASIModule && mod != abi.Module {
			return nil, fmt.Errorf("import from unknown module %q", mod)
		}
	}
	e.mu.Lock()
	e.cache[key] = cm
	e.mu.Unlock()
	return cm, nil
}

// Run implements vm.Engine.
func (e *Engine) Run(ctx context.Context, path string, code []byte, in *vm.Instance) error {
	cm, err := e.compile(context.Background(), code)
	if err != nil {
		return err
	}
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
