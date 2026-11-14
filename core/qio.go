package core

import (
	"context"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
)

// Queue is a bounded byte queue, Inferno's qio: readers block while it is
// empty and writers while it is full. After Hangup readers drain what is
// left and then see end of file, and writers get Ehungup.
type Queue struct {
	mu       sync.Mutex
	buf      []byte
	limit    int
	hungup   bool
	readers  Rendez
	writers  Rendez
	watchers []*Rendez
	// msg makes each write one message: a read returns at most one.
	msg  bool
	lens []int
}

func NewQueue(limit int) *Queue { return &Queue{limit: max(1, limit)} }

// NewMsgQueue returns a queue that keeps write boundaries, as Inferno's
// Qmsg queues do (wait files, cons input).
func NewMsgQueue(limit int) *Queue {
	q := NewQueue(limit)
	q.msg = true
	return q
}

func (q *Queue) notify() {
	q.readers.Wakeup()
	q.writers.Wakeup()
	q.mu.Lock()
	w := append([]*Rendez(nil), q.watchers...)
	q.mu.Unlock()
	for _, r := range w {
		r.Wakeup()
	}
}

// Watch wakes r when data arrives, the queue drains or it is hung up.
func (q *Queue) Watch(r *Rendez) {
	q.mu.Lock()
	q.watchers = append(q.watchers, r)
	q.mu.Unlock()
}

func (q *Queue) Unwatch(r *Rendez) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, x := range q.watchers {
		if x == r {
			q.watchers = append(q.watchers[:i], q.watchers[i+1:]...)
			return
		}
	}
}

func (q *Queue) Hungup() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.hungup
}

// Writable reports whether a writer would not block.
func (q *Queue) Writable() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.hungup || len(q.buf) < q.limit
}

// Readable reports whether a reader would not block.
func (q *Queue) Readable() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.hungup || len(q.buf) > 0
}

// Read reads at least one byte, or returns 0 at end of file.
func (q *Queue) Read(ctx context.Context, out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	for {
		err := Park(ctx, &q.readers, func() bool {
			q.mu.Lock()
			defer q.mu.Unlock()
			return len(q.buf) > 0 || q.hungup
		})
		if err != nil {
			return 0, err
		}
		q.mu.Lock()
		n := min(len(out), len(q.buf))
		if q.msg && len(q.lens) > 0 {
			n = min(len(out), q.lens[0])
			if n == q.lens[0] {
				q.lens = q.lens[1:]
			} else {
				q.lens[0] -= n
			}
		}
		copy(out, q.buf[:n])
		q.buf = q.buf[n:]
		hungup := q.hungup
		q.mu.Unlock()
		if n > 0 {
			q.notify()
			return n, nil
		}
		if hungup {
			return 0, nil
		}
	}
}

// Write writes all of data, blocking while the queue is full. A message
// queue takes data whole.
func (q *Queue) Write(ctx context.Context, data []byte) (int, error) {
	done := 0
	for done < len(data) || (q.msg && len(data) == 0 && done == 0) {
		err := Park(ctx, &q.writers, func() bool {
			q.mu.Lock()
			defer q.mu.Unlock()
			return len(q.buf) < q.limit || q.hungup
		})
		if err != nil {
			if done == 0 {
				return 0, err
			}
			return done, nil
		}
		q.mu.Lock()
		if q.hungup {
			q.mu.Unlock()
			return done, Error(abi.Ehungup)
		}
		n := min(q.limit-len(q.buf), len(data)-done)
		if q.msg {
			n = len(data) - done
			q.lens = append(q.lens, n)
		}
		q.buf = append(q.buf, data[done:done+n]...)
		done += n
		q.mu.Unlock()
		q.notify()
		if q.msg {
			break
		}
	}
	return done, nil
}

// Produce adds data without blocking, dropping it when the queue is full
// (Inferno's qproduce).
func (q *Queue) Produce(data []byte) {
	q.mu.Lock()
	if q.hungup || len(q.buf)+len(data) > q.limit {
		q.mu.Unlock()
		return
	}
	q.buf = append(q.buf, data...)
	if q.msg {
		q.lens = append(q.lens, len(data))
	}
	q.mu.Unlock()
	q.notify()
}

// Hangup marks the queue hung up and wakes everyone.
func (q *Queue) Hangup() {
	q.mu.Lock()
	q.hungup = true
	q.mu.Unlock()
	q.notify()
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buf)
}
