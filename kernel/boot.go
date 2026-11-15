package kernel

import (
	"encoding/hex"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/kernel/initrd"
	"github.com/ericvh/purgatorio-go/vm"
)

// System is the booted kernel: the core, the VM and the first prog.
type System struct {
	Kernel  *core.Kernel
	Machine *vm.Machine
	Init    *core.Prog
}

// Boot builds the device table over the embedded root and makes the
// first prog's name space as the emu does, with the root tree in place
// of #U.
func Boot(p *Platform, engine vm.Engine) (*System, error) {
	k := &core.Kernel{Platform: p, Progs: core.NewProgs(), Owner: p.User()}
	root := core.NewRoot(k.Owner)
	// The kernel has no host tree: the directories the emu gets from
	// root/ are made here.
	for _, d := range []string{"mnt", "tmp", "n/client", "n/local", "n/remote"} {
		root.AddDir(d)
	}
	if err := initrd.Load(root); err != nil {
		return nil, err
	}
	k.Mnt = core.NewDevMnt()
	k.Srv = core.NewDevSrv(k)
	k.DevTab = core.NewDevTab(
		root,
		core.NewDevCons(k),
		core.NewDevEnv(k),
		core.NewDevPipe(k),
		core.NewDevDup(k),
		core.NewDevProg(k),
		k.Srv,
		k.Mnt,
		core.NewDevSnarf(k),
		core.NewDevCap(k),
		core.DevIndir{},
		core.NewDevDS(k),
		core.NewDevIP(k, p.Net()),
		core.NewDevLethe(k, p.Net()),
		NewDevSD(k, p.disks),
		p.etherDev(k.Owner),
	)
	rootc, err := k.DevTab.Attach(nil, '/', "")
	if err != nil {
		return nil, err
	}
	init := core.NewProg(k, core.NewPgrp(k.DevTab, rootc), k.Owner)
	init.SetName("emuinit")
	for _, b := range []struct {
		new, old string
		flags    uint32
	}{
		{"#c", "/dev", abi.MBEFORE},
		{"#p", "/prog", abi.MREPL},
		{"#d", "/fd", abi.MREPL},
		{"#I", "/net", abi.MAFTER},
		{"#l", "/net", abi.MAFTER},
		{"#S", "/dev", abi.MAFTER},
	} {
		if err := init.Bind(b.new, b.old, b.flags); err != nil {
			return nil, err
		}
	}
	if err := vm.InitFds(init); err != nil {
		return nil, err
	}
	init.Egrp().Set("cputype", []byte("arm64"))
	init.Egrp().Set("emuhost", []byte("Purgatorio"))
	return &System{Kernel: k, Machine: &vm.Machine{Kernel: k, Engine: engine}, Init: init}, nil
}

// etherDev is #l, the Ethernet device (Inferno's ether(3)), cut down to
// what ip/dhcp reads: ether0/addr, the virtio-net interface's MAC
// address in hex. It is empty without a network interface.
func (p *Platform) etherDev(owner string) *core.StaticTree {
	t := core.NewStaticTree('l', "ether", owner)
	if p.ether != nil {
		t.AddFile("ether0/addr", []byte(hex.EncodeToString(p.ether.MAC)))
	}
	return t
}
