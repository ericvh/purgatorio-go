package kernel

import (
	"fmt"
	"strings"
	"time"

	"github.com/ericvh/purgatorio-go/kernel/aot"
	"github.com/ericvh/purgatorio-go/kernel/codemem"
	"github.com/ericvh/purgatorio-go/vm/wazero"
	wz "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/experimental"
)

// Engine is the kernel's WebAssembly engine, chosen at boot by
// /lib/kernel/engine (the words of its first line):
//
//	interpreter    wazero's interpreter (the default)
//	compiler       wazero's compiler, compiling each module in the kernel
//	aot            modules with a valid image beside them (kernel/aot) run
//	               as the native code in it, the others on the interpreter;
//	               an image must have a trusted hybrid signature
//	aot classical  aot, taking Ed25519 signatures too
//
// The compiler and aot put native code in kernel/codemem's region.
type Engine struct {
	*wazero.Engine
	Spec   string
	Images *aot.Images     // with aot
	Code   *codemem.Region // with compiler or aot
}

// NewEngine returns the engine spec names. read reads files in the boot
// name space, for the trust lists.
func NewEngine(spec string, read func(string) ([]byte, error)) (*Engine, error) {
	words := strings.Fields(spec)
	if len(words) == 0 {
		words = []string{"interpreter"}
	}
	e := &Engine{Spec: strings.Join(words, " ")}
	var err error
	switch {
	case words[0] == "interpreter" && len(words) == 1:
		e.Engine, err = wazero.New(wazero.Interpreter)
	case words[0] == "compiler" && len(words) == 1:
		e.installCode()
		e.Engine, err = wazero.New(wazero.Compiler)
	case words[0] == "aot" && len(words) <= 2:
		policy := aot.Hybrid
		if len(words) == 2 {
			if words[1] != "classical" {
				return nil, fmt.Errorf("unknown engine %q", spec)
			}
			policy = aot.Classical
		}
		e.installCode()
		e.Images = aot.New(aot.Trust(policy, read, uint64(time.Now().Unix())))
		e.Engine, err = wazero.NewWith(wazero.Options{Mode: wazero.Compiler,
			Cache: wz.NewCompilationCacheWithStore(e.Images), Images: e.Images})
	default:
		return nil, fmt.Errorf("unknown engine %q", spec)
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) installCode() {
	e.Code = codemem.New()
	experimental.SetCodeMemory(e.Code)
}

// Summary says what the engine did, for the end of a run.
func (e *Engine) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "engine %s", e.Spec)
	if e.Images != nil {
		s := e.Images.Stats()
		n, i := e.Stats()
		fmt.Fprintf(&b, ": %d modules from images, %d interpreted (%d without an image, %d refused, %d not loaded)",
			n, i, s.Missing, s.Refused, s.Unloaded)
		if s.Loaded > 0 {
			ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 / float64(s.Loaded) }
			fmt.Fprintf(&b, "; an image took %.1f ms to read, %.1f ms for the module's digest, %.1f ms to verify, %.1f ms to load",
				ms(s.Read), ms(s.Digest), ms(s.Verify), ms(s.Load))
		}
	}
	if e.Code != nil {
		held, used := e.Code.Stats()
		fmt.Fprintf(&b, "; code memory %d KiB held, %d KiB in use", held>>10, used>>10)
	}
	return b.String()
}
