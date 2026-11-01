package agents

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/clientip"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// EnrollmentTokenPrefix starts every enrollment token.
const EnrollmentTokenPrefix = "nct_enroll_"

// EnrollmentTokenTTL is how long an enrollment token can be redeemed.
const EnrollmentTokenTTL = time.Hour

// NewEnrollmentToken returns a fresh token (nct_enroll_ + 32 random bytes in
// lowercase base32) and its SHA-256 hash, the only form that is stored.
func NewEnrollmentToken() (token string, hash []byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	token = EnrollmentTokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
	return token, HashEnrollmentToken(token), nil
}

// HashEnrollmentToken returns the stored hash of a token.
func HashEnrollmentToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

var errInvalidToken = apierr.New(http.StatusUnauthorized, "invalid_enrollment_token", "enrollment token is invalid, expired, revoked or already used")

// handleEnroll implements POST /agent/v1/enroll.
func (h *Hub) handleEnroll(w http.ResponseWriter, r *http.Request) {
	// The proxy-aware client address: behind a reverse proxy r.RemoteAddr is the
	// proxy, which would put every enrolment in one bucket.
	host := clientip.String(r)
	if host == "" {
		host = r.RemoteAddr
	}
	if !h.enrollLimit.Allow(host) {
		w.Header().Set("Retry-After", "60")
		problem(w, apierr.TooManyRequests("too many enrollment attempts; retry later"))
		return
	}
	var req agentprotocol.EnrollRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		problem(w, apierr.BadRequest("invalid_body", "request body must be a JSON enrollment request"))
		return
	}
	if len(req.PublicKey) != ed25519.PublicKeySize {
		problem(w, apierr.BadRequest("invalid_public_key", "publicKey must be a 32-byte Ed25519 public key"))
		return
	}
	if !strings.HasPrefix(req.Token, EnrollmentTokenPrefix) {
		problem(w, errInvalidToken)
		return
	}
	resp, err := h.Enroll(r.Context(), req)
	if err != nil {
		if ae, ok := apierr.As(err); ok {
			problem(w, ae)
			return
		}
		h.log.ErrorContext(r.Context(), "enroll agent", "err", err)
		problem(w, apierr.Internal())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// Enroll redeems a one-time enrollment token: it registers the agent's public
// key for the token's server, records its facts and marks the token used, all
// in one transaction (so a token can never enroll two agents).
func (h *Hub) Enroll(ctx context.Context, req agentprotocol.EnrollRequest) (agentprotocol.EnrollResponse, error) {
	var out agentprotocol.EnrollResponse
	var serverID uuid.UUID
	err := db.InTx(ctx, h.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		tok, err := q.GetEnrollmentTokenForUpdate(ctx, HashEnrollmentToken(req.Token))
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalidToken
		}
		if err != nil {
			return err
		}
		if tok.UsedAt != nil || tok.RevokedAt != nil || !tok.ExpiresAt.After(h.now()) {
			return errInvalidToken
		}
		srv, err := q.GetServerByID(ctx, tok.ServerID)
		if err != nil {
			return err
		}
		if srv.Status == "revoked" {
			return errInvalidToken
		}
		ident, err := q.CreateAgentIdentity(ctx, db.CreateAgentIdentityParams{ServerID: tok.ServerID, PublicKey: req.PublicKey})
		if err != nil {
			if isUniqueViolation(err) {
				return apierr.Conflict("already_enrolled", "this server already has an enrolled agent")
			}
			return err
		}
		if err := q.MarkEnrollmentTokenUsed(ctx, tok.ID); err != nil {
			return err
		}
		facts, _ := json.Marshal(req.Facts)
		if _, err := q.UpdateServerEnrolled(ctx, db.UpdateServerEnrolledParams{ID: tok.ServerID, Hostname: strings.TrimSpace(req.Hostname), Facts: facts}); err != nil {
			return err
		}
		serverID = tok.ServerID
		out = agentprotocol.EnrollResponse{ServerID: tok.ServerID.String(), AgentID: ident.ID.String()}
		return audit.Record(ctx, tx, audit.Entry{
			WorkspaceID: tok.WorkspaceID,
			Action:      "server.enroll", ResourceType: "server", ResourceID: tok.ServerID.String(),
			Metadata: map[string]any{"hostname": req.Hostname, "agentId": ident.ID.String()},
		})
	})
	if err != nil {
		return out, err
	}
	h.publishServer(serverID)
	return out, nil
}

// ---- per-IP rate limiting ----

type ipLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	entries map[string]*ipEntry
	lastGC  time.Time
}

type ipEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// newIPLimiter allows n events per interval per key.
func newIPLimiter(n int, interval time.Duration) *ipLimiter {
	return &ipLimiter{limit: rate.Every(interval / time.Duration(n)), burst: n, entries: map[string]*ipEntry{}}
}

func (l *ipLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) > 5*time.Minute {
		for k, e := range l.entries {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.entries, k)
			}
		}
		l.lastGC = now
	}
	e, ok := l.entries[key]
	if !ok {
		e = &ipEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = e
	}
	e.seen = now
	return e.lim.Allow()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
