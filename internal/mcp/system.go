package mcp

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

// SystemInfo is the response of GET /system.
type SystemInfo struct {
	Version    string `json:"version" doc:"Control-plane version"`
	PublicURL  string `json:"publicUrl" doc:"NEBULACTRL_PUBLIC_URL; empty when not configured (clients fall back to the URL they reached the API on)"`
	MCPEnabled bool   `json:"mcpEnabled" doc:"Whether the MCP endpoint answers"`
	MCPLocked  bool   `json:"mcpLocked" doc:"NEBULACTRL_MCP_ENABLED is set, so it decides and the instance admin cannot switch MCP from the web UI"`
	MCPURL     string `json:"mcpUrl" doc:"MCP endpoint URL: publicUrl + /mcp; empty when publicUrl is not configured"`
}

type systemOutput struct{ Body SystemInfo }

type setMCPInput struct {
	Body struct {
		Enabled bool `json:"enabled" doc:"Turn the MCP endpoint on or off"`
	}
}

// Info describes the running control plane.
func Info(cfg *config.Config, sw *Switch) SystemInfo {
	info := SystemInfo{Version: version.Current(), PublicURL: cfg.PublicURL, MCPEnabled: sw.Enabled(), MCPLocked: sw.Locked()}
	if cfg.PublicURL != "" {
		info.MCPURL = cfg.PublicURL + Path
	}
	return info
}

// RegisterSystem registers GET /api/v1/system (any authenticated principal)
// and PUT /api/v1/system/mcp (the instance admin).
func RegisterSystem(h huma.API, cfg *config.Config, sw *Switch) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if sw == nil {
		sw = NewSwitch(cfg, nil, nil)
	}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getSystem", Method: http.MethodGet, Path: "/system", Tags: []string{"system"},
		Summary:     "Control-plane version, public URL and MCP endpoint",
		Description: "Facts about this control plane, for the web UI's MCP card and clients that need the endpoint URL.",
		Errors:      []int{http.StatusUnauthorized},
	}), func(ctx context.Context, _ *struct{}) (*systemOutput, error) {
		if _, err := policy.Authenticated(ctx); err != nil {
			return nil, err
		}
		return &systemOutput{Body: Info(cfg, sw)}, nil
	})
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setMcpEnabled", Method: http.MethodPut, Path: "/system/mcp", Tags: []string{"system"},
		Summary: "Turn the MCP endpoint on or off",
		Description: "Instance admin only; audited as `instance.mcp.enable` / `instance.mcp.disable`. Takes effect at once. " +
			"409 `mcp_locked` while NEBULACTRL_MCP_ENABLED is set on the control plane: the environment decides then.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict},
	}, policy.ScopeAdmin), func(ctx context.Context, in *setMCPInput) (*systemOutput, error) {
		if err := sw.Set(ctx, in.Body.Enabled); err != nil {
			return nil, err
		}
		return &systemOutput{Body: Info(cfg, sw)}, nil
	})
}
