package audit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listAuditEventsInput struct {
	api.PageQuery
	ProjectID  string `query:"projectId" format:"uuid" doc:"Only events of this project"`
	ResourceID string `query:"resourceId" maxLength:"255" doc:"Only events about this resource (service, deployment, …)"`
	Type       string `query:"type" maxLength:"100" doc:"Action prefix, e.g. deployment. or variable.set"`
}

type listAuditEventsOutput struct{ Body api.Page[AuditEvent] }

// Register attaches GET /audit-events.
func Register(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listAuditEvents", Method: http.MethodGet, Path: "/audit-events", Tags: []string{"audit"},
		Summary:     "List audit events, newest first",
		Description: "Requires the admin scope (browser sessions hold every scope). Filter by `projectId`, `resourceId` and `type` (an action prefix).",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeAdmin), func(ctx context.Context, in *listAuditEventsInput) (*listAuditEventsOutput, error) {
		var project *uuid.UUID
		if in.ProjectID != "" {
			id, err := uuid.Parse(in.ProjectID)
			if err != nil {
				return nil, apierr.Unprocessable("validation_failed", "projectId must be a UUID")
			}
			project = &id
		}
		items, next, err := svc.List(ctx, Filter{ProjectID: project, ResourceID: in.ResourceID, Type: in.Type}, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &listAuditEventsOutput{Body: api.Page[AuditEvent]{Items: items, NextCursor: next}}, nil
	})
}
