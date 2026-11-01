package databases

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type createDatabaseInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          struct {
		Name          string     `json:"name,omitempty" maxLength:"100" doc:"Display name; defaults to the engine name"`
		Engine        string     `json:"engine" enum:"postgres,mysql,mariadb,valkey,redis,mongodb,clickhouse" doc:"Valkey is the default key-value engine"`
		Version       string     `json:"version,omitempty" doc:"Major version; omitted means the engine's default"`
		Flavor        string     `json:"flavor,omitempty" enum:"postgis" doc:"Image variant, e.g. postgis for PostgreSQL"`
		DatabaseName  string     `json:"databaseName,omitempty" pattern:"^[a-z_][a-z0-9_]{0,62}$"`
		ServerID      *uuid.UUID `json:"serverId,omitempty"`
		AcceptLicense bool       `json:"acceptLicense,omitempty" doc:"Required for engines with a source-available licence (MongoDB, Redis)"`
	}
}

type serviceOutput struct {
	ETag string `header:"ETag"`
	Body services.ServiceView
}

type catalogOutput struct {
	Body struct {
		Engines []EngineView `json:"engines"`
	}
}

type databaseOutput struct{ Body DatabaseView }

type refreshOutput struct{ Body RefreshResult }

type revealInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Key string `json:"key" minLength:"1" maxLength:"64" doc:"One of the database's exports, e.g. DATABASE_URL"`
	}
}

type revealOutput struct {
	CacheControl string `header:"Cache-Control"`
	Pragma       string `header:"Pragma"`
	Body         Revealed
}

type rotateInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		DryRun bool `json:"dryRun,omitempty" doc:"Only return the plan"`
	}
}

type rotateOutput struct {
	Body RotationResult
}

type upgradePlanInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		TargetVersion string `json:"targetVersion" minLength:"1" maxLength:"16"`
	}
}

type upgradePlanOutput struct{ Body engines.UpgradePlan }

type upgradeInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		TargetVersion string `json:"targetVersion" minLength:"1" maxLength:"16"`
		Confirm       string `json:"confirm" doc:"Must equal the service slug"`
	}
}

type upgradeOutput struct{ Body UpgradeResult }

// Register adds the database endpoints.
func Register(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, uc, cf := http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusConflict

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDatabaseEngines", Method: http.MethodGet, Path: "/database-engines", Tags: []string{"databases"},
		Summary: "List the database engines and versions that can be created", Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*catalogOutput, error) {
		list, err := svc.Catalog(ctx)
		if err != nil {
			return nil, err
		}
		out := &catalogOutput{}
		out.Body.Engines = list
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createDatabase", Method: http.MethodPost, Path: "/environments/{environmentId}/databases", Tags: []string{"databases"},
		Summary: "Create a managed database", DefaultStatus: http.StatusCreated,
		Description: "Creates a staged database service with its data volume and generated credentials; nothing runs until it is deployed. " +
			"Other services connect with `${{<slug>.DATABASE_URL}}` (or `REDIS_URL`, `MONGO_URL`).",
		Errors: errs(nf, cf, uc),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *createDatabaseInput) (*serviceOutput, error) {
		v, err := svc.Create(ctx, CreateInput{
			EnvironmentID: in.EnvironmentID, Name: in.Body.Name, Engine: in.Body.Engine, Version: in.Body.Version, Flavor: in.Body.Flavor,
			DatabaseName: in.Body.DatabaseName, ServerID: in.Body.ServerID, AcceptLicense: in.Body.AcceptLicense,
		})
		if err != nil {
			return nil, err
		}
		return &serviceOutput{ETag: services.ETag(v.Generation), Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDatabase", Method: http.MethodGet, Path: "/services/{serviceId}/database", Tags: []string{"databases"},
		Summary: "Get a managed database's engine, exports and state", Errors: errs(nf, cf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*databaseOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &databaseOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "refreshDatabase", Method: http.MethodPost, Path: "/services/{serviceId}/database/refresh", Tags: []string{"databases"},
		Summary: "Ask the agent for the database's version, size and connections", DefaultStatus: http.StatusAccepted,
		Description: "Asynchronous: poll `GET /services/{id}/database` (or watch the event feed) for the new `info`.", Errors: errs(nf, cf),
	}, policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*refreshOutput, error) {
		r, err := svc.Refresh(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &refreshOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "revealDatabaseExport", Method: http.MethodPost, Path: "/services/{serviceId}/database/reveal", Tags: []string{"databases"},
		Summary: "Reveal a connection string (audited)",
		Description: "Returns one derived export such as `DATABASE_URL` in plain text, uncached. Requires the `deployments:write` scope " +
			"(or admin) and is recorded in the audit log as `secret.reveal`. Every other read stays masked.",
		Errors: errs(nf, cf),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *revealInput) (*revealOutput, error) {
		r, err := svc.Reveal(ctx, in.ID, in.Body.Key)
		if err != nil {
			return nil, err
		}
		return &revealOutput{CacheControl: "no-store, max-age=0", Pragma: "no-cache", Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "rotateDatabaseCredentials", Method: http.MethodPost, Path: "/services/{serviceId}/database/rotate-credentials", Tags: []string{"databases"},
		Summary: "Rotate the database password",
		Description: "Changes the application password inside the running database through the agent (or, for Valkey and Redis, stages a new " +
			"password), updates the variable and stages every service that references the database. Nothing is deployed. With `dryRun` only the plan is returned.",
		Errors: errs(nf, cf),
	}, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *rotateInput) (*rotateOutput, error) {
		r, err := svc.RotateCredentials(ctx, in.ID, in.Body.DryRun)
		if err != nil {
			return nil, err
		}
		return &rotateOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planDatabaseUpgrade", Method: http.MethodPost, Path: "/services/{serviceId}/database/upgrade/plan", Tags: []string{"databases"},
		Summary: "Plan a version upgrade", Errors: errs(nf, cf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *upgradePlanInput) (*upgradePlanOutput, error) {
		p, err := svc.PlanUpgrade(ctx, in.ID, in.Body.TargetVersion)
		if err != nil {
			return nil, err
		}
		return &upgradePlanOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "upgradeDatabase", Method: http.MethodPost, Path: "/services/{serviceId}/database/upgrade", Tags: []string{"databases"},
		Summary: "Upgrade the database version",
		Description: "In-place engines stage the new image. PostgreSQL majors are converted with pg_upgrade into a new volume (the old volume is kept) " +
			"and the new version is staged when the conversion finishes. Take a backup first.",
		Errors: errs(nf, cf, uc),
	}, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *upgradeInput) (*upgradeOutput, error) {
		r, err := svc.Upgrade(ctx, in.ID, in.Body.TargetVersion, in.Body.Confirm)
		if err != nil {
			return nil, err
		}
		return &upgradeOutput{Body: r}, nil
	})

	registerPublicPort(h, svc)
	registerReplicas(h, svc)
}
