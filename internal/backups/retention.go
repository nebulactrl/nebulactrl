package backups

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// Candidate is a stored, successful backup considered for pruning.
type Candidate struct {
	ID        uuid.UUID
	CreatedAt time.Time
	Locked    bool
}

// Prune returns the backups a retention policy removes.
//
//   - count > 0 keeps only the newest count unlocked backups.
//   - days > 0 removes unlocked backups older than days.
//   - Locked backups are never removed and do not count towards count.
//   - The newest backup is never removed, so a policy can not delete the only
//     restore point.
//
// Zero disables a limit. The result is ordered oldest first.
func Prune(cands []Candidate, count, days int, now time.Time) []uuid.UUID {
	if count <= 0 && days <= 0 || len(cands) == 0 {
		return nil
	}
	sorted := append([]Candidate(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })
	newest := sorted[0].ID
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var victims []Candidate
	kept := 0
	for _, c := range sorted {
		if c.Locked || c.ID == newest {
			if !c.Locked {
				kept++
			}
			continue
		}
		overCount := count > 0 && kept >= count
		tooOld := days > 0 && c.CreatedAt.Before(cutoff)
		if overCount || tooOld {
			victims = append(victims, c)
			continue
		}
		kept++
	}
	out := make([]uuid.UUID, len(victims))
	for i, v := range victims {
		out[len(victims)-1-i] = v.ID
	}
	return out
}
