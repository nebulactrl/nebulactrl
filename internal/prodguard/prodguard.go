// Package prodguard is the production guard. A principal that is an MCP
// client or an API token cannot change a production environment without a
// person approving that exact call first.
//
// Domain services call Check before the mutation, so every entry point (REST
// token, MCP tool, composite) is held to the same rule. The MCP dispatch also
// calls it first for the operations the catalog lists centrally
// (internal/mcp, prodgate.go) and then passes a Covered context, so the
// domain service's own check of that call does not ask a second time. Without an approval Check returns
// *ErrApprovalRequired (HTTP 409 `approval_required`); the MCP layer turns it
// into a `confirmation_required` result with the URL of the approval page
// (/approvals/{id}). Only a signed-in person approves, on that page: the
// requesting client has no way to answer for them. The approved row of the
// approvals table is consumed exactly once by the retried call.
//
// An approval is bound to the principal, the operation, a digest of its
// arguments and the generation of every target, and lives at most ten
// minutes. A retry with other arguments, or after a target changed, starts a
// new request. The guard fails closed: when no Guard is installed a guarded
// call is refused.
//
// Browser sessions are never guarded (the UI has its own guard), nor is the
// system principal. Every API token is guarded, whatever its kind (mcp or
// rest), unless the instance admin or a team Owner/Admin created it with
// allowProductionWithoutApproval.
package prodguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// TTL is the longest an approval request or approved approval lives.
const TTL = 10 * time.Minute

// DeclinedTTL is how long a declined request keeps refusing the same call.
const DeclinedTTL = time.Minute

// Error codes of the guard (problem+json `code`).
const (
	CodeApprovalRequired    = "approval_required"
	CodeApprovalDeclined    = "approval_declined"
	CodeApprovalUnavailable = "approval_unavailable"
	CodeApprovalClosed      = "approval_closed"
	CodeApprovalExpired     = "approval_expired"
)

// Audit actions. The five outcomes of an approval are approved, declined,
// expired, refused and error; requested and used mark the other two steps.
const (
	AuditRequested = "approval.requested"
	AuditApproved  = "approval.approved"
	AuditDeclined  = "approval.declined"
	AuditExpired   = "approval.expired"
	AuditRefused   = "approval.refused"
	AuditError     = "approval.error"
	AuditUsed      = "approval.used"
)

// Approval statuses (approvals.status).
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDeclined = "declined"
	StatusUsed     = "used"
	StatusExpired  = "expired"
	StatusStale    = "stale"
)

// ApprovalTarget is something the guarded operation changes. A non-zero Generation is
// the generation the approver saw; the approval is void when it moves.
type ApprovalTarget struct {
	Kind       string    `json:"kind" doc:"service, environment, project, backup, volume or domain"`
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Generation int64     `json:"generation,omitempty"`
}

// ProdScope describes one guarded call. The calling domain service fills it
// from the scope it already loaded.
type ProdScope struct {
	WorkspaceID   uuid.UUID
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	// Project and Environment are display names.
	Project     string
	Environment string
	// IsProduction is the environment's flag. Check does nothing without it.
	IsProduction bool
	// Summary says in one sentence what will happen ("Deploy api to production").
	Summary string
	// Effects are the concrete consequences, one short sentence each.
	Effects []string
	// Irreversible marks operations that cannot be undone (deletes, restores).
	Irreversible bool
	Targets      []ApprovalTarget
}

// ApprovalPlan is what the approver reads: exactly what the call will do.
type ApprovalPlan struct {
	Op           string           `json:"op"`
	Summary      string           `json:"summary"`
	Effects      []string         `json:"effects,omitempty"`
	Irreversible bool             `json:"irreversible,omitempty"`
	Project      string           `json:"project"`
	Environment  string           `json:"environment"`
	Targets      []ApprovalTarget `json:"targets"`
}

func (s ProdScope) plan(op string) ApprovalPlan {
	t := slices.Clone(s.Targets)
	if t == nil {
		t = []ApprovalTarget{}
	}
	return ApprovalPlan{
		Op: op, Summary: s.Summary, Effects: s.Effects, Irreversible: s.Irreversible,
		Project: s.Project, Environment: s.Environment, Targets: t,
	}
}

// generations maps target id → generation for the targets that track one.
func (s ProdScope) generations() map[string]int64 {
	m := make(map[string]int64, len(s.Targets))
	for _, t := range s.Targets {
		m[t.ID.String()] = t.Generation
	}
	return m
}

// Digest returns the SHA-256 that binds an approval to one call: the
// operation and its arguments (ids, commit, message, …). The same call yields
// the same digest on retry; any other argument yields another.
func Digest(op string, args ...any) []byte {
	h := sha256.New()
	h.Write([]byte(op))
	for _, a := range args {
		h.Write([]byte{0})
		fmt.Fprint(h, a)
	}
	return h.Sum(nil)
}

// DigestIDs is Digest over a set of ids, order-independent.
func DigestIDs(op string, ids []uuid.UUID, extra ...any) []byte {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = id.String()
	}
	sort.Strings(s)
	args := append([]any{strings.Join(s, ",")}, extra...)
	return Digest(op, args...)
}

// Guarded reports whether p must obtain an approval for production changes.
// Authentication sets RequireProductionApproval on every token that holds no
// exemption, so a token is guarded unless an instance admin or team admin
// switched it off when creating it.
func Guarded(p *auth.Principal) bool {
	if p == nil {
		return false
	}
	switch p.Type {
	case auth.TypeMCP:
		return p.RequireProductionApproval || !p.AllowProductionWithoutApproval
	case auth.TypeToken:
		return p.RequireProductionApproval
	}
	return false
}

// ErrApprovalRequired is returned by Check when a human has not approved the
// call yet. It unwraps to the 409 `approval_required` problem, so HTTP callers
// get the plan and the approval URL in `details`.
type ErrApprovalRequired struct {
	Plan       ApprovalPlan
	ApprovalID uuid.UUID
	ExpiresAt  time.Time
	// URL is where a human approves: /approvals/{id}, absolute when the public
	// URL is configured.
	URL string
}

// Error implements error.
func (e *ErrApprovalRequired) Error() string {
	return "approval required: " + e.Plan.Summary
}

// Unwrap exposes the problem document.
func (e *ErrApprovalRequired) Unwrap() error {
	return apierr.Conflict(CodeApprovalRequired,
		"A human must approve this production change first: "+e.Plan.Summary).
		WithDetails(map[string]any{
			"approvalId":  e.ApprovalID.String(),
			"approvalUrl": e.URL,
			"expiresAt":   e.ExpiresAt.UTC().Format(time.RFC3339),
			"plan":        e.Plan,
		})
}

// FromProblem rebuilds the error from an `approval_required` problem+json
// response body (the in-process HTTP dispatch of the MCP layer).
func FromProblem(status int, body []byte) (*ErrApprovalRequired, bool) {
	if status != 409 {
		return nil, false
	}
	var p struct {
		Code    string `json:"code"`
		Details struct {
			ApprovalID  uuid.UUID    `json:"approvalId"`
			ApprovalURL string       `json:"approvalUrl"`
			ExpiresAt   time.Time    `json:"expiresAt"`
			Plan        ApprovalPlan `json:"plan"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Code != CodeApprovalRequired || p.Details.ApprovalID == uuid.Nil {
		return nil, false //nolint:nilerr // a body that is not a well-formed approval_required problem simply is not an approval error; ok=false reports that
	}
	return &ErrApprovalRequired{
		Plan: p.Details.Plan, ApprovalID: p.Details.ApprovalID, ExpiresAt: p.Details.ExpiresAt, URL: p.Details.ApprovalURL,
	}, true
}

// AsRequired extracts an *ErrApprovalRequired from err's chain.
func AsRequired(err error) (*ErrApprovalRequired, bool) {
	var e *ErrApprovalRequired
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

type clientKey struct{}

// WithClient records the MCP client's name (clientInfo.name) for the approval
// page. The MCP layer sets it on the context of the in-process request.
func WithClient(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientKey{}, name)
}

func clientFrom(ctx context.Context) string {
	s, _ := ctx.Value(clientKey{}).(string)
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

var active atomic.Pointer[Guard]

// Installed returns the process-wide Guard set by Install, or nil.
func Installed() *Guard { return active.Load() }

// Install makes g the Guard Check consults. The control plane installs one at
// start-up; tests install their own and reset it with prodguardtest.Uninstall.
func (g *Guard) Install() { active.Store(g) }

func gensEqual(stored []byte, want map[string]int64) bool {
	var have map[string]int64
	if err := json.Unmarshal(stored, &have); err != nil {
		return false
	}
	if len(have) != len(want) {
		return false
	}
	for k, v := range want {
		if g, ok := have[k]; !ok || g != v {
			return false
		}
	}
	return true
}

// Applies reports whether Check would guard a call by the principal in ctx on
// an environment with the given production flag. Callers use it to skip
// building a plan that no one will read.
func Applies(ctx context.Context, isProduction bool) bool {
	if !isProduction || covered(ctx) {
		return false
	}
	p, ok := auth.PrincipalFrom(ctx)
	return ok && Guarded(p)
}

type coveredKey struct{}

// Covered returns a context in which Check passes. An operation whose plan
// already includes follow-up calls (a restore that deploys the new database)
// uses it after its own Check succeeded, so the nested calls do not ask for
// a second approval. The context is in-process only; no request can carry it.
func Covered(ctx context.Context) context.Context {
	return context.WithValue(ctx, coveredKey{}, true)
}

func covered(ctx context.Context) bool {
	v, _ := ctx.Value(coveredKey{}).(bool)
	return v
}
