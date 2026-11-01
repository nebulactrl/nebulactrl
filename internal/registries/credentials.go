package registries

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Registry actions a credential can grant.
const (
	ActionPush   = "push"
	ActionPull   = "pull"
	ActionDelete = "delete"
)

// BuiltinUsername and BuiltinPassword are the placeholder credential a command
// carries for the built-in registry. They are not a secret and grant nothing:
// the registry is reached through the agent's tunnel, where the control plane
// checks the repository and adds the real, short-lived token itself. The
// placeholder only tells the agent to act as a pushing client.
const (
	BuiltinUsername = "nebulactrl"
	BuiltinPassword = "tunnel"
)

// Scope is what a credential is for: Actions on one Repository.
type Scope struct {
	// Repository is the repository path without the host ("<team-id>/proj-svc").
	// External registries ignore it: their credential is whatever the user entered.
	Repository string
	// Actions is a subset of ActionPush, ActionPull and ActionDelete.
	Actions []string
}

// Normalize returns the scope with a sorted, de-duplicated action list.
func (sc Scope) normalize() (Scope, error) {
	if len(sc.Actions) == 0 {
		return sc, errors.New("a credential needs at least one action")
	}
	acts := make([]string, 0, len(sc.Actions))
	for _, a := range sc.Actions {
		switch a {
		case ActionPush, ActionPull, ActionDelete:
		default:
			return sc, fmt.Errorf("unknown registry action %q", a)
		}
		if !slices.Contains(acts, a) {
			acts = append(acts, a)
		}
	}
	slices.Sort(acts)
	sc.Actions = acts
	sc.Repository = strings.Trim(sc.Repository, "/")
	return sc, nil
}

func (sc Scope) pullOnly() bool { return len(sc.Actions) == 1 && sc.Actions[0] == ActionPull }

// Info is what the deployment code needs to know about a registry.
type Info struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	Kind        string
	Host        string
	Namespace   string
	// Insecure is true for a loopback host: plain http is acceptable there (and
	// only there; the control plane refuses it for any other host).
	Insecure        bool
	ManageRetention bool
	HasPullAuth     bool
	// Healthy is false while the built-in registry is not ready. An external
	// registry has no control-plane-side health: it is checked per server (Check).
	Healthy       bool
	HealthMessage string
	// ReadOnly is true during the garbage-collection window of the built-in
	// registry: pulls work, pushes are refused. Builds should wait.
	ReadOnly bool
}

// Resolve describes registry id for placement and dispatch. It is for trusted
// internal callers and does no authorization.
func (s *Service) Resolve(ctx context.Context, id uuid.UUID) (Info, error) {
	r, err := s.q.GetRegistryInternal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Info{}, apierr.NotFound("registry")
	}
	if err != nil {
		return Info{}, fmt.Errorf("get registry: %w", err)
	}
	return s.info(ctx, r)
}

func (s *Service) info(ctx context.Context, r db.Registry) (Info, error) {
	i := Info{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, Kind: r.Kind, Host: r.Host, Namespace: r.Namespace,
		Insecure: IsLoopbackHost(r.Host), ManageRetention: r.ManageRetention, HasPullAuth: len(r.PullAuthEnc) > 0, Healthy: true,
	}
	if r.Kind != KindBuiltin {
		return i, nil
	}
	m, err := s.builtin(ctx)
	if err != nil {
		return i, err
	}
	i.Healthy, i.HealthMessage = m.ready, m.message
	i.ReadOnly = m.state.ReadOnly
	return i, nil
}

// Credentials returns the registry credential for one command as the Docker
// X-Registry-Auth value: base64url of {"username","password","serveraddress"},
// ready for Secrets[registry_push_auth] or Secrets[registry_auth]. It is for
// trusted internal callers (the dispatcher at send time) and does no authorization.
//
// External registry: the sealed credential the user entered. A pull-only scope
// uses the read-only pull credential when one is set, otherwise the push
// credential. The scope and ttl do not apply: the provider decides.
//
// Built-in registry: the placeholder (BuiltinUsername), because the server
// holds no registry credential at all. The control plane adds a token for exactly
// the requested repository when the request passes its forwarder.
func (s *Service) Credentials(ctx context.Context, registryID uuid.UUID, sc Scope, _ time.Duration) (string, error) {
	r, err := s.q.GetRegistryInternal(ctx, registryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apierr.NotFound("registry")
	}
	if err != nil {
		return "", fmt.Errorf("get registry: %w", err)
	}
	sc, err = sc.normalize()
	if err != nil {
		return "", err
	}
	var c credential
	switch r.Kind {
	case KindExternal:
		enc, col := r.PushAuthEnc, secretbox.RegistriesPushAuth
		if sc.pullOnly() && len(r.PullAuthEnc) > 0 {
			enc, col = r.PullAuthEnc, secretbox.RegistriesPullAuth
		}
		if c, err = s.open(col, r.ID, enc); err != nil {
			return "", err
		}
		if c.Username == "" && c.Password == "" {
			return "", fmt.Errorf("registry %s has no credential", r.Name)
		}
	case KindBuiltin:
		c = credential{Username: BuiltinUsername, Password: BuiltinPassword}
	default:
		return "", fmt.Errorf("unknown registry kind %q", r.Kind)
	}
	return EncodeAuth(c.Username, c.Password, r.Host)
}

// EncodeAuth renders the X-Registry-Auth value.
func EncodeAuth(username, password, serverAddress string) (string, error) {
	raw, err := json.Marshal(agentprotocol.RegistryAuth{Username: username, Password: password, ServerAddress: serverAddress})
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

// DecodeAuth parses an X-Registry-Auth value.
func DecodeAuth(v string) (agentprotocol.RegistryAuth, bool) {
	var a agentprotocol.RegistryAuth
	raw, err := base64.URLEncoding.DecodeString(v)
	if err != nil || json.Unmarshal(raw, &a) != nil {
		return a, false
	}
	return a, true
}
