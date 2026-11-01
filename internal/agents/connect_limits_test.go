package agents_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Before the signature verifies, a frame is capped at 64 KiB: an unauthenticated
// peer cannot make the control plane buffer megabytes per socket.
func TestOversizedFrameBeforeAuthenticationClosesTheSocket(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/agent/v1/connect", nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	_, data, err := ws.Read(ctx)
	require.NoError(t, err)
	f, err := agentprotocol.Decode(data)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.TypeChallenge, f.Type)

	require.NoError(t, ws.Write(ctx, websocket.MessageText, []byte(strings.Repeat("a", 100<<10))))
	_, _, err = ws.Read(ctx)
	require.Equal(t, websocket.StatusMessageTooBig, websocket.CloseStatus(err))
}

// A source address may start only so many handshakes a minute: each one costs a
// database lookup and a log line.
func TestConnectAttemptsArePerAddressRateLimited(t *testing.T) {
	h := newHarness(t)
	limited := false
	for range 200 {
		resp, err := http.Get(h.srv.URL + "/agent/v1/connect")
		require.NoError(t, err)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			require.NotEmpty(t, resp.Header.Get("Retry-After"))
			limited = true
			break
		}
	}
	require.True(t, limited, "a flood of connect attempts must be refused with 429")
}
