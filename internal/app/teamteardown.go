package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/statuspages"
	"github.com/nebulactrl/nebulactrl/internal/teams"
)

// teamTeardown is what deleting a team runs through (teams.Teardown): each
// method calls the domain that owns the thing, as the control plane's own
// administrator of the team.
type teamTeardown struct {
	q          *db.Queries
	projects   *projects.Service
	platform   *platform.Service
	servers    *servers.Service
	pages      *statuspages.Service
	cloudflare *cloudflare.Service
	tailscale  *tailscale.Service
}

var _ teams.Teardown = (*teamTeardown)(nil)

func (t *teamTeardown) DeleteProject(ctx context.Context, projectID uuid.UUID) error {
	_, err := t.projects.DeleteCascade(ctx, projectID)
	return err
}

// RemovePlatformServices removes the team's platform service (Headscale) with
// its data. The team's slices of the instance services are not touched here:
// they are kept for the grace period and purged by the instance services job.
func (t *teamTeardown) RemovePlatformServices(ctx context.Context) error {
	rows, err := t.q.ListPlatformServices(ctx, policy.WorkspaceID(ctx))
	if err != nil {
		return fmt.Errorf("list platform services: %w", err)
	}
	for _, r := range rows {
		err := t.platform.Remove(ctx, r.ID, platform.RemoveInput{Confirm: r.Kind, DeleteData: true})
		if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", r.Kind, err)
		}
	}
	return nil
}

func (t *teamTeardown) RemoveServer(ctx context.Context, serverID uuid.UUID) (string, error) {
	outcome, err := t.servers.RemoveForTeam(ctx, serverID)
	if err != nil {
		return "", err
	}
	if outcome == servers.OutcomeCleaned {
		return teams.ServerCleaned, nil
	}
	return teams.ServerForgotten, nil
}

func (t *teamTeardown) DeleteStatusPages(ctx context.Context) error {
	pages, err := t.pages.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range pages {
		if err := t.pages.Delete(ctx, p.ID); err != nil {
			if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
				continue
			}
			return fmt.Errorf("%s: %w", p.Title, err)
		}
	}
	return nil
}

func (t *teamTeardown) DisconnectCloudflare(ctx context.Context, keepRecords bool) error {
	err := t.cloudflare.Delete(ctx, !keepRecords)
	if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
		return nil // not connected
	}
	return err
}

func (t *teamTeardown) DisconnectTailscale(ctx context.Context) error {
	_, err := t.tailscale.Disconnect(ctx)
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return nil
	}
	return err
}
