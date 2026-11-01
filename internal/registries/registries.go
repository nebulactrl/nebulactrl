// Package registries manages the OCI registries images are pushed to and pulled
// from when a build runs on a build server.
//
// Two kinds exist:
//
//   - external: any registry that takes basic auth (GHCR, Docker Hub, GitLab,
//     Harbor, a self-hosted one). The push credential and the optional read-only
//     pull credential are sealed with the instance key and write-only: no API
//     ever returns them.
//   - builtin: the team's slice of the instance registry on the control-plane
//     host (internal/instsvc). Servers reach it through the agent's local
//     endpoint, never over a public name. The control plane signs a short-lived
//     ES256 token for exactly one repository inside the team's prefix at send
//     time (token.go); nothing about a credential is stored.
//
// The deployment code asks for a credential per command with Credentials; it
// is attached at send time and never persisted in a payload.
package registries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Kinds of registry.
const (
	KindExternal = "external"
	KindBuiltin  = "builtin"
)

// EventUpdated is published whenever a registry or its check result changes.
const EventUpdated = "registry.updated"

// Audit actions.
const (
	AuditCreate = "registry.create"
	AuditUpdate = "registry.update"
	AuditDelete = "registry.delete"
	AuditCheck  = "registry.check"
	AuditSweep  = "registry.sweep"
)

// Limits.
const (
	maxNameLen      = 80
	maxNamespaceLen = 200
	maxHostLen      = 253
	maxCredLen      = 4096
)

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI generation.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Jobs       *jobs.Manager
	Box        *secretbox.Box
	Dispatcher agents.Dispatcher
	// HTTP talks to the built-in registry (the retention sweep). Tests replace it.
	HTTP *http.Client
	// BuiltinURL is the built-in registry's address on the internal network
	// (NEBULACTRL_INSTANCE_REGISTRY_URL); empty when it is not configured.
	BuiltinURL string
	Logger     *slog.Logger
	Now        func() time.Time
}

// Service implements the registry use cases.
type Service struct {
	Deps
	q *db.Queries

	limiters map[string]*rate.Limiter

	// checkWait is how long Check waits for the agent (a field so tests shorten it).
	checkWait time.Duration
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	s := &Service{Deps: d, limiters: map[string]*rate.Limiter{}, checkWait: defaultCheckWait}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// publish announces a change of registry id to its team ws.
func (s *Service) publish(ws, id uuid.UUID) {
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: EventUpdated, ResourceID: id.String(), WorkspaceID: ws.String()})
	}
}

// workspace authorizes the caller for scope and returns the team.
func (s *Service) workspace(ctx context.Context, scope string) (uuid.UUID, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return uuid.Nil, err
	}
	return policy.WorkspaceID(ctx), nil
}

func (s *Service) load(ctx context.Context, ws, id uuid.UUID) (db.Registry, error) {
	r, err := s.q.GetRegistry(ctx, db.GetRegistryParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apierr.NotFound("registry")
	}
	if err != nil {
		return r, fmt.Errorf("get registry: %w", err)
	}
	return r, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ---- validation -------------------------------------------------------------------------

var (
	hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// A namespace is one or more repository path components: lowercase
	// alphanumerics separated by single ".", "_", "__" or "-" (the OCI
	// distribution name grammar).
	nsComponentRe = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*$`)
)

// NormalizeHost validates a registry host (registry.example.com[:port], no
// scheme, path or credentials) and returns it in lower case.
func NormalizeHost(raw string) (string, error) {
	bad := func(msg string) error { return apierr.Unprocessable("validation_failed", msg) }
	h := strings.ToLower(strings.TrimSpace(raw))
	if h == "" {
		return "", bad("host is required")
	}
	if strings.Contains(h, "://") {
		return "", bad("host must not include a scheme: use registry.example.com, not https://registry.example.com")
	}
	if len(h) > maxHostLen {
		return "", bad("host is too long")
	}
	if strings.ContainsAny(h, "/@?# \t\r\n\\") {
		return "", bad("host must be a bare hostname with an optional port (registry.example.com:5000), without a path, credentials or spaces")
	}
	name, port := h, ""
	if strings.HasPrefix(h, "[") { // [::1]:5000
		end := strings.Index(h, "]")
		if end < 0 {
			return "", bad("invalid IPv6 host")
		}
		name = h[1:end]
		rest := h[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", bad("invalid host")
			}
			port = rest[1:]
		}
		if ip := net.ParseIP(name); ip == nil || ip.To4() != nil {
			return "", bad("invalid IPv6 host")
		}
	} else {
		if i := strings.LastIndex(h, ":"); i >= 0 {
			name, port = h[:i], h[i+1:]
		}
		if strings.Contains(name, ":") {
			return "", bad("an IPv6 host needs brackets, for example [::1]:5000")
		}
		if net.ParseIP(name) == nil {
			for _, l := range strings.Split(name, ".") {
				if !hostLabelRe.MatchString(l) {
					return "", bad("host is not a valid hostname")
				}
			}
		}
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", bad("port must be a number from 1 to 65535")
		}
	}
	return h, nil
}

// NormalizeNamespace validates the path between the host and the repository name
// (a GitHub user or organization, a Docker Hub namespace, a GitLab group/project).
// Empty is allowed.
func NormalizeNamespace(raw string) (string, error) {
	ns := strings.Trim(strings.ToLower(strings.TrimSpace(raw)), "/")
	if ns == "" {
		return "", nil
	}
	if len(ns) > maxNamespaceLen {
		return "", apierr.Unprocessable("validation_failed", "namespace is too long")
	}
	for _, c := range strings.Split(ns, "/") {
		if !nsComponentRe.MatchString(c) {
			return "", apierr.Unprocessable("validation_failed",
				"namespace must be lowercase letters, digits and . _ - separators, with / between parts (for example my-org or group/project)")
		}
	}
	return ns, nil
}

func validateName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || len(n) > maxNameLen {
		return "", apierr.Unprocessable("validation_failed", fmt.Sprintf("name must be 1-%d characters", maxNameLen))
	}
	return n, nil
}

func validateCredential(what, user, pass string) error {
	if user == "" || pass == "" {
		return apierr.Unprocessable("validation_failed", what+" needs a username and a password")
	}
	if len(user) > maxCredLen || len(pass) > maxCredLen {
		return apierr.Unprocessable("validation_failed", what+" is too long")
	}
	if strings.ContainsAny(user, ":\r\n\x00") || strings.ContainsAny(pass, "\r\n\x00") {
		return apierr.Unprocessable("validation_failed", what+" must not contain a colon in the username, newlines or NUL")
	}
	return nil
}

// IsLoopbackHost reports whether host (with optional port) is a loopback
// address: "localhost", a name under .localhost, 127.0.0.0/8 or ::1. Docker
// daemons accept a plain-http registry only there; everything else is https.
func IsLoopbackHost(host string) bool {
	h := strings.ToLower(host)
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end > 0 {
			h = h[1:end]
		}
	} else if i := strings.LastIndex(h, ":"); i >= 0 && strings.Count(h, ":") == 1 {
		h = h[:i]
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---- sealed credentials ----------------------------------------------------------------

// credential is the sealed form of a basic-auth credential.
type credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) seal(col secretbox.Column, id uuid.UUID, user, pass string) ([]byte, error) {
	raw, err := json.Marshal(credential{Username: user, Password: pass})
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(raw)
	return s.Box.Seal(col, id.String(), raw)
}

func (s *Service) open(col secretbox.Column, id uuid.UUID, enc []byte) (credential, error) {
	var c credential
	if len(enc) == 0 {
		return c, nil
	}
	raw, err := s.Box.Open(col, id.String(), enc)
	if err != nil {
		return c, fmt.Errorf("decrypt registry credential: %w", err)
	}
	defer secretbox.Wipe(raw)
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("decode registry credential: %w", err)
	}
	return c, nil
}
