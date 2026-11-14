package virtio

import "sync"

// RNG is a virtio entropy device.
type RNG struct {
	mu  sync.Mutex
	q   *Queue
	buf []byte
}

// NewRNG starts the entropy device d.
func NewRNG(d *Device) (*RNG, error) {
	if err := d.Init(0); err != nil {
		return nil, err
	}
	q, err := d.NewQueue(0, 8)
	if err != nil {
		return nil, err
	}
	d.Ready()
	return &RNG{q: q, buf: make([]byte, 256)}, nil
}

// Read fills b with bytes from the host's entropy source.
func (r *RNG) Read(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for got := 0; got < len(b); {
		want := min(len(b)-got, len(r.buf))
		n := int(r.q.Do(Buf{B: r.buf[:want], Write: true}))
		got += copy(b[got:], r.buf[:n])
	}
	return len(b), nil
}
