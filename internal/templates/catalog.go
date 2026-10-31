package templates

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Catalog is an immutable set of validated templates.
type Catalog struct {
	byID map[string]*CatalogTemplate
	list []*CatalogTemplate // sorted by category order, then name
}

// LoadFS parses and validates every catalog/*.yaml in fsys. Templates that fail
// are returned in errs and left out of the catalog, so one bad file never takes
// the server down; the static test asserts errs is empty.
func LoadFS(fsys fs.FS, dir string) (*Catalog, []error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return &Catalog{byID: map[string]*CatalogTemplate{}}, []error{fmt.Errorf("read catalog: %w", err)}
	}
	c := &Catalog{byID: map[string]*CatalogTemplate{}}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		t, err := Parse(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if err := Validate(t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if want := strings.TrimSuffix(e.Name(), ".yaml"); t.ID != want {
			errs = append(errs, fmt.Errorf("%s: id %q must equal the file name", e.Name(), t.ID))
			continue
		}
		if _, dup := c.byID[t.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate template id %q", e.Name(), t.ID))
			continue
		}
		c.byID[t.ID] = t
		c.list = append(c.list, t)
	}
	order := map[string]int{}
	for i, k := range Categories {
		order[k] = i
	}
	sort.Slice(c.list, func(i, j int) bool {
		a, b := c.list[i], c.list[j]
		if order[a.Category] != order[b.Category] {
			return order[a.Category] < order[b.Category]
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return c, errs
}

// Get returns a template by id.
func (c *Catalog) Get(id string) (*CatalogTemplate, bool) {
	t, ok := c.byID[id]
	return t, ok
}

// Len is the number of templates.
func (c *Catalog) Len() int { return len(c.list) }

// Filter narrows a catalog search. Empty fields match everything.
type Filter struct {
	// Query is matched word by word (every word must hit) against the id,
	// name, summary, tags, upstream licence and managed database engines.
	Query string
	// Category is a category id.
	Category string
	// LicenseClass is permissive | copyleft | source-available | proprietary.
	LicenseClass string
}

// Search returns the templates matching every whitespace-separated token of q
// (name, summary, tags, id) within category ("" = all), best match first.
func (c *Catalog) Search(q, category string) []*CatalogTemplate {
	return c.Find(Filter{Query: q, Category: category})
}

// Find applies f. Without a query the display order is kept (category order,
// then name); with one, templates rank by where the words hit: the name
// first, then the id, the tags, and last the summary or licence. Ties keep
// the display order.
func (c *Catalog) Find(f Filter) []*CatalogTemplate {
	tokens := strings.Fields(strings.ToLower(f.Query))
	type hit struct {
		t    *CatalogTemplate
		rank int
	}
	hits := make([]hit, 0, len(c.list))
	for _, t := range c.list {
		if f.Category != "" && t.Category != f.Category {
			continue
		}
		if f.LicenseClass != "" && t.Upstream.Class != f.LicenseClass {
			continue
		}
		rank, ok := matchRank(t, tokens)
		if !ok {
			continue
		}
		hits = append(hits, hit{t, rank})
	}
	if len(tokens) > 0 {
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	}
	out := make([]*CatalogTemplate, len(hits))
	for i, h := range hits {
		out[i] = h.t
	}
	return out
}

// Match ranks, lower is better.
const (
	rankName = iota
	rankNameContains
	rankID
	rankTag
	rankOther
)

// matchRank reports whether t matches every token and how well.
func matchRank(t *CatalogTemplate, tokens []string) (int, bool) {
	if len(tokens) == 0 {
		return rankOther, true
	}
	name := strings.ToLower(t.Name)
	id := strings.ToLower(t.ID)
	tags := strings.ToLower(strings.Join(t.Tags, " "))
	var other strings.Builder
	other.WriteString(strings.ToLower(t.Summary + " " + t.Upstream.License + " " + t.Upstream.Class + " " + t.Category))
	for _, s := range t.Services {
		if s.Database != nil {
			other.WriteString(" " + s.Database.Engine)
		}
	}
	hay := name + " " + id + " " + tags + " " + other.String()
	rank := rankName
	for _, tok := range tokens {
		if !strings.Contains(hay, tok) {
			return 0, false
		}
		r := rankOther
		switch {
		case name == tok || strings.HasPrefix(name, tok) || strings.Contains(name, " "+tok):
			r = rankName
		case strings.Contains(name, tok):
			r = rankNameContains
		case strings.Contains(id, tok):
			r = rankID
		case strings.Contains(tags, tok):
			r = rankTag
		}
		rank = max(rank, r)
	}
	return rank, true
}
