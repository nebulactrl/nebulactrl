package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

// The enrich step turns drafts into staged files that only need a human for
// the judgement calls. It fills what a registry or the GitHub API can answer
// without pulling anything:
//
//   - a stable tag for floating or missing tags, and the multi-arch index
//     digest of every image (HEAD requests only);
//   - requires.arch from the platforms the images publish;
//   - the upstream licence, its class and URL;
//   - automatic rejections: archived or long-idle upstreams, no amd64 image,
//     no versioned tag.
//
// It never writes to templates/catalog. Staged files lose the draft's inline
// REVIEW comments; the remaining ones are copied to <id>.notes.txt.

// ghInfo is what the enrich step reads about an upstream repository.
type ghInfo struct {
	FullName      string
	Archived      bool
	Fork          bool
	PushedAt      time.Time
	SPDX          string
	LicenseURL    string
	DefaultBranch string
}

// github reads repository facts (the gh CLI in production, a fake in tests).
type github interface {
	Repo(ctx context.Context, owner, name string) (*ghInfo, error)
}

type ghCLI struct{}

func (ghCLI) Repo(ctx context.Context, owner, name string) (*ghInfo, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", "api", "repos/"+owner+"/"+name)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if strings.Contains(errb.String(), "404") {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("gh api repos/%s/%s: %s", owner, name, strings.TrimSpace(errb.String()))
	}
	var r struct {
		FullName      string `json:"full_name"`
		Archived      bool   `json:"archived"`
		Fork          bool   `json:"fork"`
		PushedAt      string `json:"pushed_at"`
		DefaultBranch string `json:"default_branch"`
		License       *struct {
			SPDX string `json:"spdx_id"`
		} `json:"license"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return nil, err
	}
	info := &ghInfo{FullName: r.FullName, Archived: r.Archived, Fork: r.Fork, DefaultBranch: r.DefaultBranch}
	info.PushedAt, _ = time.Parse(time.RFC3339, r.PushedAt)
	if r.License != nil {
		info.SPDX = r.License.SPDX
	}
	// The licence file's own URL (path as GitHub detected it).
	var lout bytes.Buffer
	lc := exec.CommandContext(ctx, "gh", "api", "repos/"+owner+"/"+name+"/license", "--jq", ".html_url")
	lc.Stdout = &lout
	if lc.Run() == nil {
		info.LicenseURL = strings.TrimSpace(lout.String())
	}
	return info, nil
}

var errNotFound = errors.New("not found")

// licenseClassOf maps an SPDX id to the catalog's class. ok is false when a
// human must decide (unknown, NOASSERTION, "Other").
func licenseClassOf(spdx string) (class string, ok bool) {
	id := strings.TrimSpace(spdx)
	up := strings.ToUpper(id)
	switch {
	case id == "" || up == "NOASSERTION" || up == "OTHER":
		return "", false
	case strings.HasPrefix(up, "AGPL"), strings.HasPrefix(up, "GPL"), strings.HasPrefix(up, "LGPL"), strings.HasPrefix(up, "MPL"),
		strings.HasPrefix(up, "EPL"), strings.HasPrefix(up, "EUPL"), strings.HasPrefix(up, "CDDL"), strings.HasPrefix(up, "OSL"),
		strings.HasPrefix(up, "CC-BY-SA"), strings.HasPrefix(up, "CECILL"), strings.HasPrefix(up, "OPL"):
		return templates.LicenseCopyleft, true
	case strings.HasPrefix(up, "BUSL"), strings.HasPrefix(up, "SSPL"), strings.HasPrefix(up, "ELASTIC"), strings.HasPrefix(up, "FSL"),
		strings.HasPrefix(up, "COMMONS-CLAUSE"), strings.HasPrefix(up, "POLYFORM"), strings.HasPrefix(up, "BSL-"):
		if up == "BSL-1.0" {
			return templates.LicensePermissive, true
		}
		return templates.LicenseSourceAvailable, true
	}
	for _, p := range []string{"MIT", "APACHE", "BSD", "ISC", "0BSD", "UNLICENSE", "CC0", "ZLIB", "BLUEOAK", "POSTGRESQL", "WTFPL", "PYTHON", "BSL-1.0", "NCSA", "X11", "ARTISTIC", "AFL"} {
		if strings.HasPrefix(up, p) {
			return templates.LicensePermissive, true
		}
	}
	return "", false
}

// ---- tag selection ----

var versionTagRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})(-[A-Za-z0-9._-]+)?$`)

type versionTag struct {
	tag   string
	parts []int
}

func compareParts(a, b []int) int {
	for i := range max(len(a), len(b)) {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// wantedSuffix is the variant suffix (for example "-alpine") of a pinned
// compose tag, "" for floating or missing tags.
func wantedSuffix(current string) string {
	if floatingTags[strings.ToLower(current)] || current == "" {
		return ""
	}
	m := versionTagRe.FindStringSubmatch(current)
	if m == nil {
		return ""
	}
	return m[3]
}

// pickTags returns the stable, versioned tags of the wanted variant, newest
// first (at most limit). Prereleases, single-number tags (moving majors) and
// date stamps are left out; single-number tags are only used when nothing else
// exists.
func pickTags(tags []string, suffix string, limit int) []string {
	var full, majors []versionTag
	for _, t := range tags {
		m := versionTagRe.FindStringSubmatch(t)
		if m == nil || m[3] != suffix {
			continue
		}
		var parts []int
		okNum := true
		for p := range strings.SplitSeq(m[2], ".") {
			n, err := strconv.Atoi(p)
			if err != nil || n > 100000 {
				okNum = false
				break
			}
			parts = append(parts, n)
		}
		if !okNum {
			continue
		}
		vt := versionTag{tag: t, parts: parts}
		if len(parts) == 1 {
			majors = append(majors, vt)
		} else {
			full = append(full, vt)
		}
	}
	pool := full
	if len(pool) == 0 {
		pool = majors
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if c := compareParts(pool[i].parts, pool[j].parts); c != 0 {
			return c > 0
		}
		// Same version: prefer the shorter spelling (no "v" prefix noise).
		return len(pool[i].tag) < len(pool[j].tag)
	})
	var out []string
	for _, v := range pool {
		out = append(out, v.tag)
		if len(out) == limit {
			break
		}
	}
	return out
}

// ---- the per-draft work ----

type enrichOptions struct {
	In, Out string
	Only    []string
	Skip    map[string]bool // ids that were rejected earlier
	Now     time.Time
	Jobs    int
	Reg     *registry
	GH      github
}

type enriched struct {
	ID     string
	Notes  []string
	Reject string
	Err    error
}

var autoResolved = []string{
	"placeholder, run tools/template-pin", "unknown, look it up in the upstream repository", "placeholder: permissive",
	"floating tag, pick a stable version", "source image had no tag", "slug guessed from the upstream name",
}

// reviewLines copies the draft's remaining REVIEW comments (with the line they sit on).
func reviewLines(raw []byte) []string {
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.Contains(line, "# REVIEW") {
			continue
		}
		skip := false
		for _, a := range autoResolved {
			if strings.Contains(line, a) {
				skip = true
			}
		}
		if !skip {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// vanityRegistries maps project-run registry hostnames, which are redirects
// to a public registry and often reject anonymous tag listings, to the public
// repository that serves the same image.
var vanityRegistries = map[string]string{
	"docker.flagsmith.com/flagsmith/":      "flagsmith/",
	"docker.getoutline.com/outlinewiki/":   "outlinewiki/",
	"registry.rocket.chat/rocketchat/":     "rocketchat/",
	"cr.weaviate.io/semitechnologies/":     "semitechnologies/",
	"docker.flipt.io/flipt/":               "flipt/",
	"docker.dragonflydb.io/dragonflydb/":   "ghcr.io/dragonflydb/",
	"registry.supertokens.io/supertokens/": "supertokens/",
	"nabo.codimd.dev/hackmdio/":            "hackmdio/",
}

// canonicalImage rewrites a vanity registry reference to its public home.
func canonicalImage(img string) (string, bool) {
	for from, to := range vanityRegistries {
		if rest, ok := strings.CutPrefix(img, from); ok {
			return to + rest, true
		}
	}
	return img, false
}

var normRe = regexp.MustCompile(`[^a-z0-9]+`)

func norm(s string) string { return normRe.ReplaceAllString(strings.ToLower(s), "") }

// ownerHint says whether the image namespace plausibly belongs to the project.
func ownerHint(r imageRef, ghOwner, ghRepo, id string) string {
	ns, _, _ := strings.Cut(r.Repo, "/")
	if r.Host == "docker.io" && ns == "library" {
		return ""
	}
	n := norm(ns)
	for _, c := range []string{ghOwner, ghRepo, id} {
		c = norm(c)
		if c != "" && n != "" && (strings.Contains(n, c) || strings.Contains(c, n)) {
			return ""
		}
	}
	return fmt.Sprintf("image %s: namespace %q does not match upstream %s/%s: confirm the project itself publishes it", r, ns, ghOwner, ghRepo)
}

var thirdPartyNS = map[string]bool{"linuxserver": true, "hotio": true, "nfrastack": true, "tiredofit": true, "coollabsio": true, "pgsty": true}

func enrichDraft(ctx context.Context, o enrichOptions, id string, raw []byte) enriched {
	res := enriched{ID: id}
	t, err := templates.Parse(raw)
	if err != nil {
		res.Err = err
		return res
	}
	note := func(f string, a ...any) { res.Notes = append(res.Notes, fmt.Sprintf(f, a...)) }

	// Upstream repository, licence and liveness.
	owner, name := upstreamRepo(t)
	var gh *ghInfo
	if owner != "" {
		gh, err = o.GH.Repo(ctx, owner, name)
		switch {
		case errors.Is(err, errNotFound):
			note("AUTO: GitHub repository %s/%s not found; links.source is wrong or the project moved", owner, name)
		case err != nil:
			res.Err = err
			return res
		}
	} else {
		note("AUTO: no GitHub repository found in the links; find the upstream source and licence by hand")
	}
	if gh != nil {
		if gh.Archived {
			res.Reject = fmt.Sprintf("upstream %s is archived on GitHub (no further releases or security fixes)", gh.FullName)
			return res
		}
		if !gh.PushedAt.IsZero() && gh.PushedAt.Before(o.Now.AddDate(-1, 0, 0)) {
			res.Reject = fmt.Sprintf("upstream %s has had no push since %s (more than 12 months)", gh.FullName, gh.PushedAt.Format("2006-01-02"))
			return res
		}
		t.Links.Source = "https://github.com/" + gh.FullName
		if class, ok := licenseClassOf(gh.SPDX); ok {
			t.Upstream.License, t.Upstream.Class = gh.SPDX, class
			if class == templates.LicenseSourceAvailable || class == templates.LicenseProprietary || gh.LicenseURL != "" {
				t.Upstream.LicenseURL = gh.LicenseURL
			}
			if class == templates.LicenseSourceAvailable && t.Upstream.LicenseURL == "" {
				t.Upstream.LicenseURL = t.Links.Source
			}
			note("AUTO: licence %s (%s) from GitHub; confirm it is the licence of the app the image runs (open core and dual licences)", gh.SPDX, class)
		} else {
			t.Upstream.License, t.Upstream.Class = "", templates.LicensePermissive
			note("AUTO: GitHub reports licence %q: read the upstream licence and set upstream.license, class and licenseUrl", gh.SPDX)
		}
		if gh.Fork {
			note("AUTO: %s is a fork on GitHub; check it is the project's canonical repository", gh.FullName)
		}
	}

	// Images.
	haveAMD, haveARM, unknownPlat := true, true, false
	for i := range t.Services {
		s := &t.Services[i]
		if s.Image == "" {
			continue
		}
		if img, ok := canonicalImage(s.Image); ok {
			note("AUTO: image %s rewritten to its public repository %s (the project's own registry host is a redirect)", s.Image, img)
			s.Image = img
		}
		ref := parseImage(s.Image)
		if ns, _, _ := strings.Cut(ref.Repo, "/"); thirdPartyNS[ns] {
			note("AUTO: image %s is packaged by %q, not the project: accept it only if the project's own docs name it as the official image", s.Image, ns)
		}
		if owner != "" {
			if h := ownerHint(ref, owner, name, id); h != "" {
				note("AUTO: %s", h)
			}
		}
		suffix := wantedSuffix(ref.Tag)
		floating := ref.Tag == "" || floatingTags[strings.ToLower(ref.Tag)]
		if !floating {
			d, err := o.Reg.Digest(ctx, ref)
			if err != nil {
				note("AUTO: image %s: tag does not resolve (%v); picking a tag", s.Image, err)
				floating = true
			} else {
				s.Digest = d
			}
		}
		if floating {
			tag, digest, perr := chooseFloating(ctx, o.Reg, ref, suffix)
			if perr != nil {
				res.Reject = fmt.Sprintf("image %s has no usable versioned tag (%v)", s.Image, perr)
				return res
			}
			ref.Tag = tag
			s.Image, s.Digest = ref.String(), digest
			note("AUTO: service %s pinned to %s (newest stable tag; check it is the version you want)", s.Key, s.Image)
		}
		pl, err := o.Reg.Platforms(ctx, ref)
		switch {
		case err != nil:
			note("AUTO: platforms of %s unknown (%v); template-pin will check", s.Image, err)
			unknownPlat = true
		case pl == nil:
			unknownPlat = true
		default:
			haveAMD = haveAMD && pl["linux/amd64"]
			haveARM = haveARM && pl["linux/arm64"]
		}
	}
	if !haveAMD {
		res.Reject = "an image has no linux/amd64 manifest"
		return res
	}
	if !haveARM && !unknownPlat {
		t.Requires.Arch = []string{"amd64"}
		note("AUTO: some image has no linux/arm64 manifest: requires.arch is [amd64]")
	}

	t.Revision = 1
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(t); err != nil {
		res.Err = err
		return res
	}
	_ = enc.Close()
	if err := os.WriteFile(filepath.Join(o.Out, id+".yaml"), buf.Bytes(), 0o644); err != nil {
		res.Err = err
		return res
	}
	res.Notes = append(res.Notes, reviewLines(raw)...)
	return res
}

// chooseFloating picks a versioned tag for a floating one: the tag that points
// at the same index as the floating tag when there is one, else the newest.
func chooseFloating(ctx context.Context, reg *registry, ref imageRef, suffix string) (tag, digest string, err error) {
	tags, err := reg.Tags(ctx, ref)
	if err != nil {
		return "", "", err
	}
	cands := pickTags(tags, suffix, 25)
	if len(cands) == 0 {
		return "", "", errors.New("no versioned tags")
	}
	floatingDigest := ""
	if ref.Tag != "" {
		floatingDigest, _ = reg.Digest(ctx, ref)
	}
	var firstTag, firstDigest string
	for _, c := range cands {
		r := ref
		r.Tag = c
		d, derr := reg.Digest(ctx, r)
		if derr != nil {
			continue
		}
		if floatingDigest != "" && d == floatingDigest {
			return c, d, nil
		}
		if firstTag == "" {
			firstTag, firstDigest = c, d
		}
	}
	if firstTag == "" {
		return "", "", errors.New("no candidate tag has a manifest")
	}
	return firstTag, firstDigest, nil
}

var ghURLRe = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/#?]+)`)

// upstreamRepo finds the GitHub repository of a draft from its links, then
// from a ghcr.io image.
func upstreamRepo(t *templates.CatalogTemplate) (owner, name string) {
	for _, u := range []string{t.Links.Source, t.Links.Docs, t.Links.Website} {
		if m := ghURLRe.FindStringSubmatch(u); m != nil && m[1] != "orgs" {
			return m[1], strings.TrimSuffix(m[2], ".git")
		}
	}
	for _, s := range t.Services {
		if r := parseImage(s.Image); r.Host == "ghcr.io" {
			if o, n, ok := strings.Cut(r.Repo, "/"); ok && s.Image != "" {
				return o, n
			}
		}
	}
	return "", ""
}

// enrichAll runs the step over every draft in o.In.
func enrichAll(ctx context.Context, o enrichOptions) ([]enriched, error) {
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(o.In, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var ids []string
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if o.Skip[id] {
			continue
		}
		if len(o.Only) > 0 && !slices.Contains(o.Only, id) {
			continue
		}
		ids = append(ids, id)
	}
	results := make([]enriched, len(ids))
	sem := make(chan struct{}, max(o.Jobs, 1))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			raw, err := os.ReadFile(filepath.Join(o.In, id+".yaml"))
			if err != nil {
				results[i] = enriched{ID: id, Err: err}
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			results[i] = enrichDraft(cctx, o, id, raw)
		}()
	}
	wg.Wait()

	var rejected, failed []string
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed = append(failed, fmt.Sprintf("%s: ERROR %v", r.ID, r.Err))
		case r.Reject != "":
			rejected = append(rejected, fmt.Sprintf("%s: %s", r.ID, r.Reject))
		default:
			if err := os.WriteFile(filepath.Join(o.Out, r.ID+".notes.txt"), []byte(strings.Join(r.Notes, "\n")+"\n"), 0o644); err != nil {
				return results, err
			}
		}
	}
	_ = os.WriteFile(filepath.Join(o.Out, "REJECTED.auto.txt"), []byte(strings.Join(rejected, "\n")+"\n"), 0o644)
	if len(failed) > 0 {
		_ = os.WriteFile(filepath.Join(o.Out, "ERRORS.txt"), []byte(strings.Join(failed, "\n")+"\n"), 0o644)
	}
	return results, nil
}
