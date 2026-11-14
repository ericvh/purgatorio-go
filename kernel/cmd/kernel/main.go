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
	"github.com/ericvh/purgatorio-go/vm/wazero"
)

func main() {
	fmt.Println("Purgatorio-Go kernel (aarch64, QEMU virt)")
	virt.StartCPUs(0)
	fmt.Printf("kernel: %d cpus, NumCPU %d, GOMAXPROCS %d\n", virt.CPUs(), runtime.NumCPU(), runtime.GOMAXPROCS(0))
	eng, err := wazero.New(wazero.Interpreter)
	if err != nil {
		fail(err)
	}
	sys, err := kernel.Boot(kernel.New(), eng)
	if err != nil {
		fail(err)
	}
	args := []string{"emuinit"}
	if c, err := sys.Init.OpenChan("/lib/kernel/init", 0); err == nil {
		buf := make([]byte, 64*1024)
		n, _ := c.Dev.Read(sys.Init.Ctx, c, buf, 0)
		c.DecRef()
		args = append(args, "sh", "-c", strings.TrimSpace(string(buf[:n])))
	}
	status, err := sys.Machine.Run(sys.Init, "/bin/emuinit.wasm", args)
	if err != nil {
		fail(err)
	}
	sys.Init.Exit(status)
	fmt.Printf("kernel: done: %q\n", status)
	virt.PowerOff()
}

func fail(err error) {
	fmt.Println("kernel:", core.Errstr(err))
	virt.PowerOff()
}
