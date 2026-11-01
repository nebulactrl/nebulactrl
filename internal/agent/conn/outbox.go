package conn

import (
	"sync"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DefaultMaxBuffered bounds the number of droppable frames held while the
// control plane is unreachable.
const DefaultMaxBuffered = 5000

type outFrame struct {
	typ  string
	data []byte
}

// critical frames are never dropped: acks and results carry command state, and
// a dropped stream frame would corrupt a terminal session (stream senders are
// bounded by the stream window instead).
func (f outFrame) critical() bool {
	return f.typ == agentprotocol.TypeAck || f.typ == agentprotocol.TypeResult || agentprotocol.IsStreamType(f.typ)
}

// latestOnly frames supersede an older queued frame of the same type.
func latestOnly(typ string) bool {
	switch typ {
	case agentprotocol.TypeHeartbeat, agentprotocol.TypeStats, agentprotocol.TypeObserved, agentprotocol.TypeLogStatus:
		return true
	}
	return false
}

// outbox is the agent's outbound frame queue. It survives reconnects: frames
// queued while disconnected are flushed by the next connection's writer. The
// queue holds at most max non-critical frames; beyond that the oldest
// non-critical frame is dropped.
type outbox struct {
	mu     sync.Mutex
	items  []outFrame
	max    int
	signal chan struct{}
}

func newOutbox(max int) *outbox {
	if max <= 0 {
		max = DefaultMaxBuffered
	}
	return &outbox{max: max, signal: make(chan struct{}, 1)}
}

func (o *outbox) push(f outFrame) {
	o.mu.Lock()
	if latestOnly(f.typ) {
		for i, it := range o.items {
			if it.typ == f.typ {
				o.items = append(o.items[:i], o.items[i+1:]...)
				break
			}
		}
	}
	o.items = append(o.items, f)
	if !f.critical() {
		o.trimLocked()
	}
	o.mu.Unlock()
	select {
	case o.signal <- struct{}{}:
	default:
	}
}

// requeue puts a frame that failed to write back at the front.
func (o *outbox) requeue(f outFrame) {
	o.mu.Lock()
	o.items = append([]outFrame{f}, o.items...)
	o.mu.Unlock()
	select {
	case o.signal <- struct{}{}:
	default:
	}
}

// dropStreams removes every queued stream frame (their streams ended with the
// connection).
func (o *outbox) dropStreams() {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.items[:0]
	for _, it := range o.items {
		if !agentprotocol.IsStreamType(it.typ) {
			kept = append(kept, it)
		}
	}
	o.items = kept
}

func (o *outbox) trimLocked() {
	n := 0
	for _, it := range o.items {
		if !it.critical() {
			n++
		}
	}
	for n > o.max {
		for i, it := range o.items {
			if !it.critical() {
				o.items = append(o.items[:i], o.items[i+1:]...)
				n--
				break
			}
		}
	}
}

// tryNext returns the next frame without waiting. The writer drains it before
// serving the tunnel lane, then waits on signal.
func (o *outbox) tryNext() (outFrame, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.items) == 0 {
		return outFrame{}, false
	}
	f := o.items[0]
	o.items = o.items[1:]
	return f, true
}
