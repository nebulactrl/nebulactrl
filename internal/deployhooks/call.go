package deployhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// maxBody bounds a delivery; registry payloads are a few kilobytes.
const maxBody = 1 << 20

// Outcomes of an authenticated call.
const (
	OutcomeDeploying = "deploying"
	OutcomeQueued    = "queued"
	OutcomeIgnored   = "ignored"
	outcomeRejected  = "rejected"
)

// CallResult is the JSON body of a hook call (HTTP 202).
type CallResult struct {
	// Status is deploying (a deployment was created), queued (a deployment of
	// the service is running; this call deploys when it ends) or ignored.
	Status  string `json:"status"`
	Ignored bool   `json:"ignored"`
	Reason  string `json:"reason,omitempty"`
	// DeploymentID is the created deployment (only for status deploying).
	DeploymentID *uuid.UUID `json:"deploymentId,omitempty"`
	// Image is the image reference this call deploys when it overrides the
	// configured one (?tag= / ?digest=).
	Image string `json:"image,omitempty"`
}

// pendingCall is the call waiting for the running deployment of a service.
type pendingCall struct {
	ImageRef string    `json:"imageRef"`
	Source   string    `json:"source"`
	QueuedAt time.Time `json:"queuedAt"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleCall authenticates by the secret in the path, rate-limits per hook and
// deploys the service. A wrong secret, a disabled hook, a deleted service and
// a malformed id all answer the same 404.
func (s *Service) handleCall(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	notFound := func() { apierr.Write(w, apierr.NotFound("deploy hook")) }
	id, perr := uuid.Parse(chi.URLParam(r, "serviceId"))
	var h db.GetDeployHookForCallRow
	found := false
	if perr == nil {
		var err error
		h, err = s.q.GetDeployHookForCall(ctx, id)
		switch {
		case err == nil:
			found = true
		case errors.Is(err, pgx.ErrNoRows):
		default:
			s.Logger.Error("deploy hook: load", "err", err)
			apierr.Write(w, apierr.Internal())
			return
		}
	}
	matched := secretMatches(chi.URLParam(r, "secret"), h.SecretHash)
	if !found || !h.Enabled || !matched {
		notFound()
		return
	}
	if !s.allow(id) {
		w.Header().Set("Retry-After", fmt.Sprint(int(rateEvery.Seconds())))
		apierr.Write(w, apierr.TooManyRequests("this deploy hook is limited to 6 calls per minute; retry shortly"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusRequestEntityTooLarge, "payload_too_large", "the payload is too large or unreadable"))
		return
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, id)
	if err != nil {
		notFound()
		return
	}
	q := r.URL.Query()
	res, err := s.call(ctx, sc, parsePayload(r.Header, body), q.Get("tag"), q.Get("digest"))
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			apierr.Write(w, ae)
			return
		}
		s.Logger.Error("deploy hook: call", "service", id.String(), "err", err)
		apierr.Write(w, apierr.Internal())
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

// systemCtx carries the control plane's own principal for the service's
// project: deployments and audit rows it creates read "Deploy hook".
func systemCtx(ctx context.Context, sc services.Scope) context.Context {
	return auth.WithPrincipal(ctx, auth.System(sc.WorkspaceID, sc.ProjectID, "Deploy hook", policy.ScopeDeploymentsWrite))
}

// call decides what a verified call does, does it and audits it.
func (s *Service) call(ctx context.Context, sc services.Scope, push *registryPush, tag, digest string) (CallResult, error) {
	source := SourceGeneric
	if push != nil {
		source = push.Source
	}
	ref, ignore, oerr := decide(sc, push, tag, digest)
	if oerr != nil {
		s.record(ctx, sc, source, outcomeRejected, "", oerr.msg, nil)
		return CallResult{}, apierr.Unprocessable(oerr.code, oerr.msg)
	}
	if ignore != "" {
		s.record(ctx, sc, source, OutcomeIgnored, "", ignore, nil)
		return CallResult{Status: OutcomeIgnored, Ignored: true, Reason: ignore}, nil
	}
	note := ""
	if push != nil {
		note = fmt.Sprintf("%s push %s:%s", map[string]string{SourceDockerHub: "Docker Hub", SourceGHCR: "GHCR"}[push.Source], push.Image.Repo(), push.Tag)
		if len(note) > 200 {
			note = note[:200]
		}
	}
	res, err := s.start(ctx, sc, ref, source, note)
	if err != nil {
		return CallResult{}, err
	}
	s.record(ctx, sc, source, res.Status, ref, "", res.DeploymentID)
	return res, nil
}

// decide maps a call to the image reference to deploy ("" = the configured
// one), or to the reason it is ignored, or to a 422.
func decide(sc services.Scope, push *registryPush, tag, digest string) (ref, ignore string, oerr *overrideError) {
	cfg := sc.Config()
	isImage := sc.Service.SourceType == services.SourceImage
	if push != nil {
		switch {
		case push.Ignore != "":
			return "", push.Ignore, nil
		case !isImage:
			return "", "this service does not run a registry image, so a registry push does not apply", nil
		}
		svc, err := parseImage(cfg.Image)
		if err != nil {
			return "", "the service image is not a plain image reference", nil //nolint:nilerr // parseImage fails only with errBadRef; a service image that is no plain reference is the documented ignore reason of a registry push, not a failure of the call
		}
		if !sameRepo(svc, push.Image) {
			return "", fmt.Sprintf("%s:%s is not the image of this service (%s)", push.Image.Repo(), push.Tag, svc.Repo()), nil
		}
		want := svc.effectiveTag()
		switch {
		case want == "":
			return "", "the service image is pinned by digest, so no tag push applies", nil
		case want != push.Tag:
			return "", fmt.Sprintf("pushed tag %q does not match the service's tag %q", push.Tag, want), nil
		}
		return "", "", nil
	}
	if tag != "" || digest != "" {
		if !isImage {
			return "", "", &overrideError{"validation_failed", "tag and digest apply to image services only"}
		}
		ref, oerr = resolveOverride(cfg.Image, tag, digest)
	}
	return ref, "", oerr
}

// start creates the deployment, or queues the call when the service is
// deploying (the newest call replaces an older queued one).
func (s *Service) start(ctx context.Context, sc services.Scope, ref, source, note string) (CallResult, error) {
	v, err := s.Deployer.DeployWith(systemCtx(ctx, sc), sc.Service.ID, deployments.TriggerDeployHook, deployments.InternalOptions{ImageRef: ref, Message: note})
	if err == nil {
		return CallResult{Status: OutcomeDeploying, DeploymentID: &v.ID, Image: ref}, nil
	}
	if ae, ok := apierr.As(err); ok && ae.Code == "deployment_in_progress" {
		raw, _ := json.Marshal(pendingCall{ImageRef: ref, Source: source, QueuedAt: time.Now().UTC()})
		if qerr := s.q.SetDeployHookPending(ctx, db.SetDeployHookPendingParams{ServiceID: sc.Service.ID, Pending: raw}); qerr != nil {
			return CallResult{}, fmt.Errorf("queue deploy hook call: %w", qerr)
		}
		return CallResult{Status: OutcomeQueued, Reason: "a deployment of this service is running; this call deploys when it has finished", Image: ref}, nil
	}
	return CallResult{}, err
}

// record audits a call (deployment.hook) and stamps the hook. It never fails
// the call: the deployment already exists.
func (s *Service) record(ctx context.Context, sc services.Scope, source, outcome, ref, reason string, dep *uuid.UUID) {
	meta := map[string]any{"service": sc.Service.Slug, "source": source, "outcome": outcome}
	if ref != "" {
		meta["image"] = ref
	}
	if reason != "" {
		meta["reason"] = truncate(reason, 300)
	}
	if dep != nil {
		meta["deploymentId"] = dep.String()
	}
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := db.New(tx).TouchDeployHook(ctx, db.TouchDeployHookParams{ServiceID: sc.Service.ID, LastOutcome: touchOutcome(outcome)}); err != nil {
			return err
		}
		return audit.Record(systemCtx(ctx, sc), tx, audit.Entry{
			Action: "deployment.hook", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: meta, WorkspaceID: sc.WorkspaceID,
		})
	})
	if err != nil {
		s.Logger.Error("deploy hook: audit", "service", sc.Service.ID.String(), "err", err)
	}
}

func touchOutcome(o string) string {
	if o == outcomeRejected {
		return OutcomeIgnored
	}
	return o
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// Sweep deploys the queued call of every hook whose service has no running
// deployment. A call whose image no longer belongs to the service's
// repository (the service image changed meanwhile) is dropped.
func (s *Service) Sweep(ctx context.Context) error {
	ids, err := s.q.ListDeployHooksIdleWithPending(ctx)
	if err != nil {
		return fmt.Errorf("list queued hook calls: %w", err)
	}
	for _, id := range ids {
		raw, err := s.q.ClaimDeployHookPending(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("claim queued hook call: %w", err)
		}
		var pc pendingCall
		if json.Unmarshal(raw, &pc) != nil {
			continue
		}
		sc, err := services.LoadScopeInternal(ctx, s.q, id)
		if err != nil {
			continue
		}
		if pc.ImageRef != "" {
			if sc.Service.SourceType != services.SourceImage || !sameRepoRef(sc.Config().Image, pc.ImageRef) {
				s.Logger.Warn("deploy hook: queued call dropped, its image is outside the service's repository", "service", sc.Service.Slug)
				continue
			}
		}
		res, err := s.start(ctx, sc, pc.ImageRef, pc.Source, "Deploy hook call that waited for the previous deployment")
		if err != nil {
			s.Logger.Warn("deploy hook: queued call failed", "service", sc.Service.Slug, "err", err)
			continue
		}
		s.Logger.Info("deploy hook: queued call", "service", sc.Service.Slug, "outcome", res.Status)
		s.record(ctx, sc, pc.Source, res.Status, pc.ImageRef, "queued call", res.DeploymentID)
	}
	return nil
}

func sameRepoRef(a, b string) bool {
	x, err1 := parseImage(a)
	y, err2 := parseImage(b)
	return err1 == nil && err2 == nil && sameRepo(x, y)
}
