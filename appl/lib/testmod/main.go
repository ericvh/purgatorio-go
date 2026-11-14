// Package main is a loadable module for the load tests: echo returns its
// argument, fail fails, and sysname reads /dev/sysname, showing the module
// runs in its caller's prog and name space.
package main

import (
	"errors"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/module"
)

func init() {
	module.Register("echo", func(in []byte) ([]byte, error) { return in, nil })
	module.Register("fail", func([]byte) ([]byte, error) { return nil, errors.New("it failed") })
	module.Register("sysname", func([]byte) ([]byte, error) { return os.ReadFile("/dev/sysname") })
	module.Register("big", func([]byte) ([]byte, error) { return make([]byte, 100000), nil })
}

func main() {}
