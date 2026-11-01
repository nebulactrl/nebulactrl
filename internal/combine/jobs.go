package combine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Args drives one combine through deploy, decision and cutover.
type Args struct {
	CombineID uuid.UUID `json:"combineId"`
}

// Kind implements river.JobArgs.
func (Args) Kind() string { return "combine_services" }

const (
	// deployPoll is how often the job looks at the running deployment.
	deployPoll = 5 * time.Second
	// busyPoll is how often the cutover looks again while a member deploys.
	busyPoll = 10 * time.Second
	// maxMessage bounds a deployment message.
	maxMessage = 190
)

type worker struct {
	river.WorkerDefaults[Args]
	s *Service
}

// Work advances the combine as far as it can. A wait (a running deployment, a
// busy member) snoozes the job; a failure retries it, and the last attempt
// records the failure on the combine so nothing stays half-done unseen.
func (w *worker) Work(ctx context.Context, job *river.Job[Args]) error {
	wait, err := w.s.advance(ctx, job.Args.CombineID)
	if err != nil {
		if job.Attempt >= job.MaxAttempts {
			w.s.giveUp(ctx, job.Args.CombineID, err)
			return nil
		}
		return err
	}
	if wait > 0 {
		return river.JobSnooze(wait)
	}
	return nil
}

// RegisterJobs registers the combine worker.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &worker{s: s}) })
}

// advance runs the steps the combine's state asks for. It returns how long to
// wait before looking again (0: nothing more to do now). Every step is a
// compare-and-swap on the state, so running it twice, or two workers at once,
// does each effect at most once.
func (s *Service) advance(ctx context.Context, id uuid.UUID) (time.Duration, error) {
	for {
		c, err := s.q.GetCombine(ctx, id)
		if errNoRows(err) {
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf("get combine: %w", err)
		}
		switch c.State {
		case StateStaged:
			if moved, err := s.startDeploy(ctx, c); err != nil || !moved {
				return 0, err
			}
		case StateDeploying:
			moved, wait, err := s.decide(ctx, c)
			if err != nil || !moved {
				return wait, err
			}
		case StateCutover:
			return s.cutover(ctx, c)
		default:
			return 0, nil
		}
	}
}

// scopeOfTarget loads the target without authorization (a job has no caller).
func (s *Service) scopeOfTarget(ctx context.Context, c db.ServiceCombine) (services.Scope, error) {
	return services.LoadScopeInternal(ctx, s.q, c.TargetServiceID)
}

// startDeploy is step S2: one deployment of the target. When the target's
// current release already runs every process (a retry after a failed cutover),
// the deployment is skipped. It reports whether the state moved.
func (s *Service) startDeploy(ctx context.Context, c db.ServiceCombine) (bool, error) {
	var sp storedPlan
	if err := json.Unmarshal(c.Plan, &sp); err != nil {
		return false, s.fail(ctx, c, StateStaged, "the stored plan is unreadable: "+err.Error())
	}
	tsc, err := s.scopeOfTarget(ctx, c)
	if err != nil {
		if apiNotFound(err) {
			return false, s.fail(ctx, c, StateStaged, "the target service no longer exists")
		}
		return false, err
	}
	ctx = systemCtx(ctx, tsc.WorkspaceID, tsc.ProjectID)

	if ok, err := s.releaseRunsProcesses(ctx, s.q, tsc.Service, sp); err != nil {
		return false, err
	} else if ok {
		_, err := s.q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: c.ID, FromState: StateStaged, ToState: StateCutover})
		return err == nil, ignoreNoRows(err)
	}

	moved := false
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		// The combine row lock serializes this step with an abort: an abort that
		// wins takes the combine out of staged before a deployment exists.
		cur, err := q.LockCombine(ctx, c.ID)
		if err != nil {
			return err
		}
		if cur.State != StateStaged {
			return nil
		}
		s.stopPlanned(ctx, cur, tsc.WorkspaceID, tsc.ProjectID)
		dep, err := s.deployTarget(ctx, tsc, cur, sp)
		if err != nil {
			var ae *apierr.Error
			if errors.As(err, &ae) && ae.Status < 500 {
				// The deployment cannot start (no server, the agent is too old, a
				// blocking problem): retrying will not help. Record it.
				if _, err := q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: c.ID, FromState: StateStaged, ToState: StateFailed, Error: ae.Message}); err != nil {
					return err
				}
				return nil
			}
			return err
		}
		if _, err := q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: c.ID, FromState: StateStaged, ToState: StateDeploying, DeploymentID: &dep}); err != nil {
			return err
		}
		moved = true
		return nil
	})
	if err != nil {
		return false, err
	}
	s.publishTarget(tsc)
	if !moved {
		// Failed to start (or an abort won): put the sources back.
		if cur, err := s.q.GetCombine(ctx, c.ID); err == nil && cur.State == StateFailed {
			s.restartStopped(ctx, cur, tsc.WorkspaceID, tsc.ProjectID)
		}
		return false, nil
	}
	return true, nil
}

// deployTarget creates the one deployment of the target. A deployment of the
// target that is already running (a retried step that crashed after creating
// it) is adopted instead of started twice.
func (s *Service) deployTarget(ctx context.Context, tsc services.Scope, c db.ServiceCombine, sp storedPlan) (uuid.UUID, error) {
	msg := strings.TrimSpace(sp.Request.Message)
	if msg == "" {
		slugs := make([]string, 0, len(sp.SourceSlugs)+1)
		slugs = append(slugs, sp.TargetSlug)
		for _, slug := range sp.SourceSlugs {
			slugs = append(slugs, slug)
		}
		slices.Sort(slugs[1:])
		msg = "Combine services: " + strings.Join(slugs, ", ")
	}
	if len(msg) > maxMessage {
		msg = msg[:maxMessage]
	}
	view, err := s.Deployments.DeployWithOptions(ctx, tsc.Service.ID, deployments.DeployOptions{Message: msg, ByCombine: c.ID})
	if err == nil {
		return view.ID, nil
	}
	var ae *apierr.Error
	if errors.As(err, &ae) && ae.Code == "deployment_in_progress" {
		latest, lerr := s.q.ListLatestDeploymentsForServices(ctx, []uuid.UUID{tsc.Service.ID})
		if lerr == nil && len(latest) == 1 && deployments.Status(latest[0].Status).IsActive() && !latest[0].CreatedAt.Before(c.CreatedAt) {
			return latest[0].ID, nil
		}
	}
	return uuid.Nil, err
}

// decide is step S3: the deployment's outcome moves the combine to cutover, or
// to failed with the sources untouched. It reports whether the state moved.
func (s *Service) decide(ctx context.Context, c db.ServiceCombine) (moved bool, wait time.Duration, err error) {
	if c.DeploymentID == nil {
		return false, 0, s.fail(ctx, c, StateDeploying, "the combine has no deployment")
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*c.DeploymentID})
	if err != nil {
		return false, 0, fmt.Errorf("get deployment: %w", err)
	}
	if len(deps) != 1 {
		return false, 0, s.fail(ctx, c, StateDeploying, "the deployment no longer exists")
	}
	d := deps[0]
	switch st := deployments.Status(d.Status); {
	case st.IsActive():
		return false, deployPoll, nil
	case st == deployments.Succeeded:
		_, err := s.q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: c.ID, FromState: StateDeploying, ToState: StateCutover})
		return err == nil, 0, ignoreNoRows(err)
	default:
		msg := strings.TrimSpace(d.StatusMessage)
		if msg == "" {
			msg = "the deployment ended " + d.Status
		}
		return false, 0, s.fail(ctx, c, StateDeploying, fmt.Sprintf("the deployment %s: %s", strings.ReplaceAll(d.Status, "_", " "), msg))
	}
}

// fail records a failure: the sources stay as they are (a source stopped by
// the combine is started again), and the user retries or aborts.
func (s *Service) fail(ctx context.Context, c db.ServiceCombine, from, msg string) error {
	row, err := s.q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: c.ID, FromState: from, ToState: StateFailed, Error: clip(msg, 1000)})
	if errNoRows(err) {
		return nil // someone else moved it
	}
	if err != nil {
		return fmt.Errorf("record combine failure: %w", err)
	}
	if tsc, err := s.scopeOfTarget(ctx, row); err == nil {
		s.restartStopped(ctx, row, tsc.WorkspaceID, tsc.ProjectID)
		s.publishTarget(tsc)
	}
	return nil
}

// giveUp records the error of a step that kept failing.
func (s *Service) giveUp(ctx context.Context, id uuid.UUID, cause error) {
	c, err := s.q.GetCombine(ctx, id)
	if err != nil {
		return
	}
	switch c.State {
	case StateStaged, StateDeploying, StateCutover:
		if err := s.fail(ctx, c, c.State, "gave up: "+cause.Error()); err != nil {
			s.Logger.ErrorContext(ctx, "combine: recording the failure failed", "combine", id, "err", err)
		}
	}
}

func ignoreNoRows(err error) error {
	if errNoRows(err) {
		return nil
	}
	return err
}

func apiNotFound(err error) bool {
	var ae *apierr.Error
	return errors.As(err, &ae) && ae.Status == 404
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Service) publishTarget(tsc services.Scope) {
	services.PublishService(s.Bus, services.EventServiceUpdated, tsc)
}

// releaseRunsProcesses reports whether the target's current release already
// contains every process the combine added.
func (s *Service) releaseRunsProcesses(ctx context.Context, q *db.Queries, target db.Service, sp storedPlan) (bool, error) {
	rel, ok, err := services.AppliedRelease(ctx, q, target)
	if err != nil || !ok {
		return false, err
	}
	cfg := services.DecodeSnapshot(rel.Config).Config
	for _, n := range sp.ProcessNames {
		if _, ok := cfg.Process(n); !ok {
			return false, nil
		}
	}
	return true, nil
}

// errNotReady makes the cutover wait: a member is deploying or the release is
// not yet the current one.
var errNotReady = errors.New("the cutover is not ready")

// cutover is step S4, one transaction: the domains and tasks of the sources
// move to the target, the server gets one route table (every hostname flips
// from the old container to the new process at once), then the sources are
// deleted. A failure anywhere rolls all of it back; the state stays cutover and
// the job retries.
func (s *Service) cutover(ctx context.Context, c db.ServiceCombine) (time.Duration, error) {
	var sp storedPlan
	if err := json.Unmarshal(c.Plan, &sp); err != nil {
		return 0, s.fail(ctx, c, StateCutover, "the stored plan is unreadable: "+err.Error())
	}
	tsc, err := s.scopeOfTarget(ctx, c)
	if err != nil {
		if apiNotFound(err) {
			return 0, s.fail(ctx, c, StateCutover, "the target service no longer exists")
		}
		return 0, err
	}
	ctx = systemCtx(ctx, tsc.WorkspaceID, tsc.ProjectID)

	var afters []func(context.Context)
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		afters, err = s.cutoverTx(ctx, tx, c, sp)
		return err
	})
	switch {
	case errors.Is(err, errNotReady):
		return busyPoll, nil
	case errors.Is(err, errReleaseLacksProcesses):
		return 0, s.fail(ctx, c, StateCutover, "the target's current release does not run every process: retry to deploy it again")
	case err != nil:
		return 0, err
	}
	for _, f := range afters {
		f(ctx)
	}
	env := services.EnvScope{WorkspaceID: tsc.WorkspaceID}
	env.Env.ID, env.Env.ProjectID = tsc.EnvironmentID, tsc.ProjectID
	s.publish(env, []uuid.UUID{tsc.Service.ID})
	services.PublishIn(s.Bus, tsc.WorkspaceID, services.EventDomainUpdated, tsc.ProjectID, tsc.EnvironmentID, tsc.Service.ID)
	return 0, nil
}

// errReleaseLacksProcesses: nothing is deploying and the target's release still
// misses a process, so the cutover cannot succeed by waiting.
var errReleaseLacksProcesses = errors.New("the target's release does not run every process")

func (s *Service) cutoverTx(ctx context.Context, tx pgx.Tx, c db.ServiceCombine, sp storedPlan) ([]func(context.Context), error) {
	q := db.New(tx)
	cur, err := q.LockCombine(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	if cur.State != StateCutover {
		return nil, nil // another worker finished it
	}
	// Lock target and sources in ascending id, like the start.
	ids := append([]uuid.UUID{cur.TargetServiceID}, cur.SourceServiceIds...)
	slices.SortFunc(ids, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
	live := map[uuid.UUID]db.Service{}
	for _, id := range ids {
		row, err := q.LockServiceRow(ctx, id)
		if errNoRows(err) {
			if id == cur.TargetServiceID {
				return nil, errors.New("the target service no longer exists")
			}
			continue // a source deleted meanwhile has nothing left to move
		}
		if err != nil {
			return nil, fmt.Errorf("lock service: %w", err)
		}
		live[id] = row
	}
	target := live[cur.TargetServiceID]

	busy := false
	for id := range live {
		if n, err := q.CountActiveDeployments(ctx, id); err != nil {
			return nil, err
		} else if n > 0 {
			busy = true
		}
	}
	if ok, err := s.releaseRunsProcesses(ctx, q, target, sp); err != nil {
		return nil, err
	} else if !ok {
		if busy {
			return nil, errNotReady
		}
		return nil, errReleaseLacksProcesses
	}
	if busy {
		return nil, errNotReady
	}

	var afters []func(context.Context)
	sources := slices.Clone(cur.SourceServiceIds)
	slices.SortFunc(sources, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })

	// 1 and 2. Domains and tasks move to the target.
	for _, id := range sources {
		if _, ok := live[id]; !ok {
			continue
		}
		process := sp.ProcessNames[id.String()]
		doms, err := q.ListServiceDomains(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		domIDs := make([]uuid.UUID, len(doms))
		for i, d := range doms {
			domIDs[i] = d.ID
		}
		if err := s.Domains.MoveToService(ctx, tx, domIDs, target.ID, process); err != nil {
			return nil, fmt.Errorf("move domains of %s: %w", sp.SourceSlugs[id.String()], err)
		}
		if err := q.MoveServiceTasks(ctx, db.MoveServiceTasksParams{FromServiceID: id, ToServiceID: target.ID}); err != nil {
			return nil, fmt.Errorf("move tasks of %s: %w", sp.SourceSlugs[id.String()], err)
		}
	}

	// 3. One route table before the removals: the agent applies it whole, so
	// each hostname flips from the source's container to the target's process.
	if err := s.syncRoutes(ctx, tx, q, target); err != nil {
		return nil, err
	}

	// 4. The sources go, with the same cascade as a delete (their volumes stay).
	for _, id := range sources {
		if _, ok := live[id]; !ok {
			continue
		}
		after, err := s.Services.DeleteInternalTx(ctx, tx, id, services.DeleteOptions{
			Reason: "combined into " + sp.TargetSlug, ByCombine: c.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", sp.SourceSlugs[id.String()], err)
		}
		afters = append(afters, after)
	}

	// 5. Done.
	if _, err := q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: c.ID, FromState: StateCutover, ToState: StateDone}); err != nil {
		return nil, err
	}
	wsID, projID := uuid.Nil, uuid.Nil
	if sc, err := services.LoadScopeInternal(ctx, q, target.ID); err == nil {
		wsID, projID = sc.WorkspaceID, sc.ProjectID
	}
	err = audit.Record(ctx, tx, audit.Entry{
		Action: auditAction, ResourceType: "service", ResourceID: target.ID.String(), ProjectID: &projID, WorkspaceID: wsID,
		Metadata: map[string]any{"combine": c.ID.String(), "state": StateDone, "target": sp.TargetSlug, "sources": sp.SourceSlugs},
	})
	return afters, err
}

// syncRoutes enqueues the server's full route table. The table's counter lock
// is taken after every row lock and write of the transaction that precedes it.
func (s *Service) syncRoutes(ctx context.Context, tx pgx.Tx, q *db.Queries, target db.Service) error {
	if target.CurrentDeploymentID == nil || s.Dispatcher == nil {
		return nil
	}
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*target.CurrentDeploymentID})
	if err != nil {
		return fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return nil
	}
	serverID := *deps[0].ServerID
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	routes, seq, err := domains.ServerTable(ctx, q, serverID, uuid.Nil)
	if err != nil {
		return err
	}
	pc, err := proxyserver.BuildConfig(ctx, q, serverID)
	if err != nil {
		return err
	}
	pl := agentprotocol.SyncRoutesPayload{Routes: routes, Proxy: pc, TableSeq: seq}
	if srv.AcmeEmail != nil {
		pl.ACMEEmail = *srv.AcmeEmail
	}
	traffic := srv.TrafficAnalyticsEnabled
	pl.TrafficAnalytics = &traffic
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindSyncRoutes, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue sync_routes: %w", err)
	}
	return nil
}
