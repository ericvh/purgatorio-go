package core

import (
	"context"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
)

// Cap is a capability carried by a kernel-channel message: another
// channel, or an open file.
type Cap struct {
	Chan *KChan
	Fd   *Chan
}

// KMsg is one message on a kernel channel.
type KMsg struct {
	Bytes []byte
	Caps  []Cap
}

// KChan is a kernel channel between progs, the cross-instance form of
// Limbo's chan: bytes plus capabilities. A channel of capacity 0 is
// synchronous: a send completes when a receiver has taken the message.
type KChan struct {
	mu       sync.Mutex
	cap      int
	q        []KMsg
	taken    uint64 // messages received so far
	sent     uint64
	closed   bool
	r        Rendez
	watchers []*Rendez
}

func NewKChan(capacity int) *KChan { return &KChan{cap: max(0, capacity)} }

func (k *KChan) notify() {
	k.r.Wakeup()
	k.mu.Lock()
	w := append([]*Rendez(nil), k.watchers...)
	k.mu.Unlock()
	for _, r := range w {
		r.Wakeup()
	}
}

func (k *KChan) Watch(r *Rendez) { k.mu.Lock(); k.watchers = append(k.watchers, r); k.mu.Unlock() }
func (k *KChan) Unwatch(r *Rendez) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, x := range k.watchers {
		if x == r {
			k.watchers = append(k.watchers[:i], k.watchers[i+1:]...)
			return
		}
	}
}

// CanSend reports whether a send would not block (or would fail).
func (k *KChan) CanSend() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.closed || len(k.q) < max(1, k.cap)
}

// CanRecv reports whether a receive would not block.
func (k *KChan) CanRecv() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.closed || len(k.q) > 0
}

// TrySend queues m if there is room. A synchronous channel's sender then
// waits for the receiver (see Send).
func (k *KChan) trySend(m KMsg) (uint64, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return 0, false, Error(abi.Ehungup)
	}
	if len(k.q) >= max(1, k.cap) {
		return 0, false, nil
	}
	k.q = append(k.q, m)
	k.sent++
	return k.sent, true, nil
}

func (k *KChan) tryRecv() (KMsg, bool, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.q) > 0 {
		m := k.q[0]
		k.q = k.q[1:]
		k.taken++
		return m, true, false
	}
	return KMsg{}, false, k.closed
}

// TrySend sends m only if that would not block, and reports whether it
// did.
func (k *KChan) TrySend(m KMsg) bool {
	_, ok, _ := k.trySend(m)
	if ok {
		k.notify()
	}
	return ok
}

// Send sends m, blocking while the channel is full and, on a synchronous
// channel, until it is received.
func (k *KChan) Send(ctx context.Context, m KMsg) error {
	var seq uint64
	err := Park(ctx, &k.r, func() bool {
		s, ok, err := k.trySend(m)
		if err != nil || ok {
			seq = s
			return true
		}
		return false
	})
	if err != nil {
		return err
	}
	if seq == 0 {
		return Error(abi.Ehungup)
	}
	k.notify()
	if k.cap == 0 {
		return Park(ctx, &k.r, func() bool {
			k.mu.Lock()
			defer k.mu.Unlock()
			return k.taken >= seq || k.closed
		})
	}
	return nil
}

// Recv receives a message; ok is false when the channel is closed and
// empty.
func (k *KChan) Recv(ctx context.Context) (KMsg, bool, error) {
	var (
		m      KMsg
		got    bool
		closed bool
	)
	err := Park(ctx, &k.r, func() bool {
		m, got, closed = k.tryRecv()
		return got || closed
	})
	if err != nil {
		return KMsg{}, false, err
	}
	if got {
		k.notify()
	}
	return m, got, nil
}

// Close closes the channel: receivers drain what is queued and then see
// the end; senders fail.
func (k *KChan) Close() {
	k.mu.Lock()
	k.closed = true
	k.mu.Unlock()
	k.notify()
}

// AltOp is one arm of Alt: a send when Send is not nil, else a receive.
type AltOp struct {
	Chan *KChan
	Send *KMsg
	Got  KMsg
	Ok   bool
}

// Alt waits until one arm can proceed and does it, as Limbo's alt does,
// returning its index; with block false it returns -1 at once when none
// can.
func Alt(ctx context.Context, ops []AltOp, block bool) (int, error) {
	var r Rendez
	for _, op := range ops {
		op.Chan.Watch(&r)
	}
	defer func() {
		for _, op := range ops {
			op.Chan.Unwatch(&r)
		}
	}()
	idx := -1
	try := func() bool {
		for i := range ops {
			op := &ops[i]
			if op.Send != nil {
				if _, ok, err := op.Chan.trySend(*op.Send); ok || err != nil {
					idx = i
					return true
				}
			} else if m, got, closed := op.Chan.tryRecv(); got || closed {
				op.Got, op.Ok = m, got
				idx = i
				return true
			}
		}
		return false
	}
	if !block {
		if try() {
			ops[idx].Chan.notify()
		}
		return idx, nil
	}
	if err := Park(ctx, &r, try); err != nil {
		return -1, err
	}
	ops[idx].Chan.notify()
	return idx, nil
}

// Kgrp is a prog's table of kernel-channel handles.
type Kgrp struct {
	mu   sync.Mutex
	next int
	tab  map[int]*KChan
}

func NewKgrp() *Kgrp { return &Kgrp{next: 1, tab: map[int]*KChan{}} }

func (g *Kgrp) Alloc(k *KChan) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	h := g.next
	g.next++
	g.tab[h] = k
	return h
}

func (g *Kgrp) Get(h int) (*KChan, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if k, ok := g.tab[h]; ok {
		return k, nil
	}
	return nil, Error(abi.Ebadarg)
}

// Drop removes a handle without closing the channel.
func (g *Kgrp) Drop(h int) (*KChan, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k, ok := g.tab[h]
	if !ok {
		return nil, Error(abi.Ebadarg)
	}
	delete(g.tab, h)
	return k, nil
}
