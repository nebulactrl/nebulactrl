package prodguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Deps are the collaborators of a Guard.
type Deps struct {
	Pool *pgxpool.Pool
	// Box seals the request state handed to MCP clients.
	Box *secretbox.Box
	// PublicURL prefixes approval links ("https://nebula.example.com"); empty
	// yields the path only.
	PublicURL string
	Logger    *slog.Logger
}

// Guard stores approvals and decides guarded calls.
type Guard struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	box       *secretbox.Box
	publicURL string
	log       *slog.Logger
	now       func() time.Time
}

// New returns a Guard (pool may be nil for OpenAPI generation).
func New(d Deps) *Guard {
	lg := d.Logger
	if lg == nil {
		lg = slog.Default()
	}
	g := &Guard{pool: d.Pool, box: d.Box, publicURL: d.PublicURL, log: lg.With("component", "prodguard"), now: time.Now}
	if d.Pool != nil {
		g.q = db.New(d.Pool)
	}
	return g
}

// Check enforces the production guard for one call. It returns nil when the
// call may proceed: the environment is not production, the principal is not
// guarded, or a human approved this exact call (the approval is consumed).
// Otherwise it returns *ErrApprovalRequired, an `approval_declined` 403 when a
// human declined it, or `approval_unavailable` 503 when approvals cannot be
// checked (the guard fails closed).
//
// op names the operation ("deploy", "rollback", …) and digest binds the
// approval to its arguments (see Digest). Call Check after the caller is
// authorized and the input validated, before the mutation.
func Check(ctx context.Context, scope ProdScope, op string, digest []byte) error {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || !scope.IsProduction || covered(ctx) || !Guarded(p) {
		return nil
	}
	g := Installed()
	if g == nil || g.q == nil {
		return apierr.New(503, CodeApprovalUnavailable, "production approvals are not available on this control plane; the change was refused")
	}
	return g.check(ctx, p, scope, op, digest)
}

func (g *Guard) check(ctx context.Context, p *auth.Principal, scope ProdScope, op string, digest []byte) error {
	if len(digest) != 32 || op == "" || scope.ProjectID == uuid.Nil || scope.EnvironmentID == uuid.Nil {
		g.record(ctx, AuditError, scope, op, nil, map[string]any{"reason": "invalid guard call"})
		return apierr.Internal()
	}
	ws := p.WorkspaceID
	now := g.now()
	gens := scope.generations()

	row, err := g.q.LatestApproval(ctx, db.LatestApprovalParams{WorkspaceID: ws, PrincipalID: p.ID(), Tool: op, ArgsDigest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return g.request(ctx, p, scope, op, digest)
	}
	if err != nil {
		return g.fail(ctx, scope, op, fmt.Errorf("find approval: %w", err))
	}

	if !row.ExpiresAt.After(now) {
		if row.Status == StatusPending || row.Status == StatusApproved {
			g.close(ctx, scope, op, row, StatusExpired, "expired")
		}
		return g.request(ctx, p, scope, op, digest)
	}
	if row.Status != StatusDeclined && !gensEqual(row.Generations, gens) {
		g.close(ctx, scope, op, row, StatusStale, "generation_moved")
		return g.request(ctx, p, scope, op, digest)
	}

	switch row.Status {
	case StatusDeclined:
		g.record(ctx, AuditRefused, scope, op, &row, map[string]any{"reason": "declined"})
		e := apierr.New(403, CodeApprovalDeclined, "A human declined this production change: "+scope.Summary).
			WithDetails(map[string]any{"approvalId": row.ID.String()})
		return e
	case StatusApproved:
		used, err := g.q.UseApproval(ctx, row.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Consumed or expired between the read and now.
			return g.request(ctx, p, scope, op, digest)
		}
		if err != nil {
			return g.fail(ctx, scope, op, fmt.Errorf("use approval: %w", err))
		}
		g.record(ctx, AuditUsed, scope, op, &used, nil)
		return nil
	default: // pending
		return g.required(row, scope, op)
	}
}

// request opens a new approval request and returns the error that carries it.
func (g *Guard) request(ctx context.Context, p *auth.Principal, scope ProdScope, op string, digest []byte) error {
	plan, err := json.Marshal(scope.plan(op))
	if err != nil {
		return g.fail(ctx, scope, op, fmt.Errorf("marshal plan: %w", err))
	}
	gens, err := json.Marshal(scope.generations())
	if err != nil {
		return g.fail(ctx, scope, op, fmt.Errorf("marshal generations: %w", err))
	}
	ptype := auth.TypeToken
	if p.Type == auth.TypeMCP {
		ptype = auth.TypeMCP
	}
	row, err := g.q.InsertApproval(ctx, db.InsertApprovalParams{
		WorkspaceID: p.WorkspaceID, ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID,
		PrincipalID: p.ID(), PrincipalType: ptype, PrincipalLabel: p.Label, ClientName: clientFrom(ctx),
		Tool: op, ArgsDigest: digest, Plan: plan, Generations: gens, ExpiresAt: g.now().Add(TTL),
	})
	if err != nil {
		return g.fail(ctx, scope, op, fmt.Errorf("insert approval: %w", err))
	}
	_, _ = g.q.PruneApprovals(ctx)
	g.record(ctx, AuditRequested, scope, op, &row, nil)
	return g.required(row, scope, op)
}

func (g *Guard) required(row db.Approval, scope ProdScope, op string) *ErrApprovalRequired {
	return &ErrApprovalRequired{
		Plan: scope.plan(op), ApprovalID: row.ID, ExpiresAt: row.ExpiresAt, URL: g.approvalURL(row.ID),
	}
}

func (g *Guard) approvalURL(id uuid.UUID) string {
	return g.publicURL + "/approvals/" + id.String()
}

// close moves an open approval to a closed status and audits the outcome.
func (g *Guard) close(ctx context.Context, scope ProdScope, op string, row db.Approval, status, reason string) {
	closed, err := g.q.SetApprovalStatus(ctx, db.SetApprovalStatusParams{ID: row.ID, FromStatus: row.Status, Status: status})
	if err != nil {
		return // someone else closed it first
	}
	g.record(ctx, AuditExpired, scope, op, &closed, map[string]any{"reason": reason, "was": row.Status})
}

// fail records an infrastructure error and refuses the call.
func (g *Guard) fail(ctx context.Context, scope ProdScope, op string, err error) error {
	g.log.ErrorContext(ctx, "production guard", "op", op, "error", err)
	g.record(ctx, AuditError, scope, op, nil, map[string]any{"error": "approval store failure"})
	return apierr.New(503, CodeApprovalUnavailable, "production approvals could not be checked; the change was refused")
}

// record writes one audit row for the principal in ctx. It never fails the
// call: the guard's verdict does not depend on the audit write.
func (g *Guard) record(ctx context.Context, action string, scope ProdScope, op string, row *db.Approval, meta map[string]any) {
	md := map[string]any{"tool": op, "summary": scope.Summary, "environmentId": scope.EnvironmentID.String()}
	if c := clientFrom(ctx); c != "" {
		md["client"] = c
	}
	rid := ""
	if row != nil {
		rid = row.ID.String()
		md["expiresAt"] = row.ExpiresAt.UTC().Format(time.RFC3339)
	}
	for k, v := range meta {
		md[k] = v
	}
	proj := scope.ProjectID
	if proj == uuid.Nil {
		g.log.WarnContext(ctx, "production guard audit without project", "action", action)
	}
	err := db.InTx(ctx, g.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "approval", ResourceID: rid, ProjectID: optProject(proj), Metadata: md,
			WorkspaceID: scope.WorkspaceID,
		})
	})
	if err != nil {
		g.log.ErrorContext(ctx, "production guard audit", "action", action, "error", err)
	}
}

func optProject(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
