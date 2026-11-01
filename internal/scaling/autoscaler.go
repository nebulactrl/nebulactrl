package scaling

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Autoscaler tuning.
const (
	// ScaleOutWindow is how long the per-replica average CPU must stay above
	// the target before a replica is added.
	ScaleOutWindow = 2 * time.Minute
	// ScaleInWindow is how long it must stay below the scale-in threshold
	// before a replica is removed.
	ScaleInWindow = 10 * time.Minute
	// ScaleOutCooldown follows a scale-out; ScaleInCooldown follows a scale-in.
	ScaleOutCooldown = 3 * time.Minute
	ScaleInCooldown  = 10 * time.Minute
	// ScaleInBelowPercent is the scale-in threshold (percent of the CPU
	// limit); it never exceeds half of the target, so a low target cannot make
	// the autoscaler flap.
	ScaleInBelowPercent = 35.0

	// Period is how often the autoscaler runs.
	Period = 30 * time.Second

	outStep = 30 * time.Second
	inStep  = time.Minute

	// sampleFreshness is how recent the newest host and replica samples must
	// be to judge capacity.
	sampleFreshness = 2 * time.Minute
	bytesPerGiB     = 1 << 30
	bytesPerMiB     = 1 << 20
)

// BlockedPrefix starts every scale-out-blocked reason.
const BlockedPrefix = "Scale-out blocked: "

// Bucket is the mean per-replica CPU (percent of one core) of one fixed time bucket.
type Bucket struct {
	Start time.Time
	CPU   float64
}

// Direction of a scaling decision.
const (
	Hold     = 0
	ScaleOut = 1
	ScaleIn  = -1
)

// Decision is the autoscaler's verdict for one service.
type Decision struct {
	Direction int
	// Reason explains a scale action, e.g. "CPU 84% > 70% for 2m".
	Reason string
}

// DecideInput is everything Decide needs; it is pure so the policy is testable.
type DecideInput struct {
	Now      time.Time
	Replicas int
	Auto     services.Autoscaling
	// CPULimit is the replica's CPU limit in cores (0 = one core).
	CPULimit float64
	// LastScale is the time of the last scale event; LastScaledUp says whether
	// it added replicas.
	LastScale    *time.Time
	LastScaledUp bool
	// OutBuckets and InBuckets are per-replica CPU buckets of outStep and
	// inStep width covering ScaleOutWindow and ScaleInWindow, any order.
	OutBuckets []Bucket
	InBuckets  []Bucket
}

// ScaleInThreshold returns the scale-in threshold for a target (percent).
func ScaleInThreshold(target int) float64 { return math.Min(ScaleInBelowPercent, float64(target)/2) }

// Decide applies the autoscaling policy: +1 replica when the average CPU
// stayed above the target for ScaleOutWindow, -1 when it stayed below the
// scale-in threshold for ScaleInWindow, within [min, max] and outside the
// cooldown of the last scale event. Windows are made of complete buckets that
// all must carry samples; a gap resets the clock.
func Decide(in DecideInput) Decision {
	if in.Auto.Min > in.Auto.Max || in.Replicas < 1 {
		return Decision{}
	}
	if in.LastScale != nil {
		cd := ScaleInCooldown
		if in.LastScaledUp {
			cd = ScaleOutCooldown
		}
		if in.Now.Sub(*in.LastScale) < cd {
			return Decision{}
		}
	}
	limit := in.CPULimit
	if limit <= 0 {
		limit = 1
	}
	pct := func(cpu float64) float64 { return cpu / limit }

	if in.Replicas < in.Auto.Max {
		target := float64(in.Auto.TargetCPUPercent)
		if avg, ok := sustained(in.OutBuckets, in.Now, ScaleOutWindow, outStep, func(cpu float64) bool { return pct(cpu) > target }); ok {
			return Decision{Direction: ScaleOut, Reason: fmt.Sprintf("CPU %.0f%% > %d%% for %s", pct(avg), in.Auto.TargetCPUPercent, windowLabel(ScaleOutWindow))}
		}
	}
	if in.Replicas > in.Auto.Min {
		below := ScaleInThreshold(in.Auto.TargetCPUPercent)
		if avg, ok := sustained(in.InBuckets, in.Now, ScaleInWindow, inStep, func(cpu float64) bool { return pct(cpu) < below }); ok {
			return Decision{Direction: ScaleIn, Reason: fmt.Sprintf("CPU %.0f%% < %.0f%% for %s", pct(avg), below, windowLabel(ScaleInWindow))}
		}
	}
	return Decision{}
}

func windowLabel(d time.Duration) string { return fmt.Sprintf("%dm", int(d.Minutes())) }

// sustained reports whether every complete step-sized bucket of the window
// that ends at the last bucket boundary before now has samples and satisfies
// ok. It returns the mean of those buckets.
func sustained(buckets []Bucket, now time.Time, window, step time.Duration, ok func(cpu float64) bool) (float64, bool) {
	end := now.Truncate(step)
	n := int(window / step)
	byStart := make(map[int64]float64, len(buckets))
	for _, b := range buckets {
		byStart[b.Start.Unix()] = b.CPU
	}
	var sum float64
	for i := 1; i <= n; i++ {
		cpu, present := byStart[end.Add(-time.Duration(i)*step).Unix()]
		if !present || !ok(cpu) {
			return 0, false
		}
		sum += cpu
	}
	return sum / float64(n), true
}

// AutoscaleArgs is the periodic autoscaler job.
type AutoscaleArgs struct{}

// Kind implements river.JobArgs.
func (AutoscaleArgs) Kind() string { return "autoscale" }

type autoscaleWorker struct {
	river.WorkerDefaults[AutoscaleArgs]
	s *Service
}

func (w *autoscaleWorker) Work(ctx context.Context, _ *river.Job[AutoscaleArgs]) error {
	return w.s.RunAutoscaler(ctx)
}

// RegisterJobs registers the autoscaler to run every Period.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &autoscaleWorker{s: s}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(Period),
		func() (river.JobArgs, *river.InsertOpts) { return AutoscaleArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "autoscale", RunOnStart: false},
	))
}

// RunAutoscaler evaluates every deployed service with autoscaling enabled: the
// default process of each, and the named processes of services that run several.
// A failure on one of them is logged and does not stop the others.
func (s *Service) RunAutoscaler(ctx context.Context) error {
	rows, err := s.q.ListAutoscalingServices(ctx)
	if err != nil {
		return fmt.Errorf("list autoscaling services: %w", err)
	}
	for _, r := range rows {
		if r.DeploymentServerID == nil {
			continue
		}
		if err := s.evaluate(ctx, r.Service.ID, *r.DeploymentServerID, ""); err != nil && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "autoscale service", "service", r.Service.Slug, "service_id", r.Service.ID.String(), "err", err)
		}
	}
	procs, err := s.q.ListAutoscalingProcesses(ctx)
	if err != nil {
		return fmt.Errorf("list autoscaling processes: %w", err)
	}
	for _, r := range procs {
		if r.DeploymentServerID == nil {
			continue
		}
		if err := s.evaluate(ctx, r.Service.ID, *r.DeploymentServerID, r.ServiceProcessScale.Process); err != nil && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "autoscale process", "service", r.Service.Slug, "service_id", r.Service.ID.String(), "process", r.ServiceProcessScale.Process, "err", err)
		}
	}
	return nil
}

// scaleSubject is the process one autoscaler pass looks at, with its stored scale.
type scaleSubject struct {
	// process is "" for the default process (the service's own columns) and the
	// name of a named process (its service_process_scale row, the staged name).
	process string
	// running is the name the running release knows a named process by: the
	// staged name, or the old name while a rename is pending (scale commands
	// and samples address it). Empty for the default process.
	running string
	// label names the process in notices and audit entries: empty unless the
	// service runs several processes.
	label string
	p     services.EffectiveProcess
	// samples is the process filter of the metric queries: samples carry a
	// process name only when the running release has processes.
	samples        string
	replicas       int
	auto           services.Autoscaling
	lastScaleAt    *time.Time
	lastScaleEvent *services.ScaleEvent
	blocked        *string
}

// runningProcessName is the name of the staged named process stagedName in the
// running release: the same name, or the old name while a rename to it is
// pending. ok is false when the release has no such process.
func (s *Service) runningProcessName(ctx context.Context, serviceID uuid.UUID, deployed services.Config, stagedName string) (name string, ok bool, err error) {
	if !deployed.HasProcesses() {
		return "", false, nil
	}
	if dp, in := deployed.Process(stagedName); in {
		return stagedName, !dp.Default, nil
	}
	renames, err := services.PendingProcessRenames(ctx, s.q, serviceID)
	if err != nil {
		return "", false, err
	}
	for from, to := range renames {
		if dp, in := deployed.Process(from); to == stagedName && in && !dp.Default {
			return from, true, nil
		}
	}
	return "", false, nil
}

// subject resolves the process of a service for the autoscaler. process is the
// staged name, or the name of the running release while a rename to a staged name
// is pending; a scale decision is stored on the staged name's scale row and
// applied to the running process. ok is false when there is nothing to judge: the
// process left the configuration, or a named process is not part of the running
// release yet.
func (s *Service) subject(ctx context.Context, sc services.Scope, process string) (sub scaleSubject, ok bool, err error) {
	cfg := sc.Config()
	p, found := cfg.Process(process)
	if !found {
		// The running release may still know the process by the name a staged
		// rename replaces.
		renames, err := services.PendingProcessRenames(ctx, s.q, sc.Service.ID)
		if err != nil {
			return sub, false, err
		}
		to, pending := renames[process]
		if !pending {
			return sub, false, nil
		}
		if p, found = cfg.Process(to); !found {
			return sub, false, nil
		}
	}
	deployed := cfg
	rel, has, err := services.AppliedRelease(ctx, s.q, sc.Service)
	if err != nil {
		return sub, false, err
	}
	if has {
		deployed = services.DecodeSnapshot(rel.Config).Config
	}
	sub = scaleSubject{p: p, label: notifyProcess(cfg, p.Name)}
	if p.Default {
		sv := sc.Service
		sub.replicas, sub.auto = int(sv.Replicas), services.DecodeAutoscaling(sv.Autoscaling)
		sub.lastScaleAt, sub.lastScaleEvent, sub.blocked = sv.LastScaleAt, services.DecodeScaleEvent(sv.LastScaleEvent), sv.ScaleBlockedReason
		if deployed.HasProcesses() {
			sub.samples = deployed.DefaultProcessName()
		}
		return sub, true, nil
	}
	running, in, err := s.runningProcessName(ctx, sc.Service.ID, deployed, p.Name)
	if err != nil {
		return sub, false, err
	}
	if !in {
		return sub, false, nil
	}
	rows, err := s.q.ListProcessScales(ctx, sc.Service.ID)
	if err != nil {
		return sub, false, fmt.Errorf("list process scales: %w", err)
	}
	ps := services.ProcessScalingOf(rows, p.Name)
	if running != p.Name && !ps.Autoscaling.Enabled {
		return sub, false, nil // the staged row is the authority, and the old row's setting is not
	}
	sub.process, sub.running, sub.samples = p.Name, running, running
	sub.replicas, sub.auto, sub.lastScaleAt, sub.lastScaleEvent, sub.blocked = ps.Replicas, ps.Autoscaling, ps.LastScaleAt, ps.LastScaleEvent, ps.BlockedReason
	return sub, true, nil
}

// evaluate runs the policy for one process of a service ("" = the default
// process) and carries out its decision.
func (s *Service) evaluate(ctx context.Context, id, serverID uuid.UUID, process string) error {
	sc, err := services.LoadScopeInternal(ctx, s.q, id)
	if err != nil {
		return nil // deleted
	}
	sv := sc.Service
	cfg := sc.Config()
	sub, ok, err := s.subject(ctx, sc, process)
	if err != nil || !ok {
		return err
	}
	vols, err := s.q.CountServiceVolumes(ctx, sv.ID)
	if err != nil {
		return fmt.Errorf("count volumes: %w", err)
	}
	if ok, _ := services.ProcessEligibility(sv.Kind, cfg, int(vols), sub.p); !ok || sv.DesiredState == services.DesiredStopped {
		return nil
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.Status != "online" {
		return nil // no fresh samples to judge, and the command could not be delivered
	}
	active, err := s.q.CountActiveDeployments(ctx, sv.ID)
	if err != nil {
		return fmt.Errorf("count active deployments: %w", err)
	}
	if active > 0 {
		return nil
	}

	auto := sub.auto
	replicas := sub.replicas
	now := s.now()

	var d Decision
	switch {
	case replicas < auto.Min:
		d = Decision{Direction: ScaleOut, Reason: fmt.Sprintf("Below the autoscaling minimum of %d", auto.Min)}
	case replicas > auto.Max:
		d = Decision{Direction: ScaleIn, Reason: fmt.Sprintf("Above the autoscaling maximum of %d", auto.Max)}
	default:
		in := DecideInput{Now: now, Replicas: replicas, Auto: auto, CPULimit: sub.p.CPUs, LastScale: sub.lastScaleAt}
		if ev := sub.lastScaleEvent; ev != nil {
			in.LastScaledUp = ev.To > ev.From
		}
		if in.OutBuckets, err = s.buckets(ctx, sv.ID, sub.samples, now, ScaleOutWindow, outStep); err != nil {
			return err
		}
		if in.InBuckets, err = s.buckets(ctx, sv.ID, sub.samples, now, ScaleInWindow, inStep); err != nil {
			return err
		}
		d = Decide(in)
	}

	if d.Direction != ScaleOut {
		// Whatever blocked a scale-out earlier no longer applies.
		if sub.blocked != nil && strings.HasPrefix(*sub.blocked, BlockedPrefix) {
			if err := s.setBlocked(ctx, sc, sub, nil); err != nil {
				return err
			}
		}
		if d.Direction == Hold {
			return nil
		}
	} else {
		reason, err := s.capacityBlock(ctx, srv, sv.ID, sub, now)
		if err != nil {
			return err
		}
		if reason != "" {
			return s.setBlocked(ctx, sc, sub, &reason)
		}
	}

	to := min(max(replicas+d.Direction, auto.Min), auto.Max)
	if to == replicas {
		return nil
	}
	return s.autoscale(ctx, sc, sub, replicas, to, d.Reason)
}

// buckets reads the per-replica CPU of the service (or of one of its processes,
// see scaleSubject.samples) in step-sized buckets covering the window (plus the
// bucket in progress).
func (s *Service) buckets(ctx context.Context, id uuid.UUID, process string, now time.Time, window, step time.Duration) ([]Bucket, error) {
	rows, err := s.q.ServiceCPUBuckets(ctx, db.ServiceCPUBucketsParams{
		BucketSecs: step.Seconds(), ServiceID: &id, Since: now.Truncate(step).Add(-window), Process: process,
	})
	if err != nil {
		return nil, fmt.Errorf("cpu buckets: %w", err)
	}
	out := make([]Bucket, len(rows))
	for i, r := range rows {
		out[i] = Bucket{Start: r.Bucket, CPU: r.CpuAvg}
	}
	return out, nil
}

// capacityBlock returns why a scale-out must not happen on the server, or "".
// It blocks when the server's free memory is below one replica's memory (the
// process' configured limit, else the observed average per replica) or a disk
// alert is active.
func (s *Service) capacityBlock(ctx context.Context, srv db.Server, id uuid.UUID, sub scaleSubject, now time.Time) (string, error) {
	if srv.DiskAlertActive {
		return fmt.Sprintf("%s%s has an active disk alert", BlockedPrefix, srv.Name), nil
	}
	host, err := s.q.LatestHostMetricSample(ctx, db.LatestHostMetricSampleParams{ServerID: srv.ID, Since: now.Add(-sampleFreshness)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // no memory reading: cannot judge
	}
	if err != nil {
		return "", fmt.Errorf("host sample: %w", err)
	}
	need := int64(sub.p.MemoryMB) * bytesPerMiB
	if need == 0 {
		reps, err := s.q.LatestServiceInstantReplicaSamples(ctx, db.LatestServiceInstantReplicaSamplesParams{ServiceID: &id, Since: now.Add(-sampleFreshness), Process: sub.samples})
		if err != nil {
			return "", fmt.Errorf("replica samples: %w", err)
		}
		var sum int64
		for _, r := range reps {
			sum += r.MemBytes
		}
		if len(reps) > 0 {
			need = sum / int64(len(reps))
		}
	}
	free := max(host.MemLimitBytes-host.MemBytes, 0)
	if need > 0 && free < need {
		return fmt.Sprintf("%s%s has %.1f GiB free", BlockedPrefix, srv.Name, float64(free)/bytesPerGiB), nil
	}
	return "", nil
}

// setBlocked stores (or clears, with nil) the blocked reason of the process and
// announces a change.
func (s *Service) setBlocked(ctx context.Context, sc services.Scope, sub scaleSubject, reason *string) error {
	var err error
	if sub.process != "" {
		_, err = s.q.SetProcessScaleBlocked(ctx, db.SetProcessScaleBlockedParams{ServiceID: sc.Service.ID, Process: sub.process, Reason: reason})
	} else {
		_, err = s.q.SetServiceScaleBlocked(ctx, db.SetServiceScaleBlockedParams{ID: sc.Service.ID, Reason: reason})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // unchanged
	}
	if err != nil {
		return fmt.Errorf("store blocked reason: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	if reason != nil {
		s.notifyBlocked(ctx, sc, sub.label, *reason)
	}
	return nil
}

// autoscale carries out a scale action as the system actor.
func (s *Service) autoscale(ctx context.Context, sc services.Scope, sub scaleSubject, from, to int, reason string) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if sub.process != "" && sub.running != sub.process {
			err = s.applyRenamedProcessScale(ctx, tx, sc, sub, from, to, reason, services.ActorAutoscaler)
		} else if sub.process != "" {
			err = s.applyProcessScale(ctx, tx, sc, sub.process, from, to, reason, services.ActorAutoscaler)
		} else {
			err = s.applyScale(ctx, tx, sc, from, to, reason, services.ActorAutoscaler)
		}
		if err != nil {
			return err
		}
		meta := map[string]any{"slug": sc.Service.Slug, "from": from, "to": to, "reason": reason}
		if sub.label != "" {
			meta["process"] = sub.label
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionAutoscale, ResourceType: "service", ResourceID: sc.Service.ID.String(),
			ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID, Metadata: meta,
		})
	})
	if err != nil {
		return fmt.Errorf("autoscale %d -> %d: %w", from, to, err)
	}
	s.logger.InfoContext(ctx, "autoscaled service", "service", sc.Service.Slug, "service_id", sc.Service.ID.String(), "process", sub.label, "from", from, "to", to, "reason", reason)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return nil
}

// applyRenamedProcessScale is applyProcessScale for a process with a pending
// rename: the count is stored on the row of the staged (new) name, and the
// scale_service command addresses the running release's (old) name.
func (s *Service) applyRenamedProcessScale(ctx context.Context, tx pgx.Tx, sc services.Scope, sub scaleSubject, from, to int, reason, actor string) error {
	row, err := db.New(tx).ApplyProcessScale(ctx, db.ApplyProcessScaleParams{
		ServiceID: sc.Service.ID, Process: sub.process, Replicas: int32(to), ExpectedReplicas: int32(from),
		LastScaleEvent: services.NewScaleEvent(from, to, reason, actor),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Conflict("scale_conflict", "the replica count changed concurrently; retry")
		}
		return fmt.Errorf("store replicas of process %s: %w", sub.process, err)
	}
	return s.enqueueScale(ctx, tx, sc, sub.running, to, row.LastScaleAt)
}
