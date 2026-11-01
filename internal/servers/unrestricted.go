package servers

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// UnrestrictedSettings is the per-server "Unrestricted" state: whether an
// instance admin lifted the safe allow-lists for this server (Compose stacks and
// the Docker options that hand a container host devices, GPUs, a runtime or
// host-root equivalents), plus Compose availability.
type UnrestrictedSettings struct {
	Unrestricted   bool   `json:"unrestricted" doc:"An instance admin allowed this server to run stacks and services with privileged mode, host networking, devices, GPUs, a runtime, the host PID namespace, unconfined seccomp or AppArmor, NET_ADMIN, SYS_ADMIN, the Docker socket and host bind mounts"`
	ComposeVersion string `json:"composeVersion" doc:"Docker Compose plugin version the agent reported, empty when it has none"`
	Available      bool   `json:"available" doc:"The agent can run Compose stacks (Docker Compose plugin 2.24 or newer)"`
	Stacks         int    `json:"stacks" doc:"Live Compose stacks placed on this server"`
	DockerServices int    `json:"dockerServices" doc:"Live services placed on this server that use Docker options which need an unrestricted server"`
}

// UnrestrictedInput is the body of PUT /servers/{id}/unrestricted.
type UnrestrictedInput struct {
	Unrestricted bool
	Confirm      string
}

func (s *Service) unrestrictedSettings(ctx context.Context, q *db.Queries, row db.Server) (UnrestrictedSettings, error) {
	n, err := q.CountServerStacks(ctx, &row.ID)
	if err != nil {
		return UnrestrictedSettings{}, fmt.Errorf("count server stacks: %w", err)
	}
	d, err := q.CountServerUnrestrictedDockerServices(ctx, &row.ID)
	if err != nil {
		return UnrestrictedSettings{}, fmt.Errorf("count unrestricted docker services: %w", err)
	}
	out := UnrestrictedSettings{Unrestricted: row.Unrestricted, Stacks: int(n), DockerServices: int(d)}
	if sv := s.toServer(serverRow{Server: row}, nil); sv.Facts != nil {
		out.ComposeVersion = sv.Facts.ComposeVersion
		out.Available = slices.Contains(sv.Facts.Capabilities, agentprotocol.CapCompose)
	}
	return out, nil
}

// Unrestricted returns the Unrestricted settings of a server.
func (s *Service) Unrestricted(ctx context.Context, id uuid.UUID) (UnrestrictedSettings, error) {
	row, err := s.load(ctx, s.q, policy.ScopeServersRead, id)
	if err != nil {
		return UnrestrictedSettings{}, err
	}
	return s.unrestrictedSettings(ctx, s.q, fromScoped(row).Server)
}

// isInstanceAdmin reports whether p is the instance admin (the earliest-created
// user, or an admin-scoped token acting for them); the same rule as
// the other instance-admin-only switches.
func (s *Service) isInstanceAdmin(ctx context.Context, p *auth.Principal) (bool, error) {
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return false, nil
	}
	first, err := s.q.FirstUserID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("first user: %w", err)
	}
	return first == p.UserID, nil
}

// SetUnrestricted switches a server's Unrestricted mode on or off. Instance
// admin only (nobody below it can gain host root through a server); enabling
// needs the server's name as confirmation. Audited in the same transaction.
// Disabling does not touch running containers: the next deploy, rollback or
// reconcile of a stack or service that needs the lifted rules fails.
func (s *Service) SetUnrestricted(ctx context.Context, id uuid.UUID, in UnrestrictedInput) (UnrestrictedSettings, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeServersWrite, id)
	if err != nil {
		return UnrestrictedSettings{}, err
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return UnrestrictedSettings{}, err
	}
	admin, err := s.isInstanceAdmin(ctx, p)
	if err != nil {
		return UnrestrictedSettings{}, err
	}
	if !admin {
		return UnrestrictedSettings{}, apierr.Forbidden()
	}
	if in.Unrestricted && in.Confirm != cur.Name {
		return UnrestrictedSettings{}, apierr.Unprocessable("confirmation_mismatch", "type the server's name to make it unrestricted")
	}
	action := "server.unrestricted.disable"
	if in.Unrestricted {
		action = "server.unrestricted.enable"
	}
	var out UnrestrictedSettings
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.SetServerUnrestricted(ctx, db.SetServerUnrestrictedParams{
			ID: id, WorkspaceID: cur.WorkspaceID, Unrestricted: in.Unrestricted,
		})
		if err != nil {
			return err
		}
		if out, err = s.unrestrictedSettings(ctx, q, row); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "server", ResourceID: id.String(), WorkspaceID: cur.WorkspaceID,
			Metadata: map[string]any{"server": cur.Name, "stacks": out.Stacks, "services": out.DockerServices},
		})
	})
	if err != nil {
		return UnrestrictedSettings{}, fmt.Errorf("set unrestricted: %w", err)
	}
	s.publish(id)
	return out, nil
}
