package engines

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	defaults := map[string]int{}
	for _, e := range Catalog() {
		require.False(t, seen[e.ID], "duplicate engine %s", e.ID)
		seen[e.ID] = true
		require.NotEmpty(t, e.Image, e.ID)
		require.Positive(t, e.Port, e.ID)
		require.NotEmpty(t, e.PasswordKey, e.ID)
		require.NotEmpty(t, e.License, e.ID)
		require.NotEmpty(t, e.Versions, e.ID)
		if e.LicenseClass == LicenseSourceAvailable {
			require.NotEmpty(t, e.LicenseNote, "%s needs a licence label", e.ID)
		}
		d := 0
		for _, v := range e.Versions {
			if v.Default {
				d++
			}
			require.NotEmpty(t, v.Tag)
		}
		require.Equal(t, 1, d, "%s needs exactly one default version", e.ID)
		if e.Default {
			defaults[e.Category]++
		}
		for _, f := range e.Flavors {
			for id := range f.Tags {
				require.NotNil(t, e.Version(id), "%s flavor %s references unknown version %s", e.ID, f.ID, id)
			}
		}
	}
	require.Equal(t, 1, defaults[CategoryKeyValue], "exactly one default key-value engine")
	require.True(t, Lookup(Valkey).Default, "Valkey is the default key-value engine")
	require.True(t, Lookup(MongoDB).OptIn)
	require.Equal(t, LicenseSourceAvailable, Lookup(Redis).LicenseClass)
}

func TestSpecValidateAndImage(t *testing.T) {
	s := Spec{Engine: Postgres}.Normalize()
	require.Equal(t, "18", s.Version)
	require.Equal(t, "app", s.DatabaseName)
	require.Equal(t, "postgres", s.Username)
	require.NoError(t, s.Validate())
	require.Equal(t, "postgres:18", s.Image())

	require.Equal(t, "postgis/postgis:17-3.5", Spec{Engine: Postgres, Version: "17", Flavor: FlavorPostGIS}.Normalize().Image())
	require.Equal(t, "valkey/valkey:9", Spec{Engine: Valkey}.Normalize().Image())

	for name, bad := range map[string]Spec{
		"unknown engine":  {Engine: "oracle"},
		"unknown version": {Engine: Postgres, Version: "9"},
		"bad flavor":      {Engine: MySQL, Version: "8.4", Flavor: "postgis"},
		"bad db name":     {Engine: Postgres, Version: "17", DatabaseName: "a; DROP", Username: "postgres"},
		"kv with user":    {Engine: Valkey, Version: "9", Username: "x"},
	} {
		require.Error(t, bad.Validate(), name)
	}
}

func TestDataMountFollowsPostgres18Layout(t *testing.T) {
	_, p17 := Spec{Engine: Postgres, Version: "17"}.DataMount()
	_, p18 := Spec{Engine: Postgres, Version: "18"}.DataMount()
	require.Equal(t, "/var/lib/postgresql/data", p17)
	require.Equal(t, "/var/lib/postgresql", p18)
}

func TestEnvDefaultsHaveGeneratedPasswords(t *testing.T) {
	for _, e := range Catalog() {
		s := Spec{Engine: e.ID}.Normalize()
		gen := 0
		keys := map[string]bool{}
		for _, v := range s.EnvDefaults() {
			keys[v.Key] = true
			if v.Generate {
				gen++
				require.True(t, v.Secret, v.Key)
			}
		}
		require.GreaterOrEqual(t, gen, 1, e.ID)
		require.True(t, keys[e.PasswordKey], e.ID)
	}
}

func TestExportsResolveConnectionStrings(t *testing.T) {
	vars := map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": "p@ss/word:1", "POSTGRES_DB": "app"}
	v := func(k string) (string, bool) { x, ok := vars[k]; return x, ok }
	ex := toMap(Exports(Spec{Engine: Postgres, Version: "17"}.Normalize(), "pg", 5432, v))
	require.Equal(t, "postgres://postgres:p%40ss%2Fword%3A1@pg:5432/app", ex["DATABASE_URL"].Value)
	require.True(t, ex["DATABASE_URL"].Secret)
	require.Equal(t, "pg", ex["PGHOST"].Value)
	require.False(t, ex["DATABASE_HOST"].Secret)
	require.True(t, ex["DATABASE_PASSWORD"].Secret)
	require.Equal(t, "p@ss/word:1", ex["PGPASSWORD"].Value)

	my := map[string]string{"MYSQL_USER": "app", "MYSQL_PASSWORD": "pw", "MYSQL_DATABASE": "shop"}
	exm := toMap(Exports(Spec{Engine: MySQL, Version: "8.4"}.Normalize(), "db", 3306, func(k string) (string, bool) { x, ok := my[k]; return x, ok }))
	require.Equal(t, "mysql://app:pw@db:3306/shop", exm["DATABASE_URL"].Value)
	require.Equal(t, exm["DATABASE_URL"].Value, exm["MYSQL_URL"].Value)

	kv := map[string]string{"VALKEY_PASSWORD": "s3cret"}
	exk := toMap(Exports(Spec{Engine: Valkey}.Normalize(), "cache", 6379, func(k string) (string, bool) { x, ok := kv[k]; return x, ok }))
	require.Equal(t, "redis://:s3cret@cache:6379", exk["REDIS_URL"].Value)
	require.Equal(t, exk["REDIS_URL"].Value, exk["VALKEY_URL"].Value)
	require.NotContains(t, exk, "DATABASE_URL")

	mg := map[string]string{"MONGO_INITDB_ROOT_USERNAME": "root", "MONGO_INITDB_ROOT_PASSWORD": "x", "MONGO_INITDB_DATABASE": "app"}
	exg := toMap(Exports(Spec{Engine: MongoDB, Version: "8.0"}.Normalize(), "m", 27017, func(k string) (string, bool) { x, ok := mg[k]; return x, ok }))
	require.Equal(t, "mongodb://root:x@m:27017/app?authSource=admin", exg["MONGO_URL"].Value)
}

func TestPrimaryExportIsAnExport(t *testing.T) {
	for _, e := range Catalog() {
		require.Contains(t, ExportKeys(e.ID), PrimaryExport(e.ID), e.ID)
	}
}

func toMap(exs []Export) map[string]Export {
	m := map[string]Export{}
	for _, e := range exs {
		m[e.Key] = e
	}
	return m
}

func TestRotatePlanKeepsSecretsOutOfArgv(t *testing.T) {
	cur := Credentials{User: "postgres", Password: "oldoldoldoldoldold", Database: "app"}
	const nw = "newnewnewnewnewnew123"
	p, err := RotatePlan(Spec{Engine: Postgres, Version: "17"}, cur, nw)
	require.NoError(t, err)
	require.Equal(t, "psql", p.Argv[0])
	require.NotContains(t, strings.Join(p.Argv, " "), nw)
	require.NotContains(t, strings.Join(p.Argv, " "), cur.Password)
	require.NotContains(t, p.Stdin, nw, "psql reads the password with \\getenv")
	require.Contains(t, p.Stdin, "ALTER ROLE")
	require.Contains(t, p.Env, "NEBULA_DB_NEW_PASSWORD="+nw)

	m, err := RotatePlan(Spec{Engine: MySQL, Version: "8.4"}, Credentials{User: "app", RootPassword: "rootrootrootroot12"}, nw)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(m.Argv, " "), nw)
	require.Contains(t, m.Stdin, "ALTER USER 'app'@'%' IDENTIFIED BY '"+nw+"'")
	require.Contains(t, m.Env, "MYSQL_PWD=rootrootrootroot12")

	_, err = RotatePlan(Spec{Engine: Valkey, Version: "9"}, Credentials{}, nw)
	require.ErrorIs(t, err, ErrNoExecRotation)
	_, err = RotatePlan(Spec{Engine: Postgres, Version: "17"}, cur, "short'; DROP")
	require.Error(t, err)
	_, err = RotatePlan(Spec{Engine: Postgres, Version: "17"}, Credentials{User: "x; y"}, nw)
	require.Error(t, err)
}

func TestInfoPlanRejectsUnsafeNames(t *testing.T) {
	for _, engine := range []string{Postgres, MongoDB} {
		_, err := InfoPlan(Spec{Engine: engine}, Credentials{User: "app", Database: "app"})
		require.NoError(t, err, engine)
		for _, c := range []Credentials{{User: "-c evil", Database: "app"}, {User: "app", Database: "app; DROP"}, {User: "app"}} {
			_, err := InfoPlan(Spec{Engine: engine}, c)
			require.Error(t, err, "%s %+v", engine, c)
		}
	}
	_, err := InfoPlan(Spec{Engine: ClickHouse}, Credentials{User: "app"})
	require.NoError(t, err)
	_, err = InfoPlan(Spec{Engine: ClickHouse}, Credentials{User: "--config-file=x"})
	require.Error(t, err)
}

func TestParseInfo(t *testing.T) {
	i, err := ParseInfo(Postgres, "17.6 (Debian 17.6-1.pgdg120+1)|8123456|3\n")
	require.NoError(t, err)
	require.Equal(t, Info{Version: "17.6", SizeBytes: 8123456, Connections: 3}, i)
	i, err = ParseInfo(MySQL, "8.4.13|16384|2\n")
	require.NoError(t, err)
	require.Equal(t, int64(16384), i.SizeBytes)
	i, err = ParseInfo(Valkey, "# Server\r\nvalkey_version:9.0.1\r\nconnected_clients:4\r\nused_memory:1048576\r\n")
	require.NoError(t, err)
	require.Equal(t, Info{Version: "9.0.1", SizeBytes: 1048576, Connections: 4}, i)
	i, err = ParseInfo(MongoDB, "warn\n{\"version\":\"8.0.1\",\"size\":2048,\"conns\":7}\n")
	require.NoError(t, err)
	require.Equal(t, 7, i.Connections)
	_, err = ParseInfo(Postgres, "garbage")
	require.Error(t, err)
}

func TestPlanUpgrade(t *testing.T) {
	pg := Spec{Engine: Postgres, Version: "17"}

	same := PlanUpgrade(pg, "17")
	require.Equal(t, "same", same.Kind)
	require.Equal(t, MethodRedeploy, same.Method)

	up := PlanUpgrade(pg, "18")
	require.Equal(t, "upgrade", up.Kind)
	require.Equal(t, MethodPGUpgrade, up.Method)
	require.True(t, up.Executable)
	require.Equal(t, "tianon/postgres-upgrade:17-to-18", up.HelperImage)
	require.Equal(t, "postgres:18", up.Image)
	require.Empty(t, up.Blockers)
	require.NotEmpty(t, up.Steps)
	require.Contains(t, strings.Join(up.Warnings, " "), "layout")

	down := PlanUpgrade(Spec{Engine: Postgres, Version: "18"}, "16")
	require.Equal(t, "downgrade", down.Kind)
	require.NotEmpty(t, down.Blockers)
	require.False(t, down.Executable)

	gis := PlanUpgrade(Spec{Engine: Postgres, Version: "16", Flavor: FlavorPostGIS}, "17")
	require.Equal(t, MethodDumpRestore, gis.Method)
	require.False(t, gis.Executable)

	require.Equal(t, MethodRedeploy, PlanUpgrade(Spec{Engine: MySQL, Version: "8.4"}, "9.7").Method)
	require.Equal(t, MethodDumpRestore, PlanUpgrade(Spec{Engine: MongoDB, Version: "7.0"}, "8.0").Method)
	require.Equal(t, "invalid", PlanUpgrade(pg, "99").Kind)
}

func TestPGUpgradeLayout(t *testing.T) {
	l := PGUpgradeLayoutFor("17", "18")
	require.Equal(t, "/var/lib/postgresql/old", l.OldData)
	require.Equal(t, "/var/lib/postgresql/new/18/docker", l.NewData)
	l = PGUpgradeLayoutFor("18", "19")
	require.Equal(t, "/var/lib/postgresql/old/18/docker", l.OldData)
}
