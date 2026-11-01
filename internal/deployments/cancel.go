package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func cannotCancel(msg string) error { return apierr.Conflict("cannot_cancel", msg) }

// Cancel stops a deployment at whatever step it has reached.
//
//   - Before the agent acknowledged the deploy command (created, queued,
//     preparing or building with the command pending or sent) the command is
//     canceled and the deployment ends as `canceled` right away, "Canceled by
//     <actor>".
//   - Once the agent runs the command, a cancel_deploy command is queued for
//     its server and the request is recorded on the deployment
//     (cancelRequestedAt). The agent aborts at the step it is in (a build is
//     killed, new containers are removed, routes are restored, a stopped
//     previous release is started again) and reports the `canceled` result;
//     until then the deployment keeps its status. The agent answers "too late"
//     once the new release is live. If the server is offline the cancel waits
//     for it (it is delivered when the agent reconnects).
//
// A finished deployment is 409 `cannot_cancel`.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (DeploymentView, error) {
	dc, err := s.loadDeployment(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return DeploymentView{}, err
	}
	if !Status(dc.Dep.Status).IsTerminal() && prodguard.Applies(ctx, true) {
		sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, dc.Dep.ServiceID)
		if err != nil {
			return DeploymentView{}, err
		}
		if err := sc.Guard(ctx, "cancel_deployment",
			fmt.Sprintf("Cancel the running deployment of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
			[]string{"Aborts the deployment at the step it has reached.", "A new container that was started is removed; a stopped previous release is started again."},
			id); err != nil {
			return DeploymentView{}, err
		}
	}
	label := audit.ActorSystem
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Label != "" {
		label = p.Label
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockDeployment(ctx, id)
		if err != nil {
			return fmt.Errorf("lock deployment: %w", err)
		}
		st := Status(cur.Status)
		if st.IsTerminal() {
			return cannotCancel("The deployment already finished.")
		}
		// The command the agent runs now: the deploy on the target, or, before
		// the deploy exists, the build on the build server (the cancel is then
		// sent to the builder). A finished build with no deploy yet counts as
		// not dispatched.
		cmd, err := q.GetDeployCommandByDeployment(ctx, &id)
		if errors.Is(err, pgx.ErrNoRows) {
			bc, berr := q.GetBuildCommandByDeployment(ctx, &id)
			switch {
			case berr == nil && bc.Status != "succeeded":
				cmd, err = bc, nil
			case berr != nil && !errors.Is(berr, pgx.ErrNoRows):
				return fmt.Errorf("get build command: %w", berr)
			}
		}
		agentOwns := false
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Not dispatched yet.
		case err != nil:
			return fmt.Errorf("get deploy command: %w", err)
		case cmd.Status == "pending" || cmd.Status == "sent":
			if _, err := q.CancelUnackedAgentCommand(ctx, db.CancelUnackedAgentCommandParams{ID: cmd.ID, Error: "canceled by " + label}); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("cancel command: %w", err)
				}
				agentOwns = true // acknowledged meanwhile
			}
		case cmd.Status == "acked":
			agentOwns = true
		default:
			return cannotCancel("The agent already finished this deployment; its result is being applied.")
		}
		if !agentOwns {
			if _, _, err := s.advance(ctx, tx, id, Canceled, "Canceled by "+label); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{
				Action: "deployment.cancel", ResourceType: "deployment", ResourceID: id.String(), ProjectID: &dc.ProjectID,
				Metadata: map[string]any{"service": dc.ServiceSlug, "from": cur.Status, "mode": "command"}, WorkspaceID: dc.WorkspaceID,
			})
		}

		// The agent runs it: ask it to abort (an agent from before cancel_deploy
		// would reject the command and the request would hang).
		if srv, err := q.GetServerByID(ctx, cmd.ServerID); err == nil && !supportsCancel(srv.Facts) {
			return cannotCancel("This server's agent is too old to cancel a running deployment. Update the agent, or wait for the deployment to finish.")
		}
		req, err := q.RequestDeploymentCancel(ctx, db.RequestDeploymentCancelParams{ID: id, RequestedBy: label})
		if err != nil {
			return fmt.Errorf("record cancel request: %w", err)
		}
		raw, err := json.Marshal(agentprotocol.CancelDeployPayload{DeploymentID: id.String(), RequestedBy: label})
		if err != nil {
			return err
		}
		stamp := time.Now().UnixMilli()
		if req.CancelRequestedAt != nil {
			stamp = req.CancelRequestedAt.UnixMilli()
		}
		if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: cmd.ServerID, Kind: agentprotocol.KindCancelDeploy, Payload: raw,
			IdempotencyKey: fmt.Sprintf("cancel_deploy:%s:%d", id, stamp),
		}); err != nil {
			return fmt.Errorf("enqueue cancel_deploy: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "deployment.cancel", ResourceType: "deployment", ResourceID: id.String(), ProjectID: &dc.ProjectID,
			Metadata: map[string]any{"service": dc.ServiceSlug, "from": cur.Status, "mode": "agent"}, WorkspaceID: dc.WorkspaceID,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return DeploymentView{}, err
		}
		return DeploymentView{}, fmt.Errorf("cancel deployment: %w", err)
	}
	s.publish(dc)
	s.publishService(dc)
	fresh, err := s.q.GetDeploymentScopeInternal(ctx, id) // id was authorized by loadDeployment above
	if err != nil {
		return DeploymentView{}, fmt.Errorf("reload deployment: %w", err)
	}
	return s.oneView(ctx, fresh.Deployment)
}

// ListEnvironment returns the deployments of every service of an environment,
// newest first. statusCSV is an optional comma-separated status filter and
// serviceID an optional service filter.
func (s *Service) ListEnvironment(ctx context.Context, envID uuid.UUID, statusCSV string, serviceID *uuid.UUID, cursor string, limit int) ([]EnvDeploymentView, string, error) {
	if _, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID); err != nil {
		return nil, "", err
	}
	var statuses []string
	for _, st := range strings.Split(statusCSV, ",") {
		st = strings.TrimSpace(st)
		if st == "" {
			continue
		}
		if !Status(st).Valid() {
			return nil, "", apierr.Unprocessable("validation_failed", "unknown deployment status "+st)
		}
		statuses = append(statuses, st)
	}
	params := db.ListEnvironmentDeploymentsPageParams{EnvironmentID: envID, Statuses: statuses, ServiceID: serviceID, RowLimit: int32(limit + 1)}
	ts, id, ok, err := api.DecodeTimeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if ok {
		params.CursorCreatedAt, params.CursorID = &ts, &id
	}
	rows, err := s.q.ListEnvironmentDeploymentsPage(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list environment deployments: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1].Deployment
		next = api.EncodeTimeCursor(last.CreatedAt, last.ID)
	}
	deps := make([]db.Deployment, len(rows))
	for i, r := range rows {
		deps[i] = r.Deployment
	}
	views, err := s.Views(ctx, deps)
	if err != nil {
		return nil, "", err
	}
	out := make([]EnvDeploymentView, len(rows))
	for i, r := range rows {
		out[i] = EnvDeploymentView{DeploymentView: views[i], ServiceName: r.ServiceName, ServiceSlug: r.ServiceSlug}
	}
	return out, next, nil
}

// supportsCancel reports whether the server's agent advertised
// agentprotocol.CapCancelDeploy. Unknown facts count as a current agent.
func supportsCancel(facts json.RawMessage) bool {
	if len(facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapCancelDeploy)
}
