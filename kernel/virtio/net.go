package virtio

import "net"

// Network device features (section 5.1).
const netFeatureMAC = 1 << 5

// netHdrLen is the size of struct virtio_net_hdr for a modern device.
const netHdrLen = 12

// FrameSize is the largest Ethernet frame (without FCS) the driver
// passes: no offloads are negotiated, so frames fit the 1500-byte MTU.
const FrameSize = 1514

// Net is a virtio network device. Received frames go to the function
// given to Start; Transmit sends one.
type Net struct {
	d   *Device
	rx  *Queue
	tx  *Queue
	MAC net.HardwareAddr

	txbuf chan []byte
}

// NewNet sets up the network device d.
func NewNet(d *Device) (*Net, error) {
	if err := d.Init(netFeatureMAC); err != nil {
		return nil, err
	}
	rx, err := d.NewQueue(0, 64)
	if err != nil {
		return nil, err
	}
	tx, err := d.NewQueue(1, 64)
	if err != nil {
		return nil, err
	}
	n := &Net{d: d, rx: rx, tx: tx, txbuf: make(chan []byte, 32)}
	if d.Features&netFeatureMAC != 0 {
		lo, hi := d.Config32(0), d.Config32(4)
		n.MAC = net.HardwareAddr{byte(lo), byte(lo >> 8), byte(lo >> 16), byte(lo >> 24), byte(hi), byte(hi >> 8)}
	} else {
		n.MAC = net.HardwareAddr{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}
	}
	for i := 0; i < cap(n.txbuf); i++ {
		n.txbuf <- make([]byte, netHdrLen+FrameSize)
	}
	return n, nil
}

// Start fills the receive queue and starts the device; recv is called,
// one frame at a time, with each frame received. The frame is only
// valid during the call.
func (n *Net) Start(recv func(frame []byte)) {
	var post func(b []byte)
	post = func(b []byte) {
		n.rx.Submit([]Buf{{B: b, Write: true}}, func(got uint32) {
			if int(got) > netHdrLen && int(got) <= len(b) {
				recv(b[netHdrLen:got])
			}
			post(b)
		})
	}
	n.d.Ready()
	for i := 0; i < n.rx.Size(); i++ {
		post(make([]byte, netHdrLen+FrameSize))
	}
}

// Transmit sends one Ethernet frame, waiting for room in the queue.
func (n *Net) Transmit(frame []byte) {
	if len(frame) > FrameSize {
		return
	}
	b := <-n.txbuf
	clear(b[:netHdrLen])
	m := copy(b[netHdrLen:], frame)
	n.tx.Submit([]Buf{{B: b[:netHdrLen+m]}}, func(uint32) { n.txbuf <- b })
}
