package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

type fakeFetcher struct {
	vals  map[string]string
	calls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, ref varexpr.ExtRef) (string, error) {
	f.calls = append(f.calls, ref.String())
	v, ok := f.vals[ref.String()]
	if !ok {
		return "", apierr.Unprocessable("external_secret_failed", "reference ${{"+ref.String()+"}}: not found")
	}
	return v, nil
}

func extResolver(fetch ExtFetcher) *Resolver {
	rc := ResolverContext{
		EnvSlug: "production", EnvName: "production",
		Integrations: map[string]bool{"bao": true},
		Team: map[string]SharedDef{
			"REGION":   {Value: "eu-west", Layer: "team"},
			"DB_PW":    {Value: "${{vault:bao/apps/db#password}}", Layer: "team"},
			"PW_COPY":  {Value: "${{team.DB_PW}}", Layer: "team"},
			"CYCLE_A":  {Value: "${{team.CYCLE_B}}", Layer: "team"},
			"CYCLE_B":  {Value: "${{team.CYCLE_A}}", Layer: "team"},
			"STATIC_S": {Value: "static-team-secret", Secret: true, Layer: "team"},
		},
		Shared: map[string]SharedDef{"FROM_TEAM": {Value: "x-${{team.REGION}}", Layer: "project"}},
	}
	r := NewResolverCtx(rc, []ServiceData{{Slug: "api", Port: 8080, Vars: map[string]VarDef{
		"REGION":  {Value: "${{team.REGION}}"},
		"SHARED":  {Value: "${{shared.FROM_TEAM}}"},
		"TOKEN":   {Value: "${{vault:bao/apps/shop#token}}"},
		"DB_URL":  {Value: "postgres://app:${{urlencode(vault:bao/apps/db#password)}}@db/app?r=${{team.REGION}}"},
		"VIA":     {Value: "${{team.PW_COPY}}"},
		"STATIC":  {Value: "${{team.STATIC_S}}"},
		"MISSING": {Value: "${{team.NOPE}}"},
		"GONE":    {Value: "${{vault:other/a#b}}"},
	}}})
	if fetch != nil {
		r = r.WithFetcher(context.Background(), fetch)
	}
	return r
}

func TestTeamScopeResolves(t *testing.T) {
	res, probs, err := extResolver(nil).ResolveServiceLenient("api")
	require.NoError(t, err)
	require.Equal(t, "eu-west", res.Vars["REGION"])
	require.Equal(t, "x-eu-west", res.Vars["SHARED"], "project shared values may use team variables")
	require.Equal(t, "static-team-secret", res.Vars["STATIC"])
	require.True(t, res.Secret["STATIC"], "a secret team variable makes the service variable secret")
	var codes []string
	for _, p := range probs {
		codes = append(codes, p.Key+":"+p.Code)
	}
	require.ElementsMatch(t, []string{"MISSING:unresolved_reference", "GONE:unresolved_reference"}, codes)
	for _, p := range probs {
		if p.Key == "MISSING" {
			require.Contains(t, p.Message, "${{team.NOPE}}")
		}
		if p.Key == "GONE" {
			require.Contains(t, p.Message, "${{vault:other/a#b}}", "the error names the reference")
			require.Contains(t, p.Message, `"other"`)
		}
	}
}

func TestTeamCycleIsRejected(t *testing.T) {
	probs := extResolver(nil).DiagnoseShared()
	var cyc []Problem
	for _, p := range probs {
		if p.Code == "reference_cycle" {
			cyc = append(cyc, p)
		}
	}
	require.NotEmpty(t, cyc)
	require.Contains(t, cyc[0].Message, "team.CYCLE_")
}

func TestExternalReferencesAreDeferred(t *testing.T) {
	res, _, err := extResolver(nil).ResolveServiceLenient("api")
	require.NoError(t, err)
	// The stored value is the reference, never a fetched value.
	require.Equal(t, "${{vault:bao/apps/shop#token}}", res.Vars["TOKEN"])
	require.True(t, res.Secret["TOKEN"], "external values are always secret")
	require.Equal(t, "postgres://app:urlencode(${{vault:bao/apps/db#password}})@db/app?r=eu-west", res.Vars["DB_URL"])
	require.True(t, res.Secret["DB_URL"])
	require.Equal(t, "${{vault:bao/apps/db#password}}", res.Vars["VIA"], "through a team variable")
	require.Contains(t, res.Ext, "TOKEN")
	require.Contains(t, res.Ext, "DB_URL")
	require.Contains(t, res.Ext, "VIA")
	require.NotContains(t, res.Ext, "REGION")
	require.NotContains(t, res.Ext, "STATIC")
}

func TestExternalReferencesFetchedWhenAsked(t *testing.T) {
	f := &fakeFetcher{vals: map[string]string{
		"vault:bao/apps/shop#token":     "tok-abcdef",
		"vault:bao/apps/db#password":    "p@ss word/1",
		"vault:bao/apps/missing#absent": "x",
	}}
	res, _, err := extResolver(f).ResolveServiceLenient("api")
	require.NoError(t, err)
	require.Equal(t, "tok-abcdef", res.Vars["TOKEN"])
	require.Equal(t, "postgres://app:"+url.QueryEscape("p@ss word/1")+"@db/app?r=eu-west", res.Vars["DB_URL"])
	require.Equal(t, "p@ss word/1", res.Vars["VIA"])
	require.Empty(t, res.Ext)
	require.Contains(t, res.Leaves, "tok-abcdef")
	require.Contains(t, res.Leaves, "p@ss word/1")
	require.Contains(t, res.Leaves, url.QueryEscape("p@ss word/1"), "the transformed form is masked too")
}

func TestExternalFetchErrorNamesTheReference(t *testing.T) {
	f := &fakeFetcher{vals: map[string]string{}}
	_, err := extResolver(f).ResolveService("api")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "external_secret_failed", ae.Code)
	require.Contains(t, ae.Message, "${{vault:bao/")
}

func TestEnvSnapshotMaterialize(t *testing.T) {
	res, _, err := extResolver(nil).ResolveServiceLenient("api")
	require.NoError(t, err)
	snap := EnvSnapshot{Vars: res.Vars, Ext: res.Ext}

	// The snapshot survives JSON (it is sealed into releases.env_enc) and holds no value.
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "tok-abcdef")
	var back EnvSnapshot
	require.NoError(t, json.Unmarshal(raw, &back))

	f := &fakeFetcher{vals: map[string]string{
		"vault:bao/apps/shop#token":  "tok-abcdef",
		"vault:bao/apps/db#password": "p@ss word/1",
	}}
	vars, leaves, err := back.Materialize(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, "tok-abcdef", vars["TOKEN"])
	require.Equal(t, "postgres://app:"+url.QueryEscape("p@ss word/1")+"@db/app?r=eu-west", vars["DB_URL"])
	require.Equal(t, "eu-west", vars["REGION"])
	require.Contains(t, leaves, "tok-abcdef")
	require.Equal(t, res.Vars["TOKEN"], back.Vars["TOKEN"], "the snapshot itself is not modified")

	// Without external variables the fetcher is never used.
	plain := EnvSnapshot{Vars: map[string]string{"A": "1"}}
	got, _, err := plain.Materialize(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "1", got["A"])

	// A missing fetcher or a failing fetch is an error that names the variable and reference.
	_, _, err = back.Materialize(context.Background(), nil)
	require.Error(t, err)
	_, _, err = back.Materialize(context.Background(), &fakeFetcher{vals: map[string]string{}})
	require.Error(t, err)
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae))
	require.Contains(t, err.Error(), "variable ")
}

func TestRememberedExternalValuesAreBoundedAndInMemory(t *testing.T) {
	id := uuid.New()
	ext := NewExtSecrets(nil)
	for i := 0; i < 5; i++ {
		ext.Remember(id, []string{"value-" + string(rune('a'+i))})
	}
	vals, _ := ext.values(id)
	require.Equal(t, []string{"value-c", "value-d", "value-e"}, vals, "only the newest batches are kept")
	g := ext.generation()
	ext.Remember(id, nil)
	require.Equal(t, g, ext.generation(), "an empty batch changes nothing")
	// Two handles never share state, and a nil handle is inert.
	require.Empty(t, func() []string { v, _ := NewExtSecrets(nil).values(id); return v }())
	var none *ExtSecrets
	none.Remember(id, []string{"x"})
	require.Nil(t, none.Fetcher(ExtScope{}))
	require.Zero(t, none.generation())
	red := MakeRedactor(vals)
	require.Equal(t, "x "+Mask+" y", red("x value-d y"))
}

func TestReservedSlugsIncludeTeam(t *testing.T) {
	require.True(t, IsReservedSlug("team"))
	require.True(t, IsReservedSlug("shared"))
	require.True(t, IsReservedSlug("vault"))
}
