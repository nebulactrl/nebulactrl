package services

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Replica limits and autoscaler bounds. Replicas are operational settings:
// they are not part of a release snapshot and never produce staged changes.
const (
	MinReplicas            = 1
	MaxReplicas            = 10
	DefaultAutoscaleMin    = 1
	DefaultAutoscaleMax    = 3
	DefaultAutoscaleTarget = 70
	MinAutoscaleTargetCPU  = 20
	MaxAutoscaleTargetCPU  = 95
)

// ReplicasRequireStateless is the 422 message for scaling an ineligible service.
const ReplicasRequireStateless = "Replicas need a stateless service: no volumes and an HTTP port"

// Autoscaling is services.autoscaling: CPU-based replica autoscaling.
type Autoscaling struct {
	Enabled bool `json:"enabled"`
	Min     int  `json:"min"`
	Max     int  `json:"max"`
	// TargetCPUPercent is the per-replica CPU target as a percentage of the
	// replica's CPU limit (one core when no limit is set).
	TargetCPUPercent int `json:"targetCpuPercent"`
}

// DefaultAutoscaling is autoscaling off, min 1, max 3, target 70%.
func DefaultAutoscaling() Autoscaling {
	return Autoscaling{Min: DefaultAutoscaleMin, Max: DefaultAutoscaleMax, TargetCPUPercent: DefaultAutoscaleTarget}
}

// DecodeAutoscaling parses services.autoscaling; missing or zero fields take
// their defaults.
func DecodeAutoscaling(raw json.RawMessage) Autoscaling {
	a := DefaultAutoscaling()
	if len(raw) > 0 {
		var in Autoscaling
		if json.Unmarshal(raw, &in) == nil {
			a.Enabled = in.Enabled
			if in.Min != 0 {
				a.Min = in.Min
			}
			if in.Max != 0 {
				a.Max = in.Max
			}
			if in.TargetCPUPercent != 0 {
				a.TargetCPUPercent = in.TargetCPUPercent
			}
		}
	}
	return a
}

// Validate checks min <= max within 1..10 and the target within 20..95.
func (a Autoscaling) Validate() error {
	switch {
	case a.Min < MinReplicas || a.Min > MaxReplicas || a.Max < MinReplicas || a.Max > MaxReplicas:
		return fmt.Errorf("autoscaling min and max must be between %d and %d", MinReplicas, MaxReplicas)
	case a.Min > a.Max:
		return fmt.Errorf("autoscaling min (%d) must not exceed max (%d)", a.Min, a.Max)
	case a.TargetCPUPercent < MinAutoscaleTargetCPU || a.TargetCPUPercent > MaxAutoscaleTargetCPU:
		return fmt.Errorf("autoscaling targetCpuPercent must be between %d and %d", MinAutoscaleTargetCPU, MaxAutoscaleTargetCPU)
	}
	return nil
}

// ScaleEvent is services.last_scale_event.
type ScaleEvent struct {
	At     time.Time `json:"at"`
	From   int       `json:"from"`
	To     int       `json:"to"`
	Reason string    `json:"reason"`
	// Actor is who scaled: "autoscaler" or the actor label of the caller.
	Actor string `json:"actor"`
}

// DecodeScaleEvent parses services.last_scale_event (nil when never scaled).
func DecodeScaleEvent(raw json.RawMessage) *ScaleEvent {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var e ScaleEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	return &e
}

// ScalingView is the replica state of a service.
type ScalingView struct {
	// Replicas is the desired replica count.
	Replicas int `json:"replicas"`
	// Ready is how many replicas are running and healthy (from the last observation).
	Ready       int         `json:"ready"`
	Autoscaling Autoscaling `json:"autoscaling"`
	// LastScaleEvent is the most recent scale change (manual or autoscaler), or null.
	LastScaleEvent *ScaleEvent `json:"lastScaleEvent"`
	// BlockedReason says why a scale-out was not carried out, or null.
	BlockedReason *string `json:"blockedReason"`
	// Eligible is true for stateless services: no volumes, an HTTP port and kind != database.
	Eligible         bool    `json:"eligible"`
	IneligibleReason *string `json:"ineligibleReason"`
}

// SingletonReason is the ineligibility reason of a singleton process.
const SingletonReason = "Singleton processes run exactly one instance."

// Eligibility reports whether a service can run more than one replica, and why
// not otherwise. It describes the service's default process: an HTTP port is
// required unless the service has several processes (a queue worker scales
// without one), and a singleton never scales.
func Eligibility(kind string, cfg Config, volumes int) (bool, string) {
	switch {
	case kind == KindStack:
		return false, "Compose stacks run the services of their compose file; scale them there."
	case kind == KindDatabase:
		return false, "Databases run as a single instance."
	case volumes > 0:
		return false, "Services with volumes run as a single instance."
	case cfg.Port <= 0 && !cfg.HasProcesses():
		return false, "Services without an HTTP port run as a single instance."
	case len(cfg.Files) > 0:
		return false, "Services with config files run as a single instance."
	case cfg.Singleton:
		return false, SingletonReason
	}
	return true, ""
}

// EffectiveReplicas is the replica count a deploy uses: the service's setting,
// forced to 1 when the service is not eligible.
func EffectiveReplicas(kind string, cfg Config, volumes, replicas int) int {
	if ok, _ := Eligibility(kind, cfg, volumes); !ok || replicas < MinReplicas {
		return MinReplicas
	}
	return min(replicas, MaxReplicas)
}

// BuildScalingView renders the scaling state of a service row. volumes is the
// number of volumes attached to the service; applied is the config of the
// running release (nil until the first deployment). For a release with
// processes Ready is the default process' own ready count, not the sum the
// agent reports over every process.
func BuildScalingView(r db.Service, volumes int, obs *ObservedView, applied *Config) ScalingView {
	ok, why := Eligibility(r.Kind, DecodeConfig(r.Config), volumes)
	v := ScalingView{
		Replicas: int(r.Replicas), Autoscaling: DecodeAutoscaling(r.Autoscaling),
		LastScaleEvent: DecodeScaleEvent(r.LastScaleEvent), BlockedReason: r.ScaleBlockedReason, Eligible: ok,
	}
	if !ok {
		v.IneligibleReason = &why
	}
	if obs == nil {
		return v
	}
	if applied != nil && applied.HasProcesses() && len(obs.Processes) > 0 {
		// The default process may have been renamed since the release: its observed state carries the old name.
		for _, name := range []string{applied.DefaultProcessName(), DecodeConfig(r.Config).DefaultProcessName()} {
			for _, o := range obs.Processes {
				if o.Name == name {
					v.Ready = o.ReplicasReady
					return v
				}
			}
		}
		return v
	}
	v.Ready = obs.ReplicasReady
	if len(obs.Replicas) == 0 && obs.State == "running" && obs.Health == "healthy" {
		v.Ready = 1
	}
	return v
}

// ServiceVolumeCounts returns the number of volumes per service.
func ServiceVolumeCounts(ctx context.Context, q *db.Queries, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := q.CountVolumesForServices(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("count volumes: %w", err)
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		out[r.ServiceID] = int(r.N)
	}
	return out, nil
}

// decorateScaling fills ServiceView.Scaling for every row (out is parallel to
// rows). releases are the current releases keyed by deployment id.
func decorateScaling(ctx context.Context, q *db.Queries, rows []db.Service, out []ServiceView, releases map[uuid.UUID]db.Release) error {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	vols, err := ServiceVolumeCounts(ctx, q, ids)
	if err != nil {
		return err
	}
	for i, r := range rows {
		out[i].Scaling = BuildScalingView(r, vols[r.ID], out[i].Observed, releaseConfig(r, releases))
	}
	return nil
}

// ProcessEligibility reports whether a process can run more than one replica,
// and why not otherwise. Without several processes it is Eligibility (a port is
// required); with them an HTTP port is not (queue workers scale).
func ProcessEligibility(kind string, cfg Config, volumes int, p EffectiveProcess) (ok bool, reason string) {
	if !cfg.HasProcesses() || p.Default {
		return Eligibility(kind, cfg, volumes)
	}
	switch {
	case kind == KindStack:
		return false, "Compose stacks run the services of their compose file; scale them there."
	case kind == KindDatabase:
		return false, "Databases run as a single instance."
	case volumes > 0:
		return false, "Services with volumes run as a single instance."
	case len(cfg.Files) > 0:
		return false, "Services with config files run as a single instance."
	case p.Singleton:
		return false, SingletonReason
	}
	return true, ""
}

// EffectiveProcessReplicas is the replica count a deploy uses for a process: the
// setting, forced to 1 when the process is not eligible.
func EffectiveProcessReplicas(kind string, cfg Config, volumes int, p EffectiveProcess, replicas int) int {
	if ok, _ := ProcessEligibility(kind, cfg, volumes, p); !ok || replicas < MinReplicas {
		return MinReplicas
	}
	return min(replicas, MaxReplicas)
}

// ProcessStrategy is the deploy strategy of a process: recreate (stop the old
// instance, then start the new one) with volumes, for a singleton and for a
// port-less single-process service; else rolling. A port-less, non-singleton,
// volume-less process of a multi-process service rolls: new replicas first,
// then the old ones retire.
func ProcessStrategy(cfg Config, volumes int, p EffectiveProcess) string {
	if volumes > 0 || p.Singleton || (!cfg.HasProcesses() && p.Port <= 0) {
		return agentprotocol.StrategyRecreate
	}
	return agentprotocol.StrategyRolling
}

// ProcessScaling is the stored scale of one process: replicas, autoscaling and
// the last scale event. For a named process it is a service_process_scale row.
type ProcessScaling struct {
	Replicas       int
	Autoscaling    Autoscaling
	LastScaleAt    *time.Time
	LastScaleEvent *ScaleEvent
	BlockedReason  *string
}

// ProcessScalingOf reads the scale of the named process from the rows of its
// service; a process without a row has 1 replica and the default autoscaling.
func ProcessScalingOf(rows []db.ServiceProcessScale, name string) ProcessScaling {
	for _, r := range rows {
		if r.Process == name {
			return ProcessScaling{
				Replicas: int(r.Replicas), Autoscaling: DecodeAutoscaling(r.Autoscaling), LastScaleAt: r.LastScaleAt,
				LastScaleEvent: DecodeScaleEvent(r.LastScaleEvent), BlockedReason: r.ScaleBlockedReason,
			}
		}
	}
	return ProcessScaling{Replicas: MinReplicas, Autoscaling: DefaultAutoscaling()}
}

// defaultProcessScaling is the scale of the default process: the service's own columns.
func defaultProcessScaling(r db.Service) ProcessScaling {
	return ProcessScaling{
		Replicas: int(r.Replicas), Autoscaling: DecodeAutoscaling(r.Autoscaling), LastScaleAt: r.LastScaleAt,
		LastScaleEvent: DecodeScaleEvent(r.LastScaleEvent), BlockedReason: r.ScaleBlockedReason,
	}
}

// BuildProcessViews renders the processes of a service with several processes:
// the default process first, then the named ones by name, then the processes of
// the running release (applied, nil until the first deployment) that the staged
// config dropped (Removed). It is empty for a service whose staged config and
// running release have no processes. rows are the service's
// service_process_scale rows, obs its observed state.
func BuildProcessViews(r db.Service, cfg Config, applied *Config, rows []db.ServiceProcessScale, volumes int, obs *ObservedView) []ProcessView {
	out := []ProcessView{}
	releaseHasProcesses := applied != nil && applied.HasProcesses()
	if !cfg.HasProcesses() && !releaseHasProcesses {
		return out
	}
	var releaseNames []string
	releaseDefault := ""
	if applied != nil {
		releaseNames = applied.ProcessNames()
		releaseDefault = applied.DefaultProcessName()
	}
	observed := func(names ...string) *ObservedProcessView {
		if obs == nil {
			return nil
		}
		for _, name := range names {
			for _, o := range obs.Processes {
				if o.Name == name {
					return &o
				}
			}
		}
		return nil
	}
	// A release without processes reports no per-process state: the default
	// process is the whole service, so it takes the service-level observation.
	serviceObserved := func(name string) *ObservedProcessView {
		if obs == nil || releaseHasProcesses {
			return nil
		}
		return &ObservedProcessView{
			Name: name, State: obs.State, Health: obs.Health,
			ReplicasReady: obs.ReplicasReady, ReplicasDesired: obs.ReplicasDesired,
		}
	}
	view := func(p EffectiveProcess, sc ProcessScaling, ok bool, why string, o *ObservedProcessView) ProcessView {
		sv := ScalingView{
			Replicas: sc.Replicas, Autoscaling: sc.Autoscaling, LastScaleEvent: sc.LastScaleEvent,
			BlockedReason: sc.BlockedReason, Eligible: ok,
		}
		if !ok {
			sv.IneligibleReason = &why
		}
		if o != nil {
			sv.Ready = o.ReplicasReady
		}
		return ProcessView{
			Name: p.Name, Default: p.Default, Port: p.Port, Command: append([]string{}, p.Command...), Singleton: p.Singleton,
			StopGraceSeconds: p.StopGraceSeconds, Scaling: sv, Observed: o,
		}
	}
	names := cfg.ProcessNames()
	for _, p := range cfg.EffectiveProcesses() {
		ok, why := ProcessEligibility(r.Kind, cfg, volumes, p)
		var pv ProcessView
		if p.Default {
			// The default process may have been renamed since the release: its observed state carries the old name.
			o := observed(p.Name, releaseDefault)
			if o == nil {
				o = serviceObserved(cmp.Or(releaseDefault, p.Name))
			}
			sc := defaultProcessScaling(r)
			sc.Replicas = EffectiveProcessReplicas(r.Kind, cfg, volumes, p, sc.Replicas)
			pv = view(p, sc, ok, why, o)
		} else {
			sc := ProcessScalingOf(rows, p.Name)
			sc.Replicas = EffectiveProcessReplicas(r.Kind, cfg, volumes, p, sc.Replicas)
			pv = view(p, sc, ok, why, observed(p.Name))
			pv.Pending = applied != nil && !slices.Contains(releaseNames, p.Name)
		}
		out = append(out, pv)
	}
	if applied != nil {
		for i, name := range releaseNames {
			if i == 0 || slices.Contains(names, name) {
				continue // the default process is never removed, only renamed
			}
			p, _ := applied.Process(name)
			sc := ProcessScalingOf(rows, name)
			sc.Replicas = EffectiveProcessReplicas(r.Kind, *applied, volumes, p, sc.Replicas)
			pv := view(p, sc, false, "The process is removed from the staged configuration.", observed(name))
			pv.Removed = true
			out = append(out, pv)
		}
	}
	return out
}
