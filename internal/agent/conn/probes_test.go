package conn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Probes frames are buffered and acknowledged, never superseded like stats:
// every queued result batch reaches the control plane.
func TestOutboxKeepsEveryProbesFrame(t *testing.T) {
	o := newOutbox(10)
	for _, p := range []string{"p1", "p2", "p3"} {
		o.push(outFrame{typ: agentprotocol.TypeProbes, data: []byte(p)})
	}
	var got []string
	for f, ok := o.tryNext(); ok; f, ok = o.tryNext() {
		got = append(got, string(f.data))
	}
	require.Equal(t, []string{"p1", "p2", "p3"}, got, "probes frames do not replace each other")
}

func TestProbesFramesTravelAndTheAckReachesTheHandler(t *testing.T) {
	var srv *fakeServer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	defer ts.Close()
	c, _, s, _ := newTestClient(t, ts.URL)
	srv = s

	acked := make(chan uint64, 4)
	c.SetProbesAckHandler(func(seq uint64) { acked <- seq })
	trafficAcked := make(chan uint64, 1)
	c.SetTrafficAckHandler(func(seq uint64) { trafficAcked <- seq })

	// Queued while the socket is down; flushed in order after the handshake.
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	c.Probes(agentprotocol.Probes{Seq: 1, Results: []agentprotocol.ProbeResult{{ServiceID: "svc-a", At: at, Outcome: agentprotocol.ProbeUp, Replicas: 1, Passed: 1}}})
	c.Probes(agentprotocol.Probes{Seq: 2, Results: []agentprotocol.ProbeResult{{ServiceID: "svc-a", At: at.Add(30 * time.Second), Outcome: agentprotocol.ProbeDown, Replicas: 1, Error: "HTTP 503"}}})

	received := make(chan agentprotocol.Probes, 4)
	done := make(chan struct{})
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		defer close(done)
		for n := 0; n < 2; {
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, data, err := ws.Read(rctx)
			cancel()
			if err != nil {
				return
			}
			f, err := agentprotocol.Decode(data)
			if err != nil || f.Type != agentprotocol.TypeProbes {
				continue
			}
			var p agentprotocol.Probes
			if f.Into(&p) != nil {
				return
			}
			received <- p
			n++
			ack, _ := agentprotocol.Encode(agentprotocol.TypeProbesAck, "", agentprotocol.ProbesAck{Seq: p.Seq})
			if ws.Write(ctx, websocket.MessageText, ack) != nil {
				return
			}
		}
		// An ack of another kind never reaches the probes handler.
		other, _ := agentprotocol.Encode(agentprotocol.TypeTrafficAck, "", agentprotocol.TrafficAck{Seq: 9})
		_ = ws.Write(ctx, websocket.MessageText, other)
		time.Sleep(500 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	<-done
	cancel()

	require.Len(t, received, 2)
	first, second := <-received, <-received
	require.EqualValues(t, 1, first.Seq)
	require.EqualValues(t, 2, second.Seq)
	require.Equal(t, "HTTP 503", second.Results[0].Error)
	require.True(t, at.Equal(first.Results[0].At), "timestamps survive the wire")

	require.Equal(t, uint64(1), <-acked)
	require.Equal(t, uint64(2), <-acked)
	require.Empty(t, acked, "traffic acks are not probe acks")
	require.Equal(t, uint64(9), <-trafficAcked)
}
