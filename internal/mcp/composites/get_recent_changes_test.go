package composites_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

const rcLeak = "leaked-value-98765"

// rcAudit inserts an audit row `age` ago.
func rcAudit(f *fx, project *uuid.UUID, actorType, label, action, resType, resID string, meta map[string]any, age time.Duration) {
	f.t.Helper()
	raw, err := json.Marshal(meta)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(),
		`INSERT INTO audit_events (workspace_id, actor_type, actor_id, actor_label, action, resource_type, resource_id, project_id, metadata, created_at)
		 VALUES ($1, $2, '', $3, $4, $5, $6, $7, $8, now() - make_interval(secs => $9))`,
		f.ws.ID, actorType, label, action, resType, resID, project, raw, age.Seconds())
	require.NoError(f.t, err)
}

func rcEvents(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["events"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func rcFind(m map[string]any, action string) map[string]any {
	for _, e := range rcEvents(m) {
		if e["action"] == action {
			return e
		}
	}
	return nil
}

func TestGetRecentChangesServiceTimeline(t *testing.T) {
	f := newFx(t)
	ctx := f.ctx(f.admin)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	other := f.service(svcOpts{env: f.staging, slug: "other"})
	sel := f.selector(f.staging, "web")
	pid := &f.project.ID

	// A real staged variable edit by the MCP principal (secret + plain value).
	_, err := f.deps.Variables.Put(ctx, web.ID, []variables.VariableInput{
		{Key: "API_KEY", Value: new("sekret-canary-12345"), IsSecret: new(true)},
		{Key: "GREETING", Value: new("plain-value-xyz")},
	})
	require.NoError(t, err)
	// A later plain edit and a failed deployment.
	f.release(web, "traefik/whoami:2", "failed", "pull access denied")

	// Hand-written rows: a client name, value-bearing metadata that must be
	// scrubbed, a removed domain (matched by service slug), another service's
	// row, a row without project, and an old row.
	rcAudit(f, pid, "mcp", "claude-token", "variable.set", "service", web.ID.String(), map[string]any{
		"client": "Claude Code", "keys": []string{"DB_URL"}, "value": rcLeak,
		"changes": []map[string]any{{"key": "DB_URL", "op": "set", "version": 3, "from": rcLeak, "to": rcLeak, "fingerprintTo": "ab12"}},
	}, time.Minute)
	rcAudit(f, pid, "user", "Alice", "domain.remove", "domain", uuid.NewString(), map[string]any{"service": "web", "hostname": "old.example.com"}, 2*time.Minute)
	rcAudit(f, pid, "user", "Alice", "domain.remove", "domain", uuid.NewString(), map[string]any{"service": "other", "hostname": "elsewhere.example.com"}, 2*time.Minute)
	rcAudit(f, pid, "user", "Bob", "service.update", "service", other.ID.String(), map[string]any{"fields": []string{"port"}}, time.Minute)
	rcAudit(f, nil, "user", "Eve", "service.update", "service", web.ID.String(), map[string]any{"fields": []string{"image"}}, time.Minute)
	rcAudit(f, pid, "user", "Old", "service.update", "service", web.ID.String(), map[string]any{"fields": []string{"image"}}, 72*time.Hour)

	for _, detail := range []string{"concise", "full"} {
		m := f.must("get_recent_changes", map[string]any{"service": sel, "detail": detail})
		raw, _ := json.Marshal(m)
		require.Empty(t, sectionErrors(m), string(raw))
		for _, secret := range []string{"sekret-canary-12345", "plain-value-xyz", rcLeak} {
			require.NotContains(t, string(raw), secret, detail)
		}
		require.Equal(t, "service", m["scope"])
		require.Equal(t, sel, m["selector"])

		put := rcEvents(m)
		var keys []string
		for _, e := range put {
			if e["action"] == "variable.set" {
				keys = append(keys, fmt.Sprint(e["what"]))
				require.Equal(t, true, e["stagedEdit"])
				require.Equal(t, "variable", e["category"])
				require.Equal(t, "shop/staging/web", e["resource"])
			}
		}
		require.Len(t, keys, 2, string(raw))
		require.Contains(t, keys, "keys: DB_URL")
		require.Contains(t, keys, "keys: API_KEY, GREETING")

		// Actor type and client name.
		var viaClient, viaService bool
		for _, e := range put {
			if e["actor"] == "claude-token" {
				require.Equal(t, "mcp", e["actorType"])
				require.Equal(t, "Claude Code", e["client"])
				viaClient = true
			}
			if e["actor"] == "mcp-test" && e["action"] == "variable.set" {
				require.Equal(t, "mcp", e["actorType"])
				viaService = true
			}
		}
		require.True(t, viaClient && viaService, string(raw))

		// Deployments: the seeded success and the failure, newest first.
		dep := rcFind(m, "deployment.failed")
		require.NotNil(t, dep, string(raw))
		require.Equal(t, "deployment", dep["kind"])
		require.Equal(t, "traefik/whoami:2", dep["image"])
		require.NotNil(t, rcFind(m, "deployment.succeeded"))

		// Removed domain of this service only; no NULL-project or old rows.
		rm := rcFind(m, "domain.remove")
		require.NotNil(t, rm)
		require.Contains(t, rm["what"], "old.example.com")
		for _, e := range put {
			require.NotEqual(t, "Eve", e["actor"])
			require.NotEqual(t, "Old", e["actor"])
			require.NotEqual(t, "Bob", e["actor"])
			require.NotContains(t, fmt.Sprint(e["what"]), "elsewhere.example.com")
		}
		times := make([]string, 0)
		for _, e := range put {
			times = append(times, e["at"].(string))
		}
		for i := 1; i < len(times); i++ {
			require.GreaterOrEqual(t, times[i-1], times[i], "newest first")
		}
		if detail == "full" {
			md := rcFind(m, "variable.set")["metadata"]
			require.NotNil(t, md)
		} else {
			require.Nil(t, rcFind(m, "variable.set")["metadata"])
		}
	}

	// A wider window brings the old row back.
	m := f.must("get_recent_changes", map[string]any{"service": sel, "since": "7d"})
	var actors []string
	for _, e := range rcEvents(m) {
		actors = append(actors, fmt.Sprint(e["actor"]))
	}
	require.Contains(t, actors, "Old")
	require.NotContains(t, actors, "Eve")
}

func TestGetRecentChangesScopesAndPagination(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	api := f.service(svcOpts{env: f.prod, slug: "api"})
	pid := &f.project.ID
	for i := range 6 {
		rcAudit(f, pid, "user", "Alice", "service.update", "service", web.ID.String(), map[string]any{"fields": []string{fmt.Sprintf("f%d", i)}}, time.Duration(i+1)*time.Minute)
	}
	rcAudit(f, pid, "user", "Alice", "service.update", "service", api.ID.String(), map[string]any{"fields": []string{"prod-only"}}, 10*time.Minute)
	rcAudit(f, pid, "user", "Alice", "environment.update", "environment", f.prod.ID.String(), map[string]any{"name": "production"}, 11*time.Minute)
	rcAudit(f, pid, "user", "Alice", "project.update", "project", f.project.ID.String(), map[string]any{"name": "Shop"}, 12*time.Minute)

	has := func(m map[string]any, what string) bool {
		for _, e := range rcEvents(m) {
			if e["what"] != nil && fmt.Sprint(e["what"]) != "" && strings.Contains(fmt.Sprint(e["what"]), what) {
				return true
			}
		}
		return false
	}

	// Environment scope: only the staging service; project scope: everything.
	m := f.must("get_recent_changes", map[string]any{"environment": "shop/staging"})
	require.Equal(t, "environment", m["scope"])
	require.True(t, has(m, "f0"))
	require.False(t, has(m, "prod-only"))
	m = f.must("get_recent_changes", map[string]any{"project": "shop"})
	require.Equal(t, "project", m["scope"])
	require.True(t, has(m, "prod-only") && has(m, "f0"))
	require.NotNil(t, rcFind(m, "project.update"))
	require.NotNil(t, rcFind(m, "environment.update"))
	m = f.must("get_recent_changes", map[string]any{"project": "shop", "environment": "production"})
	require.Equal(t, "environment", m["scope"])
	require.True(t, has(m, "prod-only"))
	require.False(t, has(m, "f0"))

	// Pagination: pages of 4 reproduce the unpaginated timeline exactly.
	all := f.must("get_recent_changes", map[string]any{"project": "shop", "limit": 100})
	require.Empty(t, all["nextCursor"])
	var want []string
	for _, e := range rcEvents(all) {
		want = append(want, fmt.Sprint(e["at"], e["action"], e["resourceId"]))
	}
	var got []string
	cursor := ""
	for range 10 {
		args := map[string]any{"project": "shop", "limit": 4}
		if cursor != "" {
			args["cursor"] = cursor
		}
		m := f.must("get_recent_changes", args)
		require.LessOrEqual(t, len(rcEvents(m)), 4)
		for _, e := range rcEvents(m) {
			got = append(got, fmt.Sprint(e["at"], e["action"], e["resourceId"]))
		}
		cursor, _ = m["nextCursor"].(string)
		if cursor == "" {
			break
		}
		require.NotEmpty(t, m["nextTools"])
	}
	require.Equal(t, want, got)
	require.Greater(t, len(want), 8)
}

func TestGetRecentChangesPartialAndErrors(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	f.service(svcOpts{env: f.prod, slug: "web"})
	rcAudit(f, &f.project.ID, "user", "Alice", "service.update", "service", web.ID.String(), map[string]any{"fields": []string{"port"}}, time.Minute)

	// The deployments section fails (no deployment service); audit still arrives.
	deps := f.deps
	deps.Deployments = nil
	var tool mcpkit.Tool
	for _, tl := range composites.Tools(deps) {
		if tl.Name == "get_recent_changes" {
			tool = tl
		}
	}
	raw, _ := json.Marshal(map[string]any{"service": f.selector(f.staging, "web")})
	out, err := tool.Call(f.ctx(f.admin), f.admin, raw)
	require.NoError(t, err)
	b, _ := json.Marshal(out)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, []string{"deployments"}, sectionErrors(m))
	require.NotNil(t, rcFind(m, "service.update"), string(b))

	// Argument and selector errors.
	for name, args := range map[string]map[string]any{
		"no selector":  {},
		"two selector": {"service": "shop/staging/web", "project": "shop"},
		"bad since":    {"service": "shop/staging/web", "since": "yesterday"},
		"future":       {"service": "shop/staging/web", "since": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		"bad cursor":   {"service": "shop/staging/web", "cursor": "!!!"},
		"bad detail":   {"service": "shop/staging/web", "detail": "lots"},
		"bad limit":    {"service": "shop/staging/web", "limit": -2},
	} {
		_, err := f.call(f.admin, "get_recent_changes", args)
		require.Equal(t, "invalid_argument", errCode(err), name)
	}
	_, err = f.call(f.admin, "get_recent_changes", map[string]any{"service": "web"})
	require.Equal(t, "ambiguous_selector", errCode(err))
	_, err = f.call(f.admin, "get_recent_changes", map[string]any{"project": "nope"})
	require.Equal(t, "not_found", errCode(err))

	// RFC3339 since works; a project-restricted caller cannot read the project.
	m = f.must("get_recent_changes", map[string]any{"service": "shop/staging/web", "since": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	require.NotNil(t, rcFind(m, "service.update"))
	other := f.principal(policy.AllScopes...)
	other.ProjectIDs = []uuid.UUID{uuid.New()}
	_, err = f.call(other, "get_recent_changes", map[string]any{"project": f.project.ID.String()})
	require.Error(t, err)
	_, err = f.call(other, "get_recent_changes", map[string]any{"service": web.ID.String()})
	require.Error(t, err)
}
