package volbackup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Sweep cleans up after backups that did not finish (a crashed agent, a killed
// command): every helper container no running operation owns is removed, its
// staging volume with it, and a service container that helper's backup froze
// or stopped is thawed or started again. It never touches the helper of an
// operation that is running in this process.
//
// It also thaws any paused container of a service this agent manages and that
// holds no command lock, whatever the helpers say: a container must never stay
// paused. busy reports whether a service has a mutating command in flight (a
// backup window holds that lock, so its paused container is left alone).
func (r *Runner) Sweep(ctx context.Context, busy func(serviceID string) bool) error {
	var errs []error
	helpers, err := r.Docker.ContainerList(ctx, docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelHelper: agentprotocol.HelperBackup},
	})
	if err != nil {
		return fmt.Errorf("sweep: list backup helpers: %w", err)
	}
	for _, hc := range helpers {
		op := hc.Labels[agentprotocol.LabelBackupID]
		if r.isActive(op) {
			continue
		}
		if cid := hc.Labels[agentprotocol.LabelBackupContainer]; cid != "" {
			if err := r.recoverContainer(ctx, cid, hc.Labels[agentprotocol.LabelBackupMode]); err != nil {
				errs = append(errs, err)
				continue // keep the helper (and the evidence) until the service is back
			}
		}
		r.log().Info("removing a stale backup helper", "container", hc.Name)
		if err := r.Docker.ContainerRemove(ctx, hc.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Errorf("sweep: remove %s: %w", hc.Name, err))
			continue
		}
		if op != "" {
			if err := r.Docker.RemoveVolume(ctx, agentprotocol.BackupStagePrefix+agentprotocol.Short(op), true); err != nil && !errors.Is(err, docker.ErrNotFound) {
				r.log().Warn("sweep: remove staging volume", "backup", op, "err", err)
			}
		}
	}

	managed, err := r.Docker.ContainerList(ctx, docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
	})
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("sweep: list containers: %w", err))...)
	}
	for _, c := range managed {
		if c.State != "paused" || (busy != nil && busy(c.Labels[agentprotocol.LabelService])) {
			continue
		}
		r.log().Warn("thawing a paused service container that no backup owns", "container", c.Name)
		if err := r.Docker.ContainerUnpause(ctx, c.ID); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Errorf("sweep: unpause %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// recoverContainer puts a service container back after a backup that died: a
// paused one is thawed, a stopped one is started when the backup mode stops it.
func (r *Runner) recoverContainer(ctx context.Context, id, mode string) error {
	info, err := r.Docker.ContainerInspect(ctx, id)
	if errors.Is(err, docker.ErrNotFound) {
		return nil // replaced or removed meanwhile
	}
	if err != nil {
		return fmt.Errorf("sweep: inspect %s: %w", id, err)
	}
	switch {
	case info.State == "paused":
		r.log().Warn("thawing a service container a dead backup left paused", "container", info.Name)
		if err := r.Docker.ContainerUnpause(ctx, info.ID); err != nil {
			return fmt.Errorf("sweep: unpause %s: %w", info.Name, err)
		}
	case mode == agentprotocol.VolModeStop && !info.Running && info.State != "created":
		r.log().Warn("starting a service container a dead backup left stopped", "container", info.Name)
		sctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := r.Docker.ContainerStart(sctx, info.ID); err != nil {
			return fmt.Errorf("sweep: start %s: %w", info.Name, err)
		}
	}
	return nil
}
