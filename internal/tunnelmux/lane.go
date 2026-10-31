package tunnelmux

import (
	"sync"
	"time"
)

// lane is the outbound queue of binary data frames: one FIFO per stream,
// served round-robin so a stream with a deep queue cannot starve the others.
type lane struct {
	mu   sync.Mutex
	qs   map[uint32][]laneItem
	ring []uint32 // streams with queued frames, next to serve first
	sig  chan struct{}
}

type laneItem struct {
	frame   []byte
	payload int
}

func newLane() lane {
	return lane{qs: map[uint32][]laneItem{}, sig: make(chan struct{}, 1)}
}

func (l *lane) push(no uint32, frame []byte, payload int) {
	l.mu.Lock()
	q := l.qs[no]
	if len(q) == 0 {
		l.ring = append(l.ring, no)
	}
	l.qs[no] = append(q, laneItem{frame, payload})
	l.mu.Unlock()
	select {
	case l.sig <- struct{}{}:
	default:
	}
}

// pop returns the next frame, rotating the served stream to the back.
func (l *lane) pop() (frame []byte, payload int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ring) == 0 {
		return nil, 0, false
	}
	no := l.ring[0]
	q := l.qs[no]
	it := q[0]
	q[0] = laneItem{}
	q = q[1:]
	if len(q) == 0 {
		delete(l.qs, no)
		l.ring = l.ring[1:]
	} else {
		l.qs[no] = q
		copy(l.ring, l.ring[1:])
		l.ring[len(l.ring)-1] = no
	}
	return it.frame, it.payload, true
}

// drop removes the queued frames of a stream and returns their payload bytes.
func (l *lane) drop(no uint32) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	q, ok := l.qs[no]
	if !ok {
		return 0
	}
	freed := 0
	for _, it := range q {
		freed += it.payload
	}
	delete(l.qs, no)
	for i, r := range l.ring {
		if r == no {
			l.ring = append(l.ring[:i], l.ring[i+1:]...)
			break
		}
	}
	return freed
}

func (l *lane) reset() {
	l.mu.Lock()
	l.qs = map[uint32][]laneItem{}
	l.ring = nil
	l.mu.Unlock()
}

// deadline is a resettable timer exposed as a channel, as in net.Pipe: wait
// returns a channel that is closed once the deadline passed.
type deadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel chan struct{}
}

func newDeadline() *deadline { return &deadline{cancel: make(chan struct{})} }

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // wait for the timer callback to finish and close cancel
	}
	d.timer = nil
	closed := isClosed(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		d.timer = time.AfterFunc(dur, func() { close(d.cancel) })
		return
	}
	if !closed {
		close(d.cancel)
	}
}

func (d *deadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
