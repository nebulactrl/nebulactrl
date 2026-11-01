package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// SweepInterval is how often every connected team is re-checked.
const SweepInterval = 10 * time.Minute

// zonesMaxAge is how old the cached zone list may get before the sweep refreshes it.
const zonesMaxAge = 6 * time.Hour

// notifyEvery is the minimum gap between two notifications about the same failure.
const notifyEvery = 6 * time.Hour

const jobTimeout = 90 * time.Second

var uniqueStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

// SyncDomainArgs brings one domain's DNS records in line with its server.
type SyncDomainArgs struct {
	DomainID uuid.UUID `json:"domainId"`
}

// Kind implements river.JobArgs.
func (SyncDomainArgs) Kind() string { return "cloudflare_sync_domain" }

// InsertOpts implements river.JobArgsWithInsertOpts: retried with River's
// backoff, and one pending job per domain is enough.
func (SyncDomainArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// SyncStatusPageArgs brings one status page's DNS records in line with the
// control-plane server.
type SyncStatusPageArgs struct {
	PageID uuid.UUID `json:"pageId"`
}

// Kind implements river.JobArgs.
func (SyncStatusPageArgs) Kind() string { return "cloudflare_sync_status_page" }

// InsertOpts implements river.JobArgsWithInsertOpts: retried with River's
// backoff, and one pending job per page is enough.
func (SyncStatusPageArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// syncArgs are the job arguments that sync one name.
func syncArgs(kind OwnerKind, id uuid.UUID) river.JobArgs {
	if kind == OwnerStatusPage {
		return SyncStatusPageArgs{PageID: id}
	}
	return SyncDomainArgs{DomainID: id}
}

// CollectArgs deletes the records of hostnames a team no longer uses.
type CollectArgs struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
}

// Kind implements river.JobArgs.
func (CollectArgs) Kind() string { return "cloudflare_collect" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (CollectArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// SweepArgs refreshes stale zone lists and re-checks every domain of every connected team.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "cloudflare_sweep" }

// IPRefreshArgs fetches Cloudflare's IP ranges (every IPRefreshInterval).
type IPRefreshArgs struct{}

// Kind implements river.JobArgs.
func (IPRefreshArgs) Kind() string { return "cloudflare_ip_refresh" }

// InsertOpts implements river.JobArgsWithInsertOpts: one refresh at a time.
func (IPRefreshArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// EnqueueSyncDomain schedules a sync of one domain inside tx.
func (s *Service) EnqueueSyncDomain(ctx context.Context, tx pgx.Tx, domainID uuid.UUID) error {
	return s.enqueueSync(ctx, tx, OwnerDomain, domainID)
}

// EnqueueSyncStatusPage schedules a sync of one status page's records inside tx.
func (s *Service) EnqueueSyncStatusPage(ctx context.Context, tx pgx.Tx, pageID uuid.UUID) error {
	return s.enqueueSync(ctx, tx, OwnerStatusPage, pageID)
}

func (s *Service) enqueueSync(ctx context.Context, tx pgx.Tx, kind OwnerKind, id uuid.UUID) error {
	if s.jobs == nil {
		return nil
	}
	if err := s.jobs.InsertTx(ctx, tx, syncArgs(kind, id), nil); err != nil {
		return fmt.Errorf("enqueue cloudflare sync: %w", err)
	}
	return nil
}

// EnqueueCollect schedules the collector of a team inside tx.
func (s *Service) EnqueueCollect(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	if s.jobs == nil {
		return nil
	}
	if err := s.jobs.InsertTx(ctx, tx, CollectArgs{WorkspaceID: ws}, nil); err != nil {
		return fmt.Errorf("enqueue cloudflare collect: %w", err)
	}
	return nil
}

// retryLater schedules a sync after a failed inline attempt.
func (s *Service) retryLater(ctx context.Context, kind OwnerKind, id uuid.UUID) {
	if s.jobs == nil {
		return
	}
	if err := s.jobs.Insert(ctx, syncArgs(kind, id), nil); err != nil {
		s.log.WarnContext(ctx, "could not schedule cloudflare sync retry", "owner_kind", string(kind), "owner_id", id.String(), "err", err)
	}
}

type syncWorker struct {
	river.WorkerDefaults[SyncDomainArgs]
	svc *Service
}

type syncPageWorker struct {
	river.WorkerDefaults[SyncStatusPageArgs]
	svc *Service
}

type collectWorker struct {
	river.WorkerDefaults[CollectArgs]
	svc *Service
}

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	svc *Service
}

type ipRefreshWorker struct {
	river.WorkerDefaults[IPRefreshArgs]
	svc *Service
}

// RegisterJobs registers the sync, collector and sweep workers and the
// periodic sweep.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &syncWorker{svc: svc})
		river.AddWorker(w, &syncPageWorker{svc: svc})
		river.AddWorker(w, &collectWorker{svc: svc})
		river.AddWorker(w, &ipRefreshWorker{svc: svc})
		river.AddWorker(w, &sweepWorker{svc: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(SweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, nil },
		&river.PeriodicJobOpts{ID: "cloudflare_sweep"},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(IPRefreshInterval),
		func() (river.JobArgs, *river.InsertOpts) { return IPRefreshArgs{}, nil },
		&river.PeriodicJobOpts{ID: "cloudflare_ip_refresh", RunOnStart: true},
	))
}

// permanent reports whether retrying cannot help: Cloudflare refused the
// request (bad token, missing permission, missing zone, invalid record).
func permanent(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && !ae.Retryable()
}

func (w *syncWorker) Work(ctx context.Context, job *river.Job[SyncDomainArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	return w.svc.workSync(ctx, OwnerDomain, job.Args.DomainID, w.svc.SyncDomain, job.Attempt, job.MaxAttempts)
}

func (w *syncPageWorker) Work(ctx context.Context, job *river.Job[SyncStatusPageArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	return w.svc.workSync(ctx, OwnerStatusPage, job.Args.PageID, w.svc.SyncStatusPage, job.Attempt, job.MaxAttempts)
}

// workSync runs one sync job: a failure is recorded on the owner, and a
// permanent one (or the last attempt) is announced.
func (s *Service) workSync(ctx context.Context, kind OwnerKind, id uuid.UUID, sync func(context.Context, uuid.UUID) error, attempt, maxAttempts int) error {
	err := sync(ctx, id)
	if err == nil {
		return nil
	}
	perm := permanent(err)
	s.reportFailure(ctx, kind, id, err, perm || attempt >= maxAttempts)
	if perm {
		return river.JobCancel(err)
	}
	return err
}

func (w *collectWorker) Work(ctx context.Context, job *river.Job[CollectArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	err := w.svc.Collect(ctx, job.Args.WorkspaceID)
	if err == nil {
		return nil
	}
	if permanent(err) {
		w.svc.log.WarnContext(ctx, "cloudflare collector gave up", "workspace_id", job.Args.WorkspaceID.String(), "err", err)
		return river.JobCancel(err)
	}
	return err
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return w.svc.Sweep(ctx)
}

// Work fetches the list. A list that cannot be fetched or validated is not a
// job failure (the last good list stays and the failure is recorded); a failed
// database write is, so River retries it.
func (w *ipRefreshWorker) Work(ctx context.Context, _ *river.Job[IPRefreshArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	_, err := w.svc.RefreshIPs(ctx)
	return err
}

// Sweep refreshes stale zone lists and schedules a sync of every domain in a
// connected zone plus the collector, for every team with a connection.
func (s *Service) Sweep(ctx context.Context) error {
	wss, err := s.q.ListCloudflareWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list cloudflare workspaces: %w", err)
	}
	for _, ws := range wss {
		if err := s.sweepWorkspace(ctx, ws); err != nil {
			s.log.WarnContext(ctx, "cloudflare sweep failed", "workspace_id", ws.String(), "err", err)
		}
	}
	return nil
}

func (s *Service) sweepWorkspace(ctx context.Context, ws uuid.UUID) error {
	conn, ok, err := s.hasConnection(ctx, s.q, ws)
	if err != nil || !ok {
		return err
	}
	if conn.Status != "ok" || conn.ZonesSyncedAt == nil || s.now().Sub(*conn.ZonesSyncedAt) > zonesMaxAge {
		fresh, err := s.refresh(ctx, ws)
		if err != nil {
			s.reportConnectionFailure(ctx, ws, err)
			return err
		}
		conn = fresh
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.rematchPages(ctx, tx, conn) })
	if err != nil {
		return fmt.Errorf("match status pages: %w", err)
	}
	doms, err := s.q.ListCloudflareWorkspaceDomains(ctx, ws)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	for _, d := range doms {
		if d.CfZoneID == "" {
			continue
		}
		if s.jobs != nil {
			if err := s.jobs.Insert(ctx, SyncDomainArgs{DomainID: d.ID}, nil); err != nil {
				return fmt.Errorf("enqueue sync: %w", err)
			}
		}
	}
	pages, err := s.q.ListCloudflareWorkspaceStatusPages(ctx, ws)
	if err != nil {
		return fmt.Errorf("list status pages: %w", err)
	}
	for _, p := range pages {
		if p.CfZoneID == "" {
			continue
		}
		if s.jobs != nil {
			if err := s.jobs.Insert(ctx, SyncStatusPageArgs{PageID: p.ID}, nil); err != nil {
				return fmt.Errorf("enqueue status page sync: %w", err)
			}
		}
	}
	if s.jobs != nil {
		if err := s.jobs.Insert(ctx, CollectArgs{WorkspaceID: ws}, nil); err != nil {
			return fmt.Errorf("enqueue collect: %w", err)
		}
	}
	return nil
}

// shouldNotify reports whether a notification with this key may go out now
// (one per key per notifyEvery).
func (s *Service) shouldNotify(key string) bool {
	now := s.now()
	if v, ok := s.notified.Load(key); ok {
		if t, _ := v.(time.Time); now.Sub(t) < notifyEvery {
			return false
		}
	}
	s.notified.Store(key, now)
	return true
}

// tokenRejected reports whether err means Cloudflare refused the token.
func tokenRejected(err error) bool {
	if errors.Is(err, ErrAuth) {
		return true
	}
	ae, ok := apierr.As(err)
	return ok && ae.Code == "invalid_token"
}

// reportConnectionFailure notes a rejected token on the connection and tells
// the team once.
func (s *Service) reportConnectionFailure(ctx context.Context, ws uuid.UUID, err error) {
	if !tokenRejected(err) || !s.shouldNotify("conn/"+ws.String()) {
		return
	}
	notifications.Emit(ctx, s.emitter(), notifications.Event{
		Type: notifications.TypeDNSSyncFailed, Severity: notifications.SeverityError, WorkspaceID: ws,
		Title: "Cloudflare token needs attention",
		Body:  "Cloudflare rejected the API token, so DNS records are no longer kept in sync. Replace the token in Team settings → Integrations.",
		URL:   "/settings/integrations",
	})
}

// reportFailure stores the failure of a sync. final marks the last attempt (or
// a permanent failure): that one is announced, once per six hours.
func (s *Service) reportFailure(ctx context.Context, kind OwnerKind, id uuid.UUID, err error, final bool) {
	s.log.WarnContext(ctx, "cloudflare dns sync failed", "owner_kind", string(kind), "owner_id", id.String(), "final", final, "err", errorText(err))
	ow := s.ownerOf(kind)
	n, lerr := ow.load(ctx, id)
	if lerr != nil || n == nil {
		return
	}
	if ae := (*APIError)(nil); errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		msg := "Cloudflare rejected the API token: " + errorText(err)
		_ = s.q.SetCloudflareFailed(ctx, db.SetCloudflareFailedParams{WorkspaceID: n.Workspace, StatusMessage: msg})
		s.reportConnectionFailure(ctx, n.Workspace, err)
		return
	}
	if !final || !s.shouldNotify("dom/"+id.String()+"/"+errorText(err)) {
		return
	}
	notifications.Emit(ctx, s.emitter(), ow.failure(n, err))
}
