package mcp

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
)

func parseID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, toolErr(CodeInvalidArgument, "%s must be a UUID", field)
	}
	return id, nil
}

// resolveService returns the id of a service selector (uuid, project/env/
// service or hostname) under the caller's principal.
func (h *Handler) resolveService(ctx context.Context, sel string) (uuid.UUID, error) {
	sel = strings.TrimSpace(sel)
	if id, err := uuid.Parse(sel); err == nil {
		return id, nil
	}
	if h.resolver == nil {
		return uuid.Nil, mcpkit.Errf(mcpkit.CodeInvalidArgument, "serviceId must be a UUID")
	}
	s, err := h.resolver.Service(ctx, sel)
	if err != nil {
		return uuid.Nil, err
	}
	return s.ID, nil
}

// resolveEnvironment returns the id of an environment selector (uuid or
// project/env) under the caller's principal.
func (h *Handler) resolveEnvironment(ctx context.Context, sel string) (uuid.UUID, error) {
	sel = strings.TrimSpace(sel)
	if id, err := uuid.Parse(sel); err == nil {
		return id, nil
	}
	if h.resolver == nil {
		return uuid.Nil, mcpkit.Errf(mcpkit.CodeInvalidArgument, "environmentId must be a UUID")
	}
	e, err := h.resolver.Environment(ctx, sel)
	if err != nil {
		return uuid.Nil, err
	}
	return e.ID, nil
}

// commandAcked reports whether the agent acknowledged the deployment's deploy
// command (so the deployment may have started on the host).
func (e *env) commandAcked(ctx context.Context, deploymentID uuid.UUID) bool {
	if e.h.deps.Pool == nil {
		return false
	}
	var status string
	err := e.h.deps.Pool.QueryRow(ctx,
		`SELECT status FROM agent_commands WHERE deployment_id = $1 AND kind = 'deploy' ORDER BY created_at DESC LIMIT 1`,
		deploymentID).Scan(&status)
	if err != nil {
		return false
	}
	return status == "acked"
}

// nativeTools are the hand-written tools that stay distinct from the generated
// ones: command runs, backups and templates.
func (h *Handler) nativeTools() []mcpkit.Tool {
	var out []mcpkit.Tool
	out = append(out, h.taskTools()...)
	out = append(out, h.backupTools()...)
	out = append(out, h.templateTools()...)
	return out
}

// compositeDeps wires the composite tools to the same services as REST.
func (h *Handler) compositeDeps() composites.Deps {
	d := h.deps
	cd := composites.Deps{
		Pool: d.Pool, Logger: h.log,
		Projects: d.Projects, Environments: d.Environments, Servers: d.Servers, Services: d.Services,
		Deployments: d.Deployments, Variables: d.Variables, Domains: d.Domains, Databases: d.Databases, Audit: d.Audit,
		Resolver: h.resolver,
	}
	if d.ConfigSync != nil { // a nil *Service must not become a non-nil interface
		cd.ConfigSync = d.ConfigSync
	}
	return cd
}
