package testsample

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	catalogfs "github.com/nebulactrl/nebulactrl/templates"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

func tpls(heavy map[string]bool, ids ...string) []*templates.CatalogTemplate {
	var out []*templates.CatalogTemplate
	for _, id := range ids {
		out = append(out, &templates.CatalogTemplate{ID: id, Heavy: heavy[id]})
	}
	return out
}

func TestWindowWrapsAndCoversEverything(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f", "g"}
	require.Equal(t, []string{"a", "b", "c"}, Window(ids, 0, 3))
	require.Equal(t, []string{"d", "e", "f"}, Window(ids, 1, 3))
	require.Equal(t, []string{"g", "a", "b"}, Window(ids, 2, 3))
	require.Equal(t, []string{"a", "b", "c", "d", "e", "f", "g"}, Window(ids, 5, 99), "a window larger than the list is the list")
	require.Nil(t, Window(nil, 3, 3))
	require.Nil(t, Window(ids, 3, 0))

	// Every id is hit within ceil(n/size) days, for lengths divisible by the size or not.
	for _, n := range []int{7, 9, 100, 312, 329} {
		var all []string
		for i := range n {
			all = append(all, fmt.Sprintf("t%04d", i))
		}
		seen := map[string]bool{}
		days := (n + RotationSize - 1) / RotationSize
		for d := range days {
			for _, id := range Window(all, int64(20000+d), RotationSize) {
				seen[id] = true
			}
		}
		require.Len(t, seen, n, "n=%d", n)
	}
}

func TestSelectCapsChangedAndSkipsHeavy(t *testing.T) {
	var ids []string
	for i := range 40 {
		ids = append(ids, fmt.Sprintf("app%02d", i))
	}
	all := tpls(map[string]bool{"app01": true, "app05": true}, ids...)
	changed := slices.Clone(ids[:30]) // 30 changed templates, two of them heavy

	sel := Select(all, changed, 100)
	require.Len(t, sel.Rotation, RotationSize)
	require.LessOrEqual(t, len(sel.Changed), MaxChanged)
	require.Len(t, sel.Changed, MaxChanged)
	require.LessOrEqual(t, len(sel.IDs), RotationSize+MaxChanged)
	require.ElementsMatch(t, []string{"app01", "app05"}, sel.SkippedHeavy)
	for _, id := range sel.IDs {
		require.NotContains(t, []string{"app01", "app05"}, id, "a heavy template is never sampled")
	}
	require.Len(t, sel.SkippedChanged, 28-MaxChanged, "28 eligible changed minus the 8 deployed")
	// Nothing is silently dropped: deployed + skipped = eligible.
	require.ElementsMatch(t, slices.Concat(sel.Changed, sel.SkippedChanged), slices.DeleteFunc(slices.Clone(changed), func(id string) bool { return id == "app01" || id == "app05" }))

	// Deterministic for a day, different on the next, and the changed set is
	// fully covered across days.
	require.Equal(t, sel, Select(all, changed, 100))
	require.NotEqual(t, sel.Changed, Select(all, changed, 101).Changed)
	covered := map[string]bool{}
	for d := range 4 {
		for _, id := range Select(all, changed, int64(100+d)).Changed {
			covered[id] = true
		}
	}
	require.Len(t, covered, 28)
}

func TestSelectSmallAndEmpty(t *testing.T) {
	all := tpls(nil, "a", "b", "c", "d", "e")
	sel := Select(all, []string{"d", "a"}, 7)
	require.Equal(t, []string{"a", "d"}, slices.Sorted(slices.Values(sel.Changed)), "few changed templates are all deployed")
	require.Empty(t, sel.SkippedChanged)
	require.Empty(t, sel.SkippedHeavy)
	// A changed template that left the catalog is ignored.
	require.Empty(t, Select(all, []string{"gone"}, 7).Changed)
	require.Empty(t, Select(nil, nil, 7).IDs)
}

func TestInputValuesFillRequiredInputs(t *testing.T) {
	tpl := &templates.CatalogTemplate{Inputs: []templates.TemplateInput{
		{Key: "ADMIN_EMAIL", Label: "E", Required: true, Format: templates.FormatEmail},
		{Key: "OIDC_URL", Label: "U", Required: true, Format: templates.FormatURL},
		{Key: "DOMAIN", Label: "D", Required: true, Format: templates.FormatHostname},
		{Key: "PORT_X", Label: "P", Required: true, Format: templates.FormatNumber},
		{Key: "NAME", Label: "N", Required: true},
		{Key: "MODE", Label: "M", Required: true, Options: []string{"fast", "slow"}},
		{Key: "PW", Label: "P", Required: true, Secret: true},
		{Key: "HAS_DEFAULT", Label: "H", Required: true, Default: "x"},
		{Key: "OPTIONAL", Label: "O"},
	}}
	got := InputValues(tpl)
	require.Equal(t, "ops@nebulactrl.dev", got["ADMIN_EMAIL"])
	require.Equal(t, "fast", got["MODE"])
	require.NotContains(t, got, "HAS_DEFAULT")
	require.NotContains(t, got, "OPTIONAL")
	for _, in := range tpl.Inputs {
		if v, ok := got[in.Key]; ok && !in.Secret && len(in.Options) == 0 {
			require.Empty(t, templates.CheckInputFormat(in.Format, v), in.Key)
		}
	}
}

// TestEveryCatalogTemplateResolvesWithItsSampleInputs: installing any template
// with the generated test values passes input resolution, so no template can
// fail an integration run with missing_input or invalid_input.
func TestEveryCatalogTemplateResolvesWithItsSampleInputs(t *testing.T) {
	cat, errs := templates.LoadFS(catalogfs.FS, "catalog")
	require.Empty(t, errs)
	require.GreaterOrEqual(t, cat.Len(), 300)
	mat := func(string) (string, bool, error) { return "generated-value", true, nil }
	for _, tpl := range cat.Search("", "") {
		_, err := templates.ResolveInputs(tpl, InputValues(tpl), mat)
		require.NoError(t, err, tpl.ID)
	}
}
