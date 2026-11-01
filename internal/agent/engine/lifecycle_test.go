package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A stopping agent must not go on changing the host after Run returned: the
// integration harness removes the proxy once its agent's Run is back, and a
// mesh sync or a replica convergence still running re-created it. Work
// started with background ends when the engine stops, and Run waits for it.
func TestRunEndsAndWaitsForBackgroundWork(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		r.eng.Run(ctx)
		close(ran)
	}()

	// As refreshMesh and fencing do: detached from the frame that started it.
	frame, frameDone := context.WithCancel(context.Background())
	work, stop := r.eng.background(frame, time.Hour)
	defer stop()
	frameDone() // the frame ending does not end the work
	require.NoError(t, work.Err())

	var finished atomic.Bool
	r.eng.bg.Add(1)
	go func() {
		defer r.eng.bg.Done()
		<-work.Done()
		time.Sleep(50 * time.Millisecond) // unwinding takes a moment
		finished.Store(true)
	}()

	cancel()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	require.True(t, finished.Load(), "Run returned while background work was still running")
	require.ErrorIs(t, work.Err(), context.Canceled, "the engine stopping ends background work")
}
