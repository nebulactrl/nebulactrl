// Package dns01 owns DNS-01 ACME for the server-level proxy: provider
// credentials (sealed per team or per server), the resolver each server runs
// per provider, wildcard certificates, and the certificate state the agent
// observes. Resolvers are static Traefik configuration, so every change is
// staged on the server (proxy_generation) until "Deploy proxy", exactly like
// uploaded certificates and the upstream policy. DNSCredential values never leave
// this package except as send-time secrets (Secrets); payloads carry variable
// names only.
package dns01

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// providerError is the 422 of an unknown DNS provider code. A code that is an
// alias lego accepts names the canonical code to use instead.
func providerError(code string) *apierr.Error {
	if canonical := agentprotocol.LegoCanonicalCode(code); canonical != "" {
		return apierr.Unprocessable("invalid_provider", fmt.Sprintf("%q is an alias; use the lego provider code %q", code, canonical))
	}
	return apierr.Unprocessable("invalid_provider", "provider must be a lego DNS provider code, for example cloudflare, route53, ovh or gandiv5 (GET /dns/providers and GET /dns/lego-providers list them)")
}

// Service implements the DNS-01 use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	box  *secretbox.Box
}

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, box *secretbox.Box) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus, box: box}
}

func (s *Service) publish(serverID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

// loadServer authorizes scope and returns a server of the caller's team (404 otherwise).
func (s *Service) loadServer(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Server{}, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	return row, nil
}

// SupportsDNS01 reports whether the server's agent advertised agentprotocol.CapDNS01.
// A server that has never connected has no facts yet and is assumed to enroll
// with a current agent.
func SupportsDNS01(srv db.Server) bool {
	if len(srv.Facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapDNS01)
}

// RequireDNS01 returns 422 agent_too_old when the agent cannot render DNS-01
// resolvers: an old agent would silently drop the new fields.
func RequireDNS01(srv db.Server) error {
	if SupportsDNS01(srv) {
		return nil
	}
	return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old for DNS-01 certificates; update the agent first")
}

// RequireDNS01Provider is RequireDNS01 for one provider: every provider but
// the original four also needs the agent to carry the lego catalog
// (agentprotocol.CapDNS01Lego); an older agent would reject the unknown
// provider when it renders the proxy configuration.
func RequireDNS01Provider(srv db.Server, provider string) error {
	if err := RequireDNS01(srv); err != nil {
		return err
	}
	if !agentprotocol.DNSProviderNeedsLego(provider) || len(srv.Facts) == 0 {
		return nil
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" || slices.Contains(f.Capabilities, agentprotocol.CapDNS01Lego) {
		return nil //nolint:nilerr // facts that cannot be decoded, or carry no OS yet, say nothing about capabilities, so the server is not blocked
	}
	return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old for the "+provider+" DNS provider; update the agent first")
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
