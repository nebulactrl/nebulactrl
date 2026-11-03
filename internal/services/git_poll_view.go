package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// GitPollDefaultInterval is the check interval, in minutes, of a poll turned on without one.
const GitPollDefaultInterval = 5

// GitPollIntervals are the allowed check intervals in minutes.
var GitPollIntervals = []int{1, 5, 15, 60}

// ValidGitPollInterval reports whether minutes is an allowed interval.
func ValidGitPollInterval(minutes int) bool {
	for _, m := range GitPollIntervals {
		if m == minutes {
			return true
		}
	}
	return false
}

// GitPollView is the state of "check for new commits" of a git service.
type GitPollView struct {
	Enabled bool `json:"enabled"`
	// IntervalMinutes is how often the branch head is checked.
	IntervalMinutes int `json:"intervalMinutes" enum:"1,5,15,60"`
	// Deploy is whether a new commit found by checking deploys at once (off: it is only shown).
	Deploy    bool       `json:"deploy"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// HeadSha and HeadBranch are what the last successful check saw.
	HeadSha    string `json:"headSha,omitempty"`
	HeadBranch string `json:"headBranch,omitempty"`
	// Error is why the last check failed (empty when it worked).
	Error string `json:"error,omitempty"`
	// UpdateAvailable is true when the branch head differs from the commit the service runs.
	UpdateAvailable bool `json:"updateAvailable"`
}

// OffGitPollView is the view of a service that does not check.
func OffGitPollView() GitPollView {
	return GitPollView{IntervalMinutes: GitPollDefaultInterval, Deploy: true}
}

// NewGitPollView renders a poll row; running is the commit the service runs ("" when it runs none).
func NewGitPollView(p db.ServiceGitPoll, running string) GitPollView {
	v := GitPollView{Enabled: true, IntervalMinutes: int(p.IntervalMinutes), Deploy: p.Deploy, CheckedAt: p.CheckedAt, Error: p.LastError}
	if p.HeadSha != nil {
		v.HeadSha = *p.HeadSha
	}
	if p.HeadBranch != nil {
		v.HeadBranch = *p.HeadBranch
	}
	v.UpdateAvailable = running != "" && v.HeadSha != "" && v.HeadSha != running
	return v
}

// decorateGitPoll fills ServiceView.GitPoll for every service that checks for new commits (out is parallel to rows).
func decorateGitPoll(ctx context.Context, q *db.Queries, rows []db.Service, out []ServiceView) error {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if len(ids) == 0 {
		return nil
	}
	polls, err := q.ListGitPollsForServices(ctx, ids)
	if err != nil {
		return fmt.Errorf("list git polls: %w", err)
	}
	by := make(map[uuid.UUID]db.ServiceGitPoll, len(polls))
	for _, p := range polls {
		by[p.ServiceID] = p
	}
	for i, r := range rows {
		p, ok := by[r.ID]
		if !ok {
			continue
		}
		running := ""
		if out[i].CurrentRelease != nil {
			running = out[i].CurrentRelease.Source.CommitSHA
		}
		v := NewGitPollView(p, running)
		out[i].GitPoll = &v
	}
	return nil
}

// SupportsGitPoll reports whether the service can check for new commits: an app or worker built from a git source.
func (s Scope) SupportsGitPoll() bool {
	return s.Service.SourceType == SourceGit && (s.Service.Kind == KindApp || s.Service.Kind == KindWorker)
}
