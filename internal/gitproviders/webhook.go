package gitproviders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/nebulactrl/nebulactrl/internal/db"
)

const (
	// maxWebhookBody bounds a delivery (GitHub caps payloads at 25 MB; pushes
	// that large carry nothing NebulaCtrl needs).
	maxWebhookBody = 10 << 20
	// DeliveryIDTTL is how long a delivery id and the hash of a payload are
	// remembered: a second delivery with the same id, or with the same signed
	// payload under any id, inside it is a replay and is rejected. The
	// providers sign the body only, so the delivery id header proves nothing
	// by itself.
	DeliveryIDTTL = 7 * 24 * time.Hour
)

// Mount attaches the webhook endpoint at the root router. It is
// unauthenticated: every delivery is verified against its connection's secret.
func (s *Service) Mount(r chi.Router) {
	r.Post("/hooks/git/{provider}/{connectionId}", s.handleWebhook)
}

type webhookReply struct {
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
}

func writeReply(w http.ResponseWriter, code int, r webhookReply) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(r)
}

// secretHeaderParts are header name fragments whose values are never logged.
var secretHeaderParts = []string{"signature", "token", "authorization", "secret", "cookie", "key", "password"}

// redactHeaders flattens request headers for the delivery log, replacing
// credentials and signatures.
func redactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		val := strings.Join(v, ", ")
		lk := strings.ToLower(k)
		for _, p := range secretHeaderParts {
			if strings.Contains(lk, p) && lk != "idempotency-key" {
				val = "[redacted]"
				break
			}
		}
		if len(val) > 300 {
			val = val[:300] + "…"
		}
		out[http.CanonicalHeaderKey(k)] = val
	}
	return out
}

// delivery is the log entry of one webhook request.
type delivery struct {
	connectionID uuid.UUID
	workspaceID  uuid.UUID
	deliveryID   string
	event        string
	outcome      string
	status       int
	summary      string
	repo, branch string
	sha          string
	headers      http.Header
	bytes        int
	deployments  []uuid.UUID
}

func (s *Service) logDelivery(ctx context.Context, d delivery) {
	hdr, _ := json.Marshal(redactHeaders(d.headers))
	deps := d.deployments
	if deps == nil {
		deps = []uuid.UUID{}
	}
	if _, err := s.q.InsertGitDelivery(ctx, db.InsertGitDeliveryParams{
		ConnectionID: d.connectionID, DeliveryID: d.deliveryID, Event: d.event, Outcome: d.outcome, HttpStatus: int32(d.status),
		Summary: truncate(d.summary, 500), RepoFullName: d.repo, Branch: d.branch, CommitSha: d.sha, Headers: hdr,
		PayloadBytes: int32(d.bytes), Deployments: deps,
	}); err != nil {
		s.log.Error("log git delivery", "err", err)
		return
	}
	publish(s.bus, EventDelivery, d.workspaceID, d.connectionID)
}

// handleWebhook verifies, deduplicates, logs and acts on one delivery.
func (s *Service) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := chi.URLParam(r, "provider")
	connID, err := uuid.Parse(chi.URLParam(r, "connectionId"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	conn, err := s.q.GetGitConnectionInternal(ctx, connID) // authenticated below by the connection's own webhook secret
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && conn.Provider != provider) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("webhook: load connection", "err", err)
		writeReply(w, http.StatusInternalServerError, webhookReply{Status: "error"})
		return
	}
	drv := driverFor(Provider(conn.Provider))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	d := delivery{connectionID: conn.ID, workspaceID: conn.WorkspaceID, headers: r.Header, bytes: len(body)}
	if err != nil {
		d.outcome, d.status, d.summary = "rejected", http.StatusRequestEntityTooLarge, "payload too large or unreadable"
		s.logRejected(ctx, d)
		writeReply(w, d.status, webhookReply{Status: "rejected", Summary: d.summary})
		return
	}

	secret, serr := s.webhookSecret(conn)
	if serr != nil || !drv.verify(r.Header, body, secret) {
		d.outcome, d.status, d.summary = "rejected", http.StatusUnauthorized, "signature verification failed"
		s.logRejected(ctx, d)
		writeReply(w, d.status, webhookReply{Status: "rejected", Summary: d.summary})
		return
	}

	d.deliveryID = drv.deliveryID(r.Header)
	sum := sha256.Sum256(body)
	bodyKey := "body-" + hex.EncodeToString(sum[:16])
	if d.deliveryID == "" {
		d.deliveryID = bodyKey
	}
	keys := []string{d.deliveryID}
	if bodyKey != d.deliveryID {
		keys = append(keys, bodyKey)
	}
	fresh := true
	for i, key := range keys {
		n, err := s.q.InsertGitDeliveryID(ctx, db.InsertGitDeliveryIDParams{ConnectionID: conn.ID, DeliveryID: key})
		if err != nil {
			s.log.Error("webhook: store delivery id", "err", err)
			s.forgetDeliveryKeys(ctx, conn.ID, keys[:i])
			writeReply(w, http.StatusInternalServerError, webhookReply{Status: "error"})
			return
		}
		if n == 0 {
			fresh = false
			s.forgetDeliveryKeys(ctx, conn.ID, keys[:i]) // keep only what was seen before
			break
		}
	}
	if !fresh {
		d.outcome, d.status, d.summary = "replayed", http.StatusConflict, "this delivery was already processed: its id or its payload was seen in the last 7 days"
		d.event = firstHeader(r.Header, "X-GitHub-Event", "X-Gitea-Event", "X-Forgejo-Event", "X-Gitlab-Event", "X-Event-Key")
		s.logDelivery(ctx, d)
		writeReply(w, d.status, webhookReply{Status: "replayed", Summary: d.summary})
		return
	}

	p, perr := drv.parse(r.Header, body)
	d.event = p.event
	if perr != nil {
		// The signature was valid, so the sender is the provider: a payload we
		// cannot read is logged and answered 202 (retrying cannot help).
		d.outcome, d.status, d.summary = "error", http.StatusAccepted, perr.Error()
		s.logDelivery(ctx, d)
		writeReply(w, d.status, webhookReply{Status: "error", Summary: d.summary})
		return
	}
	res, err := s.process(ctx, conn, p, &d)
	if err != nil {
		// Let the provider retry: forget the id and the payload so the retry is not a replay.
		s.forgetDeliveryKeys(ctx, conn.ID, keys)
		s.log.Error("webhook processing failed", "connection", conn.Name, "event", d.event, "err", err)
		d.outcome, d.status, d.summary = "error", http.StatusInternalServerError, "processing failed: "+err.Error()
		s.logDelivery(ctx, d)
		writeReply(w, d.status, webhookReply{Status: "error", Summary: "processing failed"})
		return
	}
	d.outcome, d.status, d.summary = res.outcome, http.StatusOK, res.summary
	s.logDelivery(ctx, d)
	writeReply(w, d.status, webhookReply{Status: res.outcome, Summary: res.summary})
}

// forgetDeliveryKeys drops replay keys recorded for a delivery.
func (s *Service) forgetDeliveryKeys(ctx context.Context, conn uuid.UUID, keys []string) {
	for _, k := range keys {
		_ = s.q.DeleteGitDeliveryID(context.WithoutCancel(ctx), db.DeleteGitDeliveryIDParams{ConnectionID: conn, DeliveryID: k})
	}
}

// logRejected logs a failed delivery unless the connection already logged too
// many of them recently.
func (s *Service) logRejected(ctx context.Context, d delivery) {
	if s.rejectLimiter(d.connectionID).Allow() {
		s.logDelivery(ctx, d)
	}
}

type processResult struct {
	outcome string // processed | ignored
	summary string
}

// process acts on a verified, parsed delivery.
func (s *Service) process(ctx context.Context, conn db.GitConnection, p parsed, d *delivery) (processResult, error) {
	switch p.kind {
	case kindPing:
		return processResult{outcome: "processed", summary: "ping received: the webhook works"}, nil
	case kindInstallation:
		if conn.AuthType == AuthGitHubApp && decodeMeta(conn.Meta).InstallationID == 0 {
			if err := s.activateInstallation(ctx, conn, p.installationID); err != nil {
				s.log.Warn("activate installation from webhook", "err", err)
			}
			return processResult{outcome: "processed", summary: fmt.Sprintf("GitHub App installation %d recorded", p.installationID)}, nil
		}
		return processResult{outcome: "ignored", summary: "installation already recorded"}, nil
	case kindPush:
		var summaries []string
		for _, push := range p.pushes {
			push.ConnectionID, push.Provider = conn.ID, Provider(conn.Provider)
			if d.repo == "" {
				d.repo, d.branch, d.sha = push.RepoFullName, push.Branch, push.CommitSHA
			}
			ids, summary, err := s.onPush(ctx, conn, push)
			if err != nil {
				return processResult{}, err
			}
			d.deployments = append(d.deployments, ids...)
			summaries = append(summaries, summary)
			ev := push
			s.fire(func(ctx context.Context) { s.firePush(ctx, ev) })
		}
		return processResult{outcome: "processed", summary: strings.Join(summaries, "; ")}, nil
	case kindPullRequest:
		pr := *p.pr
		pr.ConnectionID, pr.Provider = conn.ID, Provider(conn.Provider)
		d.repo, d.branch, d.sha = pr.RepoFullName, pr.HeadBranch, pr.HeadSHA
		s.fire(func(ctx context.Context) { s.firePullRequest(ctx, pr) })
		return processResult{outcome: "processed", summary: fmt.Sprintf("pull request #%d %s", pr.Number, pr.Action)}, nil
	}
	return processResult{outcome: "ignored", summary: p.reason}, nil
}

func (s *Service) firePush(ctx context.Context, ev PushEvent) {
	s.subMu.RLock()
	subs := append([]func(context.Context, PushEvent){}, s.pushSubs...)
	s.subMu.RUnlock()
	for _, fn := range subs {
		fn(ctx, ev)
	}
}

func (s *Service) firePullRequest(ctx context.Context, ev PullRequestEvent) {
	s.subMu.RLock()
	subs := append([]func(context.Context, PullRequestEvent){}, s.prSubs...)
	s.subMu.RUnlock()
	for _, fn := range subs {
		fn(ctx, ev)
	}
}
