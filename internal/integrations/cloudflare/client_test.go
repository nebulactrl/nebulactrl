package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListZonesPagesAndSorts(t *testing.T) {
	f := newFake(t)
	for i := range 120 {
		f.addZone(fmt.Sprintf("z%03d", i), fmt.Sprintf("d%03d.example.com", 119-i))
	}
	zones, err := f.client().ListZones(context.Background())
	require.NoError(t, err)
	require.Len(t, zones, 120, "every page is read")
	require.Equal(t, "d000.example.com", zones[0].Name, "sorted by name")
	require.Equal(t, "acc1", zones[0].AccountID)
}

func TestAPIErrorsAreTypedAndNeverEchoTheToken(t *testing.T) {
	f := newFake(t)
	f.addZone("z1", "example.com")
	f.echoToken = true

	bad := New(Config{BaseURL: f.srv.URL + "/client/v4", Token: "wrong-" + secretToken, HTTPClient: f.srv.Client()})
	_, err := bad.ListZones(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAuth))
	require.NotContains(t, err.Error(), "wrong-"+secretToken)

	f.failWrites = http.StatusBadRequest
	_, err = f.client().CreateRecord(context.Background(), "z1", RecordInput{Type: "A", Name: "a.example.com", Content: "203.0.113.1", TTL: 1})
	var ae *APIError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusBadRequest, ae.Status)
	require.Equal(t, 9999, ae.Code)
	require.NotContains(t, err.Error(), secretToken, "the token is scrubbed from Cloudflare's message")
	require.Contains(t, ae.Message, "[redacted]")
	require.False(t, ae.Retryable())

	f.failWrites = http.StatusBadGateway
	_, err = f.client().CreateRecord(context.Background(), "z1", RecordInput{Type: "A", Name: "a.example.com", Content: "203.0.113.1", TTL: 1})
	require.ErrorAs(t, err, &ae)
	require.True(t, ae.Retryable())

	_, err = f.client().GetRecord(context.Background(), "z1", "missing")
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestRecordCRUDKeepsTheMarker(t *testing.T) {
	f := newFake(t)
	f.addZone("z1", "example.com")
	c := f.client()
	ctx := context.Background()

	rec, err := c.CreateRecord(ctx, "z1", RecordInput{
		Type: "A", Name: "app.example.com", Content: "203.0.113.10", TTL: 1, Comment: Marker(testDomain), Tags: []string{MarkerTag},
	})
	require.NoError(t, err)
	require.NotEmpty(t, rec.ID)

	got, err := c.ListRecords(ctx, "z1", "APP.example.com")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, Marker(testDomain), got[0].Comment)
	require.Equal(t, []string{MarkerTag}, got[0].Tags)

	upd, err := c.UpdateRecord(ctx, "z1", rec.ID, RecordInput{Content: "198.51.100.7", Proxied: true, TTL: 1, Comment: Marker(testDomain)})
	require.NoError(t, err)
	require.Equal(t, "198.51.100.7", upd.Content)
	require.True(t, upd.Proxied)

	require.NoError(t, c.DeleteRecord(ctx, "z1", rec.ID))
	require.True(t, errors.Is(c.DeleteRecord(ctx, "z1", rec.ID), ErrNotFound))
	got, err = c.ListRecords(ctx, "z1", "app.example.com")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestTagsAreBestEffort(t *testing.T) {
	f := newFake(t)
	f.addZone("z1", "example.com")
	f.noTags = true
	c := f.client()

	rec, err := c.CreateRecord(context.Background(), "z1", RecordInput{
		Type: "A", Name: "app.example.com", Content: "203.0.113.10", TTL: 1, Comment: Marker(testDomain), Tags: []string{MarkerTag},
	})
	require.NoError(t, err, "a plan without tags still gets the record")
	got := f.records("app.example.com")
	require.Len(t, got, 1)
	require.Equal(t, rec.ID, got[0].ID)
	require.Equal(t, Marker(testDomain), got[0].Comment, "the comment carries the marker either way")
	require.Empty(t, got[0].Tags)

	_, err = c.UpdateRecord(context.Background(), "z1", rec.ID, RecordInput{Content: "198.51.100.7", TTL: 1, Comment: Marker(testDomain), Tags: []string{MarkerTag}})
	require.NoError(t, err)
}

func TestListRecordsOnlyReturnsAddressAndAliasRecords(t *testing.T) {
	f := newFake(t)
	f.addZone("z1", "example.com")
	f.put(Record{Type: "TXT", Name: "app.example.com", Content: "v=spf1"})
	f.put(Record{Type: "A", Name: "app.example.com", Content: "203.0.113.10"})
	f.put(Record{Type: "A", Name: "other.example.com", Content: "203.0.113.11"})
	got, err := f.client().ListRecords(context.Background(), "z1", "app.example.com")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "A", got[0].Type)
}

func TestVerifyTokenFallsBackToTheAccountEndpoint(t *testing.T) {
	f := newFake(t)
	st, err := f.client().VerifyToken(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "active", st.Status)
	require.NotNil(t, st.ExpiresOn)

	f.verifyDenied = true
	_, err = f.client().VerifyToken(context.Background(), "")
	require.True(t, errors.Is(err, ErrAuth), "no account known: the user endpoint's refusal is returned")
	st, err = f.client().VerifyToken(context.Background(), "acc1")
	require.NoError(t, err, "an account-owned token verifies at the account endpoint")
	require.Equal(t, "active", st.Status)
}

func TestZoneSSLMode(t *testing.T) {
	f := newFake(t)
	f.addZone("z1", "example.com")
	mode, err := f.client().ZoneSSLMode(context.Background(), "z1")
	require.NoError(t, err)
	require.Equal(t, "full", mode)
	f.ssl["z1"] = "flexible"
	mode, err = f.client().ZoneSSLMode(context.Background(), "z1")
	require.NoError(t, err)
	require.Equal(t, "flexible", mode)
}
