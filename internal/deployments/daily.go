package deployments

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// MaxOutcomeDays is the longest `days` window of the daily outcome series.
const MaxOutcomeDays = 90

// DailyOutcome counts the deployments created on one UTC day.
type DailyOutcome struct {
	Date      string `json:"date" format:"date" doc:"UTC day, YYYY-MM-DD"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed" doc:"failed and rollback_failed"`
	Other     int    `json:"other" doc:"canceled, rolled_back and still in flight"`
}

// DailyOutcomes is the response of the per-day outcome series: one entry per
// UTC day, oldest first, ending today; days without deployments are zeros.
type DailyOutcomes struct {
	Days []DailyOutcome `json:"days"`
}

// DailyOutcomes counts the deployments of an environment per UTC day over the
// last `days` days (today included).
func (s *Service) DailyOutcomes(ctx context.Context, envID uuid.UUID, days int, now time.Time) (DailyOutcomes, error) {
	if days < 1 || days > MaxOutcomeDays {
		return DailyOutcomes{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("days must be between 1 and %d", MaxOutcomeDays))
	}
	if _, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID); err != nil {
		return DailyOutcomes{}, err
	}
	start := outcomeWindowStart(now, days)
	rows, err := s.q.DeploymentOutcomesPerDay(ctx, db.DeploymentOutcomesPerDayParams{EnvironmentID: envID, Since: start})
	if err != nil {
		return DailyOutcomes{}, fmt.Errorf("deployment outcomes per day: %w", err)
	}
	return FillOutcomeDays(start, days, rows), nil
}

// outcomeWindowStart is 00:00 UTC of the oldest day of a window of `days` days
// that ends on the day of now.
func outcomeWindowStart(now time.Time, days int) time.Time {
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
}

// FillOutcomeDays lays the sparse per-day rows over a dense series of `days`
// days starting at start (a UTC midnight).
func FillOutcomeDays(start time.Time, days int, rows []db.DeploymentOutcomesPerDayRow) DailyOutcomes {
	out := DailyOutcomes{Days: make([]DailyOutcome, days)}
	index := make(map[string]int, days)
	for i := range out.Days {
		d := start.AddDate(0, 0, i).Format(time.DateOnly)
		out.Days[i].Date = d
		index[d] = i
	}
	for _, r := range rows {
		if !r.Day.Valid {
			continue
		}
		i, ok := index[r.Day.Time.Format(time.DateOnly)]
		if !ok {
			continue
		}
		out.Days[i].Succeeded = int(r.Succeeded)
		out.Days[i].Failed = int(r.Failed)
		out.Days[i].Other = int(r.Other)
	}
	return out
}
