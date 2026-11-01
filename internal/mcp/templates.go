package mcp

import (
	"context"
	"encoding/json"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/templates"
)

type listTemplatesInput struct {
	Query    string `json:"query,omitempty" jsonschema:"search words (name, summary, tags, licence, database engines)"`
	Category string `json:"category,omitempty" jsonschema:"homelab, media, storage, games, devtools, ai, data, web, analytics, automation, productivity or security"`
	License  string `json:"license,omitempty" jsonschema:"licence class: permissive, copyleft, source-available or proprietary"`
}

type planTemplateInput struct {
	EnvironmentID string            `json:"environmentId" jsonschema:"the environment: uuid or project/env"`
	TemplateID    string            `json:"templateId" jsonschema:"template id from list_templates"`
	ServerID      string            `json:"serverId,omitempty" jsonschema:"server id (uuid) to run preflight checks against"`
	Inputs        map[string]string `json:"inputs,omitempty" jsonschema:"install input values by key (see the plan's inputs)"`
}

type installTemplateInput struct {
	EnvironmentID string            `json:"environmentId" jsonschema:"the environment: uuid or project/env"`
	TemplateID    string            `json:"templateId" jsonschema:"template id from list_templates"`
	ServerID      string            `json:"serverId" jsonschema:"server id (uuid) the stack runs on"`
	Inputs        map[string]string `json:"inputs,omitempty" jsonschema:"install input values by key"`
	Deploy        bool              `json:"deploy,omitempty" jsonschema:"deploy the new services right away (needs the deployments:write scope; production environments need approval); default false leaves them staged"`
}

// templateTools are the one-click template tools.
func (h *Handler) templateTools() []mcpkit.Tool {
	d := h.deps
	if d.Templates == nil {
		return nil
	}
	return []mcpkit.Tool{
		{
			Name: "list_templates", Toolset: mcpkit.ToolsetConfig, Hints: mcpkit.Hints{ReadOnly: true},
			Description: "List the one-click app templates (name, summary, category, licence class, number of services). Source-available and proprietary apps are labelled in licenseClass.",
			Scopes:      []string{policy.ScopeProjectsRead},
			Input:       listTemplatesInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[listTemplatesInput](raw)
				if err != nil {
					return nil, err
				}
				return d.Templates.List(ctx, templates.Filter{Query: in.Query, Category: in.Category, LicenseClass: in.License})
			},
		},
		{
			Name: "plan_template", Toolset: mcpkit.ToolsetConfig, Hints: mcpkit.Hints{ReadOnly: true, OpenWorld: true},
			Description: "Preflight installing a template into an environment: the services, volumes and published ports it creates, the secrets it generates (names only) and checks against the chosen server. Changes nothing.",
			Scopes:      []string{policy.ScopeProjectsRead},
			Input:       planTemplateInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[planTemplateInput](raw)
				if err != nil {
					return nil, err
				}
				envID, err := h.resolveEnvironment(ctx, in.EnvironmentID)
				if err != nil {
					return nil, err
				}
				req := templates.TemplatePlanRequest{Inputs: in.Inputs}
				if in.ServerID != "" {
					sid, err := parseID("serverId", in.ServerID)
					if err != nil {
						return nil, err
					}
					req.ServerID = &sid
				}
				return d.Templates.Plan(ctx, envID, in.TemplateID, req)
			},
		},
		{
			Name: "install_template", Toolset: mcpkit.ToolsetConfig, Hints: mcpkit.Hints{OpenWorld: true},
			Description: "Install a template into an environment as staged services (nothing runs until applied, unless deploy=true). Run plan_template first and show the user the services, published ports and licence warnings. Secrets are generated and never returned.",
			Scopes:      []string{policy.ScopeProjectsWrite},
			Input:       installTemplateInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[installTemplateInput](raw)
				if err != nil {
					return nil, err
				}
				envID, err := h.resolveEnvironment(ctx, in.EnvironmentID)
				if err != nil {
					return nil, err
				}
				sid, err := parseID("serverId", in.ServerID)
				if err != nil {
					return nil, err
				}
				return d.Templates.Install(ctx, envID, in.TemplateID, templates.TemplateInstallRequest{ServerID: sid, Inputs: in.Inputs, Deploy: in.Deploy})
			},
		},
	}
}
