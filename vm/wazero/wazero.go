// Package wazero runs guests on wazero, a pure-Go WebAssembly runtime, with
// the host functions from package vm.
package wazero

import (
	"context"
	"errors"
	"fmt"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/vm"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"
)

// Engine selects wazero's execution mode.
type Engine string

const (
	Compiler    Engine = "compiler"    // native code (amd64, arm64)
	Interpreter Engine = "interpreter" // portable, no executable memory
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

// procKey carries the *vm.Proc through the call context.
type procKey struct{}

// Run instantiates the module in code as a command, runs its _start with p
// as its prog, and returns the prog's exit status.
func Run(ctx context.Context, engine Engine, code []byte, p *vm.Proc) (string, error) {
	var cfg wazero.RuntimeConfig
	switch engine {
	case Compiler:
		cfg = wazero.NewRuntimeConfigCompiler()
	case Interpreter:
		cfg = wazero.NewRuntimeConfigInterpreter()
	default:
		return "", fmt.Errorf("unknown engine %q", engine)
	}
	cfg = cfg.WithCloseOnContextDone(true)
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer r.Close(ctx)

	builders := map[string]wazero.HostModuleBuilder{}
	for _, f := range vm.HostFuncs() {
		b, found := builders[f.Module]
		if !found {
			b = r.NewHostModuleBuilder(f.Module)
			builders[f.Module] = b
		}
		f := f
		fn := api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			p := ctx.Value(procKey{}).(*vm.Proc)
			res, err := f.Call(p, mem{mod.Memory()}, stack[:len(f.Params)])
			var exit *vm.Exit
			if errors.As(err, &exit) {
				_ = mod.CloseWithExitCode(ctx, exit.Code())
				panic(sys.NewExitError(exit.Code()))
			}
			copy(stack, res)
		})
		b.NewFunctionBuilder().WithGoModuleFunction(fn, types(f.Params), types(f.Results)).Export(f.Name)
	}
	for name, b := range builders {
		if _, err := b.Instantiate(ctx); err != nil {
			return "", fmt.Errorf("host module %s: %w", name, err)
		}
	}

	cm, err := r.CompileModule(ctx, code)
	if err != nil {
		return "", err
	}
	for _, imp := range cm.ImportedFunctions() {
		mod, _, _ := imp.Import()
		if mod != abi.WASIModule && mod != abi.Module {
			return "", fmt.Errorf("import from unknown module %q", mod)
		}
	}
	ctx = context.WithValue(ctx, procKey{}, p)
	mod, err := r.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName(""))
	if mod != nil {
		_ = mod.Close(ctx)
	}
	var exit *sys.ExitError
	if err != nil && !errors.As(err, &exit) {
		return "", err
	}
	return p.Status, nil
}
