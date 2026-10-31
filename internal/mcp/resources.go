package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/webui"
)

// Resource URIs.
const (
	resOverview      = "nebula://overview"
	resServicePrefix = "nebula://service/"
	resDeployPrefix  = "nebula://deployment/"
)

// resConfigSchema is the JSON Schema of nebula.toml (application/schema+json).
const resConfigSchema = "nebula://schema/nebula.toml"

const schemaMIME = "application/schema+json"

const jsonMIME = "application/json"

// addResources registers the read-only resources: the estate overview, one
// service and one deployment. They need projects:read and read through the
// same code paths as the tools (in-process dispatch for services and
// deployments, the get_overview composite for the overview).
func (e *env) addResources(s *mcp.Server, reg *registry, visible map[string]bool) {
	if !holdsAll(e.p, []string{policy.ScopeProjectsRead}) {
		return
	}
	if visible["get_overview"] {
		s.AddResource(&mcp.Resource{
			URI: resOverview, Name: "overview", Title: "Estate overview", MIMEType: jsonMIME,
			Description: "Everything at a glance: projects, services by state, servers, recent failures.",
		}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			rt := reg.byName["get_overview"]
			ctx = e.resourceCtx(ctx)
			out, err := rt.call(ctx, e, nil, json.RawMessage(`{}`))
			if err != nil {
				return nil, e.resourceError(ctx, "resource:overview", req.Params.URI, err)
			}
			return jsonResource(req.Params.URI, out)
		})
	}
	if oe := reg.byOp["getService"]; oe != nil {
		s.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: resServicePrefix + "{id}", Name: "service", Title: "Service", MIMEType: jsonMIME,
			Description: "One service: configuration, staged changes, domains, observed state, latest deployment. {id} is a service id.",
		}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return e.readOp(ctx, req.Params.URI, strings.TrimPrefix(req.Params.URI, resServicePrefix), "serviceId", oe)
		})
	}
	if oe := reg.byOp["getDeployment"]; oe != nil {
		s.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: resDeployPrefix + "{id}", Name: "deployment", Title: "Deployment", MIMEType: jsonMIME,
			Description: "One deployment: status, strategy, trigger and whether it is terminal. {id} is a deployment id.",
		}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return e.readOp(ctx, req.Params.URI, strings.TrimPrefix(req.Params.URI, resDeployPrefix), "deploymentId", oe)
		})
	}
	e.addSchemaResource(s, visible)
}

// addSchemaResource publishes the JSON Schema of nebula.toml next to the tool
// that reads and writes the file (sync_config). The schema holds no instance
// data; its $id carries this instance's origin when a public URL is
// configured.
func (e *env) addSchemaResource(s *mcp.Server, visible map[string]bool) {
	if !visible["sync_config"] {
		return
	}
	s.AddResource(&mcp.Resource{
		URI: resConfigSchema, Name: "nebula.toml schema", Title: "nebula.toml JSON Schema", MIMEType: schemaMIME,
		Description: "JSON Schema of the project file nebula.toml: every key with its description, type, bounds and an example. Read it before writing or editing the file; unknown keys are errors.",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		body := projectconfig.Schema()
		if origin := e.h.deps.Config.PublicURL; origin != "" {
			body = bytes.ReplaceAll(body, []byte(webui.OriginPlaceholder), []byte(origin))
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: schemaMIME, Text: string(body)}}}, nil
	})
}

func (e *env) resourceCtx(ctx context.Context) context.Context {
	ctx = auth.WithPrincipal(ctx, e.p)
	ctx = audit.WithActorType(ctx, audit.ActorMCP)
	ctx = prodguard.WithClient(ctx, clientFrom(ctx, e.ua))
	return ctx
}

func (e *env) readOp(ctx context.Context, uri, id, arg string, oe *opEntry) (*mcp.ReadResourceResult, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	ctx = e.resourceCtx(ctx)
	var proj *catalog.Projection
	if c, ok := oe.entry.(catalog.Curated); ok {
		proj = c.Projection
	}
	out, err := e.callOp(ctx, oe.op, proj, mcpkit.DetailConcise, map[string]any{arg: id})
	if err != nil {
		return nil, e.resourceError(ctx, "resource:"+oe.op.id, uri, err)
	}
	return jsonResource(uri, out)
}

// resourceError maps a failure to the protocol error: a missing or invisible
// resource is "not found" (no oracle), anything else a plain error without
// internals.
func (e *env) resourceError(ctx context.Context, what, uri string, err error) error {
	te := toToolError(ctx, e.h.log, what, err)
	switch te.Code {
	case "not_found", "forbidden":
		return mcp.ResourceNotFoundError(uri)
	}
	return &mcpError{te}
}

type mcpError struct{ te *ToolError }

func (m *mcpError) Error() string { return m.te.Code + ": " + m.te.Message }

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxResultBytes {
		raw = []byte(`{"truncated":true,"message":"the resource exceeded 64 KB; use the tools with a narrower request"}`)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: jsonMIME, Text: string(raw)}}}, nil
}
