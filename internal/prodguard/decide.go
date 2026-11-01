package prodguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ApprovalRequester says who asked for the change.
type ApprovalRequester struct {
	Kind       string `json:"kind" enum:"mcp,token" doc:"mcp for an MCP client, token for a REST API token"`
	TokenName  string `json:"tokenName"`
	ClientName string `json:"clientName,omitempty" doc:"MCP clientInfo name, when the client sent one"`
}

// ApprovalView is the API view of an approval.
type ApprovalView struct {
	ID            uuid.UUID         `json:"id"`
	Status        string            `json:"status" enum:"pending,approved,declined,used,expired,stale"`
	Plan          ApprovalPlan      `json:"plan"`
	ProjectID     uuid.UUID         `json:"projectId"`
	EnvironmentID uuid.UUID         `json:"environmentId"`
	Requester     ApprovalRequester `json:"requester"`
	CreatedAt     time.Time         `json:"createdAt"`
	ExpiresAt     time.Time         `json:"expiresAt"`
	DecidedBy     string            `json:"decidedBy,omitempty" doc:"Name of the person who decided"`
	DecidedVia    string            `json:"decidedVia,omitempty" enum:"web" doc:"Always web: an approval is only given on the dashboard by a signed-in person."`
	DecidedAt     *time.Time        `json:"decidedAt,omitempty"`
	// CanDecide is true when the caller may approve or decline right now.
	CanDecide bool `json:"canDecide"`
}

// sessionUser authorizes the caller as a signed-in person. Tokens and MCP
// principals never read or decide approvals: an agent must not approve itself.
func sessionUser(ctx context.Context) (*auth.Principal, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if p.Type != auth.TypeUser {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

func (g *Guard) load(ctx context.Context, p *auth.Principal, id uuid.UUID) (db.Approval, error) {
	row, err := g.q.GetApproval(ctx, db.GetApprovalParams{ID: id, WorkspaceID: p.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Approval{}, apierr.NotFound("approval")
	}
	if err != nil {
		return db.Approval{}, fmt.Errorf("get approval: %w", err)
	}
	if !p.CanSeeProject(row.ProjectID) {
		return db.Approval{}, apierr.NotFound("approval")
	}
	return row, nil
}

func (g *Guard) view(ctx context.Context, p *auth.Principal, row db.Approval) (ApprovalView, error) {
	var plan ApprovalPlan
	if err := json.Unmarshal(row.Plan, &plan); err != nil {
		return ApprovalView{}, fmt.Errorf("decode plan: %w", err)
	}
	status := row.Status
	if (status == StatusPending || status == StatusApproved) && !row.ExpiresAt.After(g.now()) {
		status = StatusExpired
	}
	v := ApprovalView{
		ID: row.ID, Status: status, Plan: plan, ProjectID: row.ProjectID, EnvironmentID: row.EnvironmentID,
		Requester: ApprovalRequester{Kind: row.PrincipalType, TokenName: row.PrincipalLabel, ClientName: row.ClientName},
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, DecidedAt: row.DecidedAt,
	}
	if row.DecidedVia != nil {
		v.DecidedVia = *row.DecidedVia
	}
	if row.DecidedBy != nil {
		if name, err := g.q.ApprovalDecider(ctx, *row.DecidedBy); err == nil {
			v.DecidedBy = name
		}
	}
	v.CanDecide = status == StatusPending && g.mayDecide(ctx, p, row.ProjectID) == nil
	return v, nil
}

// mayDecide reports nil when p may approve or decline requests of the project:
// a signed-in Owner, Admin or Developer with the deploy scope on it.
func (g *Guard) mayDecide(ctx context.Context, p *auth.Principal, projectID uuid.UUID) error {
	if p.Type != auth.TypeUser || !policy.RoleAtLeast(p, auth.RoleDeveloper) {
		return apierr.Forbidden()
	}
	return policy.Require(ctx, policy.ScopeDeploymentsWrite, &projectID)
}

// Get returns an approval for its page. Signed-in members who can read the
// project see it; everyone else gets 404 (outside the team or project
// restriction) or 403.
func (g *Guard) Get(ctx context.Context, id uuid.UUID) (ApprovalView, error) {
	p, err := sessionUser(ctx)
	if err != nil {
		return ApprovalView{}, err
	}
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return ApprovalView{}, err
	}
	row, err := g.load(ctx, p, id)
	if err != nil {
		return ApprovalView{}, err
	}
	return g.view(ctx, p, row)
}

// Approve approves a pending request on behalf of the signed-in person.
func (g *Guard) Approve(ctx context.Context, id uuid.UUID) (ApprovalView, error) {
	return g.decide(ctx, id, true)
}

// Decline declines a pending request on behalf of the signed-in person.
func (g *Guard) Decline(ctx context.Context, id uuid.UUID) (ApprovalView, error) {
	return g.decide(ctx, id, false)
}

func (g *Guard) decide(ctx context.Context, id uuid.UUID, approve bool) (ApprovalView, error) {
	p, err := sessionUser(ctx)
	if err != nil {
		return ApprovalView{}, err
	}
	row, err := g.load(ctx, p, id)
	if err != nil {
		return ApprovalView{}, err
	}
	if err := g.mayDecide(ctx, p, row.ProjectID); err != nil {
		return ApprovalView{}, err
	}
	out, err := g.settle(ctx, row, approve, p.UserID)
	if err != nil {
		return ApprovalView{}, err
	}
	return g.view(ctx, p, out)
}

// settle records the human decision on a pending request (exactly once) and
// audits it with the actor in ctx. Decisions come from the dashboard only.
func (g *Guard) settle(ctx context.Context, row db.Approval, approve bool, by uuid.UUID) (db.Approval, error) {
	via := "web"
	status, action := StatusDeclined, AuditDeclined
	if approve {
		status, action = StatusApproved, AuditApproved
	}
	var out db.Approval
	err := db.InTx(ctx, g.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		out, err = q.DecideApproval(ctx, db.DecideApprovalParams{
			Status: status, DecidedBy: &by, DecidedVia: &via, ID: row.ID, WorkspaceID: row.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return closedError(row, g.now())
		}
		if err != nil {
			return fmt.Errorf("decide approval: %w", err)
		}
		var plan ApprovalPlan
		_ = json.Unmarshal(out.Plan, &plan)
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "approval", ResourceID: out.ID.String(), ProjectID: &out.ProjectID,
			WorkspaceID: out.WorkspaceID,
			Metadata: map[string]any{
				"tool": out.Tool, "summary": plan.Summary, "via": via, "environmentId": out.EnvironmentID.String(),
				"token": out.PrincipalLabel, "client": out.ClientName, "requestedBy": out.PrincipalID.String(),
			},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return db.Approval{}, err
		}
		return db.Approval{}, fmt.Errorf("settle approval: %w", err)
	}
	return out, nil
}

// closedError explains why a request can no longer be decided.
func closedError(row db.Approval, now time.Time) error {
	if !row.ExpiresAt.After(now) && (row.Status == StatusPending || row.Status == StatusApproved) {
		return apierr.Conflict(CodeApprovalExpired, "this approval request expired; ask the agent to try again")
	}
	return apierr.Conflict(CodeApprovalClosed, "this approval request was already "+row.Status)
}
