package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

func (h *harness) workspaceID() uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(), "SELECT id FROM workspaces ORDER BY created_at LIMIT 1").Scan(&id))
	return id
}

func (h *harness) newRegistry(name string) string {
	h.t.Helper()
	var id string
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(),
		`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, $2, 'external', 'registry.example.com') RETURNING id::text`, h.workspaceID(), name).Scan(&id))
	return id
}

func (h *harness) auditCount(action string) int {
	h.t.Helper()
	var n int
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1", action).Scan(&n))
	return n
}

func TestServerBuildRoleThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv := h.newOnlineServer("builder", 8*gib, 1*gib)

	// Deployments is the default role.
	r := h.do("GET", "/servers/"+srv, "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	j := r.JSON()
	require.Equal(t, false, j["buildEnabled"])
	require.Equal(t, float64(2), j["buildMaxConcurrent"])
	require.Equal(t, false, j["buildAllowEmulation"])
	require.Equal(t, false, j["buildAllowPreviews"])
	require.Equal(t, true, j["acceptWorkloads"])

	updates := h.auditCount("server.update")

	// Builds: no workloads, builds on. The list shows it too.
	r = h.do("PATCH", "/servers/"+srv, "", map[string]any{
		"buildEnabled": true, "acceptWorkloads": false, "buildMaxConcurrent": 4, "buildAllowEmulation": true, "buildAllowPreviews": true,
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	j = r.JSON()
	require.Equal(t, true, j["buildEnabled"])
	require.Equal(t, float64(4), j["buildMaxConcurrent"])
	require.Equal(t, true, j["buildAllowEmulation"])
	require.Equal(t, true, j["buildAllowPreviews"])
	require.Equal(t, false, j["acceptWorkloads"])
	r = h.do("GET", "/servers", "", nil)
	require.Equal(t, true, r.JSON()["items"].([]any)[0].(map[string]any)["buildEnabled"])
	require.Equal(t, 1, h.auditCount("server.build_role"))
	require.Equal(t, updates+1, h.auditCount("server.update"), "acceptWorkloads keeps its server.update audit entry")

	// A partial change keeps the rest.
	r = h.do("PATCH", "/servers/"+srv, "", map[string]any{"buildMaxConcurrent": 1, "name": "build-box"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	j = r.JSON()
	require.Equal(t, float64(1), j["buildMaxConcurrent"])
	require.Equal(t, true, j["buildEnabled"])
	require.Equal(t, "build-box", j["name"])
	require.Equal(t, 2, h.auditCount("server.build_role"))
	require.Equal(t, updates+2, h.auditCount("server.update"))

	// A change of only the role fields is audited as server.build_role alone.
	r = h.do("PATCH", "/servers/"+srv, "", map[string]any{"buildAllowPreviews": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, 3, h.auditCount("server.build_role"))
	require.Equal(t, updates+2, h.auditCount("server.update"))

	// The cap is 1..16.
	for _, bad := range []int{0, 17} {
		r = h.do("PATCH", "/servers/"+srv, "", map[string]any{"buildMaxConcurrent": bad})
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	}

	// Writing the role needs servers:write; reading the server servers:read.
	ro := h.createToken([]string{"servers:read"}, nil)
	r = h.do("PATCH", "/servers/"+srv, ro, map[string]any{"buildEnabled": false})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	r = h.do("GET", "/servers/"+srv, ro, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["buildEnabled"])
}

func TestTeamBuildSettings(t *testing.T) {
	h := newHarness(t)
	h.setup()
	reg := h.newRegistry("main")

	r := h.do("GET", "/team/build-settings", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "target", r.JSON()["buildLocation"])
	require.Nil(t, r.JSON()["registryId"])

	r = h.do("PUT", "/team/build-settings", "", map[string]any{"buildLocation": "build_server", "registryId": reg})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "build_server", r.JSON()["buildLocation"])
	require.Equal(t, reg, r.JSON()["registryId"])
	r = h.do("GET", "/team/build-settings", "", nil)
	require.Equal(t, reg, r.JSON()["registryId"])
	require.Equal(t, 1, h.auditCount("team.build_settings"))

	// Inherit is not a team value; an unknown registry is refused; omitting the registry clears it.
	r = h.do("PUT", "/team/build-settings", "", map[string]any{"buildLocation": "inherit"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	r = h.do("PUT", "/team/build-settings", "", map[string]any{"buildLocation": "build_server", "registryId": uuid.NewString()})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "registry_not_found", r.JSON()["code"])
	r = h.do("PUT", "/team/build-settings", "", map[string]any{"buildLocation": "target"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Nil(t, r.JSON()["registryId"])

	// Anyone with read access sees it, only an administrator changes it.
	read := h.createToken([]string{"projects:read"}, nil)
	r = h.do("GET", "/team/build-settings", read, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = h.do("PUT", "/team/build-settings", read, map[string]any{"buildLocation": "target"})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	write := h.createToken([]string{"projects:read", "projects:write", "servers:write"}, nil)
	r = h.do("PUT", "/team/build-settings", write, map[string]any{"buildLocation": "target"})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
}

func TestProjectBuildPlacement(t *testing.T) {
	h := newHarness(t)
	h.setup()
	reg := h.newRegistry("main")
	builder := h.newOnlineServer("builder", 8*gib, 1*gib)
	plain := h.newOnlineServer("plain", 8*gib, 1*gib)
	pid := h.createProject("Shop")

	r := h.do("GET", "/projects", "", nil)
	p := r.JSON()["items"].([]any)[0].(map[string]any)
	require.Equal(t, "inherit", p["buildLocation"])
	require.Nil(t, p["buildServerId"])
	require.Nil(t, p["registryId"])

	// The pin must be a build server of the team.
	r = h.do("PATCH", "/projects/"+pid, "", map[string]any{"buildLocation": "build_server", "buildServerId": plain, "registryId": reg})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "not_a_build_server", r.JSON()["code"])
	r = h.do("PATCH", "/servers/"+builder, "", map[string]any{"buildEnabled": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = h.do("PATCH", "/projects/"+pid, "", map[string]any{"buildLocation": "build_server", "buildServerId": builder, "registryId": reg})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "build_server", r.JSON()["buildLocation"])
	require.Equal(t, builder, r.JSON()["buildServerId"])
	require.Equal(t, reg, r.JSON()["registryId"])

	// The placement is replaced as a unit: omitted ids are cleared; name edits leave it alone.
	r = h.do("PATCH", "/projects/"+pid, "", map[string]any{"name": "Shop 2"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "build_server", r.JSON()["buildLocation"])
	require.Equal(t, builder, r.JSON()["buildServerId"])
	r = h.do("PATCH", "/projects/"+pid, "", map[string]any{"buildLocation": "target"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "target", r.JSON()["buildLocation"])
	require.Nil(t, r.JSON()["buildServerId"])
	require.Nil(t, r.JSON()["registryId"])

	// Ids without a location, an unknown location and unknown targets are refused.
	for _, body := range []map[string]any{
		{"registryId": reg},
		{"buildLocation": "everywhere"},
		{"buildLocation": "build_server", "registryId": uuid.NewString()},
		{"buildLocation": "build_server", "buildServerId": uuid.NewString()},
	} {
		r = h.do("PATCH", "/projects/"+pid, "", body)
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "%v: %s", body, r.Raw)
	}
	require.GreaterOrEqual(t, h.auditCount("project.update"), 3)
}

func TestServiceBuildOverrideInConfig(t *testing.T) {
	h := newHarness(t)
	h.setup()
	reg := h.newRegistry("main")
	builder := h.newOnlineServer("builder", 8*gib, 1*gib)
	target := h.newOnlineServer("target", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	r := h.do("PATCH", "/servers/"+builder, "", map[string]any{"buildEnabled": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	create := func(cfg map[string]any) resp {
		return h.do("POST", "/environments/"+env+"/services", "", map[string]any{"name": "api", "sourceType": "git", "serverId": target, "config": cfg})
	}
	git := func(extra map[string]any) map[string]any {
		cfg := map[string]any{"gitUrl": "https://example.com/acme/app.git", "gitBranch": "main", "port": 8080}
		for k, v := range extra {
			cfg[k] = v
		}
		return cfg
	}
	// Validation at save time: unknown location, a server of nowhere, a registry of nowhere.
	require.Equal(t, http.StatusUnprocessableEntity, create(git(map[string]any{"buildLocation": "cloud"})).Status)
	r = create(git(map[string]any{"buildLocation": "build_server", "buildServerId": uuid.NewString()}))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "build_server_not_found", r.JSON()["code"])
	r = create(git(map[string]any{"buildLocation": "build_server", "registryId": uuid.NewString()}))
	require.Equal(t, "registry_not_found", r.JSON()["code"])

	r = create(git(map[string]any{"buildLocation": "build_server", "buildServerId": builder, "registryId": reg}))
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	id := r.JSON()["id"].(string)
	cfg := r.JSON()["config"].(map[string]any)
	require.Equal(t, "build_server", cfg["buildLocation"])
	require.Equal(t, builder, cfg["buildServerId"])
	require.Equal(t, reg, cfg["registryId"])

	// A service without an override reads as inherit.
	r = h.do("POST", "/environments/"+env+"/services", "", map[string]any{"name": "web", "sourceType": "git", "serverId": target, "config": git(nil)})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	require.Equal(t, "inherit", r.JSON()["config"].(map[string]any)["buildLocation"])

	// Changing it is a staged config change like any other.
	g := h.do("GET", "/services/"+id, "", nil)
	gen := int64(g.JSON()["generation"].(float64))
	r = h.do("PATCH", "/services/"+id, "", map[string]any{"config": git(map[string]any{"buildLocation": "target"})}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	cfg = r.JSON()["config"].(map[string]any)
	require.Equal(t, "target", cfg["buildLocation"])
	require.Nil(t, cfg["buildServerId"], "the config is replaced as a whole")
	require.Equal(t, true, r.JSON()["hasStagedChanges"])
}
