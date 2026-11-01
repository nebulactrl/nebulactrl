package projectconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// t2validFixtures lists testdata/valid/*.toml.
func t2validFixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "valid", "*.toml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "testdata/valid has no fixtures")
	sort.Strings(paths)
	return paths
}

// t2parseFixture parses a valid fixture; it must not have errors.
func t2parseFixture(t *testing.T, path string) (*File, *Index, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	f, idx, ds := Parse(data, nil)
	require.NotNil(t, f, path)
	require.False(t, ds.HasErrors(), "%s is a valid fixture but has errors:\n%s", path, ds.RenderText(""))
	return f, idx, data
}

// t2sameView requires two resolved views to hold the same services with the
// same field values (values only: positions and paths describe the text).
func t2sameView(t *testing.T, want, got *Resolved, what string) {
	t.Helper()
	require.NotNil(t, got, what)
	require.Equal(t, want.SortedSlugs(), got.SortedSlugs(), "%s: services", what)
	for _, slug := range want.SortedSlugs() {
		w, g := want.Services[slug], got.Services[slug]
		require.Equal(t, w.Kind, g.Kind, "%s: kind of %s", what, slug)
		require.Equal(t, w.ServerName, g.ServerName, "%s: server of %s", what, slug)
		require.Equal(t, w.FieldIDs(), g.FieldIDs(), "%s: field ids of %s", what, slug)
		for _, id := range w.FieldIDs() {
			if !reflect.DeepEqual(w.Fields[id].Value, g.Fields[id].Value) {
				t.Errorf("%s: %s %s: value %#v became %#v", what, slug, id, w.Fields[id].Value, g.Fields[id].Value)
			}
			require.Equal(t, w.Fields[id].Applies, g.Fields[id].Applies, "%s: %s %s applies", what, slug, id)
		}
	}
	require.Equal(t, want.Server, got.Server, "%s: default server", what)
	require.Equal(t, want.Project, got.Project, "%s: project", what)
	require.Equal(t, t2varsOrNil(want.Shared), t2varsOrNil(got.Shared), "%s: project layer", what)
	require.Equal(t, t2varsOrNil(want.EnvShared), t2varsOrNil(got.EnvShared), "%s: environment layer", what)
	require.Equal(t, want.SharedSecrets, got.SharedSecrets, "%s: shared secrets", what)
	require.Equal(t, want.EnvSharedSecrets, got.EnvSharedSecrets, "%s: environment secrets", what)
}

func t2varsOrNil(m map[string]Variable) map[string]Variable {
	if len(m) == 0 {
		return nil
	}
	return m
}

// Parse -> Resolve -> Render -> Parse, for every valid fixture and every view.
func TestRoundTripValidFixtures(t *testing.T) {
	for _, path := range t2validFixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, idx, _ := t2parseFixture(t, path)
			for _, env := range append([]string{""}, f.Environments()...) {
				t.Run("env "+env, func(t *testing.T) {
					r, rds := f.Resolve(env, idx)
					require.Empty(t, rds)

					out := Render(r, RenderOptions{})
					require.NotEmpty(t, out)
					require.Equal(t, out, Render(r, RenderOptions{}), "Render is byte-identical when called twice")
					require.Equal(t, byte('\n'), out[len(out)-1])

					f2, idx2, ds2 := Parse(out, nil)
					require.NotNil(t, f2, string(out))
					require.False(t, ds2.HasErrors(), "the rendered file has errors:\n%s\n%s", ds2.RenderText(""), out)
					r2, rds2 := f2.Resolve(env, idx2)
					require.Empty(t, rds2)
					t2sameView(t, r, r2, "re-resolved")

					require.Equal(t, string(out), string(Render(r2, RenderOptions{})), "Render is idempotent through a parse")
				})
			}
		})
	}
}

func TestRenderOptionsRoundTrip(t *testing.T) {
	for _, path := range t2validFixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, idx, _ := t2parseFixture(t, path)
			r, _ := f.Resolve("", idx)
			plain := Render(r, RenderOptions{})
			opts := RenderOptions{Origin: "https://nebula.example.com/", Comments: map[string][]string{"server": {"default server"}}}
			withHeader := Render(r, opts)
			require.Contains(t, string(withHeader), "#:schema https://nebula.example.com/schema/nebula.toml.json\n")
			f2, idx2, ds := Parse(withHeader, nil)
			require.NotNil(t, f2)
			require.False(t, ds.HasErrors(), ds.RenderText(""))
			r2, _ := f2.Resolve("", idx2)
			t2sameView(t, r, r2, "with header and comments")
			f3, idx3, _ := Parse(plain, nil)
			r3, _ := f3.Resolve("", idx3)
			t2sameView(t, r2, r3, "header does not change the content")
		})
	}
}

// t2factorEnvironments resolves every environment block of a fixture plus a
// production environment without a block, so there are always two to factor.
func t2factorEnvironments(t *testing.T, f *File, idx *Index) (map[string]*Resolved, string) {
	t.Helper()
	names := f.Environments()
	hasProduction := false
	for _, n := range names {
		hasProduction = hasProduction || n == "production"
	}
	if !hasProduction {
		names = append(names, "production")
	}
	envs := map[string]*Resolved{}
	for _, n := range names {
		r, rds := f.Resolve(n, idx)
		require.Empty(t, rds)
		envs[n] = r
	}
	return envs, "production"
}

// Factor property: Resolve(Parse(Factor(envs)), e) equals envs[e] for every
// environment, on every valid fixture.
func TestFactorPropertyOnFixtures(t *testing.T) {
	for _, path := range t2validFixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, idx, _ := t2parseFixture(t, path)
			envs, base := t2factorEnvironments(t, f, idx)

			out, err := Factor(envs, base, RenderOptions{})
			require.NoError(t, err)
			again, err := Factor(envs, base, RenderOptions{})
			require.NoError(t, err)
			require.Equal(t, string(out), string(again), "Factor is deterministic")

			f2, idx2, ds := Parse(out, nil)
			require.NotNil(t, f2, string(out))
			require.False(t, ds.HasErrors(), "the factored file has errors:\n%s\n%s", ds.RenderText(""), out)
			for name, want := range envs {
				got, rds := f2.Resolve(name, idx2)
				require.Empty(t, rds, name)
				t2sameView(t, want, got, "environment "+name)
			}
		})
	}
}

// The annotated example: staging has fewer keys than the base,
// so the factored file must carry an unset for them, and the base services
// must not leak into staging.
func TestFactorAcmeShop(t *testing.T) {
	path := filepath.Join("testdata", "valid", "acme-shop.toml")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join("testdata", "valid", "docs-full.toml")
	}
	f, idx, _ := t2parseFixture(t, path)
	require.Contains(t, f.Environments(), "staging")
	prod, _ := f.Resolve("production", idx)
	staging, _ := f.Resolve("staging", idx)
	envs := map[string]*Resolved{"production": prod, "staging": staging}

	out, err := Factor(envs, "production", RenderOptions{})
	require.NoError(t, err)
	f2, idx2, ds := Parse(out, nil)
	require.NotNil(t, f2)
	require.False(t, ds.HasErrors(), "%s\n%s", ds.RenderText(""), out)

	gotProd, _ := f2.Resolve("production", idx2)
	gotStaging, _ := f2.Resolve("staging", idx2)
	t2sameView(t, prod, gotProd, "production")
	t2sameView(t, staging, gotStaging, "staging")

	// the facts of the example, through the factored file
	require.NotNil(t, gotProd.Services["printer"], "printer exists in production")
	require.Nil(t, gotStaging.Services["printer"], "printer is production only")
	require.Equal(t, "develop", gotStaging.Services["web"].Fields["config.gitBranch"].Value)
	require.Equal(t, "main", gotProd.Services["web"].Fields["config.gitBranch"].Value)
	require.NotContains(t, gotStaging.Services["web"].FieldIDs(), "scale.replicas")
	require.Contains(t, gotProd.Services["web"].FieldIDs(), "scale.replicas")
	require.Equal(t, "fra-staging", gotStaging.Server)

	// a second pass over the staging view of the factored file changes nothing
	again, err := Factor(map[string]*Resolved{"production": gotProd, "staging": gotStaging}, "production", RenderOptions{})
	require.NoError(t, err)
	require.Equal(t, string(out), string(again))
}
