package kernel

import (
	"fmt"
	"net/netip"

	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/kernel/board/virt"
	"github.com/ericvh/purgatorio-go/kernel/netstack"
	"github.com/ericvh/purgatorio-go/kernel/virtio"
)

// QEMU user networking: the guest's address, the gateway (the host) and
// the name server.
var (
	userAddr    = netip.MustParsePrefix("10.0.2.15/24")
	userGateway = netip.MustParseAddr("10.0.2.2")
	userDNS     = netip.MustParseAddr("10.0.2.3")
)

// devices finds the virtio devices QEMU was given and starts them, each
// on its interrupt: an entropy source, which also keys the runtime's
// generator, block devices for #S, and a network interface for the IP
// stack, which otherwise has only loopback.
func (p *Platform) devices() {
	devs := virtio.Scan()
	for _, d := range devs {
		virt.Handle(d.IRQ(), d.Interrupt)
	}
	if d := virtio.Find(devs, virtio.IDEntropy); d != nil {
		if r, err := virtio.NewRNG(d); err != nil {
			fmt.Println("virtio-rng:", err)
		} else {
			var seed [32]byte
			_, _ = r.Read(seed[:])
			virt.Seed(seed)
			p.rng = r
		}
	}
	for _, d := range devs {
		if d.ID != virtio.IDBlock {
			continue
		}
		b, err := virtio.NewBlock(d)
		if err != nil {
			fmt.Println("virtio-blk:", err)
			continue
		}
		p.disks = append(p.disks, b)
	}
	st, err := netstack.New()
	if err != nil {
		fmt.Println("netstack:", err)
		return
	}
	p.net = st
	if d := virtio.Find(devs, virtio.IDNet); d != nil {
		nic, err := virtio.NewNet(d)
		if err != nil {
			fmt.Println("virtio-net:", err)
			return
		}
		// #l sits between the device and the stack, so its
		// connections see and send frames beside the stack's.
		e := core.NewEther("ether0", [6]byte(nic.MAC), 1000, nic.Transmit)
		recv, err := st.Attach(netstack.Interface{MAC: nic.MAC, Addr: userAddr, Gateway: userGateway, DNS: userDNS}, e.Output)
		if err != nil {
			fmt.Println("netstack:", err)
			return
		}
		e.SetStack(recv)
		nic.Start(e.Receive)
		p.ether = e
	}
}
