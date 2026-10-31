package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type accessEvent struct{ action, actorType, meta string }

// accessEvents lists the secret access log in order.
func (h *harness) accessEvents() []accessEvent {
	h.t.Helper()
	rows, err := h.app.Pool.Query(context.Background(),
		"SELECT action, actor_type, metadata::text FROM audit_events WHERE action LIKE 'secret.fetch%' ORDER BY id")
	require.NoError(h.t, err)
	defer rows.Close()
	var out []accessEvent
	for rows.Next() {
		var e accessEvent
		require.NoError(h.t, rows.Scan(&e.action, &e.actorType, &e.meta))
		out = append(out, e)
	}
	require.NoError(h.t, rows.Err())
	return out
}

// An integration limited to selected projects is refused for the others, when
// the variable is saved and again when the command is sent; every read of
// external secrets leaves one audit event with the references, never the value.
func TestIntegrationScopeAndSecretAccessLog(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	fb := newFakeBao(t)
	shop := h.newVarEnv("shop")
	blog := h.newVarEnv("blog")
	shopAPI := shop.service("api", "app", nil)
	blogAPI := blog.service("api", "app", nil)
	integ := h.addBao(fb)
	ref := "${{vault:bao/apps/shop/db#password}}"

	// A new integration is available to the whole team.
	r := h.do("GET", "/secret-managers", "", nil)
	require.Equal(t, "team", r.JSON()["items"].([]any)[0].(map[string]any)["scope"])

	// Limit it to the shop project. A scope needs a grant, and the grants must belong to the team.
	update := func(body map[string]any) resp {
		body["address"] = fb.srv.URL
		return h.do("PUT", "/secret-managers/"+integ, "", body)
	}
	r = update(map[string]any{"scope": "selected"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "grants_required", r.JSON()["code"])
	r = update(map[string]any{"scope": "selected", "grants": []any{map[string]any{"projectId": "00000000-0000-0000-0000-000000000001"}}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "invalid_grant", r.JSON()["code"])
	r = update(map[string]any{"scope": "selected", "grants": []any{map[string]any{"projectId": shop.projectID}}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "selected", r.JSON()["scope"])
	grants := r.JSON()["grants"].([]any)
	require.Len(t, grants, 1)
	require.Equal(t, shop.projectID, grants[0].(map[string]any)["projectId"])
	require.Equal(t, "shop", grants[0].(map[string]any)["projectName"])

	// Saving the reference: fine in shop, an unresolved reference naming it in blog.
	r = h.put(shopAPI, v("PW", ref))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	shopWarnings, _ := r.JSON()["warnings"].([]any)
	for _, w := range shopWarnings {
		require.NotEqual(t, "unresolved_reference", w.(map[string]any)["code"], string(r.Raw))
	}
	r = h.put(blogAPI, v("PW", ref))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var warned bool
	for _, w := range r.JSON()["warnings"].([]any) {
		if m := w.(map[string]any); m["code"] == "unresolved_reference" {
			warned = true
			require.Contains(t, m["message"], ref)
			require.Contains(t, m["message"], `"bao" is not available to this project and environment`)
		}
	}
	require.True(t, warned, string(r.Raw))
	r = h.do("POST", "/services/"+blogAPI+"/deployments", "", map[string]any{})
	require.NotEqual(t, http.StatusAccepted, r.Status, "a deploy cannot start with a reference its project may not use")
	require.Contains(t, string(r.Raw), ref)
	require.Zero(t, fb.reads.Load(), "nothing was read from the manager")

	// Send time: shop's deploy reads the secret and the read is logged.
	r = h.do("POST", "/services/"+shopAPI+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	depID := r.JSON()["id"].(string)
	cmdID, payload := h.deployCommand(shop.srvID)
	rig := h.extRig()
	secrets, err := rig.deps.SecretsForCommand(context.Background(), cmdID, agentprotocol.KindDeploy, payload)
	require.NoError(t, err)
	require.Equal(t, vaultCanary, secrets[agentprotocol.SecretEnvPrefix+"PW"])

	events := h.accessEvents()
	require.Len(t, events, 1)
	require.Equal(t, "secret.fetch", events[0].action)
	require.Equal(t, "system", events[0].actorType)
	require.Contains(t, events[0].meta, `"vault:bao/apps/shop/db#password"`)
	require.Contains(t, events[0].meta, `"service": "api"`)
	require.Contains(t, events[0].meta, `"project": "shop"`)
	require.Contains(t, events[0].meta, `"purpose": "deploy"`)
	require.Contains(t, events[0].meta, depID)
	require.Contains(t, events[0].meta, `"startedBy"`, "who started the deployment")
	require.NotContains(t, events[0].meta, vaultCanary)
	require.NotContains(t, h.dumpDB(), vaultCanary, "no row, audit rows included, holds the value")

	// Narrowing the integration after the variable was saved: the command is not
	// delivered, nothing is read, and the refusal is logged.
	reads := fb.reads.Load()
	r = update(map[string]any{"scope": "selected", "grants": []any{map[string]any{"projectId": blog.projectID}}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	_, err = rig.deps.SecretsForCommand(context.Background(), cmdID, agentprotocol.KindDeploy, payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), ref)
	require.Contains(t, err.Error(), `integration "bao" is not available to project "shop"`)
	require.NotContains(t, err.Error(), vaultCanary)
	require.Equal(t, reads, fb.reads.Load(), "a refused reference never reaches the manager")
	events = h.accessEvents()
	require.Len(t, events, 2)
	require.Equal(t, "secret.fetch.denied", events[1].action)
	require.Contains(t, events[1].meta, `"denied": ["vault:bao/apps/shop/db#password"]`)

	// Back to the whole team, and the scope changes are in the audit log without the credential.
	r = update(map[string]any{"scope": "team"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Empty(t, r.JSON()["grants"])
	_, err = rig.deps.SecretsForCommand(context.Background(), cmdID, agentprotocol.KindDeploy, payload)
	require.NoError(t, err)
	raw := string(h.do("GET", "/audit-events?type=secret", "", nil).Raw)
	require.Contains(t, raw, "secret_manager.update")
	require.Contains(t, raw, "secret.fetch")
	require.NotContains(t, raw, "s.root-token")
	require.NotContains(t, raw, vaultCanary)

	// Only admins change a scope.
	ro := h.createToken([]string{"projects:read"}, nil)
	r = h.do("PUT", "/secret-managers/"+integ, ro, map[string]any{"address": fb.srv.URL, "scope": "team"})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
}
