package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
)

func TestLimiterAllowsBudgetThenRefills(t *testing.T) {
	l := newTokenLimiter(3, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	a, b := uuid.New(), uuid.New()

	for i := range 3 {
		require.True(t, l.allow(a), "request %d", i)
	}
	require.False(t, l.allow(a), "budget of 3 per minute exhausted")
	require.True(t, l.allow(b), "other tokens have their own bucket")

	now = now.Add(21 * time.Second) // one token refills every 20s
	require.True(t, l.allow(a))
	require.False(t, l.allow(a))
}

func TestOkResultDropsOversizedPayload(t *testing.T) {
	res := okResult(map[string]string{"blob": strings.Repeat("a", MaxResultBytes+10)})
	require.False(t, res.IsError)
	body := res.Content[0].(*mcp.TextContent).Text
	require.LessOrEqual(t, len(body), MaxResultBytes)
	var notice struct{ Truncated bool }
	require.NoError(t, json.Unmarshal([]byte(body), &notice))
	require.True(t, notice.Truncated)
}

func TestListFitShrinksBelowCap(t *testing.T) {
	items := make([]string, 400)
	for i := range items {
		items[i] = strings.Repeat("y", 1000)
	}
	l := newList(items, "next")
	l.Fit()
	require.True(t, l.Truncated)
	require.Empty(t, l.NextCursor, "a cursor over dropped items would skip them")
	require.LessOrEqual(t, jsonSize(l), MaxResultBytes)
	require.NotEmpty(t, l.Items)
}

func TestErrorMapping(t *testing.T) {
	ctx, log := context.Background(), slog.Default()

	require.Equal(t, "forbidden", toToolError(ctx, log, "t", apierr.Forbidden()).Code)
	require.Equal(t, "server_offline", toToolError(ctx, log, "t", apierr.New(503, "server_offline", "the server is offline")).Code)
	require.Equal(t, "not_found", toToolError(ctx, log, "t", apierr.NotFound("service")).Code)

	te := toToolError(ctx, log, "t", apierr.Internal())
	require.Equal(t, CodeInternal, te.Code)
	require.Equal(t, "internal error", te.Message)

	require.Equal(t, CodeTimeout, toToolError(ctx, log, "t", context.DeadlineExceeded).Code)
	require.Equal(t, "canceled", toToolError(ctx, log, "t", context.Canceled).Code)
}

func TestErrorResultCarriesCodeAndMessage(t *testing.T) {
	res := errorResult(toolErr(CodeOutcomeUncertain, "server %q offline", "server-01"))
	require.True(t, res.IsError)
	var got ToolError
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &got))
	require.Equal(t, CodeOutcomeUncertain, got.Code)
	require.Contains(t, got.Message, "server-01")
}

func TestClipLineKeepsUTF8(t *testing.T) {
	long := strings.Repeat("é", MaxLogLineLen) // 2 bytes per rune
	out := mcpkit.Clip(long)
	require.Contains(t, out, "[line truncated]")
	require.True(t, strings.ToValidUTF8(out, "") == out)
	require.Equal(t, "short", mcpkit.Clip("short"))
}
