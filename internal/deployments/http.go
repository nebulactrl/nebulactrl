package deployments

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type deployInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body *struct {
		CommitSHA string `json:"commitSha,omitempty" doc:"Git services only: build this full 40-character commit instead of the branch head (\"Rebuild commit\")."`
		Message   string `json:"message,omitempty" maxLength:"200" doc:"Optional note shown with the deployment"`
		NoCache   bool   `json:"noCache,omitempty" doc:"Git services only: build this one deployment without any cache (--no-cache --pull, and no reuse of an image a build server already pushed). Later deployments build normally. 422 agent_too_old if the server's agent predates this option."`
	} `required:"false"`
}

type deploymentIDInput struct {
	ID uuid.UUID `path:"deploymentId"`
}

type listInput struct {
	ID uuid.UUID `path:"serviceId"`
	api.PageQuery
}

type planOutput struct{ Body Plan }
type deploymentOutput struct{ Body DeploymentView }
type rollbackOutput struct{ Body RollbackView }

type eventsOutput struct {
	Body struct {
		Items []EventView `json:"items"`
	}
}

type commandsOutput struct {
	Body struct {
		Items []CommandView `json:"items"`
	}
}

type releaseDetailOutput struct{ Body ReleaseDetailView }

type changesOutput struct {
	Body struct {
		Items []services.FieldChange `json:"items"`
	}
}

type logsInput struct {
	ID      uuid.UUID `path:"deploymentId"`
	Follow  bool      `query:"follow" doc:"Stream lines as server-sent events until the deployment finishes"`
	Streams string    `query:"streams" maxLength:"64" doc:"Which streams to return: empty for build, deploy and runtime; all to include the debug stream; or a comma-separated list of build, deploy, runtime and debug"`
}

type deployStagedInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          struct {
		ServiceIDs []uuid.UUID `json:"serviceIds,omitempty" doc:"Limit to these services (default: every service with staged changes)"`
		Message    string      `json:"message,omitempty" maxLength:"200" doc:"Optional note shown with every deployment of the batch"`
		NoCache    bool        `json:"noCache,omitempty" doc:"Build every git service of the batch without cache (--no-cache --pull) for this batch only; other services deploy as usual"`
	}
}

type deployStagedOutput struct{ Body StagedResult }

type envDeploymentsInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Status        string    `query:"status" maxLength:"200" doc:"Comma-separated statuses, e.g. failed,succeeded"`
	ServiceID     string    `query:"serviceId" format:"uuid" doc:"Only deployments of this service"`
	api.PageQuery
}

type dailyOutcomesInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Days          int       `query:"days" default:"30" minimum:"1" maximum:"90" doc:"Window length in UTC days, ending today"`
}

type discardInput struct {
	ID    uuid.UUID `path:"serviceId"`
	Field string    `path:"field" doc:"A pending-changes field: config.<path>, source, server, variables.<KEY>, domains.<host> or volumes.<name>"`
}

type serviceOutput struct {
	ETag string `header:"ETag"`
	Body services.ServiceView
}

// Register attaches the deployment endpoints.
func Register(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, extra...)
	}
	conflict, uc := http.StatusConflict, http.StatusUnprocessableEntity

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planDeployment", Method: http.MethodPost, Path: "/services/{serviceId}/deployments/plan", Tags: []string{"deployments"},
		Summary: "Plan a deployment (dry run)",
		Description: "Reports the strategy (rolling or recreate, with reasons), warnings, the pending changes, the resolved source and " +
			"blocking problems. Nothing is started.",
		Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*planOutput, error) {
		p, err := svc.Plan(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "deployService", Method: http.MethodPost, Path: "/services/{serviceId}/deployments", Tags: []string{"deployments"},
		Summary: "Deploy a service", DefaultStatus: http.StatusAccepted,
		Description: "Returns 202 with the deployment; it runs asynchronously. 409 `deployment_in_progress` while another deployment " +
			"of the service is active; 422 for blocking problems (no server, revoked server, unresolved variable references).",
		Errors: errs(conflict, uc),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *deployInput) (*deploymentOutput, error) {
		var o DeployOptions
		if in.Body != nil {
			o = DeployOptions{CommitSHA: in.Body.CommitSHA, Message: in.Body.Message, NoCache: in.Body.NoCache}
		}
		d, err := svc.DeployWithOptions(ctx, in.ID, o)
		if err != nil {
			return nil, err
		}
		return &deploymentOutput{Body: d}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDeployments", Method: http.MethodGet, Path: "/services/{serviceId}/deployments", Tags: []string{"deployments"},
		Summary: "List a service's deployments (newest first)", Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listInput) (*struct{ Body api.Page[DeploymentView] }, error) {
		items, next, err := svc.List(ctx, in.ID, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &struct{ Body api.Page[DeploymentView] }{Body: api.Page[DeploymentView]{Items: items, NextCursor: next}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDeployment", Method: http.MethodGet, Path: "/deployments/{deploymentId}", Tags: []string{"deployments"},
		Summary:     "Get a deployment",
		Description: "`waitingFor` is set while the deployment waits for an offline server.",
		Errors:      errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *deploymentIDInput) (*deploymentOutput, error) {
		d, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &deploymentOutput{Body: d}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "rollbackDeployment", Method: http.MethodPost, Path: "/deployments/{deploymentId}/rollback", Tags: []string{"deployments"},
		Summary: "Roll back to the release of a successful deployment", DefaultStatus: http.StatusAccepted,
		Description: RollbackDisclaimer + " Creates a new deployment of the old release (`rolledBackFrom` is the deployment that was running). " +
			"A git release whose built image was pruned returns 409 `image_pruned` with `commitSha` in the details: rebuild the commit instead.",
		Errors: errs(conflict, uc),
	}), policy.ScopeDeploymentsRollback), func(ctx context.Context, in *deploymentIDInput) (*rollbackOutput, error) {
		d, err := svc.Rollback(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &rollbackOutput{Body: d}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDeploymentEvents", Method: http.MethodGet, Path: "/deployments/{deploymentId}/events", Tags: []string{"deployments"},
		Summary: "A deployment's step timeline", Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *deploymentIDInput) (*eventsOutput, error) {
		items, err := svc.Events(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &eventsOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDeploymentLogs", Method: http.MethodGet, Path: "/deployments/{deploymentId}/logs", Tags: []string{"logs"},
		Summary: "Build and deploy logs of a deployment",
		Description: "Without `follow` returns the persisted lines as `{items:[{id,stream,line,createdAt}]}` (at most 10,000 lines). " +
			"With `follow=true` streams `data: {stream,line,ts}` until the deployment finishes, then `event: end`. " +
			"The `debug` stream (the agent's verbose trace: docker calls, health probe attempts, route changes, timings; at most 500 lines) " +
			"is left out unless `streams` asks for it.",
		Responses: services.StreamResponses(), Errors: errs(uc),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *logsInput) (*huma.StreamResponse, error) {
		streams, err := ParseLogStreams(in.Streams)
		if err != nil {
			return nil, err
		}
		if in.Follow {
			return svc.FollowLogs(ctx, in.ID, streams)
		}
		lines, err := svc.LogLines(ctx, in.ID, 0, MaxLogLines+MaxDebugLogLines+1, streams)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			services.WriteJSON(hctx, struct {
				Items []LogLineView `json:"items"`
			}{Items: lines})
		}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDeploymentCommands", Method: http.MethodGet, Path: "/deployments/{deploymentId}/commands", Tags: []string{"deployments"},
		Summary: "A deployment's agent commands, redacted",
		Description: "Every command the control plane sent for the deployment (deploy, build_image, cancel_deploy, …): kind, server, delivery state, " +
			"enqueue/ack/finish times, outcome and error, with the payload and result as persisted. Variable values, tokens, credentials and file " +
			"contents never appear: those fields are masked. Needs the same access as the deployment's logs.",
		Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *deploymentIDInput) (*commandsOutput, error) {
		items, err := svc.Commands(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &commandsOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDeploymentRelease", Method: http.MethodGet, Path: "/deployments/{deploymentId}/release", Tags: []string{"deployments"},
		Summary: "The release a deployment shipped, for debugging",
		Description: "The release id and generation, the resolved source (image ref and digest, or git commit), how it was built and where it ran, " +
			"the configuration snapshot with secrets and file contents masked, and the keys of its variables with which are secret, references or " +
			"provided by the platform. Never a variable value. 404 when the deployment has no release yet.",
		Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *deploymentIDInput) (*releaseDetailOutput, error) {
		v, err := svc.ReleaseDetail(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &releaseDetailOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listPendingChanges", Method: http.MethodGet, Path: "/services/{serviceId}/pending-changes", Tags: []string{"services"},
		Summary:     "Staged changes not yet deployed",
		Description: "Field-level differences between the service's configuration and its current release. Secret values are masked.",
		Errors:      errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*changesOutput, error) {
		items, err := svc.PendingChanges(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &changesOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "discardPendingChange", Method: http.MethodDelete, Path: "/services/{serviceId}/pending-changes/{field}", Tags: []string{"services"},
		Summary: "Discard one staged change",
		Description: "Reverts a single field listed by pending-changes (`config.<path>`, `source`, `server`, `variables.<KEY>`, `domains.<host>`, " +
			"`volumes.<name>`) to the state of the current release and advances the generation. Discarding `source` restores the release's source type, image, repository and build settings; the credentials and repository link the change cleared are not restored. 404 when the field is not pending. " +
			"Returns the updated service.",
		Errors: errs(conflict, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *discardInput) (*serviceOutput, error) {
		v, err := svc.DiscardPendingChange(ctx, in.ID, in.Field)
		if err != nil {
			return nil, err
		}
		return &serviceOutput{ETag: services.ETag(v.Generation), Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "deployStaged", Method: http.MethodPost, Path: "/environments/{environmentId}/deploy-staged", Tags: []string{"deployments"},
		Summary: "Deploy every service with staged changes", DefaultStatus: http.StatusAccepted,
		Description: "Services start in reference order: providers of `${{service.KEY}}` references first (otherwise databases first; " +
			"a reference cycle keeps the input order). A consumer's deployment waits until the providers of the same batch finished " +
			"(at most 10 minutes). `order` lists the service ids in the order they were started. " +
			"Services that cannot deploy are skipped unless named in `serviceIds`.",
		Errors: errs(conflict, uc),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *deployStagedInput) (*deployStagedOutput, error) {
		res, err := svc.DeployStagedWith(ctx, in.EnvironmentID, in.Body.ServiceIDs, in.Body.Message, in.Body.NoCache)
		if err != nil {
			return nil, err
		}
		return &deployStagedOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "cancelDeployment", Method: http.MethodPost, Path: "/deployments/{deploymentId}/cancel", Tags: []string{"deployments"},
		Summary: "Cancel a deployment at any step",
		Description: "Before the agent acknowledged the deployment's command the deployment ends as `canceled` at once. " +
			"Once the agent runs it, a cancel request is sent to the agent (`cancelRequestedAt` is set) and the deployment ends as `canceled` when the agent reports back, " +
			"with the step it reached in `canceledAtStep`: a pull or build is stopped (a build's work directory is removed), containers of the new release are removed and the current release keeps serving, " +
			"during verification the previous routes are restored first, and a recreate deploy that already stopped the previous release starts it again. " +
			"If the previous release cannot be restored the result is `rollback_failed` with manual recovery steps. " +
			"Once the new release is live it is too late: the deployment finishes (a still-running post-deploy command is stopped). " +
			"An offline server receives the request when it reconnects. A finished deployment is 409 `cannot_cancel`.",
		Errors: errs(conflict),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *deploymentIDInput) (*deploymentOutput, error) {
		d, err := svc.Cancel(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &deploymentOutput{Body: d}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listEnvironmentDeployments", Method: http.MethodGet, Path: "/environments/{environmentId}/deployments", Tags: []string{"deployments"},
		Summary:     "List the deployments of every service of an environment (newest first)",
		Description: "Filter by `status` (comma-separated) and `serviceId`.",
		Errors:      errs(uc),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *envDeploymentsInput) (*struct{ Body api.Page[EnvDeploymentView] }, error) {
		var svcFilter *uuid.UUID
		if in.ServiceID != "" {
			id, err := uuid.Parse(in.ServiceID)
			if err != nil {
				return nil, apierr.Unprocessable("validation_failed", "serviceId must be a UUID")
			}
			svcFilter = &id
		}
		items, next, err := svc.ListEnvironment(ctx, in.EnvironmentID, in.Status, svcFilter, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &struct{ Body api.Page[EnvDeploymentView] }{Body: api.Page[EnvDeploymentView]{Items: items, NextCursor: next}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getEnvironmentDeploymentsDaily", Method: http.MethodGet, Path: "/environments/{environmentId}/deployments/daily", Tags: []string{"deployments"},
		Summary: "Deployment outcomes per UTC day for an environment",
		Description: "One entry per day, oldest first, ending today (`days` 1..90, default 30); days without deployments are zeros. " +
			"`failed` counts failed and rollback_failed, `other` counts canceled, rolled_back and in-flight deployments.",
		Errors: errs(uc),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *dailyOutcomesInput) (*struct{ Body DailyOutcomes }, error) {
		out, err := svc.DailyOutcomes(ctx, in.EnvironmentID, in.Days, time.Now())
		if err != nil {
			return nil, err
		}
		return &struct{ Body DailyOutcomes }{Body: out}, nil
	})

	registerBuildPlan(h, svc)
	registerLogDownload(h, svc)
}
