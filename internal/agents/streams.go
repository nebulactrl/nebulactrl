package agents

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Interactive streams (terminal sessions, file transfers) ride the agent's
// connection as ephemeral frames, outside the durable command queue: nothing is
// persisted, and a stream ends when either side closes it or the connection
// drops. The hub only routes: frames from the agent whose envelope ID names a
// registered stream are queued on that stream; everything else is dropped. The
// owner of the stream (internal/terminal) implements the protocol on top.

// ErrNotConnected is returned when no agent is connected for the server.
var ErrNotConnected = errors.New("server is not connected")

// streamQueue bounds the frames queued for one stream. The stream window keeps
// a well-behaved agent far below it; overflowing it ends the stream.
const streamQueue = 256

// A stream's queue is also bounded in bytes (agentprotocol.MaxStreamQueueBytes):
// every frame may be up to the connection's 8 MiB read limit, so 256 frames
// alone would let one agent park gigabytes per open user stream. A well-behaved
// agent keeps at most StreamWindow unacknowledged payload bytes in flight.

// agentStreamType reports whether typ is a frame type an agent may send on a stream.
func agentStreamType(typ string) bool {
	switch typ {
	case agentprotocol.TypeExecOpened, agentprotocol.TypeExecOutput, agentprotocol.TypeExecExit,
		agentprotocol.TypeFSResponse, agentprotocol.TypeFSData, agentprotocol.TypeStreamAck, agentprotocol.TypeDumpRestoreEvent:
		return true
	}
	return false
}

// Stream is one open exchange with an agent, identified by ID (the envelope ID
// of every frame of the exchange).
type Stream struct {
	ID string

	h      *Hub
	c      *agentConn
	frames chan agentprotocol.Frame
	done   chan struct{}
	once   sync.Once
	lost   atomic.Bool

	// sizes remembers the encoded size of the last streamQueue frames queued,
	// and count how many were queued in all; only the connection's reader
	// goroutine (routeStream) touches them. The frames still queued are the
	// last len(frames) of them.
	sizes [streamQueue]int
	count uint64
}

// OpenStream registers stream id on the server's connection. id must be unique
// (a fresh UUID); it fails with ErrNotConnected when the server is offline.
func (h *Hub) OpenStream(serverID uuid.UUID, id string) (*Stream, error) {
	c := h.conn(serverID)
	if c == nil || c.closed() {
		return nil, ErrNotConnected
	}
	s := &Stream{ID: id, h: h, c: c, frames: make(chan agentprotocol.Frame, streamQueue), done: make(chan struct{})}
	h.strMu.Lock()
	if h.streams == nil {
		h.streams = map[string]*Stream{}
	}
	if _, dup := h.streams[id]; dup {
		h.strMu.Unlock()
		return nil, errors.New("stream id already in use")
	}
	h.streams[id] = s
	h.strMu.Unlock()
	return s, nil
}

// Frames delivers the agent's frames of this stream, in order. It is never
// closed; select on Done as well.
func (s *Stream) Frames() <-chan agentprotocol.Frame { return s.frames }

// Done is closed when the stream ended: Close, a dropped agent connection, or
// an overflowing queue.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Lost reports that the stream ended because the connection dropped (or the
// agent overran its window), not because the owner closed it.
func (s *Stream) Lost() bool { return s.lost.Load() }

// Send queues a frame to the agent. It fails once the connection is gone.
func (s *Stream) Send(typ string, body any) error {
	select {
	case <-s.done:
		return errConnClosed
	default:
	}
	return s.c.sendFrame(typ, s.ID, body)
}

// Close ends the stream and unregisters it. Safe to call more than once.
func (s *Stream) Close() { s.end(false) }

func (s *Stream) end(lost bool) {
	s.once.Do(func() {
		if lost {
			s.lost.Store(true)
		}
		s.h.strMu.Lock()
		if s.h.streams[s.ID] == s {
			delete(s.h.streams, s.ID)
		}
		s.h.strMu.Unlock()
		close(s.done)
	})
}

// routeStream queues an agent's stream frame on its stream. It runs on the
// connection's reader goroutine and never blocks.
func (h *Hub) routeStream(c *agentConn, f agentprotocol.Frame) {
	if !agentStreamType(f.Type) {
		return
	}
	h.strMu.Lock()
	s := h.streams[f.ID]
	h.strMu.Unlock()
	if s == nil || s.c != c { // unknown, finished, or another server's stream
		return
	}
	if !s.queue(f) {
		c.log.Warn("stream queue overflow; ending the stream", "stream", f.ID, "type", f.Type)
		s.end(true)
	}
}

// queue appends f to the stream's frames unless that would exceed the frame or
// byte budget of the queue. It never blocks.
func (s *Stream) queue(f agentprotocol.Frame) bool {
	size := len(f.Raw)
	queued := size
	for i, n := uint64(0), uint64(len(s.frames)); i < n && queued <= agentprotocol.MaxStreamQueueBytes; i++ {
		queued += s.sizes[(s.count-1-i)%streamQueue]
	}
	if queued > agentprotocol.MaxStreamQueueBytes {
		return false
	}
	select {
	case s.frames <- f:
		s.sizes[s.count%streamQueue] = size
		s.count++
		return true
	default:
		return false
	}
}

// closeStreams ends every stream of a connection that went away.
func (h *Hub) closeStreams(c *agentConn) {
	h.strMu.Lock()
	var lost []*Stream
	for _, s := range h.streams {
		if s.c == c {
			lost = append(lost, s)
		}
	}
	h.strMu.Unlock()
	for _, s := range lost {
		s.end(true)
	}
}
