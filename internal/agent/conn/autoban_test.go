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

func TestBanFramesTravelAndTheAckReachesTheHandler(t *testing.T) {
	var srv *fakeServer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	defer ts.Close()
	c, _, s := newTestClient(t, ts.URL)
	srv = s

	acked := make(chan uint64, 4)
	c.SetBansAckHandler(func(seq uint64) { acked <- seq })
	trafficAcked := make(chan uint64, 1)
	c.SetTrafficAckHandler(func(seq uint64) { trafficAcked <- seq })

	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	ban := agentprotocol.Ban{ID: "b1", ServiceID: "svc", RouteID: "svc", Prefix: "203.0.113.7/32", StartedAt: at, ExpiresAt: at.Add(time.Hour), Strikes: 10}
	c.AutoBans(agentprotocol.BanFrame{Seq: 1, Snapshot: true, Services: []string{"svc"},
		Events: []agentprotocol.BanEvent{{EventID: "e1", Kind: agentprotocol.BanEventBan, At: at, Ban: ban}}})

	received := make(chan agentprotocol.BanFrame, 2)
	done := make(chan struct{})
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		defer close(done)
		for {
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, data, err := ws.Read(rctx)
			cancel()
			if err != nil {
				return
			}
			f, err := agentprotocol.Decode(data)
			if err != nil || f.Type != agentprotocol.TypeBans {
				continue
			}
			var b agentprotocol.BanFrame
			if f.Into(&b) != nil {
				return
			}
			received <- b
			ack, _ := agentprotocol.Encode(agentprotocol.TypeBansAck, "", agentprotocol.BanAck{Seq: b.Seq})
			_ = ws.Write(ctx, websocket.MessageText, ack)
			// An ack of another kind never reaches the bans handler.
			other, _ := agentprotocol.Encode(agentprotocol.TypeTrafficAck, "", agentprotocol.TrafficAck{Seq: 9})
			_ = ws.Write(ctx, websocket.MessageText, other)
			wsFrames(ctx, ws, func(agentprotocol.Frame) bool { return false })
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	require.Equal(t, uint64(9), recv(t, trafficAcked))
	cancel()
	<-done

	got := <-received
	require.EqualValues(t, 1, got.Seq)
	require.True(t, got.Snapshot)
	require.Equal(t, []string{"svc"}, got.Services)
	require.Equal(t, ban.Prefix, got.Events[0].Ban.Prefix)
	require.True(t, ban.ExpiresAt.Equal(got.Events[0].Ban.ExpiresAt))
	require.Equal(t, uint64(1), <-acked)
	require.Empty(t, acked, "traffic acks are not ban acks")
}
