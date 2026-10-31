package cleanup

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type cleanupServerInput struct {
	ID uuid.UUID `path:"serverId"`
}

type updateCleanupInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Enabled                bool   `json:"enabled" doc:"Run the scheduled (daily, 04:00 UTC) and emergency cleanups"`
		RemoveStoppedUnmanaged bool   `json:"removeStoppedUnmanaged" doc:"Also remove stopped containers that NebulaCtrl does not manage. Their volumes are kept."`
		Cron                   string `json:"cron,omitempty" doc:"The schedule: a 5-field cron expression (or @daily, @weekly, ...) evaluated in UTC. Runs must be at least 15 minutes apart. Empty keeps the default, 0 4 * * *."`
		UnusedImageMinAgeHours int    `json:"unusedImageMinAgeHours,omitempty" minimum:"0" maximum:"8760" doc:"Remove unused images older than this many hours (1-8760). 0 or omitted keeps the default, 72."`
		BuildCacheMinAgeDays   int    `json:"buildCacheMinAgeDays,omitempty" minimum:"0" maximum:"365" doc:"Prune build cache older than this many days (1-365). 0 or omitted keeps the default, 7."`
		DiskTriggerPercent     int    `json:"diskTriggerPercent,omitempty" minimum:"0" maximum:"99" doc:"Start an emergency cleanup when disk usage reaches this percent (50-99). 0 or omitted follows the server's disk alert threshold."`
	}
}

type pruneVolumesInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Confirm string `json:"confirm" doc:"The server's name, typed to confirm"`
	}
}

type cleanupViewOutput struct{ Body CleanupView }

type cleanupRunOutput struct{ Body CleanupRun }

// Register attaches the Docker cleanup endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"servers"}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerCleanup", Method: http.MethodGet, Path: "/servers/{serverId}/cleanup", Tags: tags,
		Summary: "Docker cleanup settings and history of a server",
		Description: "Whether the scheduled cleanup is on, when it runs next and the most recent runs with what each prune step removed. " +
			"A cleanup removes dangling images, unused images older than `unusedImageMinAgeHours` (default 72), build cache older than `buildCacheMinAgeDays` (default 7) and unused NebulaCtrl networks, and optionally stopped containers NebulaCtrl does not manage. " +
			"It never removes the current or a rollback-eligible release's image, a pinned infrastructure image, or any volume.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersRead), func(ctx context.Context, in *cleanupServerInput) (*cleanupViewOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &cleanupViewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServerCleanup", Method: http.MethodPut, Path: "/servers/{serverId}/cleanup", Tags: tags,
		Summary:     "Change the Docker cleanup settings of a server",
		Description: "Needs the `servers:write` scope. `cron` sets the schedule (UTC), `unusedImageMinAgeHours` and `buildCacheMinAgeDays` the age limits and `diskTriggerPercent` the disk usage that starts an emergency run (omitted or 0: the server's disk alert threshold); omitted or 0 keeps each default. Turning the schedule off also turns off the emergency run; \"Run cleanup now\" still works. 422 `validation_failed` for a bad cron expression (or one that fires more often than every 15 minutes) or an out-of-range value.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersWrite), func(ctx context.Context, in *updateCleanupInput) (*cleanupViewOutput, error) {
		v, err := svc.Update(ctx, in.ID, Settings{
			Enabled: in.Body.Enabled, RemoveStoppedUnmanaged: in.Body.RemoveStoppedUnmanaged, Cron: in.Body.Cron,
			UnusedImageMinAgeHours: in.Body.UnusedImageMinAgeHours, BuildCacheMinAgeDays: in.Body.BuildCacheMinAgeDays,
			DiskTriggerPercent: in.Body.DiskTriggerPercent,
		})
		if err != nil {
			return nil, err
		}
		return &cleanupViewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "runServerCleanup", Method: http.MethodPost, Path: "/servers/{serverId}/cleanup/runs", Tags: tags,
		Summary:       "Run the Docker cleanup of a server now",
		DefaultStatus: http.StatusAccepted,
		Description:   "Queues the same cleanup the schedule runs. The result (reclaimed bytes, items per step) appears in the server's cleanup history. 409 `server_offline` when the server is offline, 409 `cleanup_running` while one is in flight.",
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeServersWrite), func(ctx context.Context, in *cleanupServerInput) (*cleanupRunOutput, error) {
		r, err := svc.RunNow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &cleanupRunOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "pruneServerVolumes", Method: http.MethodPost, Path: "/servers/{serverId}/cleanup/volumes", Tags: tags,
		Summary:       "Remove unused Docker volumes of a server",
		DefaultStatus: http.StatusAccepted,
		Description: "Removes volumes that no container (running or stopped) mounts and that NebulaCtrl does not manage: anonymous volumes and volumes made outside NebulaCtrl. " +
			"Volumes of NebulaCtrl services (`nc-vol-*`, or labeled as managed) are never removed. This deletes data and cannot be undone: needs the Owner or Admin role, and `confirm` must be the server's name. The schedule never does this.",
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeServersWrite), func(ctx context.Context, in *pruneVolumesInput) (*cleanupRunOutput, error) {
		r, err := svc.PruneVolumes(ctx, in.ID, in.Body.Confirm)
		if err != nil {
			return nil, err
		}
		return &cleanupRunOutput{Body: r}, nil
	})
}
