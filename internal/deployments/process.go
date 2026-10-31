package deployments

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// One service, several processes: the release, the image, the variables and the
// volumes are the service's; every process runs from them with its own command,
// port, scale and health check. A deployment carries all of them in one deploy
// command (DeployPayload.Processes) and the agent rolls them in order, so the
// control plane's part is to describe each process completely.

// processesNeedAgent is the blocker of a service with several processes on a
// server whose agent does not run them. A server that never connected has no
// facts and is assumed to enroll with a current agent.
func processesNeedAgent(cfg services.Config, srv *db.Server) *PlanBlocking {
	if !cfg.HasProcesses() || srv == nil {
		return nil
	}
	if has, known := services.ServerHasCapability(*srv, agentprotocol.CapProcesses); known && !has {
		return &PlanBlocking{
			Code: "agent_too_old",
			Message: fmt.Sprintf("The agent on server %s is too old to run a service with several processes. Update the agent, then deploy again. No changes were made.",
				srv.Name),
		}
	}
	return nil
}

// checkProcessDeploy refuses, before anything is persisted, to deploy a release
// with processes that cannot run: on an agent without the capability, or for a
// kind other than app and worker (configuration validation forbids it, this
// guards releases stored before a kind change).
func checkProcessDeploy(kind string, cfg services.Config, srv db.Server) error {
	if !cfg.HasProcesses() {
		return nil
	}
	if kind != services.KindApp && kind != services.KindWorker {
		return userErrorf("Only apps and workers can run several processes.")
	}
	if b := processesNeedAgent(cfg, &srv); b != nil {
		return userErrorf("%s", b.Message)
	}
	return nil
}

// processRouting is the public routing of one process: the domains it serves
// with their rules, as a deploy payload carries them.
type processRouting struct {
	Hosts      []string
	Rules      []agentprotocol.DomainRule
	OriginLock *agentprotocol.OriginLock
}

// processDomains are the domain rows a process serves: the rows without a
// component for the default process, the rows naming it for a named one. A
// process without an HTTP port serves none.
func processDomains(p services.EffectiveProcess, doms []db.Domain) []db.Domain {
	if p.Port <= 0 {
		return nil
	}
	var out []db.Domain
	for _, d := range doms {
		if (p.Default && d.Component == "") || (!p.Default && d.Component == p.Name) {
			out = append(out, d)
		}
	}
	return out
}

// processRoutings builds the routing of every process that has domains, keyed by
// process name, running each process' rules through the same passes a
// single-process deploy applies to the service's (DNS-01 resolver, Cloudflare
// origin lock, platform access).
func processRoutings(ctx context.Context, q *db.Queries, serverID, serviceID uuid.UUID, cfg services.Config, doms []db.Domain) (map[string]processRouting, error) {
	out := map[string]processRouting{}
	for _, p := range cfg.EffectiveProcesses() {
		rows := processDomains(p, doms)
		if len(rows) == 0 {
			continue
		}
		rules, hosts := domains.RulesAndHosts(rows)
		if err := dns01.ApplyAutoToRules(ctx, q, serverID, rules); err != nil {
			return nil, err
		}
		lock, err := cloudflare.ApplyOriginLockToRules(ctx, q, serverID, rules)
		if err != nil {
			return nil, err
		}
		if err := domains.ApplyPlatformAccessToRules(ctx, q, serverID, serviceID, rules); err != nil {
			return nil, err
		}
		out[p.Name] = processRouting{Hosts: hosts, Rules: rules, OriginLock: lock}
	}
	return out, nil
}

// buildProcessSpecs describes every process of cfg in rollout order (processes
// with a port first, then the rest; the default process first within each
// group). The replica count is the stored setting as the process can run it
// (volumes and singletons run one), the default process taking defaultReplicas
// (the service's own setting) and a named one its service_process_scale row (no
// row: one replica). routing is keyed by process name; a process without an
// entry has no route.
func buildProcessSpecs(kind string, cfg services.Config, volumes, defaultReplicas int, scales []db.ServiceProcessScale, routing map[string]processRouting) []agentprotocol.ProcessSpec {
	ordered := services.RolloutOrder(cfg.EffectiveProcesses())
	out := make([]agentprotocol.ProcessSpec, 0, len(ordered))
	for _, p := range ordered {
		want := defaultReplicas
		if !p.Default {
			want = services.ProcessScalingOf(scales, p.Name).Replicas
		}
		rt := routing[p.Name]
		out = append(out, agentprotocol.ProcessSpec{
			Name: p.Name, Default: p.Default,
			Replicas:     services.EffectiveProcessReplicas(kind, cfg, volumes, p, want),
			Strategy:     services.ProcessStrategy(cfg, volumes, p),
			Port:         p.Port,
			StartCommand: p.Command,
			Healthcheck:  p.Healthcheck.Spec(), StopGraceSeconds: p.StopGraceSeconds,
			Singleton: p.Singleton, MemoryMB: p.MemoryMB, CPUs: p.CPUs,
			Hosts: rt.Hosts, DomainRules: rt.Rules, OriginLock: rt.OriginLock,
		})
	}
	return out
}

// mirrorDefaultProcess sets the payload's processes and copies the default
// process' spec into the legacy top-level fields: they must equal it, the agent
// reads Processes only and everything else that still looks at the top level
// (the scale payload, the tailnet, the route table) sees the default process.
func mirrorDefaultProcess(pl *agentprotocol.DeployPayload, specs []agentprotocol.ProcessSpec) {
	pl.Processes = specs
	for _, sp := range specs {
		if !sp.Default {
			continue
		}
		pl.Strategy, pl.Replicas, pl.Port, pl.StartCommand = sp.Strategy, sp.Replicas, sp.Port, sp.StartCommand
		pl.Healthcheck, pl.StopGraceSeconds, pl.MemoryMB, pl.CPUs = sp.Healthcheck, sp.StopGraceSeconds, sp.MemoryMB, sp.CPUs
		pl.Hosts, pl.DomainRules, pl.OriginLock = sp.Hosts, sp.DomainRules, sp.OriginLock
	}
}

// applyProcesses fills the payload of a service with several processes.
// tailnetOnly ("Tailnet only") leaves every process without hosts, rules and
// origin lock; stacks never have processes.
func (s *Service) applyProcesses(ctx context.Context, q *db.Queries, sc services.Scope, serverID uuid.UUID, cfg services.Config, volumes int, doms []db.Domain, tailnetOnly bool, defaultReplicas int, pl *agentprotocol.DeployPayload) error {
	scales, err := q.ListProcessScales(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("list process scales: %w", err)
	}
	var routing map[string]processRouting
	if !tailnetOnly {
		if routing, err = processRoutings(ctx, q, serverID, sc.Service.ID, cfg, doms); err != nil {
			return err
		}
	}
	mirrorDefaultProcess(pl, buildProcessSpecs(sc.Service.Kind, cfg, volumes, defaultReplicas, scales, routing))
	return nil
}

// payloadRules lists every domain rule of a deploy payload: the service's own and
// those of each process (the default process' ones are the same as the
// top-level DomainRules). Their basic-auth secrets are attached at send time.
func payloadRules(p agentprotocol.DeployPayload) []agentprotocol.DomainRule {
	if len(p.Processes) == 0 {
		return p.DomainRules
	}
	var out []agentprotocol.DomainRule
	for _, pr := range p.Processes {
		out = append(out, pr.DomainRules...)
	}
	return out
}

// PlanProcess is one process of a deployment plan.
type PlanProcess struct {
	Name      string `json:"name"`
	Default   bool   `json:"default"`
	Port      int    `json:"port"`
	Strategy  string `json:"strategy" enum:"rolling,recreate"`
	Replicas  int    `json:"replicas"`
	Singleton bool   `json:"singleton"`
	// Order is the position in the rollout (1 = first): processes with a port go
	// first, so a broken web process fails the deployment before workers start.
	Order int `json:"order"`
	// WillChange is true when the process is new, its definition differs from the
	// running release's, or a setting every process shares changed.
	WillChange bool `json:"willChange"`
}

// defaultProcessFields are the pending-change fields that describe only the
// default process: the process itself is compared instead.
var defaultProcessFields = []string{
	"config.port", "config.startCommand", "config.healthcheck", "config.stopGraceSeconds", "config.memoryMb", "config.cpus",
	"config.singleton", "config.processName",
}

// changesEveryProcess reports whether a pending change reaches all processes: a
// new image or source, other variables, a volume. Process settings are judged per
// process, routing changes (domains) do not replace containers.
func changesEveryProcess(changes []services.FieldChange) bool {
	for _, c := range changes {
		f := c.Field
		switch {
		case strings.HasPrefix(f, "config.processes."), strings.HasPrefix(f, "domains."):
			continue
		}
		own := false
		for _, d := range defaultProcessFields {
			if f == d || strings.HasPrefix(f, d+".") {
				own = true
				break
			}
		}
		if !own {
			return true
		}
	}
	return false
}

// planProcesses describes the processes of the plan in rollout order; empty for a
// single-process service. running is the config of the current release (nil
// before the first deployment).
func planProcesses(kind string, cfg services.Config, running *services.Config, volumes int, scales []db.ServiceProcessScale, defaultReplicas int, changes []services.FieldChange) []PlanProcess {
	out := []PlanProcess{}
	if !cfg.HasProcesses() {
		return out
	}
	shared := changesEveryProcess(changes)
	for i, sp := range buildProcessSpecs(kind, cfg, volumes, defaultReplicas, scales, nil) {
		p, _ := cfg.Process(sp.Name)
		change := running == nil || shared
		if !change {
			if rp, ok := running.Process(sp.Name); !ok || !sameProcess(rp, p) {
				change = true
			}
		}
		out = append(out, PlanProcess{
			Name: sp.Name, Default: sp.Default, Port: sp.Port, Strategy: sp.Strategy, Replicas: sp.Replicas, Singleton: sp.Singleton,
			Order: i + 1, WillChange: change,
		})
	}
	return out
}

// processTimeline is the deployment events of a finished multi-process deploy:
// one per process that did not succeed. A failed process is a failed event, a
// skipped or rolled back one an info event, both named `[<process>] ` like the
// agent's own step events.
func processTimeline(results []agentprotocol.ProcessResult) []db.InsertDeploymentEventParams {
	var out []db.InsertDeploymentEventParams
	for _, r := range results {
		if r.Outcome == "" || r.Outcome == "succeeded" {
			continue
		}
		status := "info"
		if r.Outcome == "failed" {
			status = agentprotocol.StepFailed
		}
		msg := r.Outcome
		if r.Message != "" {
			msg = r.Message
		}
		out = append(out, db.InsertDeploymentEventParams{Step: "release", Status: status, Message: "[" + r.Name + "] " + msg})
	}
	return out
}

// recordProcessOutcomes writes the per-process outcomes of a deploy result to the
// deployment's timeline, just before its final status row. A result for a
// deployment that already finished (a redelivery) writes nothing.
func (s *Service) recordProcessOutcomes(ctx context.Context, q *db.Queries, depID uuid.UUID, results []agentprotocol.ProcessResult) error {
	events := processTimeline(results)
	if len(events) == 0 {
		return nil
	}
	cur, err := q.LockDeployment(ctx, depID)
	if err != nil {
		return fmt.Errorf("lock deployment: %w", err)
	}
	if Status(cur.Status).IsTerminal() {
		return nil
	}
	for _, ev := range events {
		ev.DeploymentID = depID
		if err := q.InsertDeploymentEvent(ctx, ev); err != nil {
			return fmt.Errorf("insert deployment event: %w", err)
		}
	}
	return nil
}

// runningConfig is the configuration of the release a service runs now, nil
// before its first deployment.
func (s *Service) runningConfig(ctx context.Context, q *db.Queries, sv db.Service) (*services.Config, error) {
	rel, ok, err := services.AppliedRelease(ctx, q, sv)
	if err != nil || !ok {
		return nil, err
	}
	cfg := services.DecodeSnapshot(rel.Config).Config
	return &cfg, nil
}

// sameProcess reports whether two resolved processes would run identically.
func sameProcess(a, b services.EffectiveProcess) bool {
	if a.Name != b.Name || a.Default != b.Default || a.Port != b.Port || a.StopGraceSeconds != b.StopGraceSeconds ||
		a.Singleton != b.Singleton || a.MemoryMB != b.MemoryMB || a.CPUs != b.CPUs || !slices.Equal(a.Command, b.Command) {
		return false
	}
	ha, hb := a.Healthcheck, b.Healthcheck
	same := slices.Equal(ha.Command, hb.Command)
	ha.Command, hb.Command = nil, nil
	return same && reflect.DeepEqual(ha, hb)
}
