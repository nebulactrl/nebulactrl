package composites

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The estate walker is shared by get_overview, find_issues and
// search_resources: it lists the projects, environments and services the
// caller may read, through the domain services (so scopes and project
// restriction apply), and never queries tables itself.

const (
	// estateMaxProjects bounds the projects one walk reads.
	estateMaxProjects = 400
	// estateMaxEnvs bounds the environments one walk reads services for.
	estateMaxEnvs = 80
	// estateServicePages bounds the service pages (200 each) per environment.
	estateServicePages = 5
	// estateConcurrency is the number of environments read at once.
	estateConcurrency = 6
)

// envNode is one environment with its services.
type envNode struct {
	Project projects.Project
	Env     environments.Environment
	// Services is what the walk read; ServicesTruncated reports that the
	// environment holds more than the page cap.
	Services          []services.ServiceView
	ServicesTruncated bool
}

func (n *envNode) path() string { return path(n.Project.Slug, n.Env.Slug) }

func (n *envNode) svcPath(v services.ServiceView) string {
	return path(n.Project.Slug, n.Env.Slug, v.Slug)
}

// estate is the walked part of the caller's estate.
type estate struct {
	Projects []projects.Project
	Envs     []*envNode
	// Truncated reports that projects or environments beyond the caps were
	// not read.
	Truncated bool
}

func (e *estate) serviceCount() int {
	n := 0
	for _, en := range e.Envs {
		n += len(en.Services)
	}
	return n
}

// estateSelector narrows a walk. Both empty = the whole estate.
type estateSelector struct{ Project, Environment string }

// walkEstate resolves the selector and reads the environments' services with
// at most estateConcurrency environments in flight. Failing environments are
// recorded as section errors on p and left out. The caller queues its own
// independent sections on p before the call so they overlap with the walk and
// calls p.wait() afterwards for the error list.
func (c *composites) walkEstate(ctx context.Context, p *partial, sel estateSelector) (*estate, error) {
	est := &estate{Projects: []projects.Project{}, Envs: []*envNode{}}

	switch {
	case strings.TrimSpace(sel.Environment) != "":
		envSel := strings.TrimSpace(sel.Environment)
		if ps := strings.TrimSpace(sel.Project); ps != "" && !strings.Contains(envSel, "/") {
			if _, err := uuid.Parse(envSel); err != nil {
				envSel = ps + "/" + envSel
			}
		}
		env, err := c.Resolver.Environment(ctx, envSel)
		if err != nil {
			return nil, err
		}
		prj, err := c.Projects.Get(ctx, env.ProjectID)
		if err != nil {
			return nil, err
		}
		est.Projects = append(est.Projects, prj)
		est.Envs = append(est.Envs, &envNode{Project: prj, Env: env})
	case strings.TrimSpace(sel.Project) != "":
		prj, err := c.Resolver.Project(ctx, sel.Project)
		if err != nil {
			return nil, err
		}
		est.Projects = append(est.Projects, prj)
		envs, err := c.Environments.List(ctx, prj.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range envs {
			est.Envs = append(est.Envs, &envNode{Project: prj, Env: e})
		}
	default:
		cursor := ""
		for range 20 {
			items, next, err := c.Projects.List(ctx, cursor, mcpkit.MaxPage)
			if err != nil {
				return nil, err
			}
			est.Projects = append(est.Projects, items...)
			if next == "" {
				break
			}
			cursor = next
			if len(est.Projects) >= estateMaxProjects {
				est.Truncated = true
				break
			}
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		byProject := map[uuid.UUID][]environments.Environment{}
		for _, prj := range est.Projects {
			wg.Add(1)
			p.run("environments "+prj.Slug, func(ctx context.Context) error {
				defer wg.Done()
				envs, err := c.Environments.List(ctx, prj.ID)
				if err != nil {
					return err
				}
				mu.Lock()
				byProject[prj.ID] = envs
				mu.Unlock()
				return nil
			})
		}
		// The environment lists must be complete before the services are read.
		wg.Wait()
		for _, prj := range est.Projects {
			for _, e := range byProject[prj.ID] {
				est.Envs = append(est.Envs, &envNode{Project: prj, Env: e})
			}
		}
	}

	sort.SliceStable(est.Envs, func(i, j int) bool {
		a, b := est.Envs[i], est.Envs[j]
		if a.Project.Slug != b.Project.Slug {
			return a.Project.Slug < b.Project.Slug
		}
		if a.Env.IsProduction != b.Env.IsProduction {
			return a.Env.IsProduction
		}
		return a.Env.Slug < b.Env.Slug
	})
	if len(est.Envs) > estateMaxEnvs {
		est.Envs = est.Envs[:estateMaxEnvs]
		est.Truncated = true
	}

	sem := make(chan struct{}, estateConcurrency)
	for _, n := range est.Envs {
		p.run("services "+n.path(), func(ctx context.Context) error {
			sem <- struct{}{}
			defer func() { <-sem }()
			svcs, truncated, err := c.listAllServices(ctx, n.Env.ID)
			if err != nil {
				return err
			}
			n.Services, n.ServicesTruncated = svcs, truncated
			return nil
		})
	}
	return est, nil
}

// listAllServices reads an environment's services up to the page cap.
func (c *composites) listAllServices(ctx context.Context, envID uuid.UUID) ([]services.ServiceView, bool, error) {
	out := []services.ServiceView{}
	cursor := ""
	for range estateServicePages {
		items, next, err := c.Services.List(ctx, envID, cursor, mcpkit.MaxPage)
		if err != nil {
			return nil, false, err
		}
		out = append(out, items...)
		if next == "" {
			return out, false, nil
		}
		cursor = next
	}
	return out, true, nil
}

// listAllServers reads the workspace's servers (needs servers:read).
func (c *composites) listAllServers(ctx context.Context) ([]servers.Server, error) {
	out := []servers.Server{}
	cursor := ""
	for range 5 {
		items, next, err := c.Servers.List(ctx, cursor, mcpkit.MaxPage)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

// ---- service state ----

// Service states used by get_overview.
const (
	stateRunning   = "running"
	stateStopped   = "stopped"
	stateUnhealthy = "unhealthy"
	stateUnknown   = "unknown"
	stateNever     = "never_deployed"
)

// serviceState classifies a service for roll-ups.
func serviceState(v services.ServiceView) string {
	switch {
	case v.CurrentDeploymentID == nil:
		return stateNever
	case v.DesiredState == "stopped":
		return stateStopped
	case v.Observed == nil || v.Stale:
		return stateUnknown
	}
	switch v.Observed.State {
	case agentprotocol.StateRunning:
		if len(processShortfalls(v.Observed)) > 0 {
			// A service with processes is running only when every process is.
			return stateUnhealthy
		}
		switch v.Observed.Health {
		case agentprotocol.HealthUnhealthy, agentprotocol.HealthDegraded:
			return stateUnhealthy
		}
		return stateRunning
	case agentprotocol.StateRestarting, agentprotocol.StateExited, agentprotocol.StateMissing:
		return stateUnhealthy
	}
	return stateUnknown
}

// deploymentFailed reports a failed terminal status.
func deploymentFailed(status string) bool {
	switch deployments.Status(status) {
	case deployments.Failed, deployments.RolledBack, deployments.RollbackFailed:
		return true
	}
	return false
}

func deploymentActive(status string) bool { return deployments.Status(status).IsActive() }

// ---- findings ----

// staleHeartbeat is how long an online server may stay silent before it is
// flagged (the sweeper marks it offline after agentprotocol.OfflineAfter).
const staleHeartbeat = 3 * agentprotocol.OfflineAfter

// forgottenAfter is how long staged changes may sit before they are flagged.
const forgottenAfter = 24 * time.Hour

// stuckAfter is how long an in-flight deployment may run before it is flagged.
const stuckAfter = 30 * time.Minute

// findOpts are the optional inputs of estateFindings.
type findOpts struct {
	Now time.Time
	// Servers is the servers listing; HaveServers is false when the caller
	// lacks servers:read (offline servers are then derived from the services).
	Servers     []servers.Server
	HaveServers bool
	// Plans holds the deploy plan of staged services by id (blocking problems).
	Plans map[uuid.UUID]deployments.Plan
}

func next(tool, why string, args map[string]any) *NextTool {
	return &NextTool{Tool: tool, Args: args, Why: why}
}

// sevFor lifts sev to critical on a production environment.
func sevFor(n *envNode, sev string) string {
	if n.Env.IsProduction && sev == SevWarning {
		return SevCritical
	}
	return sev
}

// estateFindings detects problems in the walked estate. It is pure: every
// input is in est and o.
func estateFindings(est *estate, o findOpts) []Finding {
	var out []Finding
	serverServices := map[uuid.UUID]int{}
	offlineRefs := map[uuid.UUID]services.ServerRef{}
	for _, n := range est.Envs {
		for _, v := range n.Services {
			out = append(out, serviceFindings(n, v, o)...)
			if v.ServerID != nil {
				serverServices[*v.ServerID]++
				if v.Server != nil && v.Server.Status != "online" {
					offlineRefs[*v.ServerID] = *v.Server
				}
			}
		}
	}
	if o.HaveServers {
		for _, s := range o.Servers {
			out = append(out, serverFindings(s, serverServices[s.ID], o.Now)...)
		}
	} else {
		for id, ref := range offlineRefs {
			out = append(out, offlineServerFinding(id, ref.Name, ref.Status, ref.LastSeenAt, serverServices[id], o.Now))
		}
	}
	sortFindings(out)
	return out
}

func serviceFindings(n *envNode, v services.ServiceView, o findOpts) []Finding {
	var out []Finding
	sel := n.svcPath(v)
	diagnose := func(why string) *NextTool {
		return next("diagnose_service", why, map[string]any{"service": sel})
	}
	serverOffline := v.Server != nil && v.Server.Status != "online"
	st := serviceState(v)

	// Runtime state.
	if v.Observed != nil && v.DesiredState != "stopped" && v.CurrentDeploymentID != nil {
		ob := v.Observed
		ev := []string{"state " + ob.State + ", health " + orDash(ob.Health)}
		for _, p := range processShortfalls(ob) {
			ev = append(ev, "process "+processReadiness(p))
		}
		maxRestarts := 0
		for _, r := range ob.Replicas {
			maxRestarts = max(maxRestarts, r.RestartCount)
		}
		switch {
		case ob.Error != "":
			out = append(out, Finding{Severity: SevCritical, Code: "recovery_gave_up", Subject: sel,
				Title:    "Automatic recovery gave up",
				Evidence: ev, Messages: untrustedLines("observer.error", clip(ob.Error, 200)),
				Suggest: diagnose("Read the runtime log tail and the failing step.")})
		case ob.State == agentprotocol.StateRestarting || (maxRestarts >= 3 && st != stateRunning):
			out = append(out, Finding{Severity: sevFor(n, SevWarning), Code: "crash_looping", Subject: sel,
				Title:    "Container is crash-looping",
				Evidence: append(ev, plural(maxRestarts, "restart", "restarts")),
				Suggest:  diagnose("Read the runtime log tail for the crash reason.")})
		case !v.Stale && (ob.State == agentprotocol.StateExited || ob.State == agentprotocol.StateMissing):
			out = append(out, Finding{Severity: sevFor(n, SevWarning), Code: "stopped_unexpectedly", Subject: sel,
				Title:    "Service is not running although it should be",
				Evidence: ev,
				Suggest:  diagnose("Find out why the container exited.")})
		case !v.Stale && st == stateUnhealthy:
			out = append(out, Finding{Severity: sevFor(n, SevWarning), Code: "unhealthy", Subject: sel,
				Title:    "Service is running but unhealthy",
				Evidence: ev,
				Suggest:  diagnose("Compare the health check with the runtime logs.")})
		case v.Stale && !serverOffline:
			ev := append(ev, "observed "+ptrAgo(o.Now, v.ObservedAt))
			out = append(out, Finding{Severity: SevWarning, Code: "observation_stale", Subject: sel,
				Title:    "Runtime state has not been refreshed recently",
				Evidence: ev,
				Suggest:  next("diagnose_server", "Check the agent heartbeat of the host.", serverArgs(v))})
		}
	} else if v.DesiredState == "stopped" && v.CurrentDeploymentID != nil {
		out = append(out, Finding{Severity: SevInfo, Code: "stopped", Subject: sel,
			Title:   "Service is stopped",
			Suggest: next("service_restart", "Start it again if that is not intended.", map[string]any{"serviceId": v.ID.String()})})
	}

	// Latest deployment.
	if d := v.LatestDeployment; d != nil {
		age := ago(o.Now, d.CreatedAt)
		serving := v.CurrentDeploymentID != nil && *v.CurrentDeploymentID != d.ID
		switch deployments.Status(d.Status) {
		case deployments.RollbackFailed:
			out = append(out, Finding{Severity: SevCritical, Code: "rollback_failed", Subject: sel,
				Title:    "Automatic rollback failed; the running version is unknown",
				Evidence: []string{"deployment " + d.ID.String() + " " + age}, Messages: untrustedLines("deployment.statusMessage", clip(d.StatusMessage, 200)),
				Suggest: next("explain_failed_deployment", "Find what failed and whether the old release is still available.", map[string]any{"deployment": d.ID.String()})})
		case deployments.Failed, deployments.RolledBack:
			code, title := "deployment_failed", "Latest deployment failed"
			sev := SevWarning
			ev := []string{"deployment " + d.ID.String() + " " + d.Status + " " + age}
			switch {
			case d.Status == string(deployments.RolledBack):
				code, title = "deployment_rolled_back", "Latest deployment was rolled back"
				ev = append(ev, "an older release keeps serving")
			case serving:
				code, title = "stale_code", "Latest deployment failed; an older release is serving (stale code)"
				ev = append(ev, "an older release keeps serving")
			case v.CurrentDeploymentID == nil:
				code, title = "first_deployment_failed", "The first deployment failed; nothing is running"
			}
			if o.Now.Sub(d.CreatedAt) > 7*24*time.Hour {
				sev = SevInfo
			}
			out = append(out, Finding{Severity: sev, Code: code, Subject: sel, Title: title,
				Evidence: ev, Messages: untrustedLines("deployment.statusMessage", clip(d.StatusMessage, 200)),
				Suggest: next("explain_failed_deployment", "See the failing step and the log excerpt around the first error.", map[string]any{"deployment": d.ID.String()})})
		case deployments.Created, deployments.Queued, deployments.Preparing, deployments.Building, deployments.Releasing, deployments.Verifying:
			started := d.CreatedAt
			switch {
			case serverOffline:
				out = append(out, Finding{Severity: SevCritical, Code: "deployment_waiting_on_offline_server", Subject: sel,
					Title: "Deployment is waiting on an offline server",
					Evidence: []string{"deployment " + d.ID.String() + " " + d.Status + " since " + ago(o.Now, started),
						"server " + v.Server.Name + " is " + v.Server.Status + "; the command may have been delivered, so the outcome is uncertain"},
					Suggest: next("diagnose_server", "Check the host and its agent before cancelling or retrying.", serverArgs(v))})
			case o.Now.Sub(started) > stuckAfter:
				out = append(out, Finding{Severity: SevWarning, Code: "deployment_stuck", Subject: sel,
					Title:    "Deployment has been in flight for a long time",
					Evidence: []string{"deployment " + d.ID.String() + " " + d.Status + " since " + ago(o.Now, started)},
					Suggest:  next("deployment_cancel", "Cancel it if it is stuck, then explain the failure.", map[string]any{"deploymentId": d.ID.String()})})
			}
		}
	}

	// Staged and forgotten.
	if v.HasStagedChanges && o.Now.Sub(v.UpdatedAt) > forgottenAfter {
		out = append(out, Finding{Severity: SevWarning, Code: "staged_changes_forgotten", Subject: sel,
			Title: "Staged changes have been waiting for more than a day",
			Evidence: []string{plural(int(v.Generation-v.AppliedGeneration), "staged generation", "staged generations") + ", last edit " + ago(o.Now, v.UpdatedAt),
				"staged edits do not change the running service until applied"},
			Suggest: next("review_changes", "Review the pending changes, then apply or discard them.", map[string]any{"service": sel})})
	}
	if plan, ok := o.Plans[v.ID]; ok && len(plan.Blocking) > 0 {
		ev := make([]string, 0, len(plan.Blocking))
		code := "staged_change_blocked"
		for _, b := range plan.Blocking {
			ev = append(ev, b.Code+": "+clip(b.Message, 200))
			if strings.Contains(b.Code, "unresolved") {
				code = "unresolved_reference"
			}
		}
		title := "Applying the staged changes would be refused"
		if code == "unresolved_reference" {
			title = "A variable references something that does not exist"
		}
		out = append(out, Finding{Severity: SevWarning, Code: code, Subject: sel, Title: title, Evidence: ev,
			Suggest: next("review_changes", "See the blocking problems and fix them with variable_set or stage_changes.", map[string]any{"service": sel})})
	}

	// Domains.
	for _, d := range v.Domains {
		sub := d.Hostname
		switch {
		case d.Status == "error":
			out = append(out, Finding{Severity: SevWarning, Code: "domain_error", Subject: sub,
				Title:    "Domain is in an error state",
				Evidence: []string{"service " + sel}, Messages: untrustedLines("domain.message", clip(d.StatusMessage, 200)),
				Suggest: next("setup_domain", "Re-check the hostname, DNS and the proxy.", map[string]any{"service": sel, "hostname": d.Hostname})})
		case d.DNSOk != nil && !*d.DNSOk:
			ev := []string{"service " + sel}
			if d.DNSCheckedAt != nil {
				ev = append(ev, "checked "+ago(o.Now, *d.DNSCheckedAt))
			}
			out = append(out, Finding{Severity: SevWarning, Code: "domain_dns_mismatch", Subject: sub,
				Title:    "DNS does not point to the server",
				Evidence: ev, Messages: untrustedLines("domain.message", clip(d.StatusMessage, 200)),
				Suggest: next("setup_domain", "Get the exact record to create and re-run the DNS check.", map[string]any{"service": sel, "hostname": d.Hostname})})
		}
		if d.Cert != nil && d.Cert.Status == "error" {
			out = append(out, Finding{Severity: SevWarning, Code: "certificate_error", Subject: sub,
				Title:    "Certificate could not be issued",
				Evidence: []string{"service " + sel}, Messages: untrustedLines("certificate.message", clip(d.Cert.Message, 200)),
				Suggest: next("setup_domain", "Check DNS and the proxy for this hostname.", map[string]any{"service": sel, "hostname": d.Hostname})})
		}
	}
	return out
}

func serverArgs(v services.ServiceView) map[string]any {
	if v.ServerID == nil {
		return map[string]any{}
	}
	return map[string]any{"server": v.ServerID.String()}
}

func offlineServerFinding(id uuid.UUID, name, status string, lastSeen *time.Time, nServices int, now time.Time) Finding {
	ev := []string{"status " + status}
	if lastSeen != nil {
		ev = append(ev, "last seen "+ago(now, *lastSeen))
	}
	if nServices > 0 {
		ev = append(ev, plural(nServices, "service is", "services are")+" placed on it")
	}
	sev, code, title := SevWarning, "server_offline", "Server is offline"
	if nServices > 0 {
		sev = SevCritical
	}
	if status == "pending" {
		sev, code, title = SevInfo, "server_pending", "Server has not connected yet"
	}
	return Finding{Severity: sev, Code: code, Title: title, Subject: name, Evidence: ev,
		Suggest: next("diagnose_server", "Check heartbeat history, agent version and the commands waiting for it.", map[string]any{"server": id.String()})}
}

func serverFindings(s servers.Server, nServices int, now time.Time) []Finding {
	var out []Finding
	sargs := map[string]any{"server": s.ID.String()}
	switch s.Status {
	case "offline", "pending":
		out = append(out, offlineServerFinding(s.ID, s.Name, s.Status, s.LastSeenAt, nServices, now))
	case "online":
		if s.LastSeenAt != nil && now.Sub(*s.LastSeenAt) > staleHeartbeat {
			out = append(out, Finding{Severity: SevWarning, Code: "heartbeat_stale", Subject: s.Name,
				Title:    "Server is online but its heartbeat is late",
				Evidence: []string{"last seen " + ago(now, *s.LastSeenAt)},
				Suggest:  next("diagnose_server", "Check the agent and the network path.", sargs)})
		}
	}
	if s.Status != "revoked" && s.Proxy.Status == agentprotocol.ProxyError {
		sev := SevWarning
		if nServices > 0 {
			sev = SevCritical
		}
		ev := []string{"proxy status error"}
		out = append(out, Finding{Severity: sev, Code: "proxy_error", Subject: s.Name,
			Title: "The proxy on this server reports an error, so domains do not route", Evidence: ev,
			Messages: untrustedLines("proxy.error", clip(s.Proxy.Error, 200)),
			Suggest:  next("diagnose_server", "See the proxy error and port 80/443 state.", sargs)})
	}
	if s.Status == "online" || s.Status == "offline" {
		pct := 0
		if s.Latest != nil && s.Latest.DiskTotal > 0 {
			pct = int(100 * s.Latest.DiskUsed / s.Latest.DiskTotal)
		}
		if s.DiskAlert || pct >= 90 {
			sev := SevWarning
			if pct >= 95 {
				sev = SevCritical
			}
			ev := []string{"disk alert threshold " + itoa(s.DiskAlertThresholdPercent) + "%"}
			if pct > 0 {
				ev = append(ev, "root filesystem "+itoa(pct)+"% used")
			}
			out = append(out, Finding{Severity: sev, Code: "disk_high", Subject: s.Name,
				Title: "Disk is almost full", Evidence: ev,
				Suggest: next("diagnose_server", "See disk usage and the cleanup state.", sargs)})
		}
	}
	return out
}

// ---- tiny helpers ----

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func ptrAgo(now time.Time, t *time.Time) string {
	if t == nil {
		return "never"
	}
	return ago(now, *t)
}

func itoa(n int) string { return strconv.Itoa(n) }
