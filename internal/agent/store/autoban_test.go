package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func banOf(route, prefix string) agentprotocol.Ban {
	return agentprotocol.Ban{ID: route + prefix, ServiceID: route, RouteID: route, Prefix: prefix, Strikes: 10}
}

func eventOf(id string, at time.Time, kind string, b agentprotocol.Ban) agentprotocol.BanEvent {
	return agentprotocol.BanEvent{EventID: id, Kind: kind, At: at, Ban: b}
}

func TestBansAndOutboxSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	require.NoError(t, err)
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	a, b := banOf("r1", "203.0.113.7/32"), banOf("r2", "2001:db8:1:2::/64")
	require.NoError(t, st.ApplyBans(BanChange{
		Put: []agentprotocol.Ban{a, b},
		Events: []agentprotocol.BanEvent{
			eventOf("e2", base.Add(time.Second), agentprotocol.BanEventBan, b),
			eventOf("e1", base, agentprotocol.BanEventBan, a),
		},
	}))
	require.NoError(t, st.Close())

	st, err = Open(dir)
	require.NoError(t, err)
	defer st.Close()
	bans, err := st.LoadBans()
	require.NoError(t, err)
	require.ElementsMatch(t, []agentprotocol.Ban{a, b}, bans)
	keys, events, err := st.PendingBanEvents(0)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Equal(t, "e1", events[0].EventID, "oldest first")
	require.Equal(t, "e2", events[1].EventID)

	_, limited, err := st.PendingBanEvents(1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
}

func TestApplyBansDeletesBansAndAckedEvents(t *testing.T) {
	st, err := Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	a := banOf("r1", "203.0.113.7/32")
	require.NoError(t, st.ApplyBans(BanChange{Put: []agentprotocol.Ban{a}, Events: []agentprotocol.BanEvent{eventOf("e1", base, agentprotocol.BanEventBan, a)}}))
	require.NoError(t, st.ApplyBans(BanChange{Delete: []agentprotocol.Ban{a}, Events: []agentprotocol.BanEvent{eventOf("e2", base.Add(time.Minute), agentprotocol.BanEventEnd, a)}}))

	bans, err := st.LoadBans()
	require.NoError(t, err)
	require.Empty(t, bans)
	keys, events, err := st.PendingBanEvents(0)
	require.NoError(t, err)
	require.Len(t, events, 2, "the end event is queued next to the unacknowledged ban event")

	require.NoError(t, st.DeleteBanEvents(keys[:1]))
	_, events, err = st.PendingBanEvents(0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "e2", events[0].EventID)
}

func TestTrimBanEventsDropsTheOldest(t *testing.T) {
	st, err := Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	a := banOf("r1", "203.0.113.7/32")
	var evs []agentprotocol.BanEvent
	for i, id := range []string{"e1", "e2", "e3", "e4"} {
		evs = append(evs, eventOf(id, base.Add(time.Duration(i)*time.Second), agentprotocol.BanEventBan, a))
	}
	require.NoError(t, st.ApplyBans(BanChange{Events: evs}))
	n, err := st.TrimBanEvents(10)
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = st.TrimBanEvents(2)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	_, events, err := st.PendingBanEvents(0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "e3", events[0].EventID)
}
