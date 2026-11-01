// Package testsample picks which catalog templates the full integration tier
// deploys. Deploying a template pulls images and takes minutes, so a run deploys
// a bounded, deterministic sample (a daily rotation plus a capped slice of the
// templates changed against the main branch, never a heavy one), while every
// template is still validated statically. NEBULACTRL_TEST_TEMPLATES widens or
// narrows it by hand.
package testsample

import (
	"slices"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

const (
	// RotationSize is how many templates the daily rotation deploys.
	RotationSize = 3
	// MaxChanged is the most changed templates one run deploys; the rest rotate
	// in on later days.
	MaxChanged = 8
)

// Window returns size ids starting at day*size, wrapping around. ids must be
// sorted. The start advances by exactly size per day, so consecutive days
// cover consecutive, non-overlapping windows and every id is hit within
// ceil(len/size) days, whatever the length.
func Window(ids []string, day int64, size int) []string {
	n := len(ids)
	if n == 0 || size <= 0 {
		return nil
	}
	size = min(size, n)
	start := int(day * int64(size) % int64(n))
	out := make([]string, 0, size)
	for i := range size {
		out = append(out, ids[(start+i)%n])
	}
	return out
}

// Selection is what a default run deploys and what it left out.
type Selection struct {
	// IDs are the templates to deploy: Rotation first, then Changed.
	IDs      []string
	Rotation []string
	// Changed are the changed templates deployed this run (at most MaxChanged).
	Changed []string
	// SkippedChanged are changed templates that did not fit this run's cap;
	// they rotate in on other days.
	SkippedChanged []string
	// SkippedHeavy are the templates left out because they are heavy: changed
	// ones that were skipped for that reason. Name them in
	// NEBULACTRL_TEST_TEMPLATES to deploy them.
	SkippedHeavy []string
}

// Select builds the default selection for a day (days since the Unix epoch,
// UTC). all is the whole catalog, changed the ids changed against the main
// branch. Heavy templates are never part of it.
func Select(all []*templates.CatalogTemplate, changed []string, day int64) Selection {
	heavy := map[string]bool{}
	var ids []string
	for _, t := range all {
		if t.Heavy {
			heavy[t.ID] = true
			continue
		}
		ids = append(ids, t.ID)
	}
	slices.Sort(ids)

	var sel Selection
	sel.Rotation = Window(ids, day, RotationSize)

	var eligible []string
	for _, id := range slices.Compact(slices.Sorted(slices.Values(changed))) {
		if heavy[id] {
			sel.SkippedHeavy = append(sel.SkippedHeavy, id)
			continue
		}
		if slices.Contains(ids, id) {
			eligible = append(eligible, id)
		}
	}
	sel.Changed = Window(eligible, day, MaxChanged)
	for _, id := range eligible {
		if !slices.Contains(sel.Changed, id) {
			sel.SkippedChanged = append(sel.SkippedChanged, id)
		}
	}

	sel.IDs = slices.Clone(sel.Rotation)
	for _, id := range sel.Changed {
		if !slices.Contains(sel.IDs, id) {
			sel.IDs = append(sel.IDs, id)
		}
	}
	return sel
}

// InputValues fills the inputs an install must be given: every required input
// that has no default (and is not a select) gets a realistic value of its
// format, and a required secret without a default gets a password. Other
// inputs are left to their defaults.
func InputValues(t *templates.CatalogTemplate) map[string]string {
	out := map[string]string{}
	for _, in := range t.Inputs {
		if !in.Required || strings.TrimSpace(in.Default) != "" {
			continue
		}
		switch {
		case len(in.Options) > 0:
			out[in.Key] = in.Options[0]
		case in.Secret:
			out[in.Key] = "Nebula-test-secret-4711"
		default:
			out[in.Key] = SampleInputValue(in.Format)
		}
	}
	return out
}

// SampleInputValue is a realistic value of a format, for tests and docs.
func SampleInputValue(format string) string {
	switch format {
	case templates.FormatEmail:
		// Not a reserved TLD such as .test: apps such as pgAdmin reject
		// special-use domains in their email validation.
		return "ops@nebulactrl.dev"
	case templates.FormatURL:
		return "https://example.nebulactrl.test"
	case templates.FormatHostname:
		return "host.nebulactrl.test"
	case templates.FormatNumber:
		return "587"
	default:
		return "nebula-test"
	}
}
