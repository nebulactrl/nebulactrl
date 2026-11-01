package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
)

// ServiceMetricPoint is one bucketed sample of a service's containers. The
// values aggregate every replica: cpuPercent, memBytes and memLimitBytes are
// sums over the replicas, cpuPerReplica is the per-replica average CPU. cpuMax
// and memMax are the peaks inside the bucket.
type ServiceMetricPoint struct {
	TS            time.Time `json:"ts"`
	CPUPercent    float64   `json:"cpuPercent" doc:"Sum over the replicas, in percent of one core"`
	CPUPerReplica float64   `json:"cpuPerReplica" doc:"Average per replica, in percent of one core"`
	CPUMax        float64   `json:"cpuMax"`
	MemBytes      int64     `json:"memBytes" doc:"Sum over the replicas"`
	MemMax        int64     `json:"memMax"`
	MemLimitBytes int64     `json:"memLimitBytes"`
	ReplicaCount  int       `json:"replicaCount" doc:"Replica containers sampled in this bucket"`
	// Replicas is the per-replica usage at the newest sample; only the last point carries it.
	Replicas []ReplicaMetric `json:"replicas,omitempty"`
}

// ReplicaMetric is the usage of one replica container at a sample instant.
type ReplicaMetric struct {
	Index      int     `json:"index"`
	CPUPercent float64 `json:"cpuPercent"`
	MemBytes   int64   `json:"memBytes"`
}

// latestReplicaWindow is how recent the newest sample must be to report per-replica usage.
const latestReplicaWindow = 2 * time.Minute

// MetricsResult is a bucketed series of one service's container usage.
type MetricsResult struct {
	Points        []ServiceMetricPoint
	StepSeconds   int
	RetentionDays int
}

// Metrics returns up to servers.MaxMetricPoints points for the range (default
// 1h), read from the raw, 1-minute or 15-minute tier per servers.PlanRange.
// process "" is the service total (every process summed); a process name of a
// service with processes narrows the series, and the last point's per-replica
// usage, to that process. Samples older than the feature carry no process, so
// they only show in the total.
func (s *Service) Metrics(ctx context.Context, id uuid.UUID, rng, process string) (MetricsResult, error) {
	sc, err := LoadScope(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return MetricsResult{}, err
	}
	if err := CheckLogProcess(sc, process); err != nil {
		return MetricsResult{}, err
	}
	plan, err := servers.PlanRange(rng)
	if err != nil {
		return MetricsResult{}, apierr.Unprocessable("invalid_range", err.Error())
	}
	retention := servers.DefaultMetricsRetentionDays
	if sc.Service.ServerID != nil {
		if srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sc.Service.ServerID}); err == nil {
			retention = int(srv.MetricsRetentionDays)
		}
	}
	since, step := time.Now().Add(-plan.Window), plan.Step.Seconds()

	out := MetricsResult{StepSeconds: int(plan.Step.Seconds()), RetentionDays: retention}
	add := func(b time.Time, cpuAvg, cpuMax float64, memAvg, memMax, memLimit int64, replicas int32) {
		n := max(int(replicas), 1)
		out.Points = append(out.Points, ServiceMetricPoint{
			TS: b, CPUPercent: cpuAvg, CPUPerReplica: cpuAvg / float64(n), CPUMax: cpuMax,
			MemBytes: memAvg, MemMax: memMax, MemLimitBytes: memLimit, ReplicaCount: n,
		})
	}
	switch plan.Tier {
	case servers.TierRaw:
		rows, err := s.q.ServiceMetricsRaw(ctx, db.ServiceMetricsRawParams{ServiceID: &id, Since: since, BucketSecs: step, Process: process})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("service metrics: %w", err)
		}
		for _, r := range rows {
			add(r.Bucket, r.CpuAvg, r.CpuMax, r.MemAvg, r.MemMax, r.MemLimit, r.Replicas)
		}
	case servers.Tier1m:
		rows, err := s.q.ServiceMetrics1m(ctx, db.ServiceMetrics1mParams{ServiceID: &id, Since: since, BucketSecs: step, Process: process})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("service metrics: %w", err)
		}
		for _, r := range rows {
			add(r.Bucket, r.CpuAvg, r.CpuMax, r.MemAvg, r.MemMax, r.MemLimit, r.Replicas)
		}
	default:
		rows, err := s.q.ServiceMetrics15m(ctx, db.ServiceMetrics15mParams{ServiceID: &id, Since: since, BucketSecs: step, Process: process})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("service metrics: %w", err)
		}
		for _, r := range rows {
			add(r.Bucket, r.CpuAvg, r.CpuMax, r.MemAvg, r.MemMax, r.MemLimit, r.Replicas)
		}
	}
	if out.Points == nil {
		out.Points = []ServiceMetricPoint{}
	}
	// A replica index counts within its process, so the total of a service with
	// processes has no meaningful per-replica list.
	if n := len(out.Points); n > 0 && (process != "" || !sc.Config().HasProcesses()) {
		cur, err := s.q.LatestServiceInstantReplicaSamples(ctx, db.LatestServiceInstantReplicaSamplesParams{ServiceID: &id, Process: process, Since: time.Now().Add(-latestReplicaWindow)})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("latest replica samples: %w", err)
		}
		for _, r := range cur {
			out.Points[n-1].Replicas = append(out.Points[n-1].Replicas, ReplicaMetric{Index: int(r.Replica), CPUPercent: r.CpuPercent, MemBytes: r.MemBytes})
		}
	}
	return out, nil
}
