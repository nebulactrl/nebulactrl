package statuspublic

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

type parsedFeed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string   `xml:"id"`
	Title   string   `xml:"title"`
	Updated string   `xml:"updated"`
	Links   []struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
		Link      struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Content string `xml:"content"`
	} `xml:"entry"`
}

func parseFeed(t *testing.T, b []byte) parsedFeed {
	t.Helper()
	var f parsedFeed
	require.NoError(t, xml.Unmarshal(b, &f), string(b))
	return f
}

func feedIDs(f parsedFeed) []string {
	var ids []string
	for _, e := range f.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestFeedHasOneEntryPerUpdateAndPerWindow(t *testing.T) {
	b, err := renderFeed(fixturePage("auto"), testHost)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(b), `<?xml version="1.0" encoding="UTF-8"?>`))
	f := parseFeed(t, b)

	require.Equal(t, "Acme Status", f.Title)
	require.Equal(t, "https://"+testHost+"/", f.ID)
	require.Len(t, f.Links, 2)
	require.Equal(t, "self", f.Links[0].Rel)
	require.Equal(t, "https://"+testHost+"/feed.atom", f.Links[0].Href)
	require.Equal(t, "https://"+testHost+"/", f.Links[1].Href)
	require.Equal(t, "2026-10-29T12:00:00Z", f.Updated, "the page's generation time: no entry is newer")

	// Newest first; equal times by id. The incident has two updates, the
	// resolved one has one and each window is one entry.
	require.Equal(t, []string{
		"urn:nebulactrl:k-inc-1:1",
		"urn:nebulactrl:k-inc-1:0",
		"urn:nebulactrl:k-inc-2:0",
		"urn:nebulactrl:k-mw-2:0",
		"urn:nebulactrl:k-mw-1:0",
	}, feedIDs(f))

	by := map[string]int{}
	for i, e := range f.Entries {
		by[e.ID] = i
	}
	newest := f.Entries[by["urn:nebulactrl:k-inc-1:1"]]
	require.Equal(t, "Dashboard unavailable: Identified", newest.Title)
	require.Equal(t, "2026-10-29T11:00:00Z", newest.Updated)
	require.Equal(t, "https://"+testHost+"/#inc-k-inc-1", newest.Link.Href)
	require.Contains(t, newest.Content, "The cause is a failed migration.\nRolling back.")
	require.Contains(t, newest.Content, "Critical impact. Affects: Dashboard")
	oldest := f.Entries[by["urn:nebulactrl:k-inc-1:0"]]
	require.Equal(t, "Dashboard unavailable: Investigating", oldest.Title)

	mw := f.Entries[by["urn:nebulactrl:k-mw-1:0"]]
	require.Equal(t, "Maintenance: Docs reindex (In progress)", mw.Title)
	require.Equal(t, "https://"+testHost+"/#mw-k-mw-1", mw.Link.Href)
	require.Contains(t, mw.Content, "Search is read only.")
	require.Contains(t, mw.Content, "From 29 Oct 2026, 11:00 UTC to 29 Oct 2026, 13:00 UTC.")
	require.Contains(t, mw.Content, "Affects: Docs")
	upcoming := f.Entries[by["urn:nebulactrl:k-mw-2:0"]]
	require.Equal(t, "Maintenance: Database upgrade (Scheduled)", upcoming.Title)
	// A window for next week is posted today: the entry carries the posting
	// time, not the start, and the start is in the body.
	require.Equal(t, "2026-10-29T09:00:00Z", upcoming.Updated)
	require.Equal(t, "2026-10-29T09:00:00Z", upcoming.Published)
	require.Contains(t, upcoming.Content, "From 1 Nov 2026, 11:00 UTC to 1 Nov 2026, 13:00 UTC.")
	for _, e := range f.Entries {
		require.LessOrEqual(t, e.Updated, f.Updated, "no entry is dated after the feed itself")
	}
}

func TestFeedEntryIDsSurviveNewUpdates(t *testing.T) {
	p := fixturePage("auto")
	before, err := renderFeed(p, testHost)
	require.NoError(t, err)
	old := feedIDs(parseFeed(t, before))

	// A newer update goes in front of the timeline (newest first).
	i := &p.ActiveIncidents[0]
	i.Updates = append([]statuspages.PublicIncidentUpdate{{Status: "resolved", Body: "Done.", At: i.Updates[0].At.Add(time.Hour)}}, i.Updates...)
	after, err := renderFeed(p, testHost)
	require.NoError(t, err)
	now := feedIDs(parseFeed(t, after))

	require.Len(t, now, len(old)+1)
	for _, id := range old {
		require.Contains(t, now, id, "an entry id never changes")
	}
	require.Contains(t, now, "urn:nebulactrl:k-inc-1:2")
	require.Equal(t, "urn:nebulactrl:k-inc-1:2", now[0], "the new update is the newest entry")
}

func TestFeedEscapesAndToleratesOddInput(t *testing.T) {
	p := fixturePage("auto")
	p.Title = `Acme <&> "Status"`
	p.ActiveIncidents[0].Updates[0].Body = "<script>alert(1)</script> & \x00 bell\x07"
	p.ActiveIncidents[0].Key = ""                           // no key: a hash of title and start
	p.PastIncidents[0].Key = "a b/c"                        // unsafe characters
	p.PastIncidents[0].Updates = nil                        // no timeline: one entry
	p.Maintenance = append(p.Maintenance, p.Maintenance[0]) // the same window twice does not break the XML
	b, err := renderFeed(p, testHost)
	require.NoError(t, err)
	f := parseFeed(t, b)
	require.Equal(t, `Acme <&> "Status"`, f.Title)
	require.NotContains(t, string(b), "<script>")
	require.Contains(t, string(b), "&lt;script&gt;")

	ids := feedIDs(f)
	require.Contains(t, ids, "urn:nebulactrl:a_b_c:0")
	for _, id := range ids {
		require.Regexp(t, `^urn:nebulactrl:[A-Za-z0-9_.-]+:\d+$`, id)
	}

	again, err := renderFeed(p, testHost)
	require.NoError(t, err)
	require.Equal(t, string(b), string(again), "ids without a key are stable across renders")
	for _, e := range f.Entries {
		if strings.HasPrefix(e.ID, "urn:nebulactrl:t") {
			require.Equal(t, "https://"+testHost+"/", e.Link.Href, "no key, no anchor")
		}
	}
}

func TestFeedOfAnEmptyPage(t *testing.T) {
	b, err := renderFeed(statuspages.PublicPage{Title: "T", GeneratedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, testHost)
	require.NoError(t, err)
	f := parseFeed(t, b)
	require.Empty(t, f.Entries)
	require.Equal(t, "2026-01-02T03:04:05Z", f.Updated)
}

func TestFeedIsServedLikeThePage(t *testing.T) {
	_, _, h := oneHost()
	w := get(h, testHost, "/feed.atom")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/atom+xml; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=30", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Header().Get("Content-Security-Policy"), "default-src 'none'")
	etag := w.Header().Get("ETag")
	require.NotEmpty(t, etag)
	f := parseFeed(t, w.Body.Bytes())
	require.Equal(t, "https://"+testHost+"/feed.atom", f.Links[0].Href, "the host the feed was requested on")
	require.Len(t, f.Entries, 5)

	require.Equal(t, http.StatusNotModified, get(h, testHost, "/feed.atom", "If-None-Match", etag).Code)
	require.Equal(t, http.StatusNotFound, get(h, "other.example", "/feed.atom").Code)
	require.Equal(t, http.StatusNotFound, get(h, testHost, "/feed.atom/").Code)
	require.Equal(t, http.StatusNotFound, get(h, testHost, "/feed.xml").Code)
	require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}
