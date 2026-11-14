// Command kernel is the Purgatorio-Go native kernel for QEMU's aarch64
// virt machine: it boots the portable core over the embedded root and
// runs emuinit, which runs the first command, then powers the machine
// off. The first command is sh -c with the text of /lib/kernel/init when
// the root has it, else sh -l.
package main

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/kernel"
	"github.com/ericvh/purgatorio-go/kernel/board/virt"
	"github.com/ericvh/purgatorio-go/vm"
)

func main() {
	fmt.Println("Purgatorio-Go kernel (aarch64, QEMU virt)")
	virt.StartCPUs(0)
	fmt.Printf("kernel: %d cpus, NumCPU %d, GOMAXPROCS %d\n", virt.CPUs(), runtime.NumCPU(), runtime.GOMAXPROCS(0))
	sys, err := kernel.Boot(kernel.New(), nil)
	if err != nil {
		fail(err)
	}
	eng, err := kernel.NewEngine(readFile(sys, "/lib/kernel/engine"), func(path string) ([]byte, error) {
		return vm.ReadFile(sys.Init, path)
	})
	if err != nil {
		fail(err)
	}
	sys.Machine.Engine = eng
	if eng.Spec != "interpreter" {
		fmt.Printf("kernel: engine %s\n", eng.Spec)
	}
	args := []string{"emuinit"}
	if cmd := readFile(sys, "/lib/kernel/init"); cmd != "" {
		args = append(args, "sh", "-c", cmd)
	}
	status, err := sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", args)
	if err != nil {
		fail(err)
	}
	sys.Init.Exit(status)
	if eng.Spec != "interpreter" {
		fmt.Printf("kernel: %s\n", eng.Summary())
	}
	fmt.Printf("kernel: done: %q\n", status)
	virt.PowerOff()
}

// readFile returns the text of a file in the root, or "".
func readFile(sys *kernel.System, path string) string {
	b, err := vm.ReadFile(sys.Init, path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func fail(err error) {
	fmt.Println("kernel:", core.Errstr(err))
	virt.PowerOff()
}
