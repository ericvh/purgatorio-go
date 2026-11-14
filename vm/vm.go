// Package vm holds the guest ABI host functions, written once against a
// small Memory interface so any WebAssembly engine can carry them: WASI
// preview 1 implemented natively over the prog's name space and fd
// group, plus the purgatorio extension module.
//
// It is a portable package. Engine adapters live in subpackages
// (vm/wazero).
package vm

import (
	"context"
	"encoding/binary"
	"strconv"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
)

var le = binary.LittleEndian

// Memory is a guest's linear memory. Read returns a view of n bytes at off,
// or false if any of them is out of bounds.
type Memory interface {
	Read(off, n uint32) ([]byte, bool)
	Write(off uint32, b []byte) bool
}

// Exit stops the guest. It is returned by a host function, never by a guest.
type Exit struct {
	Status string // Inferno exit status; empty means success
}

func (e *Exit) Error() string { return "exit: " + e.Status }

// Code is the host exit code for the status.
func (e *Exit) Code() uint32 {
	if e.Status == "" {
		return 0
	}
	return 1
}

// Engine runs a module as a prog. vm/wazero implements it.
type Engine interface {
	// Run instantiates code and runs its _start under ctx (the prog's),
	// calling host functions with inst. It returns when the guest exits.
	Run(ctx context.Context, path string, code []byte, inst *Instance) error
	// Load instantiates a reactor module (one with _initialize) in the
	// prog of inst, for purgatorio.load.
	Load(ctx context.Context, path string, code []byte, inst *Instance) (Loaded, error)
}

// Loaded is a loaded reactor module.
type Loaded interface {
	// Call calls the module's named function with in and returns its
	// result, through the module's purgatorio_call export (sdk/module).
	Call(ctx context.Context, name string, in []byte) ([]byte, error)
	Close()
}

// Machine is the runtime shared by all progs: an engine and a kernel.
type Machine struct {
	Kernel *core.Kernel
	Engine Engine
}

// Instance is one running guest: its prog and the per-instance state the
// ABI keeps.
type Instance struct {
	M      *Machine
	Prog   *core.Prog
	Args   []string
	Status string
	// Exec is set when the guest called exec: the module to run next.
	Exec *Exec

	mu sync.Mutex
	// readdir snapshots, by fd, for fd_readdir cookies.
	dirs map[int][]dirent
	// Loaded modules, by handle, and a call result too long for the
	// caller's buffer, waiting for call_result.
	env     []string // environ's snapshot
	mods    map[int]Loaded
	modNext int
	pending []byte
}

type dirent struct {
	name string
	qid  uint64
	dir  bool
}

// Type is a WebAssembly value type.
type Type byte

const (
	I32 Type = 'i'
	I64 Type = 'I'
)

// Func is one host function.
type Func struct {
	Module  string
	Name    string
	Params  []Type
	Results []Type
	// Call reads its arguments from args and returns its results. A
	// non-nil error (an *Exit) stops the guest.
	Call func(in *Instance, m Memory, args []uint64) ([]uint64, error)
}

type hostFn = func(*Instance, Memory, []uint64) ([]uint64, error)

func sig(s string) []Type {
	t := make([]Type, len(s))
	for i := range s {
		t[i] = Type(s[i])
	}
	return t
}

func ok() ([]uint64, error)            { return []uint64{0}, nil }
func errno(e uint16) ([]uint64, error) { return []uint64{uint64(e)}, nil }
func ret(v int64) ([]uint64, error)    { return []uint64{uint64(uint32(int32(v)))}, nil }

// fail records err as the errstr and returns its WASI errno.
func (in *Instance) fail(err error) ([]uint64, error) {
	s := core.Errstr(err)
	in.Prog.SetErrstr(s)
	return errno(abi.Errno(s))
}

// fault is a bad guest pointer.
func (in *Instance) fault() ([]uint64, error) {
	in.Prog.SetErrstr(abi.Ebadaddr)
	return errno(abi.ErrnoFault)
}

// pfail is a purgatorio call's failure: -1 and the errstr.
func (in *Instance) pfail(err error) ([]uint64, error) {
	in.Prog.SetError(err)
	return ret(-1)
}

func status(code uint32) string {
	if code == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(code), 10)
}

func i32(v uint64) int32 { return int32(uint32(v)) }

func putU32(m Memory, off, v uint32) bool {
	var b [4]byte
	le.PutUint32(b[:], v)
	return m.Write(off, b[:])
}

func putU64(m Memory, off uint32, v uint64) bool {
	var b [8]byte
	le.PutUint64(b[:], v)
	return m.Write(off, b[:])
}

func getU32(m Memory, off uint32) (uint32, bool) {
	b, ok := m.Read(off, 4)
	if !ok {
		return 0, false
	}
	return le.Uint32(b), true
}

func getStr(m Memory, ptr, n uint64) (string, bool) {
	b, ok := m.Read(uint32(ptr), uint32(n))
	if !ok {
		return "", false
	}
	return string(b), true
}

// HostFuncs returns every host function, in both modules.
func HostFuncs() []Func {
	return append(append(append(append(wasiFuncs(), purgatorioFuncs()...), chanFuncs()...), srvFuncs()...), loadFuncs()...)
}

// quick lists the host functions that never block. The engine reports
// every other host call to the prog (core.Prog.EnterHost), which is
// where /prog/n/stack and dbgctl see the guest.
var quick = map[string]bool{
	"args_get": true, "args_sizes_get": true, "environ_get": true, "environ_sizes_get": true,
	"clock_res_get": true, "clock_time_get": true, "random_get": true, "sched_yield": true,
	"proc_exit": true, "fd_prestat_get": true, "fd_prestat_dir_name": true, "fd_fdstat_get": true,
}

// MayBlock reports whether the host function may block, so the engine
// should report calls to it.
func (f Func) MayBlock() bool { return !quick[f.Name] }
