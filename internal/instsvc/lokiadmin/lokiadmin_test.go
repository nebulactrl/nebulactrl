package lokiadmin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
)

func TestQueryRangeAsksForOneTenantAndParsesStreams(t *testing.T) {
	var gotTenant, gotQuery, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, gotQuery, gotPath = r.Header.Get("X-Scope-OrgID"), r.URL.Query().Get("query"), r.URL.Path
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"service_id":"s1"},"values":[["1793357005000000000","line two"],["1793357004000000000","line one"]]}]}}`)
	}))
	defer srv.Close()
	b, err := New(Config{URL: srv.URL})
	require.NoError(t, err)

	now := time.Now()
	streams, err := b.QueryRange(context.Background(), "team-1", `{service_id="s1"}`, now.Add(-time.Hour), now, 50)
	require.NoError(t, err)
	require.Equal(t, "team-1", gotTenant)
	require.Equal(t, "/loki/api/v1/query_range", gotPath)
	require.Equal(t, `{service_id="s1"}`, gotQuery)
	require.Len(t, streams, 1)
	require.Equal(t, map[string]string{"service_id": "s1"}, streams[0].Labels)
	require.Equal(t, []Entry{
		{Time: time.Unix(0, 1793357005000000000).UTC(), Line: "line two"},
		{Time: time.Unix(0, 1793357004000000000).UTC(), Line: "line one"},
	}, streams[0].Entries)

	_, err = b.QueryRange(context.Background(), "", "{}", now, now, 1)
	require.Error(t, err, "a query without a tenant would be Loki's decision, not ours")
}

func TestPurgeDeletesTheTenantAndReportsRefusals(t *testing.T) {
	var tenant, start string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, start = r.Header.Get("X-Scope-OrgID"), r.URL.Query().Get("start")
		require.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	b, err := New(Config{URL: srv.URL})
	require.NoError(t, err)
	require.NoError(t, b.Purge(context.Background(), instsvc.Slice{Kind: "loki", Ref: "team-1"}))
	require.Equal(t, "team-1", tenant)
	require.NotEqual(t, "0", start, "Loki rejects a start of 0")
	status = http.StatusBadRequest
	require.Error(t, b.Purge(context.Background(), instsvc.Slice{Kind: "loki", Ref: "team-1"}))
	require.Error(t, b.Purge(context.Background(), instsvc.Slice{Kind: "loki"}))
}

func TestHealth(t *testing.T) {
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	defer srv.Close()
	b, err := New(Config{URL: srv.URL})
	require.NoError(t, err)
	require.NoError(t, b.Health(context.Background()))
	code = http.StatusServiceUnavailable
	require.ErrorContains(t, b.Health(context.Background()), "not ready")
}
