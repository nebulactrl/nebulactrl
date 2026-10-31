package hardening

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HardeningRun is one audit, apply or rollback.
type HardeningRun struct {
	ID             uuid.UUID             `json:"id"`
	Action         string                `json:"action" enum:"audit,apply,rollback"`
	Trigger        string                `json:"trigger" enum:"scheduled,manual"`
	Status         string                `json:"status" enum:"pending,succeeded,failed,skipped" doc:"skipped: Lynis is not installed on the server"`
	IndexBefore    *int                  `json:"indexBefore" doc:"The hardening index before the run; null when there was no audit yet"`
	HardeningIndex *int                  `json:"hardeningIndex" doc:"The hardening index the run measured; null when it did not audit"`
	LynisVersion   string                `json:"lynisVersion,omitempty"`
	Message        string                `json:"message,omitempty"`
	Error          string                `json:"error,omitempty"`
	Outcomes       []HardeningFixOutcome `json:"outcomes" nullable:"false" doc:"What an apply or rollback did, per fix"`
	FixIDs         []string              `json:"fixIds" nullable:"false"`
	CreatedAt      time.Time             `json:"createdAt"`
	FinishedAt     *time.Time            `json:"finishedAt"`
}

// HardeningFixOutcome is what an apply or rollback did to one fix.
type HardeningFixOutcome struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome" enum:"applied,unchanged,skipped,failed,rolled_back"`
	Message string `json:"message,omitempty"`
}

// HardeningFix is one safe fix with its state on the server.
type HardeningFix struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Why            string   `json:"why" doc:"What the fix changes and why"`
	Risk           string   `json:"risk" doc:"The honest downside"`
	Default        bool     `json:"default" doc:"Part of Apply safe fixes; the others are applied one by one"`
	RebootRequired bool     `json:"rebootRequired" doc:"Takes effect after the next reboot"`
	LynisTests     []string `json:"lynisTests" nullable:"false" doc:"The Lynis test IDs the fix answers"`
	State          string   `json:"state" enum:"applied,compliant,pending,unavailable,unknown" doc:"applied: NebulaCtrl applied it. compliant: the host already satisfies it. pending: it would change something. unavailable: it does not apply here (the message says why). unknown: no audit has reported yet"`
	Message        string   `json:"message,omitempty"`
	Manual         []string `json:"manual" nullable:"false" doc:"What to type to do the same by hand"`
}

// HardeningRemaining is a finding the latest audit still reports.
type HardeningRemaining struct {
	TestID      string   `json:"testId"`
	Severity    string   `json:"severity" enum:"warning,suggestion"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail,omitempty"`
	FixID       string   `json:"fixId,omitempty" doc:"The safe fix that answers it, when there is one"`
	FixState    string   `json:"fixState,omitempty"`
	FixMessage  string   `json:"fixMessage,omitempty"`
	Explanation string   `json:"explanation,omitempty"`
	Manual      []string `json:"manual" nullable:"false"`
	Never       string   `json:"never,omitempty" doc:"Set when NebulaCtrl deliberately never changes this, and why"`
	Link        string   `json:"link" doc:"CISOfy's page for the Lynis control"`
}

// HardeningView is the Security tab of a server.
type HardeningView struct {
	AgentSupported bool                 `json:"agentSupported" doc:"The server's agent can audit the host"`
	Weekly         bool                 `json:"weekly" doc:"A weekly audit runs (default true)"`
	Running        bool                 `json:"running" doc:"An audit or fix run waits for the agent or is running"`
	LynisInstalled bool                 `json:"lynisInstalled"`
	LynisVersion   string               `json:"lynisVersion,omitempty"`
	Mode           string               `json:"mode,omitempty" enum:"host,helper," doc:"How the agent reaches the host: directly, or through a privileged helper container"`
	HardeningIndex *int                 `json:"hardeningIndex" doc:"Lynis's 0 to 100 score from the latest audit; null before the first audit"`
	FirstIndex     *int                 `json:"firstIndex" doc:"The score of the first audit"`
	AuditedAt      *time.Time           `json:"auditedAt"`
	NextAuditAt    *time.Time           `json:"nextAuditAt" doc:"The next weekly audit; null when the weekly audit is off"`
	TestsDone      int                  `json:"testsDone"`
	RebootRequired bool                 `json:"rebootRequired" doc:"An applied fix takes effect after the next reboot"`
	PendingSafe    int                  `json:"pendingSafe" doc:"Default fixes that would change something now"`
	Warnings       int                  `json:"warnings"`
	Suggestions    int                  `json:"suggestions"`
	Fixes          []HardeningFix       `json:"fixes" nullable:"false"`
	Remaining      []HardeningRemaining `json:"remaining" nullable:"false"`
	Runs           []HardeningRun       `json:"runs" nullable:"false" doc:"The most recent runs, newest first"`
}

func runView(r db.ServerHardeningRun) HardeningRun {
	v := HardeningRun{
		ID: r.ID, Action: r.Action, Trigger: r.Trigger, Status: r.Status, LynisVersion: r.LynisVersion, Message: r.Message, Error: r.Error,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Outcomes: []HardeningFixOutcome{}, FixIDs: r.FixIds,
	}
	if v.FixIDs == nil {
		v.FixIDs = []string{}
	}
	if r.IndexBefore != nil {
		i := int(*r.IndexBefore)
		v.IndexBefore = &i
	}
	if r.HardeningIndex != nil {
		i := int(*r.HardeningIndex)
		v.HardeningIndex = &i
	}
	var outs []agentprotocol.HardeningFixOutcome
	if len(r.Outcomes) > 0 && json.Unmarshal(r.Outcomes, &outs) == nil {
		for _, o := range outs {
			v.Outcomes = append(v.Outcomes, HardeningFixOutcome{ID: o.ID, Outcome: o.Outcome, Message: o.Message})
		}
	}
	return v
}

// Get returns the Security tab of a server.
func (s *Service) Get(ctx context.Context, serverID uuid.UUID) (HardeningView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersRead, serverID)
	if err != nil {
		return HardeningView{}, err
	}
	return s.view(ctx, srv)
}

func (s *Service) view(ctx context.Context, srv db.Server) (HardeningView, error) {
	weekly := true
	if st, err := s.q.GetServerHardeningSettings(ctx, srv.ID); err == nil {
		weekly = st.Weekly
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return HardeningView{}, fmt.Errorf("get hardening settings: %w", err)
	}
	runs, err := s.q.ListServerHardeningRuns(ctx, db.ListServerHardeningRunsParams{ServerID: srv.ID, Limit: RecentRuns})
	if err != nil {
		return HardeningView{}, fmt.Errorf("list hardening runs: %w", err)
	}
	pending, err := s.q.HasPendingServerHardeningRun(ctx, srv.ID)
	if err != nil {
		return HardeningView{}, fmt.Errorf("check pending run: %w", err)
	}
	v := HardeningView{
		AgentSupported: SupportsHardening(srv.Facts), Weekly: weekly, Running: pending,
		Fixes: []HardeningFix{}, Remaining: []HardeningRemaining{}, Runs: make([]HardeningRun, 0, len(runs)),
	}
	for _, r := range runs {
		v.Runs = append(v.Runs, runView(r))
	}
	if weekly {
		next := s.now()
		if len(runs) > 0 {
			next = runs[0].CreatedAt.Add(AuditEvery)
		}
		if next.Before(s.now()) {
			next = s.now()
		}
		v.NextAuditAt = &next
	}

	var report *agentprotocol.HardeningReport
	var states []agentprotocol.HardeningFixState
	st, err := s.q.GetServerHardeningState(ctx, srv.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return HardeningView{}, fmt.Errorf("get hardening state: %w", err)
	default:
		v.LynisInstalled, v.LynisVersion, v.Mode, v.AuditedAt, v.RebootRequired = st.LynisInstalled, st.LynisVersion, st.Mode, st.AuditedAt, st.RebootRequired
		if st.HardeningIndex != nil {
			i := int(*st.HardeningIndex)
			v.HardeningIndex = &i
		}
		if st.FirstIndex != nil {
			i := int(*st.FirstIndex)
			v.FirstIndex = &i
		}
		if len(st.Report) > 0 {
			var r agentprotocol.HardeningReport
			if json.Unmarshal(st.Report, &r) == nil {
				report = &r
			}
		}
		if len(st.Fixes) > 0 {
			_ = json.Unmarshal(st.Fixes, &states)
		}
	}
	v.Fixes, v.PendingSafe = fixViews(states)
	if report != nil && v.LynisInstalled {
		v.TestsDone, v.Warnings, v.Suggestions = report.TestsDone, len(report.Warnings), len(report.Suggestions)
		for _, it := range lynis.Remaining(report, states) {
			v.Remaining = append(v.Remaining, remainingView(it))
		}
	}
	return v, nil
}

func fixViews(states []agentprotocol.HardeningFixState) ([]HardeningFix, int) {
	byID := map[string]agentprotocol.HardeningFixState{}
	for _, s := range states {
		byID[s.ID] = s
	}
	var out []HardeningFix
	pending := 0
	for _, f := range lynis.Catalog() {
		fv := HardeningFix{
			ID: f.ID, Title: f.Title, Why: f.Why, Risk: f.Risk, Default: f.Default, RebootRequired: f.Reboot,
			LynisTests: f.Tests, State: stateUnknown, Manual: f.Manual,
		}
		if st, ok := byID[f.ID]; ok {
			fv.State, fv.Message = st.State, st.Message
		}
		if f.Default && fv.State == agentprotocol.HardeningFixPending {
			pending++
		}
		out = append(out, fv)
	}
	return out, pending
}

func remainingView(it lynis.Item) HardeningRemaining {
	m := it.Manual
	if m == nil {
		m = []string{}
	}
	return HardeningRemaining{
		TestID: it.TestID, Severity: it.Severity, Title: it.Title, Detail: it.Detail, FixID: it.FixID, FixState: it.FixState,
		FixMessage: it.FixMessage, Explanation: it.Explanation, Manual: m, Never: it.Never, Link: it.Link,
	}
}
