package services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

func dbConfig(engine string) Config {
	return Config{Database: &engines.Spec{Engine: engine}}.WithDefaults()
}

func keysOf(ps []ProposedVariable) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Key
	}
	return out
}

// The recommended set per engine is the connection URL, host and port under
// the engine's own names; user, database name and password are offered but
// never preselected.
func TestRecommendedLinkRefsPerEngine(t *testing.T) {
	cases := []struct {
		engine      string
		recommended []string
		optional    []string
		urlValue    string
	}{
		{engines.Postgres, []string{"DATABASE_URL", "DATABASE_HOST", "DATABASE_PORT"}, []string{"DATABASE_USER", "DATABASE_NAME", "DATABASE_PASSWORD"}, "${{db.DATABASE_URL}}"},
		{engines.MySQL, []string{"DATABASE_URL", "DATABASE_HOST", "DATABASE_PORT"}, []string{"DATABASE_USER", "DATABASE_NAME", "DATABASE_PASSWORD"}, "${{db.DATABASE_URL}}"},
		{engines.MariaDB, []string{"DATABASE_URL", "DATABASE_HOST", "DATABASE_PORT"}, []string{"DATABASE_USER", "DATABASE_NAME", "DATABASE_PASSWORD"}, "${{db.DATABASE_URL}}"},
		{engines.MongoDB, []string{"MONGO_URL", "DATABASE_HOST", "DATABASE_PORT"}, []string{"DATABASE_USER", "DATABASE_NAME", "DATABASE_PASSWORD"}, "${{db.MONGO_URL}}"},
		{engines.ClickHouse, []string{"CLICKHOUSE_URL", "DATABASE_HOST", "DATABASE_PORT"}, []string{"DATABASE_USER", "DATABASE_NAME", "DATABASE_PASSWORD"}, "${{db.CLICKHOUSE_URL}}"},
		{engines.Redis, []string{"REDIS_URL", "REDIS_HOST", "REDIS_PORT"}, []string{"REDIS_PASSWORD"}, "${{db.REDIS_URL}}"},
		{engines.Valkey, []string{"REDIS_URL", "REDIS_HOST", "REDIS_PORT"}, []string{"REDIS_PASSWORD"}, "${{db.REDIS_URL}}"},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			cfg := dbConfig(tc.engine)
			all := LinkRefs("db", KindDatabase, cfg, "")
			var rec, opt []string
			for _, p := range all {
				if p.Recommended {
					rec = append(rec, p.Key)
				} else {
					opt = append(opt, p.Key)
				}
			}
			require.Equal(t, tc.recommended, rec)
			require.Equal(t, tc.optional, opt)
			require.Equal(t, tc.urlValue, all[0].Value, "the URL leads and is one export reference")
			require.True(t, all[0].Secret, "the connection URL carries the password")
			require.Equal(t, "${{db.NEBULA_PRIVATE_HOST}}", all[1].Value)
			require.Equal(t, keysOf(all[:3]), keysOf(RecommendedLinkRefs("db", KindDatabase, cfg, "")))
		})
	}
}

// A prefix renames every key and nothing else: this is what add_database's
// `prefix` input does.
func TestLinkRefsPrefix(t *testing.T) {
	got := RecommendedLinkRefs("billing-db", KindDatabase, dbConfig(engines.Postgres), "BILLING_DB")
	require.Equal(t, []string{"BILLING_DB_URL", "BILLING_DB_HOST", "BILLING_DB_PORT"}, keysOf(got))
	require.Equal(t, "${{billing-db.DATABASE_URL}}", got[0].Value)
}

// A service that is not a managed database offers its private host and port
// (preselected), an internal URL and its public domain.
func TestLinkRefsForServices(t *testing.T) {
	api := LinkRefs("billing-api", KindApp, Config{Port: 8080}, "")
	require.Equal(t, []string{"BILLING_API_HOST", "BILLING_API_PORT", "BILLING_API_URL", "BILLING_API_DOMAIN"}, keysOf(api))
	require.True(t, api[0].Recommended && api[1].Recommended)
	require.False(t, api[2].Recommended || api[3].Recommended)
	require.Equal(t, "http://${{billing-api.NEBULA_PRIVATE_HOST}}:${{billing-api.PORT}}", api[2].Value)
	require.Equal(t, "${{billing-api.NEBULA_PUBLIC_DOMAIN}}", api[3].Value)

	// No port: no PORT and no URL, only the host and the domain.
	worker := LinkRefs("jobs", KindWorker, Config{}, "")
	require.Equal(t, []string{"JOBS_HOST", "JOBS_DOMAIN"}, keysOf(worker))

	// A database kind without an engine spec falls back to the generic rows.
	custom := LinkRefs("vec", KindDatabase, Config{Port: 5432}, "")
	require.Equal(t, "VEC_HOST", custom[0].Key)

	require.Equal(t, []string{"BILLING_API_HOST", "BILLING_API_PORT"}, keysOf(RecommendedLinkRefs("billing-api", KindApp, Config{Port: 8080}, "")))
}

func TestMarkStagedLinks(t *testing.T) {
	api, web, pg := uuid.New(), uuid.New(), uuid.New()
	slugs := map[uuid.UUID]string{api: "api", web: "web", pg: "postgres"}
	link := func(from, to uuid.UUID, keys ...string) LinkView {
		refs := make([]LinkReferenceKey, len(keys))
		for i, k := range keys {
			refs[i] = LinkReferenceKey{Key: k}
		}
		return LinkView{FromServiceID: from, ToServiceID: to, References: refs}
	}
	ref := func(v string) RawVar { return RawVar{Value: v} }

	t.Run("a reference the running release holds is applied", func(t *testing.T) {
		links := []LinkView{link(api, pg, "DATABASE_URL")}
		MarkStagedLinks(links, slugs, nil, map[uuid.UUID]map[string]RawVar{
			api: {"DATABASE_URL": ref("${{postgres.DATABASE_URL}}")},
		})
		require.False(t, links[0].Staged)
		require.False(t, links[0].References[0].Staged)
	})
	t.Run("a new reference is staged", func(t *testing.T) {
		links := []LinkView{link(api, pg, "DATABASE_URL")}
		MarkStagedLinks(links, slugs, nil, map[uuid.UUID]map[string]RawVar{api: {"OTHER": ref("x")}})
		require.True(t, links[0].Staged)
		require.True(t, links[0].References[0].Staged)
	})
	t.Run("a key re-pointed at another service is staged", func(t *testing.T) {
		links := []LinkView{link(api, pg, "DATABASE_URL")}
		MarkStagedLinks(links, slugs, nil, map[uuid.UUID]map[string]RawVar{
			api: {"DATABASE_URL": ref("${{redis.REDIS_URL}}")},
		})
		require.True(t, links[0].Staged)
	})
	t.Run("a never deployed consumer stages everything", func(t *testing.T) {
		links := []LinkView{link(web, api, "API_HOST", "API_PORT")}
		MarkStagedLinks(links, slugs, nil, nil)
		require.True(t, links[0].Staged)
	})
	t.Run("a consumer with nothing staged is applied whatever the release says", func(t *testing.T) {
		links := []LinkView{link(web, api, "API_HOST")}
		MarkStagedLinks(links, slugs, map[uuid.UUID]bool{web: true}, nil)
		require.False(t, links[0].Staged)
	})
	t.Run("one live reference keeps the edge solid", func(t *testing.T) {
		links := []LinkView{link(api, pg, "DATABASE_HOST", "DATABASE_URL")}
		MarkStagedLinks(links, slugs, nil, map[uuid.UUID]map[string]RawVar{
			api: {"DATABASE_HOST": ref("${{postgres.NEBULA_PRIVATE_HOST}}")},
		})
		require.False(t, links[0].Staged)
		require.False(t, links[0].References[0].Staged)
		require.True(t, links[0].References[1].Staged)
	})
	t.Run("a hand-drawn link is never staged", func(t *testing.T) {
		links := []LinkView{{FromServiceID: web, ToServiceID: api, References: []LinkReferenceKey{}}}
		MarkStagedLinks(links, slugs, nil, nil)
		require.False(t, links[0].Staged)
	})
}

// The edges themselves come from References: this pins how a variable's text
// becomes a provider slug, so staged and applied edges are drawn from the
// same parse.
func TestReferencesParseProviders(t *testing.T) {
	require.Equal(t, [][2]string{{"postgres", "DATABASE_URL"}}, References("${{postgres.DATABASE_URL}}"))
	require.Equal(t, [][2]string{{"api", "NEBULA_PRIVATE_HOST"}, {"api", "PORT"}}, References("http://${{api.NEBULA_PRIVATE_HOST}}:${{api.PORT}}"))
	require.Empty(t, References("postgres://localhost/db"))
	require.Empty(t, References("${{shared.TOKEN}}"), "shared references are not edges")
}
