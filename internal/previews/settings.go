package previews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// authorizeProject checks scope and that the project exists in the caller's
// workspace and project restriction (404 otherwise).
func (s *Service) authorizeProject(ctx context.Context, scope string, projectID uuid.UUID) (db.Project, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Project{}, err
	}
	if err := policy.Require(ctx, scope, &projectID); err != nil {
		return db.Project{}, err
	}
	p, err := s.q.GetProject(ctx, db.GetProjectParams{ID: projectID, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Project{}, apierr.NotFound("")
	}
	if err != nil {
		return db.Project{}, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

func toSettings(r db.PreviewSetting) PreviewSettings {
	st := PreviewSettings{
		ProjectID: r.ProjectID, Enabled: r.Enabled, BaseEnvironmentID: r.BaseEnvironmentID, ForkPRs: r.ForkPrs,
		MaxConcurrent: int(r.MaxConcurrent), IdleDays: int(r.IdleDays), ExcludedServices: r.ExcludedServices,
		SeedCommands: []PreviewSeedCommand{},
	}
	t := r.UpdatedAt
	st.UpdatedAt = &t
	_ = json.Unmarshal(r.SeedCommands, &st.SeedCommands)
	if st.ExcludedServices == nil {
		st.ExcludedServices = []string{}
	}
	return st
}

// GetSettings returns the project's preview settings (defaults while none
// were saved: disabled, the production environment as base).
func (s *Service) GetSettings(ctx context.Context, projectID uuid.UUID) (PreviewSettings, error) {
	if _, err := s.authorizeProject(ctx, policy.ScopeProjectsRead, projectID); err != nil {
		return PreviewSettings{}, err
	}
	row, err := s.q.GetPreviewSettings(ctx, projectID)
	if err == nil {
		return toSettings(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PreviewSettings{}, fmt.Errorf("get preview settings: %w", err)
	}
	st := PreviewSettings{
		ProjectID: projectID, MaxConcurrent: DefaultMaxConcurrent, IdleDays: DefaultIdleDays,
		SeedCommands: []PreviewSeedCommand{}, ExcludedServices: []string{},
	}
	envs, err := s.q.ListEnvironments(ctx, projectID)
	if err != nil {
		return PreviewSettings{}, fmt.Errorf("list environments: %w", err)
	}
	for _, e := range envs {
		if e.Kind == "standard" && e.IsProduction {
			id := e.ID
			st.BaseEnvironmentID = &id
			break
		}
	}
	return st, nil
}

// PutSettings validates and stores the project's preview settings.
func (s *Service) PutSettings(ctx context.Context, projectID uuid.UUID, in PreviewSettings) (PreviewSettings, error) {
	proj, err := s.authorizeProject(ctx, policy.ScopeProjectsWrite, projectID)
	if err != nil {
		return PreviewSettings{}, err
	}
	if in.MaxConcurrent == 0 {
		in.MaxConcurrent = DefaultMaxConcurrent
	}
	if in.IdleDays == 0 {
		in.IdleDays = DefaultIdleDays
	}
	if in.MaxConcurrent < 1 || in.MaxConcurrent > 50 {
		return PreviewSettings{}, apierr.Unprocessable("validation_failed", "maxConcurrent must be between 1 and 50")
	}
	if in.IdleDays < 1 || in.IdleDays > 365 {
		return PreviewSettings{}, apierr.Unprocessable("validation_failed", "idleDays must be between 1 and 365")
	}
	if in.BaseEnvironmentID == nil {
		if in.Enabled {
			return PreviewSettings{}, apierr.Unprocessable("validation_failed", "choose the base environment previews are cloned from")
		}
	} else {
		base, err := s.q.GetEnvironmentScoped(ctx, db.GetEnvironmentScopedParams{ID: *in.BaseEnvironmentID, WorkspaceID: proj.WorkspaceID})
		if err != nil || base.ProjectID != projectID {
			return PreviewSettings{}, apierr.Unprocessable("validation_failed", "the base environment does not belong to this project")
		}
		if base.Kind != "standard" {
			return PreviewSettings{}, apierr.Unprocessable("validation_failed", "a preview environment cannot be the base of previews")
		}
		slugs, err := s.q.ListEnvironmentLiveServiceIDs(ctx, base.ID)
		if err != nil {
			return PreviewSettings{}, fmt.Errorf("list services: %w", err)
		}
		have := map[string]bool{}
		for _, sv := range slugs {
			have[sv.Slug] = true
		}
		excluded := map[string]bool{}
		for _, slug := range in.ExcludedServices {
			if !have[slug] {
				return PreviewSettings{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("excluded service %q does not exist in the base environment", slug))
			}
			excluded[slug] = true
		}
		for i := range in.SeedCommands {
			c := &in.SeedCommands[i]
			c.Command = strings.TrimSpace(c.Command)
			if c.RunOn == "" {
				c.RunOn = RunOnCreate
			}
			if c.RunOn != RunOnCreate && c.RunOn != RunOnPush {
				return PreviewSettings{}, apierr.Unprocessable("validation_failed", "seed command runOn must be create or push")
			}
			if c.Command == "" {
				return PreviewSettings{}, apierr.Unprocessable("validation_failed", "seed command must not be empty")
			}
			if !have[c.Service] {
				return PreviewSettings{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("seed command service %q does not exist in the base environment", c.Service))
			}
			if excluded[c.Service] {
				return PreviewSettings{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("seed command service %q is excluded from previews", c.Service))
			}
		}
	}
	if in.SeedCommands == nil {
		in.SeedCommands = []PreviewSeedCommand{}
	}
	if in.ExcludedServices == nil {
		in.ExcludedServices = []string{}
	}
	seeds, err := json.Marshal(in.SeedCommands)
	if err != nil {
		return PreviewSettings{}, err
	}
	var row db.PreviewSetting
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpsertPreviewSettings(ctx, db.UpsertPreviewSettingsParams{
			ProjectID: projectID, WorkspaceID: proj.WorkspaceID, Enabled: in.Enabled, BaseEnvironmentID: in.BaseEnvironmentID,
			ForkPrs: in.ForkPRs, MaxConcurrent: int32(in.MaxConcurrent), IdleDays: int32(in.IdleDays), SeedCommands: seeds,
			ExcludedServices: in.ExcludedServices,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "preview.settings.update", ResourceType: "project", ResourceID: projectID.String(), ProjectID: &projectID,
			Metadata: map[string]any{
				"enabled": in.Enabled, "forkPrs": in.ForkPRs, "maxConcurrent": in.MaxConcurrent, "idleDays": in.IdleDays,
				"seedCommands": len(in.SeedCommands), "excludedServices": in.ExcludedServices,
			},
		})
	})
	if err != nil {
		return PreviewSettings{}, fmt.Errorf("save preview settings: %w", err)
	}
	return toSettings(row), nil
}
