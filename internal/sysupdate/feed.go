package sysupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/version"
)

// Limits on what a feed may hand the control plane: the document comes from a
// URL an operator may point at a mirror, and its notes are rendered in the UI.
const (
	maxFeedBytes = 1 << 20
	maxNotes     = 64 << 10
)

// Feed is the release feed: https://nebulactrl.com/releases/latest.json
// (publishing: .github/workflows/release.yml).
type Feed struct {
	// Version is the newest release, normalized without a leading "v".
	Version string
	// PublishedAt is when the release was published (nil when the feed omits it).
	PublishedAt *time.Time
	// NotesURL is the release page (empty unless an http(s) URL).
	NotesURL string
	// Notes is the release notes in Markdown, trimmed and capped.
	Notes string
	// MinAgent is the oldest agent version the release supports (empty when none is given).
	MinAgent string
}

type feedDoc struct {
	Version     string     `json:"version"`
	PublishedAt *time.Time `json:"publishedAt"`
	NotesURL    string     `json:"notesUrl"`
	Notes       string     `json:"notes"`
	MinAgent    string     `json:"minAgent"`
}

// ParseFeed reads and validates a feed document. A version that is not a plain
// release (a pre-release, "latest", a git describe) is an error: it would never
// be offered. Unknown fields are ignored, so the feed may grow.
func ParseFeed(r io.Reader) (Feed, error) {
	var d feedDoc
	dec := json.NewDecoder(io.LimitReader(r, maxFeedBytes+1))
	if err := dec.Decode(&d); err != nil {
		return Feed{}, fmt.Errorf("the feed is not valid JSON: %w", err)
	}
	v, ok := version.Parse(d.Version)
	if !ok {
		return Feed{}, fmt.Errorf("the feed's version %q is not a release version", d.Version)
	}
	if len(v.Pre) > 0 {
		return Feed{}, fmt.Errorf("the feed's version %q is a pre-release", d.Version)
	}
	f := Feed{Version: strings.TrimPrefix(strings.TrimSpace(d.Version), "v"), PublishedAt: d.PublishedAt}
	if f.PublishedAt != nil && f.PublishedAt.IsZero() {
		f.PublishedAt = nil
	}
	if u, err := url.Parse(strings.TrimSpace(d.NotesURL)); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		f.NotesURL = u.String()
	}
	f.Notes = trimNotes(d.Notes)
	if _, ok := version.Parse(d.MinAgent); ok {
		f.MinAgent = strings.TrimPrefix(strings.TrimSpace(d.MinAgent), "v")
	}
	return f, nil
}

// trimNotes trims the notes and cuts them at maxNotes bytes on a rune boundary.
func trimNotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxNotes {
		return s
	}
	s = s[:maxNotes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s)
}

// Available reports whether latest is a release strictly newer than current.
// A development build ("dev", a git describe) never has an update, and a
// pre-release is never offered.
func Available(current, latest string) bool {
	cur, ok := version.Parse(current)
	if !ok {
		return false
	}
	lat, ok := version.Parse(latest)
	if !ok || len(lat.Pre) > 0 {
		return false
	}
	return version.Compare(lat, cur) > 0
}

// Fetcher reads the feed with an anonymous GET: no cookies, no credentials, no
// identifiers. The only thing it sends about the caller is the User-Agent
// "nebulactrl/<version>".
type Fetcher struct {
	HTTP      *http.Client
	URL       string
	UserAgent string
}

// Fetch downloads and parses the feed.
func (f Fetcher) Fetch(ctx context.Context) (Feed, error) {
	if u, perr := url.Parse(f.URL); perr != nil || u.Scheme != "https" || u.Host == "" {
		return Feed{}, errors.New("the release feed URL must be https:// (a plain http feed can be changed on the way, and the update it announces runs as root)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return Feed{}, errors.New("the release feed URL is not valid")
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "application/json")
	hc := f.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// A *url.Error repeats the feed URL, which may carry credentials.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Feed{}, fmt.Errorf("could not reach the release feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Feed{}, fmt.Errorf("the release feed answered %s", resp.Status)
	}
	return ParseFeed(resp.Body)
}
