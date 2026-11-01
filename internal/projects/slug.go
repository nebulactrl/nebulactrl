package projects

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

const maxSlugLen = 48

// Slugify lowercases s and reduces it to [a-z0-9-], collapsing runs of other
// characters into one hyphen. It returns fallback when nothing is left.
func Slugify(s, fallback string) string {
	var b strings.Builder
	dash := true // suppress leading hyphens
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLower(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxSlugLen {
		out = strings.Trim(out[:maxSlugLen], "-")
	}
	if out == "" {
		return fallback
	}
	return out
}

// UniqueSlug returns base, or base-2, base-3, … so that it is not in taken.
func UniqueSlug(base string, taken []string) string {
	set := make(map[string]struct{}, len(taken))
	for _, t := range taken {
		set[t] = struct{}{}
	}
	if _, ok := set[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		c := base + "-" + strconv.Itoa(i)
		if _, ok := set[c]; !ok {
			return c
		}
	}
}

// reservedEnvironmentSlugs are the paths the web app serves under
// /p/{project}/ next to an environment's slug; an environment named like one
// would be unreachable.
var reservedEnvironmentSlugs = map[string]bool{"settings": true}

// CheckEnvironmentSlug refuses (422 slug_reserved) an environment slug that the
// dashboard uses for one of its own pages. The slug of an environment is made
// from its name when it is created or cloned and never changes, so a rename
// cannot produce a reserved one.
func CheckEnvironmentSlug(slug string) error {
	if reservedEnvironmentSlugs[slug] {
		return apierr.Unprocessable("slug_reserved", "An environment cannot be called "+slug+": the project's "+slug+" page uses that address. Choose another name.").
			WithDetails(map[string]any{"slug": slug})
	}
	return nil
}
