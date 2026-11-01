package meshlinks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// This file is the deployments.MeshHook: the part of a deployment that opens
// the links a consumer needs and waits for them.

// triggerReconcile mirrors deployments.TriggerReconcile: an automated redeploy
// that never adds a link.
const triggerReconcile = "reconcile"

// checkTimeout bounds one connectivity check while a deployment waits.
const checkTimeout = 45 * time.Second

// MessageNotReady is the failure of a deployment whose links did not come up in
// ReadyTimeout.
const MessageNotReady = "Not deployed: mesh link not ready. No changes were made."

// notDeployed turns the blocker that stopped a deployment into its failure.
func notDeployed(b services.MeshBlocker) error {
	msg := strings.TrimSuffix(strings.TrimSpace(b.Message), ".")
	if b.Code == apierr.CodeMeshPaused {
		msg = "mesh paused"
	}
	return apierr.Unprocessable(b.Code, "Not deployed: "+msg+". No changes were made.")
}

// Prepare opens the links, exposures and grants the deployment of serviceID
// needs. The deploy of a consumer is the confirmation of the devices the plan
// listed. Automated redeploys (trigger reconcile) add nothing.
func (s *Service) Prepare(ctx context.Context, serviceID uuid.UUID, trigger string) error {
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return err
	}
	if sc.Service.ServerID == nil {
		return nil
	}
	opts := ForConsumers(serviceID)
	if trigger == triggerReconcile {
		opts = Options{}
	}
	res, err := s.ReconcileEnv(ctx, sc.EnvironmentID, ModeAdd, opts)
	if err != nil {
		return err
	}
	if len(res.Blockers) > 0 {
		return notDeployed(res.Blockers[0])
	}
	return nil
}

// WaitReady reports what the deployment of serviceID still waits for ("" when
// every link it needs carries traffic). Under an advisory tailnet policy a link
// counts as ready once a connectivity check reaches the provider. After
// ReadyTimeout (counted from since) it fails with mesh_link_not_ready.
func (s *Service) WaitReady(ctx context.Context, serviceID uuid.UUID, since time.Time) (string, error) {
	waiting, err := s.waiting(ctx, serviceID)
	if err != nil || waiting == "" {
		return "", err
	}
	if s.now().Sub(since) >= ReadyTimeout {
		return "", apierr.Conflict(apierr.CodeMeshLinkNotReady, MessageNotReady)
	}
	return waiting, nil
}

func (s *Service) waiting(ctx context.Context, serviceID uuid.UUID) (string, error) {
	if s.mesh == nil {
		return "", nil
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return "", err
	}
	if sc.Service.ServerID == nil || !consumes(sc.Service.Kind) {
		return "", nil
	}
	d, err := Derive(ctx, s.q, s.box, sc.EnvInfo())
	if err != nil {
		return "", err
	}
	wants := wantsOfConsumer(d, serviceID)
	if len(wants) == 0 {
		return "", nil
	}
	st, err := loadState(ctx, s.q, sc.EnvironmentID)
	if err != nil {
		return "", err
	}
	settings, err := s.q.GetMeshSettings(ctx, sc.WorkspaceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("get mesh settings: %w", err)
	}
	ids := make([]uuid.UUID, 0, 2*len(wants))
	for _, w := range wants {
		ids = append(ids, w.ConsumerServer, w.ProviderServer)
	}
	names, err := serverNames(ctx, s.q, ids)
	if err != nil {
		return "", err
	}
	for _, w := range wants {
		if w.NotExposable != "" {
			continue
		}
		pair := fmt.Sprintf("%s -> %s", names[w.ConsumerServer], names[w.ProviderServer])
		link, ok := st.linkByKey[w.linkKey()]
		ex, eok := st.exposureOfService(w.ProviderID)
		if _, granted := st.grants[grantKey{link.ID, ex.ID, w.ConsumerID, w.Source}]; !ok || !eok || !granted {
			return fmt.Sprintf("Waiting for the mesh link %s (%s) to be created.", pair, w.ProviderSlug), nil
		}
		r, err := s.mesh.Ready(ctx, link.ID)
		if err != nil {
			return "", err
		}
		if !r.Online {
			return waitMessage(fmt.Sprintf("Waiting for the mesh link %s to come online", pair), r.Reason), nil
		}
		if !r.ACLOK {
			return waitMessage(fmt.Sprintf("Waiting for the mesh access rules for %s to apply", pair), r.Reason), nil
		}
		if settings.AclState == "advisory" {
			// The tailnet policy is not ours: only a connection proves the path.
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			cr, err := s.mesh.Check(cctx, link.ID, ex.ID)
			cancel()
			if err != nil {
				return waitMessage(fmt.Sprintf("Waiting for a connection %s to %s", pair, w.ProviderSlug), err.Error()), nil
			}
			if cr.Result != agentprotocol.MeshCheckConnected {
				return waitMessage(fmt.Sprintf("Waiting for a connection %s to %s (%s)", pair, w.ProviderSlug, cr.Result), cr.Message), nil
			}
		}
	}
	return "", nil
}

func waitMessage(head, reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return head + ": " + strings.TrimSuffix(reason, ".") + "."
	}
	return head + "."
}
