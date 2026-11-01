package composites

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
)

// ConfigService is the part of *configsync.Service the nebula.toml tool uses.
// Apply goes through the production guard (ApplyGuarded). Validation is part
// of Plan (its diagnostics), so the tool does not call Validate.
type ConfigService interface {
	Plan(ctx context.Context, projectID uuid.UUID, r configsync.PlanRequest) (configsync.Plan, error)
	ApplyGuarded(ctx context.Context, projectID uuid.UUID, r configsync.ApplyRequest) (configsync.ApplyResult, error)
	Export(ctx context.Context, projectID uuid.UUID, env, base string, all, defaults bool) (configsync.ExportResult, error)
}

// maxConfigFileBytes mirrors projectconfig.MaxFileBytes: a longer file is
// refused before it reaches the use case.
const maxConfigFileBytes = 256 << 10

// configInput resolves the project and builds the use-case input of an
// inline file sent by an agent.
func (c *composites) configInput(ctx context.Context, project, file string, attachments map[string]string) (uuid.UUID, configsync.Input, error) {
	if c.ConfigSync == nil {
		return uuid.Nil, configsync.Input{}, mcpkit.Errf(mcpkit.CodeUnavailable, "the nebula.toml tools are not available on this control plane")
	}
	if strings.TrimSpace(project) == "" {
		return uuid.Nil, configsync.Input{}, invalid("project is required (id, slug or name)")
	}
	if strings.TrimSpace(file) == "" {
		return uuid.Nil, configsync.Input{}, invalid("file is required: the nebula.toml text (start from action=export)")
	}
	if len(file) > maxConfigFileBytes {
		return uuid.Nil, configsync.Input{}, invalid("file is %d bytes; the limit is %d", len(file), maxConfigFileBytes)
	}
	prj, err := c.Resolver.Project(ctx, project)
	if err != nil {
		return uuid.Nil, configsync.Input{}, err
	}
	return prj.ID, configsync.Input{
		File: file, Attachments: attachments,
		Source: configsync.SourceInfo{Kind: configsync.SourceInline, Origin: configsync.OriginMCP},
	}, nil
}

// ConfigPlanService is one service of a plan or an apply result, as plain
// strings and numbers.
type ConfigPlanService struct {
	Slug       string                  `json:"slug"`
	Kind       string                  `json:"kind,omitempty" jsonschema:"git, image, database, route or stack (the source key of the service)"`
	Action     string                  `json:"action" jsonschema:"create, update, unchanged, not_in_file (kept, never deleted) or blocked"`
	ServiceID  string                  `json:"serviceId,omitempty"`
	Generation int64                   `json:"generation,omitempty"`
	Managed    bool                    `json:"managed" jsonschema:"true when a file applied to the service before"`
	State      string                  `json:"state,omitempty" jsonschema:"in_sync, file_changed, drift (edited in the dashboard since the last apply), conflict, new or adopt"`
	Note       string                  `json:"note,omitempty"`
	Changes    []configsync.PlanChange `json:"changes" jsonschema:"fields the apply would write; applies says staged (next deploy) or immediate (at once); secret values are masked"`
	Released   []string                `json:"released,omitempty" jsonschema:"file key paths the file stopped listing: no longer owned, live value kept"`
	Unmanaged  map[string]int          `json:"unmanaged,omitempty" jsonschema:"live members the file does not mention (counts only)"`
	Blockers   []string                `json:"blockers,omitempty" jsonschema:"codes of the errors that block this service"`
}

// ConfigSharedPlan holds the changes of the shared variable layers.
type ConfigSharedPlan struct {
	Project     []configsync.PlanChange `json:"project"`
	Environment []configsync.PlanChange `json:"environment"`
}

func planServices(in []configsync.PlanService) []ConfigPlanService {
	out := make([]ConfigPlanService, 0, len(in))
	for _, s := range in {
		id := ""
		if s.ServiceID != nil {
			id = s.ServiceID.String()
		}
		out = append(out, ConfigPlanService{
			Slug: s.Slug, Kind: s.Kind, Action: s.Action, ServiceID: id, Generation: s.Generation, Managed: s.Managed,
			State: s.State, Note: s.Note, Changes: nonNilChanges(s.Changes), Released: s.Released, Unmanaged: s.Unmanaged, Blockers: s.Blockers,
		})
	}
	return out
}

func nonNilChanges(cs []configsync.PlanChange) []configsync.PlanChange {
	if cs == nil {
		return []configsync.PlanChange{}
	}
	return cs
}

func nonNilDiagnostics(ds configsync.Diagnostics) []configsync.Diagnostic {
	if ds == nil {
		return []configsync.Diagnostic{}
	}
	return ds
}

// firstError is the first error diagnostic, rendered like the CLI does.
func firstError(ds configsync.Diagnostics) string {
	for _, d := range ds {
		if d.Severity != "error" {
			continue
		}
		loc := d.Path
		if d.Line > 0 {
			loc = fmt.Sprintf("line %d: %s", d.Line, d.Path)
		}
		return fmt.Sprintf("%s [%s] %s", loc, d.Code, clip(d.Message, 200))
	}
	return ""
}

// configErr maps a use-case error to a tool error. A refused plan or a stale
// plan carries the whole plan in details; the tool error keeps its id, summary
// and errors so the result stays small.
func configErr(err error) error {
	if isApproval(err) {
		return err // the MCP layer turns it into the approval flow
	}
	if ae, ok := apierr.As(err); ok && ae.Status < 500 {
		var plan *configsync.Plan
		switch p := ae.Details["plan"].(type) {
		case configsync.Plan:
			plan = &p
		case *configsync.Plan:
			plan = p
		}
		if plan != nil {
			var errs []configsync.Diagnostic
			for _, d := range plan.Diagnostics {
				if d.Severity == "error" && len(errs) < 20 {
					errs = append(errs, d)
				}
			}
			return &mcpkit.Error{Code: ae.Code, Message: ae.Message, Data: map[string]any{
				"plan": map[string]any{"planId": plan.PlanID, "valid": plan.Valid, "summary": plan.Summary, "errors": errs},
			}}
		}
	}
	return changesErr(err)
}
