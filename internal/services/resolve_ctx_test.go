package services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func ctxResolver() *Resolver {
	api := ServiceData{ID: uuid.New(), Slug: "api", Name: "API", Port: 8080, FirstDomain: "api.example.com", Vars: map[string]VarDef{
		"REGION":   {Value: "${{shared.REGION}}"},
		"CONTACT":  {Value: "${{team.CONTACT}}"},
		"PLAIN":    {Value: "hello"},
		"TOKEN":    {Value: "tok", Secret: true, Scope: ScopeBuild},
		"DERIVED":  {Value: "x-${{shared.SECRET}}"},
		"INTERNAL": {Value: "http://${{worker.NEBULA_PRIVATE_HOST}}"},
	}}
	worker := ServiceData{ID: uuid.New(), Slug: "worker", Name: "Worker", Vars: map[string]VarDef{}}
	return NewResolverCtx(ResolverContext{
		ProjectName: "Shop", EnvSlug: "production", EnvName: "Production",
		Shared: map[string]SharedDef{
			"REGION": {Value: "eu-west", Layer: "project"},
			"SECRET": {Value: "s3", Secret: true, Layer: "environment"},
			"A":      {Value: "${{shared.B}}", Layer: "project"},
			"B":      {Value: "${{shared.A}}", Layer: "project"},
			"BAD":    {Value: "${{api.PLAIN}}", Layer: "project"},
		},
		Team: map[string]SharedDef{
			"CONTACT": {Value: "ops@example.com", Layer: "team"},
			"KEY":     {Value: "k", Secret: true, Layer: "team"},
			"MISSING": {Value: "${{team.NOPE}}", Layer: "team"},
		},
	}, []ServiceData{api, worker})
}

func TestResolverExposesItsEnvironment(t *testing.T) {
	r := ctxResolver()

	require.Equal(t, []string{"api", "worker"}, r.Slugs(), "services are listed sorted")
	sd, ok := r.Service("api")
	require.True(t, ok)
	require.Equal(t, 8080, sd.Port)
	_, ok = r.Service("missing")
	require.False(t, ok)

	pv := r.PlatformValues("api")
	require.Equal(t, "API", pv[VarServiceName])
	require.Equal(t, "production", pv[VarEnvironment])
	require.Equal(t, "Production", pv[VarEnvironmentName])
	require.Equal(t, "Shop", pv[VarProjectName])
	require.Equal(t, "api", pv[VarPrivateHost])
	require.Equal(t, "api.example.com", pv[VarPublicDomain])
	require.Equal(t, "https://api.example.com", pv[VarPublicURL])
	require.Equal(t, "8080", pv[VarPort])
	require.Equal(t, sd.ID.String(), pv[VarServiceID])
	wv := r.PlatformValues("worker")
	require.Empty(t, wv[VarPublicURL], "no domain, no public URL")
	require.NotContains(t, wv, VarPort, "a service without a port has no PORT")
	require.Nil(t, r.PlatformValues("missing"))

	def, ok := r.Shared("REGION")
	require.True(t, ok)
	require.Equal(t, SharedDef{Value: "eu-west", Layer: "project"}, def)
	_, ok = r.Shared("NOPE")
	require.False(t, ok)
	require.Equal(t, []string{"A", "B", "BAD", "REGION", "SECRET"}, r.SharedKeys())
	require.Equal(t, []string{"CONTACT", "KEY", "MISSING"}, r.TeamKeys())
}

func TestSharedAndTeamSecrecyFollowsDeclarationAndDerivation(t *testing.T) {
	r := NewResolverCtx(ResolverContext{
		Shared: map[string]SharedDef{
			"PLAIN":   {Value: "x"},
			"SECRET":  {Value: "y", Secret: true},
			"DERIVED": {Value: "pre-${{shared.SECRET}}"},
			"CYCLE":   {Value: "${{shared.CYCLE}}"},
		},
		Team: map[string]SharedDef{
			"PLAIN":   {Value: "x"},
			"SECRET":  {Value: "y", Secret: true},
			"DERIVED": {Value: "pre-${{team.SECRET}}"},
		},
	}, nil)
	require.False(t, r.SharedIsSecret("PLAIN"))
	require.True(t, r.SharedIsSecret("SECRET"))
	require.True(t, r.SharedIsSecret("DERIVED"), "a value built from a secret is a secret")
	require.False(t, r.SharedIsSecret("CYCLE"), "a variable that cannot be evaluated is not reported secret")
	require.False(t, r.SharedIsSecret("UNKNOWN"))
	require.False(t, r.TeamIsSecret("PLAIN"))
	require.True(t, r.TeamIsSecret("SECRET"))
	require.True(t, r.TeamIsSecret("DERIVED"))
	require.False(t, r.TeamIsSecret("UNKNOWN"))
}

func TestDiagnoseSharedReportsEveryVariableThatCannotBeEvaluated(t *testing.T) {
	probs := ctxResolver().DiagnoseShared()
	keys := map[string]Problem{}
	for _, p := range probs {
		keys[p.Key] = p
		require.NotEmpty(t, p.Code, p.Key)
		require.NotEmpty(t, p.Message, p.Key)
	}
	require.Contains(t, keys, "A", "a shared cycle is reported")
	require.Contains(t, keys, "B")
	require.Contains(t, keys, "BAD", "a shared value cannot reference a service")
	require.Contains(t, keys, "team.MISSING", "team problems are prefixed")
	require.NotContains(t, keys, "REGION")
	require.NotContains(t, keys, "SECRET")
	require.NotContains(t, keys, "team.CONTACT")
	require.Len(t, probs, 4)

	require.Empty(t, NewResolverCtx(ResolverContext{Shared: map[string]SharedDef{"OK": {Value: "v"}}}, nil).DiagnoseShared())
}

func TestWithVarOverridesOneVariableWithoutTouchingTheOriginal(t *testing.T) {
	r := ctxResolver()
	d := r.WithVar("api", "PLAIN", VarDef{Value: "changed"})
	d2 := r.WithVar("api", "BRAND_NEW", VarDef{Value: "new"})

	got, err := d.ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "changed", got.Vars["PLAIN"])
	got, err = d2.ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "new", got.Vars["BRAND_NEW"])

	orig, err := r.ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "hello", orig.Vars["PLAIN"], "the original resolver is unchanged")
	require.NotContains(t, orig.Vars, "BRAND_NEW")
	other, err := d.ResolveService("worker")
	require.NoError(t, err)
	require.NotContains(t, other.Vars, "PLAIN")
}

func TestResolveDraftEvaluatesATemplateAsIfItWereStored(t *testing.T) {
	r := ctxResolver()

	val, secret, err := r.ResolveDraft("api", "NEW", "https://${{api.NEBULA_PUBLIC_DOMAIN}}/${{shared.REGION}}", false)
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/eu-west", val)
	require.False(t, secret)

	val, secret, err = r.ResolveDraft("api", "NEW", "prefix-${{shared.SECRET}}", false)
	require.NoError(t, err)
	require.Equal(t, "prefix-s3", val)
	require.True(t, secret, "a draft that embeds a secret is itself secret")

	_, secret, err = r.ResolveDraft("api", "NEW", "plain", true)
	require.NoError(t, err)
	require.True(t, secret, "a declared secret stays secret")

	_, _, err = r.ResolveDraft("api", "NEW", "${{shared.NOPE}}", false)
	require.Error(t, err, "a reference to nothing is reported before the draft is saved")
	_, _, err = r.ResolveDraft("api", "NEW", "${{api.NEW}}", false)
	require.Error(t, err, "a draft that references itself is a cycle")
}

func TestReferenceExtractionSeparatesServicesFromSharedAndTeam(t *testing.T) {
	value := "${{db.HOST}}:${{db.PORT}} ${{shared.REGION}} ${{team.CONTACT}} ${{OWN}} ${{shared.A}} ${{team.KEY}}"
	require.Equal(t, [][2]string{{"db", "HOST"}, {"db", "PORT"}}, References(value), "only references to other services")
	require.Equal(t, []string{"REGION", "A"}, SharedReferences(value))
	require.Equal(t, []string{"CONTACT", "KEY"}, TeamReferences(value))
	require.Nil(t, References("${{unterminated"))
	require.Nil(t, SharedReferences("${{unterminated"))
	require.Nil(t, TeamReferences("${{unterminated"))
	require.Nil(t, References("no references"))
}

func TestPlatformKeysAndReservedNamespaces(t *testing.T) {
	require.True(t, IsPlatformKey("PORT"))
	require.True(t, IsPlatformKey("NEBULA_ANYTHING"))
	require.False(t, IsPlatformKey("PORTAL"))
	require.False(t, IsPlatformKey("MY_PORT"))
	for _, s := range []string{"shared", "team", "vault"} {
		require.True(t, IsReservedSlug(s) || s == "vault", s)
	}
	require.True(t, IsReservedSlug("shared"))
	require.True(t, IsReservedSlug("team"))
	require.False(t, IsReservedSlug("api"))
	require.True(t, ValidScope(ScopeRuntime))
	require.True(t, ValidScope(ScopeBuild))
	require.True(t, ValidScope(ScopeBoth))
	require.False(t, ValidScope(""))
	require.False(t, ValidScope("everywhere"))
}

func TestEffectivePortUsesTheMeshPortOnlyAcrossServers(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	provider := ServiceData{Port: 5432, MeshPort: 41000, ServerID: &a}

	require.Equal(t, 41000, EffectivePort(&b, provider), "another server dials the mesh exposure")
	require.Equal(t, 5432, EffectivePort(&a, provider), "the same server dials the container")
	require.Equal(t, 5432, EffectivePort(nil, provider), "an unplaced consumer dials the container")
	require.Equal(t, 5432, EffectivePort(&b, ServiceData{Port: 5432, ServerID: &a}), "no exposure yet: the container port")
	require.Equal(t, 5432, EffectivePort(&b, ServiceData{Port: 5432, MeshPort: 41000}), "an unplaced provider is never remote")
	require.True(t, remoteOf(&b, &provider))
	require.False(t, remoteOf(&a, &provider))
	require.False(t, remoteOf(nil, &provider))
}
