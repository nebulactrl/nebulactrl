// Command template-translate is a ONE-TIME OFFLINE helper that drafts
// NebulaCtrl templates from the Coolify (Apache-2.0) and Dokploy (MIT)
// catalogs. It never runs at runtime and never writes to templates/catalog:
// drafts land in templates/drafts, visibly unfinished (REVIEW comments, a
// placeholder digest, no upstream licence) and are finished by hand. See
// README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

type options struct {
	Coolify string
	Dokploy string
	Out     string
	Catalog string
	Only    []string
	Limit   int
	// MaxServices is the most compose services a default-selected app may have.
	MaxServices int
	// Enrich switches to the enrich step: pin tags and digests, fill licences
	// and reject dead upstreams for the drafts in this directory.
	Enrich    string
	EnrichOut string
	Rejected  string
	// Check validates the files given as arguments (the static tier-1 rules of
	// the catalog, plus no leftover draft markers); Resolve and Tags look an
	// image up in its registry without pulling it.
	Check bool
	// Verify compares the pinned digests of the template files given as
	// arguments with their registries (HEAD requests, never a pull).
	Verify  bool
	Resolve string
	Tags    string
}

type skipped struct {
	ID     string
	Source string
	Reason string
}

type result struct {
	Written []string
	Skipped []skipped
}

func main() {
	var o options
	var only string
	flag.StringVar(&o.Coolify, "coolify", "", "directory of a Coolify checkout (contains templates/compose)")
	flag.StringVar(&o.Dokploy, "dokploy", "", "directory of a Dokploy templates checkout (contains blueprints)")
	flag.StringVar(&o.Out, "out", "templates/drafts", "output directory for drafts")
	flag.StringVar(&o.Catalog, "catalog", "templates/catalog", "existing catalog; its ids are skipped by the default selection")
	flag.StringVar(&only, "only", "", "comma separated ids to translate (skips the selection heuristics)")
	flag.IntVar(&o.Limit, "limit", 0, "write at most N drafts (0 = no limit)")
	flag.IntVar(&o.MaxServices, "max-services", 3, "most compose services of a default-selected app (the format allows 8)")
	flag.StringVar(&o.Enrich, "enrich", "", "enrich the drafts in this directory (registry HEAD + GitHub API; no pulls) instead of translating")
	flag.StringVar(&o.EnrichOut, "enrich-out", "", "output directory of -enrich (required with -enrich)")
	flag.StringVar(&o.Rejected, "rejected", "templates/drafts/REJECTED.txt", "list of ids rejected earlier, skipped by -enrich")
	flag.BoolVar(&o.Check, "check", false, "validate the template files given as arguments (schema, pinning, licence, no draft markers)")
	flag.BoolVar(&o.Verify, "verify", false, "check the pinned digests and architectures of the template files given as arguments against their registries (HEAD requests, no pulls)")
	flag.StringVar(&o.Resolve, "resolve", "", "print the index digest and platforms of repo:tag (HEAD request, no pull)")
	flag.StringVar(&o.Tags, "tags", "", "print the newest stable tags of repo (registry tag list, no pull)")
	flag.Parse()
	if o.Check {
		os.Exit(runCheck(flag.Args()))
	}
	if o.Verify {
		os.Exit(runVerify(flag.Args()))
	}
	if o.Resolve != "" || o.Tags != "" {
		os.Exit(runLookup(o))
	}
	for id := range strings.SplitSeq(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			o.Only = append(o.Only, id)
		}
	}
	if o.Enrich == "" && !o.Check && !o.Verify && o.Resolve == "" && o.Tags == "" && o.Coolify == "" && o.Dokploy == "" {
		fmt.Fprintln(os.Stderr, "template-translate: give -coolify and/or -dokploy")
		os.Exit(2)
	}
	if o.Enrich != "" {
		os.Exit(runEnrich(o))
	}
	res, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "template-translate:", err)
		os.Exit(1)
	}
	fmt.Printf("drafts written: %d\nskipped: %d\n", len(res.Written), len(res.Skipped))
	counts := map[string]int{}
	for _, s := range res.Skipped {
		seen := map[string]bool{}
		for _, part := range strings.Split(s.Reason, "; ") {
			_, r, _ := strings.Cut(part, ": ")
			c := reasonClass(r)
			if !seen[c] {
				seen[c] = true
				counts[c]++
			}
		}
	}
	var classes []string
	for c := range counts {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for _, c := range classes {
		fmt.Printf("  %4d  %s\n", counts[c], c)
	}
	fmt.Println("ids:", strings.Join(res.Written, " "))
}

// reasonClass strips the per-service detail from a skip reason.
func reasonClass(r string) string {
	if i := strings.Index(r, " (service"); i >= 0 {
		r = r[:i]
	}
	if i := strings.Index(r, ":"); i >= 0 {
		r = r[:i]
	}
	return strings.TrimSpace(r)
}

// candidate is one app, possibly present in both catalogs.
type candidate struct {
	norm string
	cool *source
	dok  *source
}

func (c *candidate) both() bool { return c.cool != nil && c.dok != nil }

func (c *candidate) id() string {
	if c.cool != nil {
		return c.cool.ID
	}
	return c.dok.ID
}

func gitCommit(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func run(o options) (*result, error) {
	var cool, dok []*source
	var err error
	if o.Coolify != "" {
		if cool, err = loadCoolify(o.Coolify, gitCommit(o.Coolify)); err != nil {
			return nil, fmt.Errorf("coolify: %w", err)
		}
	}
	if o.Dokploy != "" {
		if dok, err = loadDokploy(o.Dokploy, gitCommit(o.Dokploy)); err != nil {
			return nil, fmt.Errorf("dokploy: %w", err)
		}
	}
	return translateAll(cool, dok, o)
}

// translateAll selects, translates and writes drafts.
func translateAll(cool, dok []*source, o options) (*result, error) {
	if o.MaxServices <= 0 {
		o.MaxServices = 3
	}
	cands := pairUp(cool, dok)
	catalog := catalogIDs(o.Catalog)
	var picked []*candidate
	if len(o.Only) > 0 {
		for _, id := range o.Only {
			found := false
			for _, c := range cands {
				if c.norm == normID(id) || c.id() == id {
					picked, found = append(picked, c), true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("-only: no template %q in the given catalogs", id)
			}
		}
	} else {
		for _, c := range cands {
			if c.eligible(catalog, o.MaxServices) {
				picked = append(picked, c)
			}
		}
		sort.SliceStable(picked, func(i, j int) bool { return picked[i].both() && !picked[j].both() })
	}

	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	res := &result{}
	for _, c := range picked {
		if o.Limit > 0 && len(res.Written) >= o.Limit {
			break
		}
		var order []*source
		for _, s := range []*source{c.cool, c.dok} {
			if s != nil && (len(o.Only) > 0 || eligibleSource(s, catalog, o.MaxServices)) {
				order = append(order, s)
			}
		}
		var reasons []string
		done := false
		for _, s := range order {
			if other := c.other(s); other != nil {
				enrich(s, other)
			}
			d, err := translate(s)
			if err != nil {
				var se *skipError
				if !errors.As(err, &se) {
					return nil, fmt.Errorf("%s %s: %w", s.Kind, s.ID, err)
				}
				reasons = append(reasons, s.Kind+": "+se.reason)
				continue
			}
			data, err := render(d, s)
			if err != nil {
				// One draft that cannot be written (for example a comment the YAML
				// emitter rejects) must not abort the whole run.
				reasons = append(reasons, s.Kind+": render error: "+err.Error())
				continue
			}
			if _, err := templates.Parse(data); err != nil {
				return nil, fmt.Errorf("%s %s: generated draft does not parse: %w", s.Kind, s.ID, err)
			}
			if err := os.WriteFile(filepath.Join(o.Out, d.T.ID+".yaml"), data, 0o644); err != nil {
				return nil, err
			}
			res.Written = append(res.Written, d.T.ID)
			done = true
			break
		}
		if !done {
			src := "coolify"
			if c.cool == nil {
				src = "dokploy"
			} else if c.dok != nil {
				src = "coolify+dokploy"
			}
			res.Skipped = append(res.Skipped, skipped{ID: c.id(), Source: src, Reason: strings.Join(reasons, "; ")})
		}
	}
	if err := writeSkipped(o.Out, res.Skipped); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *candidate) other(s *source) *source {
	if s == c.cool {
		return c.dok
	}
	return c.cool
}

func writeSkipped(dir string, sk []skipped) error {
	sort.Slice(sk, func(i, j int) bool { return sk[i].ID < sk[j].ID })
	var b strings.Builder
	for _, s := range sk {
		fmt.Fprintf(&b, "%s [%s]: %s\n", s.ID, s.Source, s.Reason)
	}
	return os.WriteFile(filepath.Join(dir, "SKIPPED.txt"), []byte(b.String()), 0o644)
}

// pairUp joins the catalogs by normalised id, keeping Coolify order first and
// Dokploy-only apps after it.
func pairUp(cool, dok []*source) []*candidate {
	byNorm := map[string]*candidate{}
	var out []*candidate
	for _, s := range cool {
		n := normID(s.ID)
		if byNorm[n] == nil {
			c := &candidate{norm: n, cool: s}
			byNorm[n] = c
			out = append(out, c)
		}
	}
	for _, s := range dok {
		n := normID(s.ID)
		if c := byNorm[n]; c != nil {
			if c.dok == nil {
				c.dok = s
			}
			continue
		}
		c := &candidate{norm: n, dok: s}
		byNorm[n] = c
		out = append(out, c)
	}
	return out
}

// enrich fills gaps of the primary source from its twin in the other catalog.
func enrich(p, o *source) {
	if p.Kind == "coolify" && o.Kind == "dokploy" {
		if o.Name != "" && o.Name != titleFromID(o.ID) {
			p.Name = o.Name
		}
	}
	if p.Website == "" {
		p.Website = o.Website
	}
	if p.Github == "" {
		p.Github = o.Github
	}
	if p.Docs == "" {
		p.Docs = o.Docs
	}
	if p.Summary == "" {
		p.Summary = o.Summary
	}
	for _, t := range o.Tags {
		dup := false
		for _, x := range p.Tags {
			if strings.EqualFold(x, t) {
				dup = true
			}
		}
		if !dup {
			p.Tags = append(p.Tags, t)
		}
	}
}

func catalogIDs(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".yaml"); ok {
			out[normID(n)] = true
		}
	}
	return out
}

var abandonedRe = regexp.MustCompile(`(?i)(bitnami|enterprise|-ee(:|$|/)|/[a-z-]*-ee(:|$)|mailhog|watchtower|dockerhub\.io|bitnamilegacy)`)

func (c *candidate) eligible(catalog map[string]bool, maxServices int) bool {
	if catalog[c.norm] {
		return false
	}
	return (c.cool != nil && eligibleSource(c.cool, catalog, maxServices)) || (c.dok != nil && eligibleSource(c.dok, catalog, maxServices))
}

// eligibleSource applies the default selection heuristics: at most
// maxServices services, each a plain image, nothing ignored, abandoned or enterprise.
func eligibleSource(s *source, catalog map[string]bool, maxServices int) bool {
	if s.Err != nil || s.Ignore || s.Compose == nil || catalog[normID(s.ID)] {
		return false
	}
	if n := len(s.Compose.Services); n == 0 || n > maxServices {
		return false
	}
	for _, c := range s.Compose.Services {
		if c.Image == "" || c.Raw["build"] != nil || abandonedRe.MatchString(c.Image) {
			return false
		}
	}
	return true
}

// runEnrich runs the enrich step and reports; it returns the exit code.
func runEnrich(o options) int {
	if o.EnrichOut == "" {
		fmt.Fprintln(os.Stderr, "template-translate: -enrich needs -enrich-out")
		return 2
	}
	skip := map[string]bool{}
	if data, err := os.ReadFile(o.Rejected); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if id, _, ok := strings.Cut(line, ":"); ok {
				skip[strings.TrimSpace(id)] = true
			}
		}
	}
	eo := enrichOptions{In: o.Enrich, Out: o.EnrichOut, Only: o.Only, Skip: skip, Now: time.Now(), Jobs: 6, Reg: newRegistry(), GH: ghCLI{}}
	results, err := enrichAll(context.Background(), eo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "template-translate:", err)
		return 1
	}
	ok, rej, bad := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Err != nil:
			bad++
		case r.Reject != "":
			rej++
		default:
			ok++
		}
	}
	fmt.Printf("staged: %d  rejected: %d  errors: %d (see %s)\n", ok, rej, bad, o.EnrichOut)
	return 0
}

// runCheck validates template files. It exits 1 when any file has a problem.
func runCheck(files []string) int {
	bad := 0
	for _, f := range files {
		if problems := checkFile(f); len(problems) > 0 {
			bad++
			fmt.Printf("FAIL %s\n", f)
			for _, p := range problems {
				fmt.Println("   -", p)
			}
		} else {
			fmt.Printf("ok   %s\n", f)
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

// checkFile returns the problems of one template file.
func checkFile(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	if strings.Contains(string(raw), "REVIEW") || strings.Contains(string(raw), "DRAFT generated") {
		out = append(out, "draft markers (REVIEW / DRAFT) are still in the file")
	}
	t, err := templates.Parse(raw)
	if err != nil {
		return append(out, err.Error())
	}
	if want := strings.TrimSuffix(filepath.Base(path), ".yaml"); t.ID != want {
		out = append(out, fmt.Sprintf("id %q must equal the file name %q", t.ID, want))
	}
	if err := templates.Validate(t); err != nil {
		var ve *templates.ValidationError
		if errors.As(err, &ve) {
			out = append(out, ve.Problems...)
		} else {
			out = append(out, err.Error())
		}
	}
	return out
}

// runLookup answers -resolve and -tags.
func runLookup(o options) int {
	ctx := context.Background()
	reg := newRegistry()
	if o.Resolve != "" {
		ref := parseImage(o.Resolve)
		d, err := reg.Digest(ctx, ref)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("digest:", d)
		pl, err := reg.Platforms(ctx, ref)
		if err != nil {
			fmt.Println("platforms: unknown:", err)
		} else {
			var ps []string
			for p := range pl {
				ps = append(ps, p)
			}
			sort.Strings(ps)
			fmt.Println("platforms:", strings.Join(ps, ", "))
		}
	}
	if o.Tags != "" {
		ref := parseImage(o.Tags)
		tags, err := reg.Tags(ctx, ref)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		suffix := wantedSuffix(ref.Tag)
		fmt.Printf("%d tags; newest stable (suffix %q):\n", len(tags), suffix)
		for _, t := range pickTags(tags, suffix, 12) {
			fmt.Println("  ", t)
		}
	}
	return 0
}

// runVerify checks every pinned image of the given template files against its
// registry: the tag must resolve to the pinned index digest, and the images
// must publish the architectures the template claims. It never pulls.
func runVerify(files []string) int {
	ctx := context.Background()
	reg := newRegistry()
	type job struct {
		file, key, image, digest string
		arch                     []string
	}
	var jobs []job
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", f, err)
			return 1
		}
		t, err := templates.Parse(raw)
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", f, err)
			return 1
		}
		arch := t.Requires.Arch
		if len(arch) == 0 {
			arch = []string{"amd64", "arm64"}
		}
		for _, s := range t.Services {
			if s.Image != "" {
				jobs = append(jobs, job{f, s.Key, s.Image, s.Digest, arch})
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	bad := 0
	report := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		bad++
		fmt.Printf(format+"\n", a...)
	}
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			ref := parseImage(j.image)
			var d string
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				if d, err = reg.Digest(cctx, ref); err == nil {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			}
			name := filepath.Base(j.file) + "/" + j.key
			if err != nil {
				report("FAIL %s: %s: %v", name, j.image, err)
				return
			}
			if d != j.digest {
				report("FAIL %s: %s resolves to %s, the template pins %s", name, j.image, d, j.digest)
				return
			}
			pl, err := reg.Platforms(cctx, ref)
			if err != nil || pl == nil {
				return // platforms unknown: not a failure, template-pin can look closer
			}
			for _, a := range j.arch {
				if !pl["linux/"+a] {
					report("FAIL %s: %s has no linux/%s manifest", name, j.image, a)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("verified %d images in %d files, %d problems\n", len(jobs), len(files), bad)
	if bad > 0 {
		return 1
	}
	return 0
}
