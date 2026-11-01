package services

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

func resolver() *Resolver {
	return NewResolver("production", []ServiceData{
		{Slug: "postgres", Name: "Postgres", Port: 5432, Vars: map[string]VarDef{
			"POSTGRES_PASSWORD": {Value: "s3cr3t-pass", Secret: true},
			"POSTGRES_USER":     {Value: "postgres"},
		}},
		{Slug: "api", Name: "API", Port: 8080, FirstDomain: "api.example.com", Vars: map[string]VarDef{
			"DB_HOST": {Value: "${{postgres.NEBULA_PRIVATE_HOST}}"},
			"DB_PORT": {Value: "${{postgres.PORT}}"},
			"DB_URL":  {Value: "postgres://${{postgres.POSTGRES_USER}}:${{ postgres.POSTGRES_PASSWORD }}@${{postgres.NEBULA_PRIVATE_HOST}}:${{postgres.PORT}}/app"},
			"PLAIN":   {Value: "hello"},
		}},
	})
}

func TestResolverExpandsReferencesAndPlatformVars(t *testing.T) {
	res, err := resolver().ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "postgres", res.Vars["DB_HOST"])
	require.Equal(t, "5432", res.Vars["DB_PORT"])
	require.Equal(t, "postgres://postgres:s3cr3t-pass@postgres:5432/app", res.Vars["DB_URL"])
	require.Equal(t, "8080", res.Vars[VarPort])
	require.Equal(t, "api", res.Vars[VarPrivateHost])
	require.Equal(t, "production", res.Vars[VarEnvironment])
	require.Equal(t, "api.example.com", res.Vars[VarPublicDomain])
	require.Equal(t, "API", res.Vars[VarServiceName])
	// A variable derived from a secret is itself secret.
	require.True(t, res.Secret["DB_URL"])
	require.False(t, res.Secret["DB_HOST"])
	require.False(t, res.Secret["PLAIN"])
}

func TestResolverAddsEngineContainerEnv(t *testing.T) {
	db := func(version string) *engines.Spec { return &engines.Spec{Engine: engines.MongoDB, Version: version} }
	r := NewResolver("e", []ServiceData{
		{Slug: "m8", Config: Config{Database: db("8.0")}, Vars: map[string]VarDef{"A": {Value: "1"}}},
		{Slug: "m7", Config: Config{Database: db("7.0")}, Vars: map[string]VarDef{}},
		{Slug: "own", Config: Config{Database: db("8.0")}, Vars: map[string]VarDef{"GLIBC_TUNABLES": {Value: "glibc.malloc.check=1"}}},
		{Slug: "app", Vars: map[string]VarDef{}},
	})
	get := func(slug string) (string, bool) {
		res, err := r.ResolveService(slug)
		require.NoError(t, err)
		v, ok := res.Vars["GLIBC_TUNABLES"]
		return v, ok
	}
	v, ok := get("m8")
	require.True(t, ok)
	require.Equal(t, "glibc.pthread.rseq=1", v)
	_, ok = get("m7")
	require.False(t, ok)
	v, _ = get("own")
	require.Equal(t, "glibc.malloc.check=1", v, "the service's own variable wins")
	_, ok = get("app")
	require.False(t, ok)
}

func TestResolverRejectsUnknownReferences(t *testing.T) {
	r := NewResolver("e", []ServiceData{{Slug: "a", Vars: map[string]VarDef{
		"X": {Value: "${{nope.KEY}}"},
	}}, {Slug: "b", Vars: map[string]VarDef{
		"Y": {Value: "${{a.MISSING}}"},
	}}})
	for _, slug := range []string{"a", "b"} {
		_, err := r.ResolveService(slug)
		ae, ok := apierr.As(err)
		require.True(t, ok, slug)
		require.Equal(t, "unresolved_reference", ae.Code)
	}
}

func TestResolverRejectsReferenceCycle(t *testing.T) {
	r := NewResolver("e", []ServiceData{
		{Slug: "a", Vars: map[string]VarDef{"X": {Value: "${{b.Y}}"}}},
		{Slug: "b", Vars: map[string]VarDef{"Y": {Value: "prefix-${{a.X}}"}}},
		{Slug: "self", Vars: map[string]VarDef{"Z": {Value: "${{self.Z}}"}}},
	})
	for _, slug := range []string{"a", "self"} {
		_, err := r.ResolveService(slug)
		ae, ok := apierr.As(err)
		require.True(t, ok, slug)
		require.Equal(t, "reference_cycle", ae.Code)
	}
}

func TestResolverDoesNotCrossEnvironments(t *testing.T) {
	// Only services of the resolver's environment are known to it.
	r := NewResolver("staging", []ServiceData{{Slug: "web", Vars: map[string]VarDef{"U": {Value: "${{db.NEBULA_PRIVATE_HOST}}"}}}})
	_, err := r.ResolveService("web")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "unresolved_reference", ae.Code)
}

func v2Resolver() *Resolver {
	return NewResolverCtx(ResolverContext{
		ProjectName: "Shop", EnvSlug: "prod", EnvName: "Production",
		Shared: map[string]SharedDef{
			"BASE":   {Value: "https://${{shared.HOST}}", Layer: "project"},
			"HOST":   {Value: "example.com", Layer: "environment"},
			"SECRET": {Value: "sekret-canary-12345", Secret: true, Layer: "project"},
		},
	}, []ServiceData{
		{Slug: "db", Name: "DB", Port: 5432, Vars: map[string]VarDef{
			"PW": {Value: "p@ss/w:rd#1", Secret: true}, "USER": {Value: "app"},
		}},
		{Slug: "api", Name: "API", Port: 8080, FirstDomain: "api.example.com", Vars: map[string]VarDef{
			"A":        {Value: "${{B}}-x"},
			"B":        {Value: "b"},
			"URL":      {Value: "postgres://${{db.USER}}:${{urlencode(db.PW)}}@${{db.NEBULA_PRIVATE_HOST}}/app"},
			"AUTH":     {Value: "${{base64(AUTH_RAW)}}"},
			"AUTH_RAW": {Value: "app:${{db.PW}}"},
			"S":        {Value: "${{shared.SECRET}}"},
			"BASE":     {Value: "${{shared.BASE}}/v1"},
			"LIT":      {Value: "$${{not.a.ref}} and ${{ PORT }}"},
			"PUB":      {Value: "${{NEBULA_PUBLIC_URL}} ${{NEBULA_PROJECT_NAME}} ${{NEBULA_ENVIRONMENT_NAME}}"},
			"BUILDK":   {Value: "v", Scope: ScopeBuild},
		}},
	})
}

func TestResolverV2Grammar(t *testing.T) {
	res, err := v2Resolver().ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "b-x", res.Vars["A"], "bare same-service reference")
	require.Equal(t, "postgres://app:p%40ss%2Fw%3Ard%231@db/app", res.Vars["URL"], "urlencode transform")
	require.Equal(t, "YXBwOnBAc3MvdzpyZCMx", res.Vars["AUTH"])
	require.Equal(t, "https://example.com/v1", res.Vars["BASE"], "shared, env layer overrides at every depth")
	require.Equal(t, "sekret-canary-12345", res.Vars["S"])
	require.Equal(t, "${{not.a.ref}} and 8080", res.Vars["LIT"], "escape and platform reference")
	require.Equal(t, "https://api.example.com Shop Production", res.Vars["PUB"])
	require.Equal(t, map[string]string{"BUILDK": ScopeBuild}, res.Scope)
	// Secret propagation: through refs, transforms and shared.
	for _, k := range []string{"URL", "AUTH", "AUTH_RAW", "S"} {
		require.True(t, res.Secret[k], k)
	}
	require.False(t, res.Secret["A"])
	// Leaves include the bare password and its encodings, so a log line that
	// only shows the password (or its base64 form) is masked.
	require.Contains(t, res.Leaves, "p@ss/w:rd#1")
	require.Contains(t, res.Leaves, "p%40ss%2Fw%3Ard%231")
	red := MakeRedactor(res.Leaves)
	require.NotContains(t, red("password is p@ss/w:rd#1"), "p@ss")
	require.NotContains(t, red("x p%40ss%2Fw%3Ard%231 y"), "p%40ss")
	require.NotContains(t, red("Basic YXBwOnBAc3MvdzpyZCMx"), "YXBwOnBAc3Mv")
}

func TestResolverV2Errors(t *testing.T) {
	r := NewResolverCtx(ResolverContext{EnvSlug: "e", Integrations: map[string]bool{"bao": true}, Shared: map[string]SharedDef{
		"S1": {Value: "${{shared.S2}}"}, "S2": {Value: "${{shared.S1}}"}, "SVC": {Value: "${{a.X}}"}, "BARE": {Value: "${{X}}"},
	}}, []ServiceData{{Slug: "a", Vars: map[string]VarDef{
		"X": {Value: "${{X}}"}, "G": {Value: "${{secret(8)}}"}, "V": {Value: "${{vault.TOKEN}}"}, "VX": {Value: "${{vault:nope/a/b#k}}"}, "M": {Value: "${{shared.NOPE}}"},
		"BAD": {Value: "${{"}, "NP": {Value: "${{NEBULA_DEPLOYMENT_ID}}"},
	}}})
	probs, err := r.Diagnose("a")
	require.NoError(t, err)
	codes := map[string]string{}
	for _, p := range probs {
		codes[p.Key] = p.Code
	}
	require.Equal(t, "reference_cycle", codes["X"], "self cycle through a bare reference")
	require.Equal(t, "ungenerated_function", codes["G"])
	require.Equal(t, "invalid_template", codes["V"], "the old vault.KEY form is a syntax error that names the new form")
	require.Equal(t, "unresolved_reference", codes["VX"], "an unknown secret manager integration is an unresolved reference")
	require.Equal(t, "unresolved_reference", codes["M"])
	require.Equal(t, "invalid_template", codes["BAD"])
	require.Equal(t, "unresolved_reference", codes["NP"], "deployment-scoped variables are not referenceable")
	sp := map[string]string{}
	for _, p := range r.DiagnoseShared() {
		sp[p.Key] = p.Code
	}
	require.Equal(t, "reference_cycle", sp["S1"])
	require.Equal(t, "unresolved_reference", sp["SVC"], "shared values cannot reference services")
	require.Equal(t, "unresolved_reference", sp["BARE"])
}

func TestResolverExports(t *testing.T) {
	RegisterExportProvider(func(in ExportInput) []Export {
		if in.Kind != KindDatabase {
			return nil
		}
		pw, secret, _ := in.Var("PW")
		return []Export{{Key: "DATABASE_URL", Value: "pg://u:" + pw + "@" + in.Host, Secret: secret, Description: "url"}}
	})
	t.Cleanup(func() { RegisterExportProvider(nil) })
	r := NewResolver("e", []ServiceData{
		{Slug: "db", Kind: KindDatabase, Port: 5432, Vars: map[string]VarDef{"PW": {Value: "hunter2-hunter2", Secret: true}}},
		{Slug: "app", Vars: map[string]VarDef{"U": {Value: "${{db.DATABASE_URL}}"}}},
	})
	res, err := r.ResolveService("app")
	require.NoError(t, err)
	require.Equal(t, "pg://u:hunter2-hunter2@db", res.Vars["U"])
	require.True(t, res.Secret["U"], "an export containing a secret is derived-secret")
	require.Contains(t, res.Leaves, "hunter2-hunter2", "the bare password is a leaf")
	require.Equal(t, []ExportInfo{{Key: "DATABASE_URL", Description: "url", Secret: true}}, r.Exports("db"))
	// A user variable with the same key wins over an export.
	r2 := NewResolver("e", []ServiceData{{Slug: "db", Kind: KindDatabase, Vars: map[string]VarDef{"PW": {Value: "x"}, "DATABASE_URL": {Value: "mine"}}},
		{Slug: "app", Vars: map[string]VarDef{"U": {Value: "${{db.DATABASE_URL}}"}}}})
	res, err = r2.ResolveService("app")
	require.NoError(t, err)
	require.Equal(t, "mine", res.Vars["U"])
}

func TestReservedKeys(t *testing.T) {
	for _, k := range []string{"PORT", "NEBULA_FUTURE", "NEBULA_PUBLIC_URL"} {
		require.True(t, IsPlatformKey(k), k)
	}
	require.False(t, IsPlatformKey("NEBULAX"))
	require.True(t, IsReservedSlug("shared"))
	require.True(t, IsReservedSlug("vault"))
}
