// Package deploykeys manages the SSH deploy keys of git-source services: key
// generation, upload and rotation, the pinned host keys (trust on first use,
// "Accept new host key"), and the credentials a clone or ls-remote uses.
//
// The private key is stored encrypted, never returned by any read, and goes to
// the agent as a send-time secret. Host keys are public and travel in the
// command payload.
package deploykeys

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Audit actions.
const (
	ActionKeyCreate = "service.ssh_key.create"
	ActionKeyRotate = "service.ssh_key.rotate"
	ActionKeyDelete = "service.ssh_key.delete"
	ActionHostPin   = "service.ssh_host.pin"
	ActionHostAllow = "service.ssh_host.accept"
	ActionHostClash = "service.ssh_host.mismatch"
)

// Key sources.
const (
	SourceGenerated = "generated"
	SourceUploaded  = "uploaded"
)

// Service implements the deploy-key use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	box  *secretbox.Box
}

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, box *secretbox.Box) *Service {
	s := &Service{pool: pool, bus: bus, box: box}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// KeyView is a deploy key as the API shows it: never the private half.
type KeyView struct {
	// PublicKey is the authorized_keys line to add to the repository.
	PublicKey   string     `json:"publicKey"`
	Fingerprint string     `json:"fingerprint"`
	Type        string     `json:"type" doc:"Key algorithm, e.g. ssh-ed25519"`
	Source      string     `json:"source" enum:"generated,uploaded"`
	CreatedAt   time.Time  `json:"createdAt"`
	RotatedAt   *time.Time `json:"rotatedAt,omitempty"`
}

// MismatchView is the last host key that was refused for a pinned host.
type MismatchView struct {
	Fingerprint string    `json:"fingerprint"`
	SeenAt      time.Time `json:"seenAt"`
}

// HostView is one pinned host key.
type HostView struct {
	Host        string        `json:"host" doc:"known_hosts name: host, or [host]:port"`
	KeyType     string        `json:"keyType"`
	Fingerprint string        `json:"fingerprint"`
	FirstSeenAt time.Time     `json:"firstSeenAt"`
	Mismatch    *MismatchView `json:"mismatch,omitempty" doc:"Set when the server last presented a different key: clones fail until the new key is accepted"`
}

// StatusView is the SSH state of a git service.
type StatusView struct {
	// UsesSSHURL is true when the service's git URL is an SSH URL.
	UsesSSHURL bool `json:"usesSshUrl"`
	// Host is the known_hosts name of the URL's host.
	Host string `json:"host,omitempty"`
	// Key is the deploy key; absent until "Use SSH" generated or uploaded one.
	Key *KeyView `json:"key,omitempty"`
	// DeployKeyLink is the provider's "add deploy key" page when the host is a known public instance.
	DeployKeyLink *gitssh.DeployKeyLink `json:"deployKeyLink,omitempty"`
	HostKeys      []HostView            `json:"hostKeys"`
}

func keyView(k db.ServiceSshKey) *KeyView {
	typ, _, _ := strings.Cut(k.PublicKey, " ")
	return &KeyView{PublicKey: k.PublicKey, Fingerprint: k.Fingerprint, Type: typ, Source: k.Source, CreatedAt: k.CreatedAt, RotatedAt: k.RotatedAt}
}

func hostView(h db.ServiceSshHost) HostView {
	v := HostView{Host: h.Host, KeyType: h.KeyType, Fingerprint: h.Fingerprint, FirstSeenAt: h.FirstSeenAt}
	if h.MismatchFingerprint != nil && h.MismatchAt != nil {
		v.Mismatch = &MismatchView{Fingerprint: *h.MismatchFingerprint, SeenAt: *h.MismatchAt}
	}
	return v
}

func (s *Service) publish(sc services.Scope) {
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
}

// loadGit authorizes scope on a git-source service.
func (s *Service) loadGit(ctx context.Context, scope string, id uuid.UUID) (services.Scope, error) {
	sc, err := services.LoadScope(ctx, s.q, scope, id)
	if err != nil {
		return services.Scope{}, err
	}
	if sc.Service.SourceType != services.SourceGit {
		return services.Scope{}, apierr.Unprocessable("not_git_source", "SSH deploy keys exist only for services with a git source.")
	}
	return sc, nil
}

// Status returns the service's deploy key and pinned host keys.
func (s *Service) Status(ctx context.Context, serviceID uuid.UUID) (StatusView, error) {
	sc, err := s.loadGit(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return StatusView{}, err
	}
	return s.status(ctx, s.q, sc)
}

func (s *Service) status(ctx context.Context, q *db.Queries, sc services.Scope) (StatusView, error) {
	out := StatusView{HostKeys: []HostView{}}
	url := services.DecodeConfig(sc.Service.Config).GitURL
	if ep, err := gitssh.ParseURL(url); err == nil {
		out.UsesSSHURL, out.Host = true, ep.HostName()
		if l, ok := gitssh.DeployKeyLinkFor(ep); ok {
			out.DeployKeyLink = &l
		}
	}
	k, err := q.GetServiceSSHKey(ctx, sc.Service.ID)
	switch {
	case err == nil:
		out.Key = keyView(k)
	case !errors.Is(err, pgx.ErrNoRows):
		return StatusView{}, fmt.Errorf("get ssh key: %w", err)
	}
	hosts, err := q.ListServiceSSHHosts(ctx, sc.Service.ID)
	if err != nil {
		return StatusView{}, fmt.Errorf("list ssh hosts: %w", err)
	}
	for _, h := range hosts {
		out.HostKeys = append(out.HostKeys, hostView(h))
	}
	return out, nil
}

// SetKeyInput selects how a key is set.
type SetKeyInput struct {
	// PrivateKey, when set, is uploaded; otherwise a new Ed25519 key is generated.
	PrivateKey string
	// Rotate replaces an existing key; without it an existing key is a 409.
	Rotate bool
}

// SetKey generates or uploads the service's deploy key. Replacing a key keeps
// the pinned host keys (the server is the same; only who we are changes).
func (s *Service) SetKey(ctx context.Context, serviceID uuid.UUID, in SetKeyInput) (StatusView, error) {
	sc, err := s.loadGit(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return StatusView{}, err
	}
	comment := "nebulactrl-" + sc.Service.Slug
	var kp gitssh.KeyPair
	source := SourceGenerated
	if in.PrivateKey != "" {
		if kp, err = gitssh.ParsePrivateKey([]byte(in.PrivateKey), comment); err != nil {
			return StatusView{}, apierr.Unprocessable("invalid_private_key", err.Error())
		}
		source = SourceUploaded
	} else if kp, err = gitssh.Generate(comment); err != nil {
		return StatusView{}, fmt.Errorf("generate deploy key: %w", err)
	}
	enc, err := s.box.Seal(secretbox.ServiceSSHKeysPrivateKey, serviceID.String(), kp.PrivatePEM)
	if err != nil {
		return StatusView{}, fmt.Errorf("seal deploy key: %w", err)
	}
	var out StatusView
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		_, getErr := q.GetServiceSSHKey(ctx, serviceID)
		exists := getErr == nil
		if getErr != nil && !errors.Is(getErr, pgx.ErrNoRows) {
			return getErr
		}
		if exists && !in.Rotate {
			return apierr.Conflict("ssh_key_exists", "the service already has a deploy key; rotate it to replace it")
		}
		if _, err := q.UpsertServiceSSHKey(ctx, db.UpsertServiceSSHKeyParams{
			ServiceID: serviceID, PrivateKeyEnc: enc, PublicKey: kp.PublicKey, Fingerprint: kp.Fingerprint, Source: source,
		}); err != nil {
			return err
		}
		action := ActionKeyCreate
		if exists {
			action = ActionKeyRotate
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"source": source, "fingerprint": kp.Fingerprint},
		}); err != nil {
			return err
		}
		out, err = s.status(ctx, q, sc)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return StatusView{}, err
		}
		return StatusView{}, fmt.Errorf("set deploy key: %w", err)
	}
	s.publish(sc)
	return out, nil
}

// DeleteKey removes the deploy key and the pinned host keys.
func (s *Service) DeleteKey(ctx context.Context, serviceID uuid.UUID) error {
	sc, err := s.loadGit(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.DeleteServiceSSHKey(ctx, serviceID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apierr.NotFound("deploy key")
		}
		if err := q.DeleteServiceSSHHosts(ctx, serviceID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionKeyDelete, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete deploy key: %w", err)
	}
	s.publish(sc)
	return nil
}

// AcceptHostKey pins the key the host presents right now, replacing the pinned
// one. fingerprint must be the one the user reviewed: if the server presents
// yet another key in between, nothing is pinned (409).
func (s *Service) AcceptHostKey(ctx context.Context, serviceID uuid.UUID, fingerprint string) (StatusView, error) {
	sc, err := s.loadGit(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return StatusView{}, err
	}
	ep, err := gitssh.ParseURL(services.DecodeConfig(sc.Service.Config).GitURL)
	if err != nil {
		return StatusView{}, apierr.Unprocessable("not_ssh_url", "the service's git URL is not an SSH URL")
	}
	key, err := gitssh.ScanHostKey(ctx, ep)
	if err != nil {
		return StatusView{}, apierr.New(http.StatusBadGateway, "host_unreachable", err.Error())
	}
	if key.Fingerprint != fingerprint {
		return StatusView{}, apierr.Conflict("host_key_changed", "the server now presents a different host key than the one you reviewed ("+key.Fingerprint+"); review it again").
			WithDetails(map[string]any{"fingerprint": key.Fingerprint})
	}
	var out StatusView
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var by *uuid.UUID
		if p, ok := auth.PrincipalFrom(ctx); ok && p.Type == auth.TypeUser {
			id := p.UserID
			by = &id
		}
		if _, err := q.PinServiceSSHHost(ctx, db.PinServiceSSHHostParams{
			ServiceID: serviceID, Host: key.Host, KeyType: key.Type, PublicKey: key.Authorized(), Fingerprint: key.Fingerprint, AcceptedBy: by,
		}); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionHostAllow, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"host": key.Host, "fingerprint": key.Fingerprint},
		}); err != nil {
			return err
		}
		out, err = s.status(ctx, q, sc)
		return err
	})
	if err != nil {
		return StatusView{}, fmt.Errorf("accept host key: %w", err)
	}
	s.publish(sc)
	return out, nil
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK        bool   `json:"ok"`
	CommitSHA string `json:"commitSha,omitempty" doc:"The branch head, when the test succeeded"`
	// Code classifies a failure: host_key_changed, no_key, not_ssh_url or git_unreachable.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Status is the service's SSH state after the test (it may have pinned the host key).
	Status StatusView `json:"status"`
}

// Test fetches the configured branch over SSH with the deploy key: the first
// connection pins the host key, a changed key is recorded as a mismatch.
func (s *Service) Test(ctx context.Context, serviceID uuid.UUID) (TestResult, error) {
	sc, err := s.loadGit(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return TestResult{}, err
	}
	cfg := services.DecodeConfig(sc.Service.Config)
	res := TestResult{}
	if !gitssh.IsSSHURL(cfg.GitURL) {
		res.Code, res.Message = "not_ssh_url", "the service's git URL is not an SSH URL (ssh://… or git@host:org/repo.git)"
	} else if sha, err := s.Resolve(ctx, sc.Service.ID, cfg.GitURL, cfg.GitBranch); err != nil {
		res.Message = err.Error()
		var mm *gitssh.MismatchError
		switch {
		case errors.Is(err, gitssh.ErrNoKey):
			res.Code = "no_key"
		case errors.As(err, &mm):
			res.Code = "host_key_changed"
		default:
			res.Code = "git_unreachable"
		}
	} else {
		res.OK, res.CommitSHA = true, sha
	}
	if res.Status, err = s.status(ctx, s.q, sc); err != nil {
		return TestResult{}, err
	}
	return res, nil
}
