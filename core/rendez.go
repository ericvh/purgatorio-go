package core

import (
	"context"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
)

// Rendez is Inferno's sleep and wakeup point. A sleeper rechecks its
// condition after every Wakeup, so a wakeup with nobody asleep is harmless.
type Rendez struct {
	mu sync.Mutex
	ch chan struct{}
}

// waitch returns a channel closed by the next Wakeup. Take it before
// checking the condition so no wakeup is lost.
func (r *Rendez) waitch() <-chan struct{} {
	r.mu.Lock()
	if r.ch == nil {
		r.ch = make(chan struct{})
	}
	c := r.ch
	r.mu.Unlock()
	return c
}

// Wakeup wakes every sleeper.
func (r *Rendez) Wakeup() {
	r.mu.Lock()
	if r.ch != nil {
		close(r.ch)
		r.ch = nil
	}
	r.mu.Unlock()
}

// Note is a prog's kill and interrupt state. A kill ends every blocking
// call for good; an interrupt ends the next one only, and reaches a prog
// only when it catches interrupts.
type Note struct {
	killOnce sync.Once
	killed   chan struct{}

	mu        sync.Mutex
	interrupt bool
	intr      Rendez
}

func NewNote() *Note { return &Note{killed: make(chan struct{})} }

func (n *Note) Kill() { n.killOnce.Do(func() { close(n.killed) }) }

func (n *Note) Killed() bool {
	select {
	case <-n.killed:
		return true
	default:
		return false
	}
}

// Dead is closed when the prog is killed.
func (n *Note) Dead() <-chan struct{} { return n.killed }

// Interrupt posts an interrupt.
func (n *Note) Interrupt() {
	n.mu.Lock()
	n.interrupt = true
	n.mu.Unlock()
	n.intr.Wakeup()
}

// TakeInterrupt reports and clears a pending interrupt.
func (n *Note) TakeInterrupt() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	i := n.interrupt
	n.interrupt = false
	return i
}

func (n *Note) Interrupted() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.interrupt
}

type noteKey struct{}

// WithNote returns a context that carries n.
func WithNote(ctx context.Context, n *Note) context.Context {
	return context.WithValue(ctx, noteKey{}, n)
}

// NoteFrom returns the note in ctx, or nil.
func NoteFrom(ctx context.Context) *Note {
	if ctx == nil {
		return nil
	}
	n, _ := ctx.Value(noteKey{}).(*Note)
	return n
}

// Interrupted reports whether the prog behind ctx has been killed.
func Interrupted(ctx context.Context) bool {
	n := NoteFrom(ctx)
	return n != nil && n.Killed()
}

// Check returns Eintr if the prog behind ctx has been killed or has an
// interrupt pending, which it takes.
func Check(ctx context.Context) error {
	if n := NoteFrom(ctx); n != nil && (n.TakeInterrupt() || n.Killed()) {
		return Error(abi.Eintr)
	}
	return nil
}

var never = make(chan struct{})

// Park sleeps on r until cond returns true. cond may run more than once and
// must not sleep. A killed or interrupted prog gets Eintr.
func Park(ctx context.Context, r *Rendez, cond func() bool) error {
	_, err := park(ctx, r, cond, nil, true)
	return err
}

// ParkKillable is Park, but only a kill ends the sleep early; an interrupt
// stays pending.
func ParkKillable(ctx context.Context, r *Rendez, cond func() bool) error {
	_, err := park(ctx, r, cond, nil, false)
	return err
}

// ParkTimeout is Park with a limit: timeoutMs < 0 waits for ever, 0 does
// not sleep. It reports whether cond holds.
func ParkTimeout(ctx context.Context, r *Rendez, cond func() bool, timeoutMs int) (bool, error) {
	if timeoutMs < 0 {
		return true, Park(ctx, r, cond)
	}
	t := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
	defer t.Stop()
	return park(ctx, r, cond, t.C, true)
}

func park(ctx context.Context, r *Rendez, cond func() bool, timeout <-chan time.Time, intr bool) (bool, error) {
	n := NoteFrom(ctx)
	for {
		w := r.waitch()
		dead, ich := never, (<-chan struct{})(never)
		if n != nil {
			dead = n.killed
			if intr {
				ich = n.intr.waitch()
				if n.TakeInterrupt() {
					return false, Error(abi.Eintr)
				}
			}
			if n.Killed() {
				return false, Error(abi.Eintr)
			}
		}
		if cond() {
			return true, nil
		}
		select {
		case <-w:
		case <-ich:
		case <-dead:
			return false, Error(abi.Eintr)
		case <-timeout:
			return cond(), nil
		}
	}
}
