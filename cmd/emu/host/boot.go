package host

import (
	"runtime"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/vm"
)

// System is a booted emu: the kernel, the VM and the first prog.
type System struct {
	Kernel  *core.Kernel
	Machine *vm.Machine
	Init    *core.Prog
}

// Boot builds the device table and the first prog's name space as
// Inferno's emu does (emu/port/main.c): #U after #/ with create, #c before
// /dev, #p on /prog, #d on /fd, then the host's own /dev after.
func Boot(h *Host, root string, engine vm.Engine) (*System, error) {
	fs, err := NewDevFS(root)
	if err != nil {
		return nil, err
	}
	k := &core.Kernel{Platform: h, Progs: core.NewProgs(), Owner: h.User()}
	k.Mnt = core.NewDevMnt()
	k.Srv = core.NewDevSrv(k)
	k.DevTab = core.NewDevTab(
		core.NewRoot(k.Owner),
		core.NewDevCons(k),
		core.NewDevEnv(k),
		fs,
		core.NewDevPipe(k),
		core.NewDevDup(k),
		core.NewDevProg(k),
		k.Srv,
		k.Mnt,
		core.NewDevSnarf(k),
		core.NewDevCap(k),
		core.DevIndir{},
		core.NewDevIP(k, h.Net()),
	)
	rootc, err := k.DevTab.Attach(nil, '/', "")
	if err != nil {
		return nil, err
	}
	p := core.NewProg(k, core.NewPgrp(k.DevTab, rootc), k.Owner)
	p.SetName("emuinit")
	binds := []struct {
		new, old string
		flags    uint32
	}{
		{"#U", "/", abi.MAFTER | abi.MCREATE},
		{"#c", "/dev", abi.MBEFORE},
		{"#p", "/prog", abi.MREPL},
		{"#d", "/fd", abi.MREPL},
		{"#I", "/net", abi.MAFTER},
	}
	for _, b := range binds {
		if err := p.Bind(b.new, b.old, b.flags); err != nil {
			return nil, err
		}
	}
	// The host tree's own /dev, when it has one, follows #c.
	_ = p.Bind("#U/dev", "/dev", abi.MAFTER)
	if err := vm.InitFds(p); err != nil {
		return nil, err
	}
	eg := p.Egrp()
	eg.Set("emuhost", []byte(hosttype()))
	eg.Set("cputype", []byte(cputype()))
	return &System{Kernel: k, Machine: &vm.Machine{Kernel: k, Engine: engine}, Init: p}, nil
}

// hosttype and cputype are Inferno's names for the host OS and CPU.
func hosttype() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOSX"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

func cputype() string {
	switch runtime.GOARCH {
	case "amd64":
		return "386"
	case "arm64":
		return "arm64"
	}
	return runtime.GOARCH
}
