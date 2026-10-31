package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/reconciler"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	agentupgrade "github.com/nebulactrl/nebulactrl/internal/servers/agentupgrade"
	servercleanup "github.com/nebulactrl/nebulactrl/internal/servers/cleanup"
	serverhardening "github.com/nebulactrl/nebulactrl/internal/servers/hardening"
	serverpatching "github.com/nebulactrl/nebulactrl/internal/servers/patching"
	servertunnel "github.com/nebulactrl/nebulactrl/internal/servers/tunnel"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/sysupdate"
	"github.com/nebulactrl/nebulactrl/internal/tailnet"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// agentHandler is what the agent hub calls back into. Deployment state
// (secrets, events, results, undelivered commands) belongs to
// deployments.Service, observed/desired state to the reconciler, log
// redaction to services.Service.
type agentHandler struct {
	deps      *deployments.Service
	rec       *reconciler.Reconciler
	svcs      *services.Service
	scal      *scaling.Service
	bk        *backups.Service
	dbs       *databases.Service
	proxy     *proxyserver.Service
	tasks     *tasks.Service
	tn        *tailnet.Service
	tunnel    *servertunnel.Service
	cleanup   *servercleanup.Service
	hardening *serverhardening.Service
	upgrades  *agentupgrade.Service
	patching  *serverpatching.Service
	reg       *registries.Service
	mesh      *mesh.Service
	update    *sysupdate.Service
}

var (
	_ agents.SecretSource  = (*agentHandler)(nil)
	_ agents.ResultHandler = (*agentHandler)(nil)
	_ agents.Redactor      = (*agentHandler)(nil)
)

func (h *agentHandler) SecretsForCommand(ctx context.Context, id uuid.UUID, kind string, payload json.RawMessage) (map[string]string, error) {
	if tasks.Handles(kind) {
		return h.tasks.SecretsForCommand(ctx, kind, payload)
	}
	if backups.Handles(kind) {
		return h.bk.SecretsForCommand(ctx, kind, payload)
	}
	if databases.Handles(kind) {
		return h.dbs.SecretsForCommand(ctx, id, kind, payload)
	}
	if tailnet.Handles(kind) {
		return h.tn.SecretsForCommand(ctx, kind, payload)
	}
	if servertunnel.Handles(kind) {
		return h.tunnel.SecretsForCommand(ctx, kind, payload)
	}
	if servercleanup.Handles(kind) {
		return nil, nil // the command carries no secrets
	}
	if serverhardening.Handles(kind) {
		return nil, nil // the command carries no secrets
	}
	if agentupgrade.Handles(kind) {
		return nil, nil // the command carries no secrets
	}
	if serverpatching.Handles(kind) {
		return nil, nil // the command carries no secrets
	}
	if sysupdate.Handles(kind) {
		return nil, nil // the command carries no secrets
	}
	if mesh.Handles(kind) {
		return h.mesh.SecretsForCommand(ctx, kind, payload)
	}
	if registries.Handles(kind) {
		return h.reg.SecretsForCommand(ctx, kind, payload)
	}
	return h.deps.SecretsForCommand(ctx, id, kind, payload)
}

func (h *agentHandler) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, deploymentID *uuid.UUID, res agentprotocol.ResultFrame) error {
	if deploymentID == nil {
		if err := h.scal.HandleResult(ctx, commandID, res); err != nil {
			return err
		}
		if err := h.bk.HandleResult(ctx, commandID, res); err != nil {
			return err
		}
		if err := h.dbs.HandleResult(ctx, commandID, res); err != nil {
			return err
		}
		if err := h.proxy.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.tasks.HandleResult(ctx, commandID, res); err != nil {
			return err
		}
		if err := h.tn.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.tunnel.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.cleanup.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.hardening.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.upgrades.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.update.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.patching.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
		if err := h.reg.HandleResult(ctx, commandID, res); err != nil {
			return err
		}
		if err := h.mesh.HandleResult(ctx, serverID, commandID, res); err != nil {
			return err
		}
	}
	return h.deps.HandleResult(ctx, serverID, commandID, deploymentID, res)
}

func (h *agentHandler) HandleUndelivered(ctx context.Context, serverID, commandID uuid.UUID, deploymentID *uuid.UUID, msg string) error {
	if deploymentID == nil {
		if err := h.bk.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.dbs.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.tasks.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.tn.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.tunnel.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.cleanup.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.hardening.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.upgrades.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.update.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.patching.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.reg.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
		if err := h.mesh.HandleUndelivered(ctx, commandID, msg); err != nil {
			return err
		}
	}
	return h.deps.HandleUndelivered(ctx, serverID, commandID, deploymentID, msg)
}

func (h *agentHandler) HandleEvent(ctx context.Context, serverID uuid.UUID, ev agentprotocol.EventFrame) error {
	return h.deps.HandleEvent(ctx, serverID, ev)
}

func (h *agentHandler) HandleObserved(ctx context.Context, serverID uuid.UUID, obs agentprotocol.Observed) error {
	err := h.rec.HandleObserved(ctx, serverID, obs)
	// Replication status rides in the same frame; one failing must not hide the others.
	if derr := h.dbs.HandleObserved(ctx, serverID, obs); derr != nil && err == nil {
		err = derr
	}
	// The tailnet sidecar state rides in the same frame; one failing must not hide the other.
	if terr := h.tn.HandleObserved(ctx, serverID, obs); terr != nil && err == nil {
		err = terr
	}
	return err
}

func (h *agentHandler) DesiredState(ctx context.Context, serverID uuid.UUID) (agentprotocol.Reconcile, error) {
	rec, err := h.rec.DesiredState(ctx, serverID)
	if err != nil {
		return rec, err
	}
	// Which databases the agent polls for replication status, and which are fenced.
	if err := h.dbs.AnnotateDesired(ctx, serverID, &rec); err != nil {
		slog.WarnContext(ctx, "reconcile: replication set", "server_id", serverID.String(), "err", err)
	}
	// The complete mesh set rides every hello, so a sync_mesh command that expired
	// never leaves the server unconverged. A mesh failure must not hide the rest.
	if err := h.mesh.AddReconcile(ctx, serverID, &rec); err != nil {
		slog.WarnContext(ctx, "reconcile: mesh set", "server_id", serverID.String(), "err", err)
	}
	return rec, nil
}

func (h *agentHandler) RedactorFor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error) {
	return h.svcs.RedactorFor(ctx, serviceID)
}
