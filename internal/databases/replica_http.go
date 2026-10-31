package databases

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type replicationOutput struct{ Body ReplicationView }

type addReplicaInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Name              string    `json:"name,omitempty" maxLength:"100" doc:"Display name; defaults to <primary>-replica-<n>"`
		ServerID          uuid.UUID `json:"serverId" doc:"The server the replica runs on (the primary's server, or another server with the mesh enabled)"`
		DryRun            bool      `json:"dryRun,omitempty" doc:"Only return the plan"`
		AcceptMeshChanges bool      `json:"acceptMeshChanges,omitempty" doc:"Confirms that the replica adds tailnet devices (a replication link)"`
	}
}

type addReplicaOutput struct {
	// Status is 200 for a dry run and 202 when the replica was created.
	Status int
	Body   AddReplicaResult
}

type removeReplicaOutput struct{ Body RemoveResult }

type promoteInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		DryRun         bool   `json:"dryRun,omitempty" doc:"Only return the plan"`
		Confirm        string `json:"confirm,omitempty" doc:"Must equal the replica's slug"`
		AcceptDataLoss bool   `json:"acceptDataLoss,omitempty" doc:"Required for a forced promote: what the replica had not received is lost"`
	}
}

type promoteOutput struct {
	// Status is 200 for a dry run and 202 when the promotion started.
	Status int
	Body   PromoteResult
}

type rebuildInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		FollowServiceID *uuid.UUID `json:"followServiceId,omitempty" doc:"For a fenced old primary: the new primary to follow (default: the replica that was promoted)"`
	}
}

type rebuildOutput struct{ Body RebuildResult }

// registerReplicas adds the read replica endpoints.
func registerReplicas(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}
	tags := []string{"databases"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDatabaseReplication", Method: http.MethodGet, Path: "/services/{serviceId}/database/replication", Tags: tags,
		Summary: "Get the read replica group of a PostgreSQL database", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*replicationOutput, error) {
		v, err := svc.Replication(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &replicationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "addDatabaseReplica", Method: http.MethodPost, Path: "/services/{serviceId}/database/replicas", Tags: tags,
		Summary: "Add a read replica of a PostgreSQL primary", DefaultStatus: http.StatusAccepted,
		Description: "Creates a read replica service in the primary's environment and clones the primary into it. The replica runs on the primary's " +
			"server or, with the server mesh enabled on both, on another server. Asynchronous: the replica is `pending`, then `preparing`, `cloning`, " +
			"`deploying` and `streaming`; follow it with `GET /services/{id}/database/replication`. With `dryRun` only the plan is returned (status 200). " +
			"Replication is asynchronous and manual: nothing is detected or switched automatically.",
		Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *addReplicaInput) (*addReplicaOutput, error) {
		r, err := svc.AddReplica(ctx, in.ID, AddReplicaInput{
			Name: in.Body.Name, ServerID: in.Body.ServerID, DryRun: in.Body.DryRun, AcceptMeshChanges: in.Body.AcceptMeshChanges,
		})
		if err != nil {
			return nil, err
		}
		status := http.StatusAccepted
		if r.DryRun {
			status = http.StatusOK
		}
		return &addReplicaOutput{Status: status, Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "removeDatabaseReplica", Method: http.MethodDelete, Path: "/services/{serviceId}/database/replica", Tags: tags,
		Summary: "Remove a read replica", DefaultStatus: http.StatusAccepted,
		Description: "Called on the replica. Deletes its container and data volume, drops its slot, role and pg_hba.conf line on the primary and " +
			"releases its mesh link. Asynchronous.",
		Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*removeReplicaOutput, error) {
		r, err := svc.RemoveReplica(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &removeReplicaOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "promoteDatabaseReplica", Method: http.MethodPost, Path: "/services/{serviceId}/database/promote", Tags: tags,
		Summary: "Promote a read replica to primary (manual)", DefaultStatus: http.StatusAccepted,
		Description: "Called on the replica. Manual: NebulaCtrl does not detect failures or switch automatically. With `dryRun` the plan is returned " +
			"(status 200): `switchover` while the old primary is reachable (it is frozen, stopped and fenced, the replica replays everything, no data " +
			"is lost) or `forced` otherwise (it needs `acceptDataLoss`: what the replica had not received is lost). The old primary is fenced and " +
			"never runs as a primary again; rebuild it as a replica or delete it. Variables that reference the old primary are rewritten as staged " +
			"changes. Production environments need approval. Asynchronous: poll `replication.promotion`.",
		Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *promoteInput) (*promoteOutput, error) {
		r, err := svc.Promote(ctx, in.ID, PromoteInput{DryRun: in.Body.DryRun, Confirm: in.Body.Confirm, AcceptDataLoss: in.Body.AcceptDataLoss})
		if err != nil {
			return nil, err
		}
		status := http.StatusAccepted
		if r.DryRun {
			status = http.StatusOK
		}
		return &promoteOutput{Status: status, Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "rebuildDatabaseReplica", Method: http.MethodPost, Path: "/services/{serviceId}/database/rebuild", Tags: tags,
		Summary: "Rebuild a replica, or a fenced old primary as a replica", DefaultStatus: http.StatusAccepted,
		Description: "Clones the primary again into a new data volume (the old volume is kept until it is deleted). On a fenced old primary the body may " +
			"name the new primary to follow. Asynchronous.",
		Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *rebuildInput) (*rebuildOutput, error) {
		r, err := svc.RebuildReplica(ctx, in.ID, RebuildInput{FollowServiceID: in.Body.FollowServiceID})
		if err != nil {
			return nil, err
		}
		return &rebuildOutput{Body: r}, nil
	})
}
