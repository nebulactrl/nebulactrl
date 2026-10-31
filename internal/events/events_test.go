package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBusFanOutAndCancel(t *testing.T) {
	b := NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	c1, c2 := b.Subscribe(ctx), b.Subscribe(ctx)
	b.Publish(Event{Type: "x", ProjectID: "p"})
	require.Equal(t, "x", (<-c1).Type)
	require.Equal(t, "p", (<-c2).ProjectID)

	cancel()
	select {
	case _, ok := <-c1:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
	b.Publish(Event{Type: "after"}) // must not panic or block
}

func TestPublishNeverBlocksOnSlowSubscriber(t *testing.T) {
	b := NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = b.Subscribe(ctx) // never read
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*3; i++ {
			b.Publish(Event{Type: "spam"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked")
	}
}
