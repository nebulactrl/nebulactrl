package instsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

const purgeBatch = 50

// Purge deletes the slices of deleted teams whose 30 days are over: the
// bucket and its key, the team's registry images and its logs, then the rows.
// A slice that cannot be purged stays (state failed with the reason) and is
// tried again on the next run; the others go on.
func (s *Service) Purge(ctx context.Context) error {
	now := s.now()
	rows, err := s.q.ListSlicesDuePurge(ctx, db.ListSlicesDuePurgeParams{PurgeAt: &now, Limit: purgeBatch})
	if err != nil {
		return fmt.Errorf("list slices due for purge: %w", err)
	}
	return s.purgeRows(ctx, rows)
}

// PurgeTeam deletes a deleted team's slices now, ahead of their 30 days (the
// instance administrator's choice). It never touches a team that still exists:
// the caller checks that.
func (s *Service) PurgeTeam(ctx context.Context, teamID uuid.UUID) error {
	rows, err := s.q.ListDeletedTeamSlices(ctx, teamID)
	if err != nil {
		return fmt.Errorf("list deleted slices: %w", err)
	}
	return s.purgeRows(ctx, rows)
}

func (s *Service) purgeRows(ctx context.Context, rows []db.TeamServiceSlice) error {
	if len(rows) == 0 {
		return nil
	}
	def, err := defaultQuota(ctx, s.q)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		if err := s.purgeSlice(ctx, r, def); err != nil {
			errs = append(errs, fmt.Errorf("team %s %s: %w", r.TeamID, r.Kind, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) purgeSlice(ctx context.Context, r db.TeamServiceSlice, def int64) error {
	if be := s.backends[r.Kind]; be != nil {
		if err := be.Purge(ctx, sliceOf(r, def)); err != nil {
			if serr := s.q.SetSliceState(ctx, setState(r.ID, StateFailed, "Purge failed: "+truncate(err.Error()))); serr != nil {
				s.log.WarnContext(ctx, "record purge failure", "slice_id", r.ID, "err", serr)
			}
			return err
		}
	}
	if err := s.q.DeleteSlice(ctx, r.ID); err != nil {
		return fmt.Errorf("delete slice row: %w", err)
	}
	s.log.InfoContext(ctx, "purged team slice", "team_id", r.TeamID, "kind", r.Kind)
	return nil
}
