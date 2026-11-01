package logs

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DefaultTail is used when a subscription does not ask for a tail.
const DefaultTail = 200

// ReadLines calls fn for every line of r (timestamp prefix parsed when
// present). It returns when r ends or errors; io.EOF is not an error.
func ReadLines(r io.Reader, fn func(ts time.Time, text string)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, isPrefix, err := br.ReadLine()
		if len(line) > 0 || err == nil {
			ts, text := docker.ParseLogLine(string(line))
			// A very long line arrives in pieces; drop the rest of it.
			for isPrefix && err == nil {
				_, isPrefix, err = br.ReadLine()
			}
			fn(ts, text)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// Tail returns the last n lines of a container's output (stdout and stderr).
func Tail(ctx context.Context, eng docker.Engine, id string, n int) ([]string, error) {
	rc, err := eng.ContainerLogs(ctx, id, docker.LogOptions{Tail: n})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var out []string
	err = ReadLines(rc, func(_ time.Time, text string) { out = append(out, text) })
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, err
}

// Sink receives the frames produced by subscriptions.
type Sink interface {
	Log(agentprotocol.LogFrame)
	LogEnd(agentprotocol.LogEnd)
}

type sub struct{ cancel context.CancelFunc }

// Subscriptions tracks live runtime log subscriptions by stream ID.
type Subscriptions struct {
	eng  docker.Engine
	sink Sink
	log  *slog.Logger

	mu   sync.Mutex
	subs map[string]*sub
}

// NewSubscriptions builds a subscription registry.
func NewSubscriptions(eng docker.Engine, sink Sink, log *slog.Logger) *Subscriptions {
	if log == nil {
		log = slog.Default()
	}
	return &Subscriptions{eng: eng, sink: sink, log: log, subs: map[string]*sub{}}
}

// Source is one container feeding a subscription. Prefix, when set, is
// prepended to every line so merged replica streams stay distinguishable.
type Source struct {
	ContainerID string
	Prefix      string
}

// Start begins streaming the logs of every source, merged into one stream, for
// req. It returns once every stream is established (or one failed to open, in
// which case none run); frames flow on background goroutines bound to ctx. A
// non-follow subscription ends with one LogEnd after all tails; a follow
// subscription ends when cancelled, when every container stops, or on error.
// Re-using a stream ID replaces the earlier subscription.
func (s *Subscriptions) Start(ctx context.Context, srcs []Source, req agentprotocol.LogsSubscribe) error {
	if len(srcs) == 0 {
		return errors.New("no containers to stream")
	}
	tail := req.Tail
	if tail <= 0 {
		tail = DefaultTail
	}
	sctx, cancel := context.WithCancel(ctx)
	rcs := make([]io.ReadCloser, 0, len(srcs))
	for _, src := range srcs {
		rc, err := s.eng.ContainerLogs(sctx, src.ContainerID, docker.LogOptions{Tail: tail, Follow: req.Follow})
		if err != nil {
			for _, o := range rcs {
				_ = o.Close()
			}
			cancel()
			return err
		}
		rcs = append(rcs, rc)
	}

	s.mu.Lock()
	me := &sub{cancel: cancel}
	if old, ok := s.subs[req.StreamID]; ok {
		old.cancel()
	}
	s.subs[req.StreamID] = me
	s.mu.Unlock()

	b := NewBatcher(agentprotocol.LogFrame{
		StreamID: req.StreamID, ServiceID: req.ServiceID, Stream: agentprotocol.StreamRuntime,
	}, s.sink.Log, nil)
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for i, rc := range rcs {
		prefix := srcs[i].Prefix
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer rc.Close()
			err := ReadLines(rc, func(ts time.Time, text string) { b.AddAt(ts, prefix+text) })
			if err != nil && sctx.Err() == nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			// Only forget our own registration (a replacement may exist).
			if s.subs[req.StreamID] == me {
				delete(s.subs, req.StreamID)
			}
			s.mu.Unlock()
		}()
		wg.Wait()
		b.Close()
		end := agentprotocol.LogEnd{StreamID: req.StreamID}
		if firstErr != nil {
			end.Error = firstErr.Error()
			s.log.Warn("log stream ended with error", "stream_id", req.StreamID, "err", firstErr)
		}
		s.sink.LogEnd(end)
	}()
	return nil
}

// Stop cancels a subscription; unknown IDs are ignored.
func (s *Subscriptions) Stop(streamID string) {
	s.mu.Lock()
	cur := s.subs[streamID]
	s.mu.Unlock()
	if cur != nil {
		cur.cancel()
	}
}
