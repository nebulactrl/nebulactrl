package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

func TestParseImage(t *testing.T) {
	cases := []struct{ in, host, repo, tag string }{
		{"redis:7", "docker.io", "library/redis", "7"},
		{"redis", "docker.io", "library/redis", ""},
		{"n8nio/n8n:1.2.3", "docker.io", "n8nio/n8n", "1.2.3"},
		{"ghcr.io/umami-software/umami:3.4.1", "ghcr.io", "umami-software/umami", "3.4.1"},
		{"localhost:5000/app:1", "localhost:5000", "app", "1"},
		{"quay.io/org/app", "quay.io", "org/app", ""},
	}
	for _, c := range cases {
		r := parseImage(c.in)
		if r.Host != c.host || r.Repo != c.repo || r.Tag != c.tag {
			t.Errorf("parseImage(%q) = %+v", c.in, r)
		}
	}
	if got := parseImage("ghcr.io/a/b:1").String(); got != "ghcr.io/a/b:1" {
		t.Errorf("String = %q", got)
	}
	if got := parseImage("redis:7").String(); got != "redis:7" {
		t.Errorf("String = %q", got)
	}
}

func TestPickTags(t *testing.T) {
	tags := []string{"latest", "1", "1.9", "1.10.2", "1.10.1", "v1.10.0", "1.11.0-rc1", "1.11.0-beta", "2.0.0-alpine", "1.10.2-alpine", "20240101", "nightly", "1.9.9"}
	got := pickTags(tags, "", 3)
	if !slices.Equal(got, []string{"1.10.2", "1.10.1", "v1.10.0"}) {
		t.Errorf("plain: %v", got)
	}
	got = pickTags(tags, "-alpine", 5)
	if !slices.Equal(got, []string{"2.0.0-alpine", "1.10.2-alpine"}) {
		t.Errorf("alpine: %v", got)
	}
	// Only moving majors: used when nothing better exists.
	if got := pickTags([]string{"latest", "3", "2"}, "", 5); !slices.Equal(got, []string{"3", "2"}) {
		t.Errorf("majors: %v", got)
	}
	// Date stamps and calver.
	if got := pickTags([]string{"2026.10.1", "2026.9.30", "20260101"}, "", 5); !slices.Equal(got, []string{"2026.10.1", "2026.9.30"}) {
		t.Errorf("calver: %v", got)
	}
	// A shorter spelling wins a tie.
	if got := pickTags([]string{"v1.2.3", "1.2.3"}, "", 2); got[0] != "1.2.3" {
		t.Errorf("tie: %v", got)
	}
}

func TestWantedSuffix(t *testing.T) {
	for in, want := range map[string]string{"latest": "", "": "", "1.2.3-alpine": "-alpine", "7": "", "v2.1": "", "stable": ""} {
		if got := wantedSuffix(in); got != want {
			t.Errorf("wantedSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLicenseClassOf(t *testing.T) {
	cases := map[string]string{
		"MIT": "permissive", "Apache-2.0": "permissive", "BSD-3-Clause": "permissive", "ISC": "permissive", "Unlicense": "permissive",
		"AGPL-3.0": "copyleft", "AGPL-3.0-only": "copyleft", "GPL-3.0": "copyleft", "LGPL-2.1": "copyleft", "MPL-2.0": "copyleft",
		"BUSL-1.1": "source-available", "SSPL-1.0": "source-available", "Elastic-2.0": "source-available", "FSL-1.1-MIT": "source-available",
		"BSL-1.0": "permissive",
	}
	for spdx, want := range cases {
		got, ok := licenseClassOf(spdx)
		if !ok || got != want {
			t.Errorf("licenseClassOf(%q) = %q, %v; want %q", spdx, got, ok, want)
		}
	}
	for _, spdx := range []string{"", "NOASSERTION", "Other", "Weird-1.0"} {
		if _, ok := licenseClassOf(spdx); ok {
			t.Errorf("licenseClassOf(%q) must need a human", spdx)
		}
	}
}

func TestOwnerHint(t *testing.T) {
	if h := ownerHint(parseImage("redis:7"), "redis", "redis", "redis"); h != "" {
		t.Errorf("official image flagged: %s", h)
	}
	if h := ownerHint(parseImage("ghcr.io/umami-software/umami:3"), "umami-software", "umami", "umami"); h != "" {
		t.Errorf("own namespace flagged: %s", h)
	}
	if h := ownerHint(parseImage("randomguy/umami-fork:3"), "umami-software", "umami", "umami"); h != "" {
		// name contains "umami": accepted by the heuristic, a reviewer still checks it.
		t.Logf("flagged: %s", h)
	}
	if h := ownerHint(parseImage("someoneelse/thing:3"), "umami-software", "umami", "umami"); h == "" {
		t.Error("unrelated namespace must be flagged")
	}
}

// fakeRegistry serves HEAD manifests and tag lists for one repository.
func fakeRegistry(t *testing.T, repo string, digests map[string]string) *registry {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer t" {
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, srv.URL, repo))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			var tags []string
			for k := range digests {
				tags = append(tags, k)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
		case strings.Contains(r.URL.Path, "/manifests/"):
			tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			d, ok := digests[tag]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", d)
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]any{"manifests": []map[string]any{
					{"platform": map[string]string{"os": "linux", "architecture": "amd64"}},
					{"platform": map[string]string{"os": "linux", "architecture": "arm64"}},
					{"platform": map[string]string{"os": "unknown", "architecture": "unknown"}},
				}})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	g := newRegistry()
	g.hc = srv.Client()
	g.scheme = "https"
	// The fake host replaces the registry host in imageRef.
	g.hc.Transport = rewriteHost(srv)
	return g
}

type rewriteTransport struct {
	base http.RoundTripper
	host string
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "registry.test" {
		req = req.Clone(req.Context())
		req.URL.Host = r.host
	}
	return r.base.RoundTrip(req)
}

func rewriteHost(srv *httptest.Server) http.RoundTripper {
	return rewriteTransport{base: srv.Client().Transport, host: strings.TrimPrefix(srv.URL, "https://")}
}

func dg(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func TestRegistryDigestTagsAndPlatforms(t *testing.T) {
	g := fakeRegistry(t, "acme/app", map[string]string{"latest": dg("a"), "1.2.3": dg("a"), "1.3.0": dg("b"), "1.1.0": dg("c")})
	ctx := context.Background()
	ref := imageRef{Host: "registry.test", Repo: "acme/app", Tag: "1.2.3"}
	d, err := g.Digest(ctx, ref)
	if err != nil || d != dg("a") {
		t.Fatalf("Digest = %q, %v", d, err)
	}
	if _, err := g.Digest(ctx, imageRef{Host: "registry.test", Repo: "acme/app", Tag: "9.9.9"}); err == nil {
		t.Error("a missing tag must fail")
	}
	tags, err := g.Tags(ctx, ref)
	if err != nil || len(tags) != 4 {
		t.Fatalf("Tags = %v, %v", tags, err)
	}
	pl, err := g.Platforms(ctx, ref)
	if err != nil || !pl["linux/amd64"] || !pl["linux/arm64"] || len(pl) != 2 {
		t.Fatalf("Platforms = %v, %v", pl, err)
	}
	// chooseFloating prefers the tag that has the digest of "latest", not the newest.
	latest := ref
	latest.Tag = "latest"
	tag, digest, err := chooseFloating(ctx, g, latest, "")
	if err != nil || tag != "1.2.3" || digest != dg("a") {
		t.Fatalf("chooseFloating = %q %q %v", tag, digest, err)
	}
	// Without a matching tag the newest wins.
	g2 := fakeRegistry(t, "acme/app", map[string]string{"latest": dg("z"), "1.2.3": dg("a"), "1.3.0": dg("b")})
	tag, _, err = chooseFloating(ctx, g2, latest, "")
	if err != nil || tag != "1.3.0" {
		t.Fatalf("chooseFloating fallback = %q %v", tag, err)
	}
	// A repo with only floating tags cannot be pinned.
	g3 := fakeRegistry(t, "acme/app", map[string]string{"latest": dg("z"), "nightly": dg("y")})
	if _, _, err := chooseFloating(ctx, g3, latest, ""); err == nil {
		t.Error("only floating tags must fail")
	}
}

type fakeGH map[string]*ghInfo

func (f fakeGH) Repo(_ context.Context, owner, name string) (*ghInfo, error) {
	if i, ok := f[owner+"/"+name]; ok {
		return i, nil
	}
	return nil, errNotFound
}

const enrichDraftYAML = `schema: 1
id: demo
revision: 1
name: Demo
summary: A demo.
category: devtools
links:
  website: https://example.org
  source: https://github.com/acme/demo
upstream:
  license: ""
  class: permissive
origin:
  type: coolify
  path: templates/compose/demo.yaml
  commit: 1d28642
  license: Apache-2.0
requires: {}
services:
  - key: app
    name: Demo
    kind: app
    image: registry.test/acme/app:latest # REVIEW: floating tag, pick a stable version
    digest: sha256:0000000000000000000000000000000000000000000000000000000000000000 # REVIEW: placeholder, run tools/template-pin
    port: 8080
    healthcheck: # REVIEW: no usable compose healthcheck
      type: tcp
`

func TestEnrichDraft(t *testing.T) {
	g := fakeRegistry(t, "acme/app", map[string]string{"latest": dg("a"), "1.2.3": dg("a"), "1.3.0-rc1": dg("b")})
	out := t.TempDir()
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	o := enrichOptions{Out: out, Now: now, Reg: g, GH: fakeGH{
		"acme/demo": {FullName: "acme/demo", SPDX: "AGPL-3.0", LicenseURL: "https://github.com/acme/demo/blob/main/LICENSE", PushedAt: now.AddDate(0, -2, 0)},
	}}
	res := enrichDraft(context.Background(), o, "demo", []byte(enrichDraftYAML))
	if res.Err != nil || res.Reject != "" {
		t.Fatalf("enrich = %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(out, "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := tpl.Services[0]
	if s.Image != "registry.test/acme/app:1.2.3" || s.Digest != dg("a") {
		t.Errorf("service = %s %s", s.Image, s.Digest)
	}
	if tpl.Upstream.License != "AGPL-3.0" || tpl.Upstream.Class != templates.LicenseCopyleft || tpl.Upstream.LicenseURL == "" {
		t.Errorf("upstream = %+v", tpl.Upstream)
	}
	// Only the not auto-resolved review remains.
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "no usable compose healthcheck") || strings.Contains(joined, "placeholder, run tools/template-pin") {
		t.Errorf("notes = %q", joined)
	}
	if len(tpl.Requires.Arch) != 0 {
		t.Errorf("both architectures are published, arch must stay empty: %v", tpl.Requires.Arch)
	}
}

func TestEnrichRejects(t *testing.T) {
	g := fakeRegistry(t, "acme/app", map[string]string{"latest": dg("a"), "1.2.3": dg("a")})
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for name, info := range map[string]*ghInfo{
		"archived": {FullName: "acme/demo", Archived: true, SPDX: "MIT", PushedAt: now},
		"idle":     {FullName: "acme/demo", SPDX: "MIT", PushedAt: now.AddDate(-2, 0, 0)},
	} {
		o := enrichOptions{Out: t.TempDir(), Now: now, Reg: g, GH: fakeGH{"acme/demo": info}}
		res := enrichDraft(context.Background(), o, "demo", []byte(enrichDraftYAML))
		if res.Reject == "" {
			t.Errorf("%s: want a rejection, got %+v", name, res)
		}
		if _, err := os.Stat(filepath.Join(o.Out, "demo.yaml")); err == nil {
			t.Errorf("%s: a rejected draft must not be staged", name)
		}
	}
	// No versioned tag at all.
	g2 := fakeRegistry(t, "acme/app", map[string]string{"latest": dg("a")})
	o := enrichOptions{Out: t.TempDir(), Now: now, Reg: g2, GH: fakeGH{"acme/demo": {FullName: "acme/demo", SPDX: "MIT", PushedAt: now}}}
	if res := enrichDraft(context.Background(), o, "demo", []byte(enrichDraftYAML)); res.Reject == "" {
		t.Errorf("no versioned tag must reject: %+v", res)
	}
}

func TestReviewLinesDropsResolved(t *testing.T) {
	got := reviewLines([]byte(enrichDraftYAML))
	if len(got) != 1 || !strings.Contains(got[0], "healthcheck") {
		t.Errorf("reviewLines = %v", got)
	}
}

func TestCanonicalImage(t *testing.T) {
	got, ok := canonicalImage("docker.flipt.io/flipt/flipt:latest")
	if !ok || got != "flipt/flipt:latest" {
		t.Errorf("canonicalImage = %q, %v", got, ok)
	}
	got, ok = canonicalImage("docker.dragonflydb.io/dragonflydb/dragonfly:latest")
	if !ok || got != "ghcr.io/dragonflydb/dragonfly:latest" {
		t.Errorf("canonicalImage = %q, %v", got, ok)
	}
	if got, ok := canonicalImage("ghcr.io/acme/app:1"); ok || got != "ghcr.io/acme/app:1" {
		t.Errorf("unrelated image rewritten: %q", got)
	}
}
