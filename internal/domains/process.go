package domains

import (
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A service with several processes routes each process that has an HTTP port
// through its own Route: domains.component names the process ("" = the default
// process), the way a compose stack's domains name their component.

// ProcessUpstreams returns "<containerName>:<port>" for each of n replicas of a
// NAMED process of a deployment (at least one). The default process keeps the
// service's own container names and uses Upstreams.
func ProcessUpstreams(slug, deploymentID, process string, n, port int) []string {
	names := agentprotocol.ProcessContainerNames(slug, deploymentID, process, false, max(n, 1))
	out := make([]string, len(names))
	p := strconv.Itoa(port)
	for i, name := range names {
		out[i] = net.JoinHostPort(name, p)
	}
	return out
}

// DecodeProcessReplicas parses the process_replicas aggregate of the route and
// current-service queries: named process → desired replicas.
func DecodeProcessReplicas(raw json.RawMessage) map[string]int {
	out := map[string]int{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// DecodeProcessScaledAt parses the process_scaled_at aggregate: named process →
// the time of its last scale (a process that was never scaled is absent).
func DecodeProcessScaledAt(raw json.RawMessage) map[string]time.Time {
	out := map[string]time.Time{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// processRoute builds the table entry of the NAMED process a domain row routes
// to, from the running release's configuration: its upstreams are the process'
// replicas, its health path the process' own. ok is false when the release has
// no such process (the domain was added after the release, or the process was
// removed) or the process has no HTTP port: there is nothing to route to.
func processRoute(r db.ListServerRouteDomainsRow) (agentprotocol.Route, bool) {
	snap := services.DecodeSnapshot(r.ReleaseConfig)
	cfg := snap.Config
	p, ok := cfg.Process(r.Domain.Component)
	if !cfg.HasProcesses() || !ok || p.Default || p.Port <= 0 {
		return agentprotocol.Route{}, false
	}
	want := 1
	if n, ok := DecodeProcessReplicas(r.ProcessReplicas)[p.Name]; ok {
		want = n
	}
	n := services.EffectiveProcessReplicas(r.ServiceKind, cfg, len(snap.Volumes), p, want)
	return agentprotocol.Route{
		ServiceID:  agentprotocol.ProcessRouteID(r.ServiceID.String(), p.Name),
		Stack:      r.ServiceID.String(),
		Upstreams:  ProcessUpstreams(r.ServiceSlug, r.DeploymentID.String(), p.Name, n, int(r.Domain.TargetPort)),
		HealthPath: processHealthPath(p),
		Networks:   services.RouteNetworks(r.EnvironmentID, cfg.BlockEgress),
		RouteExt:   agentprotocol.RouteExt{Maintenance: maintenanceFrom(r.MaintenanceEnabled, r.MaintenanceTitle, r.MaintenanceMessage, r.MaintenanceEta)},
	}, true
}

// processHealthPath is the proxy health check path of a process: the http
// health check's path, empty for every other type.
func processHealthPath(p services.EffectiveProcess) string {
	if p.Healthcheck.Type != services.HealthHTTP {
		return ""
	}
	if p.Healthcheck.Path == "" {
		return "/"
	}
	return p.Healthcheck.Path
}

// generateComponent is the component of a generated hostname's domain: for an app
// the process the domain routes to (empty for the default process), resolved the
// way a created domain resolves it. Stacks and route services pass theirs through.
func generateComponent(sc services.Scope, component string) (string, error) {
	switch sc.Service.Kind {
	case services.KindStack, services.KindRoute:
		return component, nil
	}
	_, stored, err := sc.Config().ResolveDomainProcess(component, 0)
	return stored, err
}

// componentOrProcess folds the request's component (compose stacks) and process
// (apps) into the one component argument the service methods take. They name the
// same column for different kinds of service, so only one can be given.
func componentOrProcess(component, process string) (string, error) {
	if component != "" && process != "" {
		return "", apierr.Unprocessable("validation_failed", "component and process cannot be combined: component is for Compose stacks, process for services with several processes")
	}
	if process != "" {
		return process, nil
	}
	return component, nil
}
