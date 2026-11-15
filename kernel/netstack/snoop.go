package netstack

// snoopers are the functions an interface's snoop readers gave, by the
// number SnoopIfc handed out. They belong to the interface, not its
// medium, as Inferno's sq belongs to the ipifc conversation.
type snoopers map[int]func(pkt []byte)

// SnoopIfc copies the IP packets interface n's medium sends and
// receives to deliver, without their Ethernet header, as /net/ipifc/n/snoop
// gives them. Only the Ethernet medium has packets to copy: gVisor's
// loopback is not tapped.
func (s *Stack) SnoopIfc(n int, deliver func(pkt []byte)) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.getifc(n)
	if err != nil {
		return nil, err
	}
	if f.snoop == nil {
		f.snoop = snoopers{}
	}
	s.snoopid++
	id := s.snoopid
	f.snoop[id] = deliver
	s.snooping.Add(1)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := f.snoop[id]; ok {
			delete(f.snoop, id)
			s.snooping.Add(-1)
		}
	}, nil
}

// snoopFrame gives an Ethernet frame's IP packet (IPv4 or IPv6) to the
// snoopers of the interface m is bound to.
func (s *Stack) snoopFrame(m *medium, frame []byte) {
	if s.snooping.Load() == 0 || len(frame) <= 14 {
		return
	}
	if t := int(frame[12])<<8 | int(frame[13]); t != 0x800 && t != 0x86dd {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.bound == nil {
		return
	}
	for _, deliver := range m.bound.snoop {
		deliver(frame[14:])
	}
}
