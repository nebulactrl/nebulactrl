package configsync

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// setOwned records the fingerprint (and projection) a field was written with.
func setOwned(own map[string]string, f *fieldPlan) {
	own[f.fv.ID] = f.desired.FP
	if f.desired.Proj != "" {
		own[f.fv.ID+auxSuffix] = f.desired.Proj
	} else {
		delete(own, f.fv.ID+auxSuffix)
	}
}

func dropOwned(own map[string]string, id string) {
	delete(own, id)
	delete(own, id+auxSuffix)
}

// recordStagedOwnership updates the in-memory ownership of a service after its staged part was
// written: fields written (or already equal) are owned with the fingerprint of the value; an
// immediate change not run yet keeps its old base until its step succeeds.
func (a *applier) recordStagedOwnership(sp *svcPlan) {
	id := a.rows[sp.slug].ID
	own := a.owned[id]
	for _, f := range sp.fields {
		if f.skipped {
			continue
		}
		if phase1Field(f) || !f.changed {
			setOwned(own, f)
		}
	}
	pendingRemove := map[string]bool{}
	for _, f := range sp.removes {
		if f.fv.Applies == AppliesImmediate {
			pendingRemove[f.fv.ID] = true
			continue
		}
		dropOwned(own, f.fv.ID)
	}
	for _, rid := range sp.released {
		if !pendingRemove[rid] {
			dropOwned(own, rid)
		}
	}
}

// recordOwnership writes the config_sources rows and the environment's owned shared keys.
func (a *applier) recordOwnership() error {
	p := a.p
	typ, actorID, label := actorOf(a.prin)
	origin := originOf(p.src, a.prin)
	for _, sp := range p.svcs {
		if sp.pl.Action == ActionBlocked {
			continue
		}
		row := a.rows[sp.slug]
		own := a.owned[row.ID]
		if own == nil {
			continue
		}
		var (
			prevOwned = map[string]string{}
			pending   = json.RawMessage("{}")
			prevSHA   string
			prevSrc   = false
		)
		if sp.ls != nil {
			prevOwned = sp.ls.Owned
			if sp.ls.Source != nil {
				prevSrc = true
				prevSHA = sp.ls.Source.FileSha256
				if len(sp.ls.Source.Pending) > 0 {
					pending = sp.ls.Source.Pending
				}
			}
		}
		if sp.create || !prevSrc || !reflect.DeepEqual(prevOwned, own) || prevSHA != p.fileSHA {
			sc, err := a.reload(sp.slug, row.ID)
			if err != nil {
				return err
			}
			if _, err := a.q.UpsertConfigSource(a.ctx, db.UpsertConfigSourceParams{
				ServiceID: row.ID, EnvironmentID: p.env.Env.ID, WorkspaceID: p.env.WorkspaceID, Origin: origin,
				RepoFullName: p.src.Repo, Branch: p.src.Branch, Path: p.src.Path, CommitSha: p.src.Commit, FileSha256: p.fileSHA,
				SchemaVersion: 1, Owned: ownedJSON(own), Pending: pending, AppliedGeneration: sc.Service.Generation,
				AppliedByType: typ, AppliedByID: actorID, AppliedByLabel: label,
			}); err != nil {
				return a.fail(stepName("ownership", sp.slug), err)
			}
			a.wrote = true
			a.step(stepName("ownership", sp.slug), StepApplied, "")
		}
	}
	if !reflect.DeepEqual(a.sharedOwned, p.live.OwnedShared) && (len(a.sharedOwned) > 0 || len(p.live.OwnedShared) > 0) {
		if err := a.q.SetEnvironmentConfigOwnedShared(a.ctx, db.SetEnvironmentConfigOwnedSharedParams{
			EnvironmentID: p.env.Env.ID, WorkspaceID: p.env.WorkspaceID, OwnedShared: ownedJSON(a.sharedOwned),
		}); err != nil {
			return a.fail("ownership:shared", err)
		}
		a.wrote = true
	}
	return nil
}

// markOwned records the outcome of an immediate step in its own small transaction: the fields that
// were written become owned, removed members stop being owned, and the pending record is updated.
func (s *Service) markOwned(ctx context.Context, serviceID uuid.UUID, set []*fieldPlan, drop []string, pending map[string]json.RawMessage, clear []string) error {
	return db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.GetConfigSourceForUpdate(ctx, serviceID)
		if err != nil {
			return fmt.Errorf("get config source: %w", err)
		}
		own := map[string]string{}
		_ = json.Unmarshal(row.Owned, &own)
		pend := map[string]json.RawMessage{}
		_ = json.Unmarshal(row.Pending, &pend)
		for _, f := range set {
			setOwned(own, f)
		}
		for _, id := range drop {
			dropOwned(own, id)
		}
		for _, id := range clear {
			delete(pend, id)
		}
		for id, v := range pending {
			pend[id] = v
		}
		pj, _ := json.Marshal(pend)
		return q.SetConfigSourceOwned(ctx, db.SetConfigSourceOwnedParams{
			ServiceID: serviceID, Owned: ownedJSON(own), Pending: pj, AppliedGeneration: row.AppliedGeneration,
		})
	})
}
