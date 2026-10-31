package agents

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func newQueueStream() *Stream {
	return &Stream{frames: make(chan agentprotocol.Frame, streamQueue)}
}

func frameOf(n int) agentprotocol.Frame {
	return agentprotocol.Frame{Raw: make([]byte, n)}
}

// A stream's queue is bounded in bytes, not only in frames: 256 frames of up to
// 8 MiB each would park gigabytes per open user stream.
func TestStreamQueueIsBoundedInBytes(t *testing.T) {
	s := newQueueStream()
	require.False(t, s.queue(frameOf(agentprotocol.MaxStreamQueueBytes+1)), "one frame above the budget")

	s = newQueueStream()
	chunk := agentprotocol.MaxStreamQueueBytes / 4
	for range 4 {
		require.True(t, s.queue(frameOf(chunk)))
	}
	require.False(t, s.queue(frameOf(1)), "the backlog is full")

	// Consuming makes room again, in order: the queued sizes follow the channel.
	<-s.frames
	require.True(t, s.queue(frameOf(chunk)))
	require.False(t, s.queue(frameOf(1)))
}

func TestStreamQueueStillCountsFrames(t *testing.T) {
	s := newQueueStream()
	for range streamQueue {
		require.True(t, s.queue(frameOf(10)))
	}
	require.False(t, s.queue(frameOf(10)), "256 small frames fill the channel")
	for range 100 {
		<-s.frames
	}
	for range 100 {
		require.True(t, s.queue(frameOf(10)))
	}
}
