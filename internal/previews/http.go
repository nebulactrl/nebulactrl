package previews

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// List returns the previews of a project, newest first.
func (s *Service) List(ctx context.Context, projectID uuid.UUID) ([]PreviewView, error) {
	if _, err := s.authorizeProject(ctx, policy.ScopeProjectsRead, projectID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListProjectPreviews(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list previews: %w", err)
	}
	out := make([]PreviewView, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) view(ctx context.Context, r db.Preview) (PreviewView, error) {
	urls, err := s.previewURLs(ctx, r.EnvironmentID)
	if err != nil {
		return PreviewView{}, fmt.Errorf("list preview urls: %w", err)
	}
	return PreviewView{
		ID: r.ID, ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID, EnvironmentSlug: s.envSlug(ctx, r.EnvironmentID),
		Status: r.Status, StatusMessage: r.StatusMessage, Provider: r.Provider, Repo: r.Repo, PRNumber: int(r.PrNumber),
		PRTitle: r.PrTitle, PRAuthor: r.PrAuthor, PRURL: r.PrUrl, HeadBranch: r.HeadBranch, HeadSHA: r.HeadSha,
		FromFork: r.FromFork, URLs: urls, CreatedAt: r.CreatedAt, LastPushAt: r.LastPushAt, PausedAt: r.PausedAt,
	}, nil
}

// Delete starts the teardown of a preview (202): its services, containers,
// volumes and domains are removed, then the environment.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) (PreviewView, error) {
	pv, err := s.q.GetPreview(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return PreviewView{}, apierr.NotFound("preview")
	}
	if err != nil {
		return PreviewView{}, fmt.Errorf("get preview: %w", err)
	}
	if _, err := s.authorizeProject(ctx, policy.ScopeProjectsWrite, pv.ProjectID); err != nil {
		return PreviewView{}, err
	}
	if err := s.beginDelete(ctx, id, "manual"); err != nil {
		return PreviewView{}, fmt.Errorf("delete preview: %w", err)
	}
	fresh, err := s.q.GetPreview(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		fresh = pv
		fresh.Status = StatusDeleting
	} else if err != nil {
		return PreviewView{}, err
	}
	return s.view(ctx, fresh)
}

type projectIDInput struct {
	ProjectID uuid.UUID `path:"projectId"`
}

type listOutput struct {
	Body struct {
		Items []PreviewView `json:"items"`
	}
}

type settingsOutput struct{ Body PreviewSettings }

type putSettingsInput struct {
	ProjectID uuid.UUID `path:"projectId"`
	Body      struct {
		Enabled           bool                 `json:"enabled,omitempty"`
		BaseEnvironmentID *uuid.UUID           `json:"baseEnvironmentId,omitempty"`
		ForkPRs           bool                 `json:"forkPrs,omitempty"`
		MaxConcurrent     int                  `json:"maxConcurrent,omitempty" minimum:"0" maximum:"50"`
		IdleDays          int                  `json:"idleDays,omitempty" minimum:"0" maximum:"365"`
		SeedCommands      []PreviewSeedCommand `json:"seedCommands,omitempty" maxItems:"20"`
		ExcludedServices  []string             `json:"excludedServices,omitempty" maxItems:"100"`
	}
}

type previewIDInput struct {
	ID uuid.UUID `path:"previewId"`
}

type previewOutput struct{ Body PreviewView }

// Register attaches the preview endpoints.
func Register(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getPreviewSettings", Method: http.MethodGet, Path: "/projects/{projectId}/previews/settings", Tags: []string{"previews"},
		Summary: "Get a project's pull-request preview settings",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectIDInput) (*settingsOutput, error) {
		st, err := svc.GetSettings(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updatePreviewSettings", Method: http.MethodPut, Path: "/projects/{projectId}/previews/settings", Tags: []string{"previews"},
		Summary: "Update a project's pull-request preview settings",
		Description: "Previews clone the base environment for every open pull request of a connected repository. " +
			"Fork pull requests are off by default; a fork preview never receives secrets.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *putSettingsInput) (*settingsOutput, error) {
		st, err := svc.PutSettings(ctx, in.ProjectID, PreviewSettings{
			Enabled: in.Body.Enabled, BaseEnvironmentID: in.Body.BaseEnvironmentID, ForkPRs: in.Body.ForkPRs,
			MaxConcurrent: in.Body.MaxConcurrent, IdleDays: in.Body.IdleDays, SeedCommands: in.Body.SeedCommands,
			ExcludedServices: in.Body.ExcludedServices,
		})
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listPreviews", Method: http.MethodGet, Path: "/projects/{projectId}/previews", Tags: []string{"previews"},
		Summary: "List a project's pull-request previews",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectIDInput) (*listOutput, error) {
		items, err := svc.List(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		out := &listOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deletePreview", Method: http.MethodDelete, Path: "/previews/{previewId}", Tags: []string{"previews"},
		Summary:       "Delete a pull-request preview",
		Description:   "Removes the preview's services, containers, volumes and domains, then its environment. Runs asynchronously.",
		DefaultStatus: http.StatusAccepted,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *previewIDInput) (*previewOutput, error) {
		v, err := svc.Delete(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &previewOutput{Body: v}, nil
	})
}
