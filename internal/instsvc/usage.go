package instsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

func setState(id uuid.UUID, state, message string) db.SetSliceStateParams {
	return db.SetSliceStateParams{ID: id, State: state, Message: message}
}

// Usage re-reads the bytes every team's S3 slice holds (the 5-minute job), then
// sets the slice's state and the 80% / 100% alerts. A slice whose listing fails
// keeps its old numbers; the failure is joined into the returned error.
func (s *Service) Usage(ctx context.Context) error {
	be := s.backends[KindS3]
	if be == nil {
		return nil
	}
	rows, err := s.q.ListSlicesByKind(ctx, KindS3)
	if err != nil {
		return fmt.Errorf("list s3 slices: %w", err)
	}
	def, err := defaultQuota(ctx, s.q)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		if r.State != StateReady && r.State != StateFull {
			continue
		}
		if err := s.refreshSlice(ctx, be, r, def); err != nil {
			errs = append(errs, fmt.Errorf("team %s: %w", r.TeamID, err))
		}
	}
	return errors.Join(errs...)
}

// RefreshTeamUsage re-reads one team's usage now (after a backup result).
func (s *Service) RefreshTeamUsage(ctx context.Context, teamID uuid.UUID) error {
	be := s.backends[KindS3]
	if be == nil {
		return nil
	}
	row, err := s.q.GetTeamSlice(ctx, db.GetTeamSliceParams{TeamID: teamID, Kind: KindS3})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.DeletedAt != nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get slice: %w", err)
	}
	def, err := defaultQuota(ctx, s.q)
	if err != nil {
		return err
	}
	return s.refreshSlice(ctx, be, row, def)
}

func (s *Service) refreshSlice(ctx context.Context, be Backend, r db.TeamServiceSlice, def int64) error {
	used, err := be.Usage(ctx, sliceOf(r, def))
	if err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	return s.applyUsage(ctx, r, used, def)
}

// applyUsage stores the usage and derives the state and alerts from it. The
// state is full at or over the quota (new writes fail, nothing is deleted) and
// ready again below it. The alert level is 80 or 100 once the slice crossed
// that share; it falls back to 0 only below 75%, so a slice hovering around
// 80% alerts once, not on every crossing.
func (s *Service) applyUsage(ctx context.Context, r db.TeamServiceSlice, used int64, def int64) error {
	if err := s.q.SetSliceUsage(ctx, db.SetSliceUsageParams{ID: r.ID, UsedBytes: used}); err != nil {
		return fmt.Errorf("store usage: %w", err)
	}
	return s.evaluate(ctx, r, used, def)
}

// evaluate derives state and alerts from a usage figure that is already stored
// (a quota change re-evaluates the last reading without listing the bucket).
func (s *Service) evaluate(ctx context.Context, r db.TeamServiceSlice, used int64, def int64) error {
	quota := effectiveQuota(r.QuotaBytes, def)
	if r.State == StateReady || r.State == StateFull {
		want, msg := StateReady, ""
		if used >= quota {
			want, msg = StateFull, "Built-in backup storage is full."
		}
		if want != r.State {
			if err := s.q.SetSliceState(ctx, setState(r.ID, want, msg)); err != nil {
				return fmt.Errorf("store state: %w", err)
			}
		}
	}
	level := alertLevelFor(used, quota, int(r.AlertLevel))
	if level == int(r.AlertLevel) {
		return nil
	}
	if err := s.q.SetSliceAlertLevel(ctx, db.SetSliceAlertLevelParams{ID: r.ID, AlertLevel: int32(level)}); err != nil {
		return fmt.Errorf("store alert level: %w", err)
	}
	if level > int(r.AlertLevel) {
		s.notifyQuota(ctx, r, level, used, quota)
	}
	return nil
}

// alertLevelFor maps usage to the alert level: 100 at or over the quota, 80 from
// 80%, kept in [75%, 80%) and 0 below 75%. Falling from 100 to 80 does not alert
// again.
func alertLevelFor(used, quota int64, cur int) int {
	if quota <= 0 {
		return cur
	}
	pct := used * 100 / quota
	switch {
	case used >= quota:
		return 100
	case pct >= 80:
		return 80
	case pct >= 75:
		return min(cur, 80)
	}
	return 0
}
