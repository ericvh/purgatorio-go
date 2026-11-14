// Package module makes a guest a loadable module, the counterpart of a
// Limbo module others load: build it with -buildmode=c-shared (tools/build
// does this for appl/lib), Register its functions in init, and callers in
// the same prog reach them with sys.Load and Module.Call.
package module

import "sync"

// Func is a module function: bytes in, bytes or an error out.
type Func func(in []byte) ([]byte, error)

var (
	mu    sync.Mutex
	funcs = map[string]Func{}
)

// Register makes f callable as name.
func Register(name string, f Func) {
	mu.Lock()
	funcs[name] = f
	mu.Unlock()
}

func lookup(name string) (Func, bool) {
	mu.Lock()
	defer mu.Unlock()
	f, ok := funcs[name]
	return f, ok
}

// dispatch runs a call and encodes its result: 0 then the result, or 1
// then an error string.
func dispatch(name string, in []byte) []byte {
	f, ok := lookup(name)
	if !ok {
		return append([]byte{1}, "no such function: "+name...)
	}
	out, err := f(in)
	if err != nil {
		return append([]byte{1}, err.Error()...)
	}
	return append([]byte{0}, out...)
}
