package sysupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

func TestParseFeed(t *testing.T) {
	f, err := ParseFeed(strings.NewReader(`{
		"version": "v0.1.2",
		"publishedAt": "2026-10-29T10:00:00Z",
		"notesUrl": "https://github.com/nebulactrl/nebulactrl/releases/tag/v0.1.2",
		"notes": "\n\n## Fixes\n- one\n\n  ",
		"minAgent": "0.1.0",
		"somethingNew": true
	}`))
	require.NoError(t, err)
	require.Equal(t, "0.1.2", f.Version, "the v prefix is dropped")
	require.Equal(t, "## Fixes\n- one", f.Notes, "notes are trimmed")
	require.Equal(t, "https://github.com/nebulactrl/nebulactrl/releases/tag/v0.1.2", f.NotesURL)
	require.Equal(t, "0.1.0", f.MinAgent)
	require.NotNil(t, f.PublishedAt)
	require.Equal(t, time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC), f.PublishedAt.UTC())

	// Only the version is required.
	f, err = ParseFeed(strings.NewReader(`{"version":"1.0.0"}`))
	require.NoError(t, err)
	require.Nil(t, f.PublishedAt)
	require.Empty(t, f.NotesURL)
	require.Empty(t, f.Notes)
	require.Empty(t, f.MinAgent)

	// A notes link that is not http(s) and a minAgent that is not a version are dropped, not trusted.
	f, err = ParseFeed(strings.NewReader(`{"version":"1.0.0","notesUrl":"javascript:alert(1)","minAgent":"latest"}`))
	require.NoError(t, err)
	require.Empty(t, f.NotesURL)
	require.Empty(t, f.MinAgent)

	for name, doc := range map[string]string{
		"not json":       `<html>`,
		"no version":     `{}`,
		"latest":         `{"version":"latest"}`,
		"pre-release":    `{"version":"0.2.0-rc.1"}`,
		"git describe":   `{"version":"0.1.0-3-gabc1234"}`,
		"version number": `{"version":2}`,
	} {
		_, err := ParseFeed(strings.NewReader(doc))
		require.Error(t, err, name)
	}
}

func TestParseFeedCapsNotesOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", maxNotes) // two bytes each
	f, err := ParseFeed(strings.NewReader(`{"version":"1.0.0","notes":"` + long + `"}`))
	require.NoError(t, err)
	require.LessOrEqual(t, len(f.Notes), maxNotes)
	require.Greater(t, len(f.Notes), maxNotes-4)
	require.True(t, strings.HasSuffix(f.Notes, "é"), "no half character at the cut")
}

func TestAvailable(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		want            bool
		why             string
	}{
		{"0.1.0", "0.1.2", true, "newer patch"},
		{"0.1.9", "0.2.0", true, "newer minor"},
		{"v0.1.0", "v0.1.2", true, "v prefixes"},
		{"0.1.2", "0.1.2", false, "the same version"},
		{"0.2.0", "0.1.2", false, "older"},
		{"dev", "9.9.9", false, "a development build never has an update"},
		{"", "9.9.9", false, "no version"},
		{"0.1.0-3-gabc1234-dirty", "9.9.9", false, "git describe output is a development build"},
		{"0.1.0", "", false, "nothing announced"},
		{"0.1.0", "0.2.0-rc.1", false, "a pre-release is never offered"},
		{"0.2.0-rc.1", "0.2.0", true, "a pre-release build is behind its release"},
		{"0.1.0", "nonsense", false, "an unreadable version"},
	} {
		require.Equal(t, c.want, Available(c.current, c.latest), c.why)
	}
}

func TestFetcherSendsOnlyAUserAgent(t *testing.T) {
	var got http.Header
	var path string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path = r.Header.Clone(), r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"version":"0.1.2"}`))
	}))
	defer srv.Close()

	f, err := Fetcher{HTTP: srv.Client(), URL: srv.URL + "/releases/latest.json", UserAgent: "nebulactrl/0.1.0"}.Fetch(context.Background())
	require.NoError(t, err)
	require.Equal(t, "0.1.2", f.Version)
	require.Equal(t, "/releases/latest.json", path, "no query string, nothing about this installation")
	require.Equal(t, "nebulactrl/0.1.0", got.Get("User-Agent"))
	for _, h := range []string{"Cookie", "Authorization", "Referer", "X-Forwarded-For"} {
		require.Empty(t, got.Values(h), h)
	}
}

func TestFetcherErrors(t *testing.T) {
	status := http.StatusInternalServerError
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"version":"0.1.2"}`))
	}))
	defer srv.Close()
	_, err := Fetcher{HTTP: srv.Client(), URL: srv.URL, UserAgent: "nebulactrl/dev"}.Fetch(context.Background())
	require.ErrorContains(t, err, "500")

	srv.Close()
	_, err = Fetcher{URL: srv.URL, UserAgent: "nebulactrl/dev"}.Fetch(context.Background())
	require.ErrorContains(t, err, "could not reach the release feed")
}

func TestFetcherRefusesAPlainHTTPFeed(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"version":"0.1.2"}`))
	}))
	defer srv.Close()
	_, err := Fetcher{URL: srv.URL + "/releases/latest.json", UserAgent: "nebulactrl/dev"}.Fetch(context.Background())
	require.ErrorContains(t, err, "must be https://")
	require.Zero(t, hits, "nothing is sent to a plain http feed")
}

func TestSettleDecision(t *testing.T) {
	now := time.Now()
	run := func(from, target string, age time.Duration) db.InstanceUpdate {
		started := now.Add(-age)
		return db.InstanceUpdate{State: StateRunning, FromVersion: from, TargetVersion: target, StartedAt: &started}
	}
	for _, c := range []struct {
		name    string
		row     db.InstanceUpdate
		current string
		state   string
		msg     string
	}{
		{"the target runs", run("0.1.0", "0.1.2", time.Minute), "0.1.2", StateSucceeded, ""},
		{"the target runs, with a v prefix", run("0.1.0", "0.1.2", time.Minute), "v0.1.2", StateSucceeded, ""},
		{"still the old version while the installer works", run("0.1.0", "0.1.2", 5*time.Minute), "0.1.0", "", ""},
		{"a third version came up", run("0.1.0", "0.1.2", time.Minute), "0.0.9", StateFailed, "came up as 0.0.9, not as the requested 0.1.2"},
		{"no result for too long", run("0.1.0", "0.1.2", StaleAfter+time.Minute), "0.1.0", StateFailed, "No result within 30 minutes"},
		{"not running", db.InstanceUpdate{State: StateFailed, TargetVersion: "0.1.2"}, "0.1.2", "", ""},
		{"never updated", db.InstanceUpdate{}, "0.1.2", "", ""},
	} {
		state, msg := settleDecision(c.row, c.current, now)
		require.Equal(t, c.state, state, c.name)
		require.Contains(t, msg, c.msg, c.name)
	}
}
