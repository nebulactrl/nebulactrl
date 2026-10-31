package mcp_test

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestTemplateTools(t *testing.T) {
	h := newHarness(t, true)
	f := h.service("tpl")
	ro := session(t, h.srv.URL, h.token([]string{"projects:read"}, nil))

	res := call(t, ro, "list_templates", map[string]any{"query": "n8n"})
	require.False(t, res.IsError, text(res))
	items := structured(t, res)["items"].([]any)
	require.NotEmpty(t, items)
	require.Equal(t, "source-available", items[0].(map[string]any)["licenseClass"])

	res = call(t, ro, "plan_template", map[string]any{"environmentId": f.projectSlug + "/production", "templateId": "umami", "serverId": f.serverID})
	require.False(t, res.IsError, text(res))
	require.Contains(t, structured(t, res), "services")
	require.NotContains(t, text(res), "APP_SECRET=", "secret values are never part of a plan")

	// A read-only token does not see install_template, and cannot call it.
	require.NotContains(t, toolNames(t, ro), "install_template")
	_, err := ro.CallTool(context.Background(), &sdk.CallToolParams{Name: "install_template", Arguments: map[string]any{
		"environmentId": f.envID, "templateId": "umami", "serverId": f.serverID,
	}})
	require.Error(t, err)

	// A writer installs staged services; deploy:true additionally needs deployments:write.
	rw := session(t, h.srv.URL, h.token([]string{"projects:read", "projects:write"}, nil))
	res = call(t, rw, "install_template", map[string]any{"environmentId": f.envID, "templateId": "umami", "serverId": f.serverID, "deploy": true})
	require.Equal(t, "forbidden", errCode(t, res))
	res = call(t, rw, "install_template", map[string]any{"environmentId": f.envID, "templateId": "umami", "serverId": f.serverID})
	require.False(t, res.IsError, text(res))
	require.Len(t, structured(t, res)["services"], 2)
}
