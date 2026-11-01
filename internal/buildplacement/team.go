package buildplacement

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// TeamSettings is the team default of where services are built.
type TeamSettings struct {
	// BuildLocation is target (every service builds on its own server) or
	// build_server (git services build on a build server and push to RegistryID).
	BuildLocation string `json:"buildLocation" enum:"target,build_server"`
	// RegistryID is the default registry builds are pushed to (null: none).
	RegistryID *uuid.UUID `json:"registryId"`
	UpdatedAt  *time.Time `json:"updatedAt"`
}

// TeamService reads and writes the team build settings.
type TeamService struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// NewTeamService returns a TeamService (pool may be nil for OpenAPI generation).
func NewTeamService(pool *pgxpool.Pool) *TeamService {
	return &TeamService{pool: pool, q: db.New(pool)}
}

// Get returns the team's build settings (the defaults while none were saved).
func (s *TeamService) Get(ctx context.Context) (TeamSettings, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return TeamSettings{}, err
	}
	row, err := s.q.GetTeamBuildSettings(ctx, policy.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamSettings{BuildLocation: LocationTarget}, nil
	}
	if err != nil {
		return TeamSettings{}, fmt.Errorf("get team build settings: %w", err)
	}
	t := row.UpdatedAt
	return TeamSettings{BuildLocation: row.BuildLocation, RegistryID: row.RegistryID, UpdatedAt: &t}, nil
}

// Put replaces the team's build settings.
func (s *TeamService) Put(ctx context.Context, in TeamSettings) (TeamSettings, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return TeamSettings{}, err
	}
	if !ValidLocation(in.BuildLocation, false) {
		return TeamSettings{}, apierr.Unprocessable("validation_failed", "buildLocation must be target or build_server")
	}
	ws := policy.WorkspaceID(ctx)
	if err := ValidateTargets(ctx, s.q, ws, nil, in.RegistryID); err != nil {
		return TeamSettings{}, err
	}
	var row db.TeamBuildSetting
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpsertTeamBuildSettings(ctx, db.UpsertTeamBuildSettingsParams{WorkspaceID: ws, BuildLocation: in.BuildLocation, RegistryID: in.RegistryID})
		if err != nil {
			return err
		}
		meta := map[string]any{"buildLocation": in.BuildLocation}
		if in.RegistryID != nil {
			meta["registryId"] = in.RegistryID.String()
		}
		return audit.Record(ctx, tx, audit.Entry{Action: "team.build_settings", ResourceType: "team", ResourceID: ws.String(), Metadata: meta})
	})
	if err != nil {
		return TeamSettings{}, fmt.Errorf("save team build settings: %w", err)
	}
	t := row.UpdatedAt
	return TeamSettings{BuildLocation: row.BuildLocation, RegistryID: row.RegistryID, UpdatedAt: &t}, nil
}

type teamSettingsOutput struct{ Body TeamSettings }

type putTeamSettingsInput struct {
	Body struct {
		BuildLocation string     `json:"buildLocation" enum:"target,build_server" doc:"Where services build by default: on their own server (target) or on a build server."`
		RegistryID    *uuid.UUID `json:"registryId,omitempty" doc:"Default registry builds are pushed to; omitted clears it."`
	}
}

// Register attaches GET and PUT /team/build-settings.
func Register(h huma.API, svc *TeamService) {
	tags := []string{"builds"}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTeamBuildSettings", Method: http.MethodGet, Path: "/team/build-settings", Tags: tags,
		Summary:     "Get the team's default build location and registry",
		Description: "Projects and services can override the default; resolution is service, then project, then team.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*teamSettingsOutput, error) {
		st, err := svc.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &teamSettingsOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "putTeamBuildSettings", Method: http.MethodPut, Path: "/team/build-settings", Tags: tags,
		Summary: "Set the team's default build location and registry",
		Description: "With buildLocation build_server, git services build on a build server and push the image to the registry; " +
			"the deploying server pulls it by digest. Without a usable build server or registry a build falls back to the target server " +
			"and says so in the deployment's events. Administrators only.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, policy.ScopeAdmin), func(ctx context.Context, in *putTeamSettingsInput) (*teamSettingsOutput, error) {
		st, err := svc.Put(ctx, TeamSettings{BuildLocation: in.Body.BuildLocation, RegistryID: in.Body.RegistryID})
		if err != nil {
			return nil, err
		}
		return &teamSettingsOutput{Body: st}, nil
	})
}
