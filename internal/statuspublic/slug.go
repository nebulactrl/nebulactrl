package statuspublic

import (
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// maxSlug bounds a component slug, and with it a badge path segment.
const maxSlug = 80

// slugify turns a display name into a URL slug: lower-case ASCII letters and
// digits, runs of anything else become one hyphen, no hyphen at either end. A
// name without any such character (a name in another script, say) is
// "component".
func slugify(name string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && sb.Len() > 0 {
				sb.WriteByte('-')
			}
			dash = false
			sb.WriteRune(r)
		default:
			dash = true
		}
	}
	s := sb.String()
	if len(s) > maxSlug {
		s = strings.TrimRight(s[:maxSlug], "-")
	}
	if s == "" {
		return "component"
	}
	return s
}

// componentRef is a component of a page with its slug.
type componentRef struct {
	Slug      string
	Component statuspages.PublicComponent
}

// componentRefs lists the page's components in page order with unique slugs:
// the second component that would get "api" gets "api-2", the third "api-3".
func componentRefs(p statuspages.PublicPage) []componentRef {
	var out []componentRef
	seen := map[string]bool{}
	for _, g := range p.Groups {
		for _, c := range g.Components {
			base := slugify(c.Name)
			s := base
			for n := 2; seen[s]; n++ {
				suffix := "-" + strconv.Itoa(n)
				s = strings.TrimRight(base[:min(len(base), maxSlug-len(suffix))], "-") + suffix
			}
			seen[s] = true
			out = append(out, componentRef{Slug: s, Component: c})
		}
	}
	return out
}

// findComponent returns the component with the given slug.
func findComponent(p statuspages.PublicPage, slug string) (statuspages.PublicComponent, bool) {
	for _, r := range componentRefs(p) {
		if r.Slug == slug {
			return r.Component, true
		}
	}
	return statuspages.PublicComponent{}, false
}
