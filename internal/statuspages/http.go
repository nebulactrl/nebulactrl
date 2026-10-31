package statuspages

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/dnscheck"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type pageIDInput struct {
	ID uuid.UUID `path:"pageId"`
}

type dnsCheckInput struct {
	ID    uuid.UUID `path:"pageId"`
	Fresh bool      `query:"fresh" doc:"Skip the 60-second cache of lookups: a Recheck"`
}

type incidentIDInput struct {
	ID uuid.UUID `path:"incidentId"`
}

type windowIDInput struct {
	ID uuid.UUID `path:"windowId"`
}

type pageOutput struct{ Body StatusPageView }

type pageListOutput struct {
	Body struct {
		Items []StatusPageView `json:"items"`
	}
}

type createPageInput struct {
	Body struct {
		Title         string     `json:"title" minLength:"1" maxLength:"100" doc:"Shown as the page's heading"`
		Slug          string     `json:"slug,omitempty" maxLength:"50" doc:"Unique in the workspace: lowercase letters, digits and hyphens. Default: derived from the title"`
		Description   string     `json:"description,omitempty" maxLength:"500"`
		Footer        string     `json:"footer,omitempty" maxLength:"500" doc:"Plain text shown under the public page; line breaks kept"`
		Hostname      string     `json:"hostname,omitempty" maxLength:"253" doc:"The public DNS name the page is served on, e.g. status.example.com. Lowercased; unique; not the dashboard's host or a service domain"`
		ShowUptime    *bool      `json:"showUptime,omitempty" doc:"Show availability percentages (default true)"`
		Theme         string     `json:"theme,omitempty" enum:"auto,light,dark" doc:"Default auto: the visitor's preference"`
		FromProjectID *uuid.UUID `json:"fromProjectId,omitempty" doc:"Start from a project: the page gets that project's production-environment services that have at least one public domain as components (display name = service name, no group, in name order). Editable afterwards. 422 unknown_project when the project is not in the workspace"`
	}
}

type updatePageInput struct {
	ID   uuid.UUID `path:"pageId"`
	Body struct {
		Title       *string `json:"title,omitempty" minLength:"1" maxLength:"100"`
		Slug        *string `json:"slug,omitempty" maxLength:"50"`
		Description *string `json:"description,omitempty" maxLength:"500"`
		Footer      *string `json:"footer,omitempty" maxLength:"500" doc:"Plain text shown under the public page; line breaks kept"`
		Hostname    *string `json:"hostname,omitempty" maxLength:"253" doc:"Empty removes the hostname (the page must not be published)"`
		Published   *bool   `json:"published,omitempty" doc:"true serves the page on its hostname; needs a hostname and at least one component"`
		ShowUptime  *bool   `json:"showUptime,omitempty"`
		Theme       *string `json:"theme,omitempty" enum:"auto,light,dark"`
	}
}

type componentBody struct {
	ServiceID   uuid.UUID `json:"serviceId"`
	DisplayName string    `json:"displayName,omitempty" maxLength:"60" doc:"The public name. Default: the service's name"`
	GroupName   string    `json:"groupName,omitempty" maxLength:"60" doc:"Components with the same group name are shown together; empty = ungrouped"`
	URL         string    `json:"url,omitempty" maxLength:"300" doc:"Optional link the component's name points to on the public page: an absolute https URL"`
}

type setComponentsInput struct {
	ID   uuid.UUID `path:"pageId"`
	Body struct {
		Components []componentBody `json:"components" maxItems:"100" doc:"The full ordered list: it replaces the page's components"`
	}
}

type dnsOutput struct{ Body StatusPageDNSCheck }

type incidentOutput struct{ Body StatusIncidentView }

type incidentPageOutput struct{ Body api.Page[StatusIncidentView] }

type listIncidentsInput struct {
	Status string `query:"status" enum:"active,investigating,identified,monitoring,resolved" doc:"Only incidents in this status; active = not resolved"`
	PageID string `query:"pageId" format:"uuid" doc:"Only incidents posted to this status page"`
	api.PageQuery
}

type createIncidentInput struct {
	Body struct {
		Title      string      `json:"title" minLength:"1" maxLength:"120"`
		Impact     string      `json:"impact" enum:"none,minor,major,critical" doc:"How the page's overall state changes while the incident is active: critical = major outage, major = partial outage, minor = degraded, none = nothing"`
		Status     string      `json:"status,omitempty" enum:"investigating,identified,monitoring,resolved" doc:"Default investigating"`
		Body       string      `json:"body" minLength:"1" maxLength:"4000" doc:"The first update of the timeline. Plain text, line breaks kept"`
		PageIDs    []uuid.UUID `json:"pageIds,omitempty" maxItems:"50" doc:"The status pages to post it to"`
		ServiceIDs []uuid.UUID `json:"serviceIds,omitempty" maxItems:"100" doc:"The affected services"`
	}
}

type updateIncidentInput struct {
	ID   uuid.UUID `path:"incidentId"`
	Body struct {
		Title      *string     `json:"title,omitempty" minLength:"1" maxLength:"120"`
		Impact     *string     `json:"impact,omitempty" enum:"none,minor,major,critical"`
		PageIDs    []uuid.UUID `json:"pageIds,omitempty" maxItems:"50" doc:"Replaces the pages; [] withdraws the incident from every page"`
		ServiceIDs []uuid.UUID `json:"serviceIds,omitempty" maxItems:"100" doc:"Replaces the affected services"`
	}
}

type postIncidentUpdateInput struct {
	ID   uuid.UUID `path:"incidentId"`
	Body struct {
		Status string `json:"status" enum:"investigating,identified,monitoring,resolved"`
		Body   string `json:"body" minLength:"1" maxLength:"4000" doc:"Plain text, line breaks kept"`
	}
}

type maintenanceOutput struct{ Body MaintenanceWindowView }

type maintenancePageOutput struct {
	Body api.Page[MaintenanceWindowView]
}

type listMaintenanceInput struct {
	Phase  string `query:"phase" enum:"upcoming,active,past" doc:"upcoming: not started; active: running; past: ended or canceled"`
	PageID string `query:"pageId" format:"uuid" doc:"Only windows posted to this status page"`
	api.PageQuery
}

type createMaintenanceInput struct {
	Body struct {
		Title       string      `json:"title" minLength:"1" maxLength:"120"`
		Description string      `json:"description,omitempty" maxLength:"2000"`
		StartsAt    time.Time   `json:"startsAt"`
		EndsAt      time.Time   `json:"endsAt" doc:"After startsAt and in the future"`
		ServiceIDs  []uuid.UUID `json:"serviceIds,omitempty" maxItems:"100" doc:"The services in maintenance while the window is active"`
		PageIDs     []uuid.UUID `json:"pageIds,omitempty" maxItems:"50" doc:"The status pages that announce it"`
	}
}

type updateMaintenanceInput struct {
	ID   uuid.UUID `path:"windowId"`
	Body struct {
		Title       *string     `json:"title,omitempty" minLength:"1" maxLength:"120"`
		Description *string     `json:"description,omitempty" maxLength:"2000"`
		StartsAt    *time.Time  `json:"startsAt,omitempty"`
		EndsAt      *time.Time  `json:"endsAt,omitempty"`
		ServiceIDs  []uuid.UUID `json:"serviceIds,omitempty" maxItems:"100" doc:"Replaces the services; [] clears them"`
		PageIDs     []uuid.UUID `json:"pageIds,omitempty" maxItems:"50" doc:"Replaces the pages; [] clears them"`
	}
}

// optionalUUID parses an optional uuid query parameter.
func optionalUUID(name, v string) (*uuid.UUID, error) {
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, apierr.BadRequest("invalid_"+name, name+" must be a UUID")
	}
	return &id, nil
}

// Register attaches the status page, incident and maintenance endpoints.
// Reads need projects:read; writes need the admin scope (workspace owners and
// admins) because they publish statements on the public internet.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	writeErrs := append(append([]int{}, errs...), http.StatusConflict, http.StatusUnprocessableEntity)
	pages, incidents, windows := []string{"status-pages"}, []string{"status-incidents"}, []string{"maintenance-windows"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listStatusPages", Method: http.MethodGet, Path: "/status-pages", Tags: pages,
		Summary:     "List status pages",
		Description: "The workspace's public status pages with their components. Component service, environment and project names are empty for tokens restricted to other projects.",
		Errors:      errs[:2],
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*pageListOutput, error) {
		items, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &pageListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createStatusPage", Method: http.MethodPost, Path: "/status-pages", Tags: pages,
		Summary: "Create a status page", DefaultStatus: http.StatusCreated,
		Description: "Creates an unpublished page. Add components with PUT /status-pages/{pageId}/components (or start from a project's production services with `fromProjectId`), point the hostname's DNS at the control-plane server (GET /status-pages/{pageId}/dns; with a connected Cloudflare zone NebulaCtrl manages the records itself, see GET /status-pages/{pageId}/cloudflare), then publish with PATCH. " +
			"409 `hostname_reserved` for the dashboard's own host, `hostname_conflict` (\"the hostname is not available\") for a hostname that is taken or lies in a Cloudflare zone another team connected: the answer never says which, or whose, and `slug_conflict` for a slug in use. Requires the admin scope.",
		Errors: append(errs[:2:2], http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeAdmin), func(ctx context.Context, in *createPageInput) (*pageOutput, error) {
		v, err := svc.Create(ctx, PageCreate{
			Title: in.Body.Title, Slug: in.Body.Slug, Description: in.Body.Description, Footer: in.Body.Footer, Hostname: in.Body.Hostname,
			ShowUptime: in.Body.ShowUptime, Theme: in.Body.Theme, FromProjectID: in.Body.FromProjectID,
		})
		if err != nil {
			return nil, err
		}
		return &pageOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getStatusPage", Method: http.MethodGet, Path: "/status-pages/{pageId}", Tags: pages,
		Summary: "Get a status page", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *pageIDInput) (*pageOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &pageOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateStatusPage", Method: http.MethodPatch, Path: "/status-pages/{pageId}", Tags: pages,
		Summary: "Update a status page",
		Description: "Omitted fields stay as they are. `published: true` needs a hostname and at least one component (422 `publish_needs_hostname`, `publish_needs_components`). " +
			"Changing the hostname of a published page, or publishing or unpublishing it, re-sends the control-plane server's routes: the page is served (or withdrawn) within moments. Requires the admin scope.",
		Errors: writeErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *updatePageInput) (*pageOutput, error) {
		v, err := svc.Update(ctx, in.ID, PagePatch{
			Title: in.Body.Title, Slug: in.Body.Slug, Description: in.Body.Description, Footer: in.Body.Footer, Hostname: in.Body.Hostname,
			Published: in.Body.Published, ShowUptime: in.Body.ShowUptime, Theme: in.Body.Theme,
		})
		if err != nil {
			return nil, err
		}
		return &pageOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteStatusPage", Method: http.MethodDelete, Path: "/status-pages/{pageId}", Tags: pages,
		Summary: "Delete a status page", DefaultStatus: http.StatusNoContent,
		Description: "Deletes the page and withdraws its route. Incidents and maintenance windows posted to it stay. Requires the admin scope.",
		Errors:      errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *pageIDInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setStatusPageComponents", Method: http.MethodPut, Path: "/status-pages/{pageId}/components", Tags: pages,
		Summary: "Set the components of a status page",
		Description: "Replaces the page's components with the ordered list: the services it shows, each under a public display name (default: the service's name, at most 60 characters) " +
			"and an optional group. Every service must belong to the workspace (422 `unknown_service`). A published page needs at least one component. Requires the admin scope.",
		Errors: writeErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *setComponentsInput) (*pageOutput, error) {
		comps := make([]ComponentInput, 0, len(in.Body.Components))
		for _, c := range in.Body.Components {
			comps = append(comps, ComponentInput{ServiceID: c.ServiceID, DisplayName: c.DisplayName, GroupName: c.GroupName, URL: c.URL})
		}
		v, err := svc.SetComponents(ctx, in.ID, comps)
		if err != nil {
			return nil, err
		}
		return &pageOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkStatusPageDNS", Method: http.MethodGet, Path: "/status-pages/{pageId}/dns", Tags: pages,
		Summary: "Check the DNS of a status page's hostname",
		Description: "The A and AAAA records the hostname needs (the control-plane server's public addresses) and whether the hostname resolves to them right now. " +
			"The hostname is looked up at the zone's authoritative nameservers, so a name that exists is never reported missing because of a cached answer; answers are kept for 60 seconds, and `fresh=true` skips that cache. " +
			"A page whose Cloudflare record is proxied is `ok` when the hostname resolves to Cloudflare's addresses (`proxied: true`), not `mismatch`. " +
			"`cloudflareRecord` says Cloudflare's API confirmed the record exists: a `missing` hostname then only waits for DNS caches. " +
			"422 `no_hostname` while the page has no hostname.",
		Errors: append(errs[:3:3], http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *dnsCheckInput) (*dnsOutput, error) {
		if in.Fresh {
			ctx = dnscheck.Fresh(ctx)
		}
		v, err := svc.DNS(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listStatusIncidents", Method: http.MethodGet, Path: "/status-incidents", Tags: incidents,
		Summary: "List status incidents", Description: "Incidents written by people and posted to status pages, newest first, with their timelines.",
		Errors: append(errs[:2:2], http.StatusBadRequest),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listIncidentsInput) (*incidentPageOutput, error) {
		pageID, err := optionalUUID("page_id", in.PageID)
		if err != nil {
			return nil, err
		}
		page, err := svc.ListIncidents(ctx, IncidentFilter{Status: in.Status, PageID: pageID}, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &incidentPageOutput{Body: page}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createStatusIncident", Method: http.MethodPost, Path: "/status-incidents", Tags: incidents,
		Summary: "Open a status incident", DefaultStatus: http.StatusCreated,
		Description: "Opens an incident whose first timeline entry is `body` and posts it to the pages. Its impact moves the pages' overall state while it is not resolved. Requires the admin scope.",
		Errors:      writeErrs,
	}), policy.ScopeAdmin), func(ctx context.Context, in *createIncidentInput) (*incidentOutput, error) {
		v, err := svc.CreateIncident(ctx, IncidentCreate{
			Title: in.Body.Title, Impact: in.Body.Impact, Status: in.Body.Status, Body: in.Body.Body,
			PageIDs: in.Body.PageIDs, ServiceIDs: in.Body.ServiceIDs,
		})
		if err != nil {
			return nil, err
		}
		return &incidentOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getStatusIncident", Method: http.MethodGet, Path: "/status-incidents/{incidentId}", Tags: incidents,
		Summary: "Get a status incident", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *incidentIDInput) (*incidentOutput, error) {
		v, err := svc.GetIncident(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &incidentOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateStatusIncident", Method: http.MethodPatch, Path: "/status-incidents/{incidentId}", Tags: incidents,
		Summary:     "Update a status incident",
		Description: "Changes the title, impact, pages or services; omitted fields stay. The status and timeline change with POST /status-incidents/{incidentId}/updates. Requires the admin scope.",
		Errors:      writeErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateIncidentInput) (*incidentOutput, error) {
		v, err := svc.UpdateIncident(ctx, in.ID, IncidentPatch{
			Title: in.Body.Title, Impact: in.Body.Impact, PageIDs: in.Body.PageIDs, ServiceIDs: in.Body.ServiceIDs,
		})
		if err != nil {
			return nil, err
		}
		return &incidentOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "postStatusIncidentUpdate", Method: http.MethodPost, Path: "/status-incidents/{incidentId}/updates", Tags: incidents,
		Summary: "Post an incident update", DefaultStatus: http.StatusCreated,
		Description: "Adds a timeline entry and moves the incident to its status. `resolved` sets `resolvedAt` and ends the incident's effect on the pages' overall state; a later update with another status reopens it. Requires the admin scope.",
		Errors:      writeErrs,
	}), policy.ScopeAdmin), func(ctx context.Context, in *postIncidentUpdateInput) (*incidentOutput, error) {
		v, err := svc.PostIncidentUpdate(ctx, in.ID, IncidentUpdateInput{Status: in.Body.Status, Body: in.Body.Body})
		if err != nil {
			return nil, err
		}
		return &incidentOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteStatusIncident", Method: http.MethodDelete, Path: "/status-incidents/{incidentId}", Tags: incidents,
		Summary: "Delete a status incident", DefaultStatus: http.StatusNoContent,
		Description: "Deletes the incident and its timeline; it disappears from the pages. Requires the admin scope.",
		Errors:      errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *incidentIDInput) (*struct{}, error) {
		return nil, svc.DeleteIncident(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listMaintenanceWindows", Method: http.MethodGet, Path: "/maintenance-windows", Tags: windows,
		Summary: "List maintenance windows", Description: "Scheduled maintenance, latest start first.",
		Errors: append(errs[:2:2], http.StatusBadRequest, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listMaintenanceInput) (*maintenancePageOutput, error) {
		pageID, err := optionalUUID("page_id", in.PageID)
		if err != nil {
			return nil, err
		}
		page, err := svc.ListMaintenance(ctx, MaintenanceFilter{Phase: in.Phase, PageID: pageID}, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &maintenancePageOutput{Body: page}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createMaintenanceWindow", Method: http.MethodPost, Path: "/maintenance-windows", Tags: windows,
		Summary: "Schedule a maintenance window", DefaultStatus: http.StatusCreated,
		Description: "While the window is active its services are in maintenance: no uptime alerts, and their checks are left out of the availability. The pages it is posted to announce it from 14 days ahead. " +
			"422 `invalid_window` unless endsAt is after startsAt, `window_in_past` when it ends in the past. Requires the admin scope.",
		Errors: writeErrs,
	}), policy.ScopeAdmin), func(ctx context.Context, in *createMaintenanceInput) (*maintenanceOutput, error) {
		v, err := svc.CreateMaintenance(ctx, MaintenanceCreate{
			Title: in.Body.Title, Description: in.Body.Description, StartsAt: in.Body.StartsAt, EndsAt: in.Body.EndsAt,
			ServiceIDs: in.Body.ServiceIDs, PageIDs: in.Body.PageIDs,
		})
		if err != nil {
			return nil, err
		}
		return &maintenanceOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getMaintenanceWindow", Method: http.MethodGet, Path: "/maintenance-windows/{windowId}", Tags: windows,
		Summary: "Get a maintenance window", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *windowIDInput) (*maintenanceOutput, error) {
		v, err := svc.GetMaintenance(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &maintenanceOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateMaintenanceWindow", Method: http.MethodPatch, Path: "/maintenance-windows/{windowId}", Tags: windows,
		Summary:     "Update a maintenance window",
		Description: "Omitted fields stay. 409 `window_ended` / `window_canceled` for a window that is over. Requires the admin scope.",
		Errors:      writeErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateMaintenanceInput) (*maintenanceOutput, error) {
		v, err := svc.UpdateMaintenance(ctx, in.ID, MaintenancePatch{
			Title: in.Body.Title, Description: in.Body.Description, StartsAt: in.Body.StartsAt, EndsAt: in.Body.EndsAt,
			ServiceIDs: in.Body.ServiceIDs, PageIDs: in.Body.PageIDs,
		})
		if err != nil {
			return nil, err
		}
		return &maintenanceOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "cancelMaintenanceWindow", Method: http.MethodPost, Path: "/maintenance-windows/{windowId}/cancel", Tags: windows,
		Summary:     "Cancel a maintenance window",
		Description: "Ends the window now (sets `canceledAt`): its services leave maintenance and the pages stop announcing it. Canceling a canceled window changes nothing; 409 `window_ended` for one that has already ended. Requires the admin scope.",
		Errors:      writeErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *windowIDInput) (*maintenanceOutput, error) {
		v, err := svc.CancelMaintenance(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &maintenanceOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteMaintenanceWindow", Method: http.MethodDelete, Path: "/maintenance-windows/{windowId}", Tags: windows,
		Summary: "Delete a maintenance window", DefaultStatus: http.StatusNoContent,
		Description: "Deletes the window. Requires the admin scope.", Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *windowIDInput) (*struct{}, error) {
		return nil, svc.DeleteMaintenance(ctx, in.ID)
	})
}
