package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ActorAutoscaler is ScaleEvent.Actor for autoscaler actions.
const ActorAutoscaler = "autoscaler"

// NewScaleEvent builds services.last_scale_event.
func NewScaleEvent(from, to int, reason, actor string) json.RawMessage {
	raw, _ := json.Marshal(ScaleEvent{At: time.Now().UTC(), From: from, To: to, Reason: reason, Actor: actor})
	return raw
}

// ClampReplicas runs after a successful deployment of release config cfg with
// volumes volumes. A service that is no longer stateless (a volume was added,
// the HTTP port removed) was deployed with one replica, so its replica count
// is reset to 1 and autoscaling is switched off. Only a service that is
// ineligible in the staged configuration too is clamped: a recovery redeploy or
// a rollback runs an older release, and the staged configuration, which a later
// deploy ships, decides what the setting means. It returns whether anything
// changed.
func ClampReplicas(ctx context.Context, tx pgx.Tx, sc Scope, cfg Config, volumes int) (bool, error) {
	ok, why := Eligibility(sc.Service.Kind, cfg, volumes)
	if ok {
		return false, nil
	}
	if staged, _ := Eligibility(sc.Service.Kind, sc.Config(), volumes); staged {
		return false, nil
	}
	q := db.New(tx)
	changed := false
	if from := int(sc.Service.Replicas); from != MinReplicas {
		if _, err := q.ApplyServiceScale(ctx, db.ApplyServiceScaleParams{
			ID: sc.Service.ID, Replicas: MinReplicas, ExpectedReplicas: sc.Service.Replicas,
			LastScaleEvent: NewScaleEvent(from, MinReplicas, why, "deployment"),
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("reset replicas: %w", err)
		} else if err == nil {
			changed = true
			if err := audit.Record(ctx, tx, audit.Entry{
				Action: "service.scale", ResourceType: "service", ResourceID: sc.Service.ID.String(),
				ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
				Metadata: map[string]any{"slug": sc.Service.Slug, "from": from, "to": MinReplicas, "reason": why, "actor": "deployment"},
			}); err != nil {
				return false, err
			}
		}
	}
	if as := DecodeAutoscaling(sc.Service.Autoscaling); as.Enabled {
		as.Enabled = false
		raw, _ := json.Marshal(as)
		if _, err := q.SetServiceAutoscaling(ctx, db.SetServiceAutoscalingParams{ID: sc.Service.ID, Autoscaling: raw}); err != nil {
			return false, fmt.Errorf("disable autoscaling: %w", err)
		}
		changed = true
	}
	return changed, nil
}

// ClampProcessReplicas runs after a successful deployment of release config cfg,
// next to ClampReplicas (which handles the default process). A named process that
// is no longer eligible (a volume was added, it became a singleton) was deployed
// with one replica, so its replica count is reset to 1 and its autoscaling is
// switched off, unless the staged configuration makes it eligible again (a
// rollback ran an older release). The scale rows of processes that neither cfg
// nor the staged configuration has are deleted: a rollback must not forget the
// scale of a process the release lacks but the service still has.
func ClampProcessReplicas(ctx context.Context, tx pgx.Tx, sc Scope, cfg Config, volumes int) error {
	q := db.New(tx)
	staged := sc.Config()
	rows, err := q.ListProcessScales(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("list process scales: %w", err)
	}
	for _, p := range cfg.EffectiveProcesses() {
		if p.Default {
			continue
		}
		ok, why := ProcessEligibility(sc.Service.Kind, cfg, volumes, p)
		if ok {
			continue
		}
		if sp, found := staged.Process(p.Name); found {
			if ok, _ := ProcessEligibility(sc.Service.Kind, staged, volumes, sp); ok {
				continue
			}
		}
		for _, row := range rows {
			if row.Process != p.Name {
				continue
			}
			if from := int(row.Replicas); from != MinReplicas {
				if _, err := q.ApplyProcessScale(ctx, db.ApplyProcessScaleParams{
					ServiceID: sc.Service.ID, Process: p.Name, Replicas: MinReplicas, ExpectedReplicas: row.Replicas,
					LastScaleEvent: NewScaleEvent(from, MinReplicas, why, "deployment"),
				}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("reset replicas of process %s: %w", p.Name, err)
				} else if err == nil {
					if err := audit.Record(ctx, tx, audit.Entry{
						Action: "service.scale", ResourceType: "service", ResourceID: sc.Service.ID.String(),
						ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
						Metadata: map[string]any{"slug": sc.Service.Slug, "process": p.Name, "from": from, "to": MinReplicas, "reason": why, "actor": "deployment"},
					}); err != nil {
						return err
					}
				}
			}
			if as := DecodeAutoscaling(row.Autoscaling); as.Enabled {
				as.Enabled = false
				raw, _ := json.Marshal(as)
				if _, err := q.SetProcessAutoscaling(ctx, db.SetProcessAutoscalingParams{ServiceID: sc.Service.ID, Process: p.Name, Autoscaling: raw}); err != nil {
					return fmt.Errorf("disable autoscaling of process %s: %w", p.Name, err)
				}
			}
		}
	}
	keep := []string{}
	for _, c := range []Config{cfg, staged} {
		for _, p := range c.Processes {
			keep = append(keep, p.Name)
		}
	}
	if err := q.PruneProcessScales(ctx, db.PruneProcessScalesParams{ServiceID: sc.Service.ID, Keep: keep}); err != nil {
		return fmt.Errorf("prune process scales: %w", err)
	}
	return nil
}
