package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// verify_service: "is it actually up?" after a deploy. It combines the
// observed state the agent reports, an HTTP probe of every domain of the
// service and a scan of the last runtime log lines into one pass/warn/fail.

const (
	verifyResultPass = "pass"
	verifyResultWarn = "warn"
	verifyResultFail = "fail"

	verifyLogLines       = 50
	verifyMaxErrorLines  = 10
	verifyProbeParallel  = 4
	verifyMaxProbesShort = 8
	verifyMaxProbesFull  = 20
	verifySlowMs         = 3000
	// verifyCrashLoopRestarts is the restart count from which a recently
	// started container counts as crash-looping.
	verifyCrashLoopRestarts = 5
	verifyRecentStart       = 10 * time.Minute
)

// VerifyServiceInput selects the service and what to request.
type VerifyServiceInput struct {
	Service string `json:"service" jsonschema:"the service: id, project/environment/service, one of its hostnames, or a unique service slug"`
	Path    string `json:"path,omitempty" jsonschema:"absolute request path to probe on each domain (default: the http healthcheck path of the service config, else /)"`
	Detail  string `json:"detail,omitempty" jsonschema:"concise (default) or full (adds per-replica state, domain table and up to 20 probes)"`
}

// VerifyReason is one finding that lowers the result below pass.
type VerifyReason struct {
	Level   string `json:"level" jsonschema:"fail or warn"`
	Code    string `json:"code" jsonschema:"stable code, e.g. not_deployed, unhealthy, domain_5xx, domain_unreachable, domain_redirect_offhost, dns_not_ok, log_errors"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty" jsonschema:"the message of the platform or agent behind the reason, as an untrusted block"`
}

// VerifyReplica is the observed state of one replica (detail:full).
type VerifyReplica struct {
	Index        int    `json:"index"`
	Process      string `json:"process,omitempty"`
	State        string `json:"state"`
	Health       string `json:"health"`
	RestartCount int    `json:"restartCount"`
	StartedAt    string `json:"startedAt,omitempty"`
}

// VerifyProcess is the observed state of one process of a service with several processes.
type VerifyProcess struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Health          string `json:"health"`
	ReplicasReady   int    `json:"replicasReady"`
	ReplicasDesired int    `json:"replicasDesired"`
}

// VerifyObserved is what the agent last reported about the service.
type VerifyObserved struct {
	Reported         bool            `json:"reported" jsonschema:"false when the agent never reported a state for the service"`
	State            string          `json:"state,omitempty"`
	Health           string          `json:"health,omitempty"`
	Stale            bool            `json:"stale" jsonschema:"true when the state is old (agent offline or not reporting)"`
	ObservedAt       string          `json:"observedAt,omitempty"`
	StartedAt        string          `json:"startedAt,omitempty"`
	DesiredState     string          `json:"desiredState"`
	ReplicasReady    int             `json:"replicasReady"`
	ReplicasDesired  int             `json:"replicasDesired"`
	RestartCount     int             `json:"restartCount" jsonschema:"restarts summed over all replicas"`
	Error            any             `json:"error,omitempty" jsonschema:"error the agent reported for the container, as an untrusted block"`
	CurrentDeployID  string          `json:"currentDeploymentId,omitempty"`
	LatestDeployment string          `json:"latestDeployment,omitempty" jsonschema:"status of the newest deployment"`
	StagedChanges    bool            `json:"hasStagedChanges"`
	Replicas         []VerifyReplica `json:"replicas"`
	Processes        []VerifyProcess `json:"processes,omitempty" jsonschema:"per-process state of a service with several processes; the service is up only when every process has all its replicas ready"`
}

// VerifyDomain is the control plane's view of a domain (detail:full).
type VerifyDomain struct {
	Hostname string `json:"hostname"`
	Kind     string `json:"kind"`
	TLSMode  string `json:"tlsMode"`
	Status   string `json:"status"`
	DNSOk    string `json:"dnsOk" jsonschema:"true, false or unknown (not checked yet)"`
	Message  any    `json:"message,omitempty" jsonschema:"status message of the domain, as an untrusted block"`
	Probed   bool   `json:"probed"`
}

// VerifyServiceOutput is the verify_service result.
type VerifyServiceOutput struct {
	Envelope
	Service   string `json:"service" jsonschema:"project/environment/service"`
	ServiceID string `json:"serviceId"`
	Result    string `json:"result" jsonschema:"pass, warn or fail"`
	// Reasons lists every finding that lowered the result; empty on pass.
	Reasons []VerifyReason `json:"reasons"`
	// Notes are facts that do not change the result (no domains, redirect
	// domains skipped, staged changes).
	Notes []string `json:"notes"`
	// DomainsProbed is false when the verdict rests on observed health only.
	DomainsProbed bool           `json:"domainsProbed"`
	Path          string         `json:"path" jsonschema:"the path that was probed"`
	Probes        []ProbeResult  `json:"probes"`
	Observed      VerifyObserved `json:"observed"`
	Domains       []VerifyDomain `json:"domains,omitempty"`
	// LogLinesScanned is how many runtime lines were scanned (0 when logs
	// were not readable); LogErrorCount how many of them looked like errors.
	LogLinesScanned int `json:"logLinesScanned"`
	LogErrorCount   int `json:"logErrorCount"`
	// ErrorLines holds at most 10 of the error-looking lines, newest last.
	ErrorLines mcpkit.Block `json:"errorLines"`
}

func (c *composites) verifyService() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "verify_service",
		Title: "Verify a service is up",
		Description: "Use after a deploy (or when a user says it is broken) to answer \"is it actually up?\" in one call. " +
			"Checks the observed container state and health, sends one HTTP GET to each of the service's OWN domains (status and latency; redirects that leave the host are reported, never followed; hosts on private/loopback addresses are refused), " +
			"scans the last 50 runtime log lines for errors, and reports restarts. Returns result pass|warn|fail with reasons[], probes[] and an observed block. " +
			"A service without domains is verified through observed health only, and the result says so. " +
			"Does NOT change anything and does not read response bodies. Traffic error rates are not included. " +
			"Log lines need logs:read and the server's public address needs servers:read; without them those parts are skipped and listed in errors[]. " +
			"Next: diagnose_service for a fail, explain_failed_deployment when the latest deployment failed, tail_logs for more log text.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: true},
		Input:   VerifyServiceInput{},
		Output:  VerifyServiceOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[VerifyServiceInput](raw)
			if err != nil {
				return nil, err
			}
			return c.verifyServiceCall(ctx, p, in)
		},
	}
}

func (c *composites) verifyServiceCall(ctx context.Context, p *auth.Principal, in VerifyServiceInput) (VerifyServiceOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return VerifyServiceOutput{}, err
	}
	if strings.TrimSpace(in.Service) == "" {
		return VerifyServiceOutput{}, invalid("service is required")
	}
	if in.Path != "" && !verifyValidPath(in.Path) {
		return VerifyServiceOutput{}, invalid("path must be an absolute path such as /health (no //, spaces, backslashes or line breaks)")
	}
	view, err := c.Resolver.Service(ctx, in.Service)
	if err != nil {
		return VerifyServiceOutput{}, err
	}
	sc, err := c.scope(ctx, view.ID)
	if err != nil {
		return VerifyServiceOutput{}, err
	}
	sel := selectorOf(sc)

	probePath := in.Path
	if probePath == "" {
		probePath = "/"
		if hc := view.Config.Healthcheck; hc.Type == services.HealthHTTP && strings.HasPrefix(hc.Path, "/") && verifyValidPath(hc.Path) {
			probePath = hc.Path
		}
	}

	out := VerifyServiceOutput{
		Service: sel, ServiceID: view.ID.String(), Path: probePath,
		Reasons: []VerifyReason{}, Notes: []string{}, Probes: []ProbeResult{},
		Observed: verifyObserved(view, detail.Full()),
	}
	out.Envelope.NextTools = []NextTool{}
	out.ErrorLines = mcpkit.WrapLines("runtime:"+sel, []string{})
	var reasons verifyReasons

	deployed := view.CurrentDeploymentID != nil
	stopped := view.DesiredState == "stopped"
	switch {
	case !deployed:
		reasons.noDeployment(view)
	case stopped:
		reasons.add(verifyResultFail, "stopped", "the service is stopped (desired state stopped); nothing is serving")
	}

	reasons.observed(c.Now(), view, &out)
	var errs []SectionError
	if deployed && !stopped {
		errs = c.verifyLive(ctx, p, view, probePath, detail, &out, &reasons)
	} else {
		out.Notes = append(out.Notes, "domains were not probed because the service is not running a deployment")
	}
	out.Errors = errs
	if out.Errors == nil {
		out.Errors = []SectionError{}
	}

	out.Reasons = append([]VerifyReason{}, reasons.list...)
	out.Result = reasons.result()
	if view.HasStagedChanges {
		n := view.PendingChangeCounts.Variables + view.PendingChangeCounts.Settings
		out.Notes = append(out.Notes, fmt.Sprintf("%s not live yet (generation %d, applied %d); verification covers what is deployed", plural(n, "staged change is", "staged changes are"), view.Generation, view.AppliedGeneration))
	}
	out.Summary = verifySummary(out)
	out.NextTools = verifyNextTools(sel, view, out, holds(p, policy.ScopeLogsRead))
	return out, nil
}

// verifyLive probes domains and scans logs for a deployed, running service.
func (c *composites) verifyLive(ctx context.Context, p *auth.Principal, view services.ServiceView, probePath string, detail mcpkit.Detail, out *VerifyServiceOutput, reasons *verifyReasons) []SectionError {
	run := c.newPartial(ctx, "verify_service", verifyProbeParallel)

	targets, skipped := verifyTargets(view, probePath, detail)
	if skipped.redirects > 0 {
		out.Notes = append(out.Notes, plural(skipped.redirects, "redirect domain was", "redirect domains were")+" not probed (the proxy answers them with a redirect)")
	}
	if skipped.over > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d more domains were not probed; use detail:full for up to %d", skipped.over, verifyMaxProbesFull))
	}
	if len(view.Domains) == 0 {
		out.Notes = append(out.Notes, "the service has no domains: verified through observed health only (no HTTP probe)")
	}

	// The server's public address lets a homelab host on a private address
	// be probed; without servers:read only public addresses are probed.
	var serverIPs []netip.Addr
	if len(targets) > 0 && view.ServerID != nil {
		if holds(p, policy.ScopeServersRead) {
			srv, err := c.Servers.Get(ctx, *view.ServerID)
			if err != nil {
				run.fail("server", err)
			} else if srv.PublicIP != nil {
				serverIPs = serverAddrs(*srv.PublicIP)
			}
		} else {
			out.Notes = append(out.Notes, "the server address is unknown (needs servers:read): domains on a private address are refused")
		}
	}

	results := make([]ProbeResult, len(targets))
	for i, t := range targets {
		run.run("probe:"+t.target.Hostname, func(ctx context.Context) error {
			results[i] = c.probe(ctx, t.target, serverIPs)
			return nil
		})
	}

	var lines []services.LogEntry
	var logReason string
	logsRead := false
	if holds(p, policy.ScopeLogsRead) {
		run.run("logs", func(ctx context.Context) error {
			var err error
			lines, logReason, err = c.runtimeTail(ctx, view.ID, verifyLogLines, "")
			logsRead = err == nil
			return err
		})
	} else {
		run.note("logs", "forbidden", "runtime log scan skipped: needs logs:read")
	}
	errs := run.wait()

	out.DomainsProbed = len(targets) > 0
	for i, t := range targets {
		out.Probes = append(out.Probes, results[i])
		reasons.probe(t.domain, results[i])
	}
	if detail.Full() {
		out.Domains = verifyDomains(view, targets)
	}
	if logsRead {
		verifyScanLogs(view, lines, logReason, out, reasons)
	}
	return errs
}

type verifyTarget struct {
	target ProbeTarget
	domain services.DomainView
}

type verifySkipped struct{ redirects, over int }

// verifyTargets picks the domains to probe: serve domains only, capped.
func verifyTargets(view services.ServiceView, path string, detail mcpkit.Detail) ([]verifyTarget, verifySkipped) {
	limit := verifyMaxProbesShort
	if detail.Full() {
		limit = verifyMaxProbesFull
	}
	var out []verifyTarget
	var skipped verifySkipped
	for _, d := range view.Domains {
		if d.Kind == "redirect" {
			skipped.redirects++
			continue
		}
		if len(out) >= limit {
			skipped.over++
			continue
		}
		p := path
		if d.PathPrefix != "" {
			p = strings.TrimRight(d.PathPrefix, "/") + path
		}
		out = append(out, verifyTarget{target: ProbeTarget{Hostname: d.Hostname, HTTP: d.TLSMode == "none", Path: p}, domain: d})
	}
	return out, skipped
}

func verifyDomains(view services.ServiceView, probed []verifyTarget) []VerifyDomain {
	was := map[string]bool{}
	for _, t := range probed {
		was[t.domain.ID.String()] = true
	}
	out := make([]VerifyDomain, 0, len(view.Domains))
	for _, d := range view.Domains {
		dns := "unknown"
		if d.DNSOk != nil {
			dns = fmt.Sprint(*d.DNSOk)
		}
		out = append(out, VerifyDomain{Hostname: d.Hostname, Kind: d.Kind, TLSMode: d.TLSMode, Status: d.Status, DNSOk: dns, Message: untrustedText("domain.message", clip(d.StatusMessage, 200)), Probed: was[d.ID.String()]})
	}
	return out
}

// verifyScanLogs counts error-looking runtime lines and keeps the newest few.
func verifyScanLogs(view services.ServiceView, lines []services.LogEntry, reason string, out *VerifyServiceOutput, reasons *verifyReasons) {
	if reason != "" {
		out.Notes = append(out.Notes, "no runtime logs: "+reason)
		return
	}
	out.LogLinesScanned = len(lines)
	var hits []string
	for _, l := range lines {
		if errorLine(l.Line) {
			hits = append(hits, l.Line)
		}
	}
	out.LogErrorCount = len(hits)
	if len(hits) == 0 {
		return
	}
	if len(hits) > verifyMaxErrorLines {
		hits = hits[len(hits)-verifyMaxErrorLines:]
	}
	out.ErrorLines = mcpkit.WrapLines("runtime:"+view.Slug, hits)
	reasons.add(verifyResultWarn, "log_errors", fmt.Sprintf("%d of the last %d runtime log lines look like errors (newest %d are in errorLines)", out.LogErrorCount, out.LogLinesScanned, len(hits)))
}

// verifyReasons collects findings.
type verifyReasons struct{ list []VerifyReason }

func (r *verifyReasons) add(level, code, msg string) {
	r.addDetail(level, code, msg, "")
}

// addDetail adds a reason whose cause is a message from the platform or the
// agent; the message goes in an untrusted block beside the reason, never in
// the reason's own text (which also feeds the summary).
func (r *verifyReasons) addDetail(level, code, msg, detail string) {
	r.list = append(r.list, VerifyReason{Level: level, Code: code, Message: msg, Detail: untrustedText(code, detail)})
}

func (r *verifyReasons) result() string {
	res := verifyResultPass
	for _, x := range r.list {
		if x.Level == verifyResultFail {
			return verifyResultFail
		}
		res = verifyResultWarn
	}
	return res
}

func (r *verifyReasons) noDeployment(view services.ServiceView) {
	ld := view.LatestDeployment
	switch {
	case ld != nil && verifyActive(ld.Status):
		r.add(verifyResultWarn, "deploy_in_progress", fmt.Sprintf("the first deployment is still %s; nothing is live yet", ld.Status))
	case ld != nil:
		r.add(verifyResultFail, "not_deployed", fmt.Sprintf("not deployed: no deployment has succeeded yet (latest is %s)", ld.Status))
	default:
		r.add(verifyResultFail, "not_deployed", "not deployed: the service has never been deployed (stage its settings, then apply_changes)")
	}
}

// observed turns the observed state into reasons.
func (r *verifyReasons) observed(now time.Time, view services.ServiceView, out *VerifyServiceOutput) {
	if view.CurrentDeploymentID == nil || view.DesiredState == "stopped" {
		return
	}
	if ld := view.LatestDeployment; ld != nil && view.CurrentDeploymentID != nil && ld.ID != *view.CurrentDeploymentID {
		switch {
		case verifyActive(ld.Status):
			r.add(verifyResultWarn, "deploy_in_progress", fmt.Sprintf("a newer deployment is %s; this verifies the release that is live now", ld.Status))
		case verifyFailedStatus(ld.Status):
			r.add(verifyResultWarn, "latest_deployment_failed", fmt.Sprintf("the latest deployment %s; this verifies the previous release that is still live (explain_failed_deployment says why)", ld.Status))
		}
	}
	o := view.Observed
	if o == nil {
		r.add(verifyResultWarn, "observed_missing", "the agent has not reported a state for this service yet")
		return
	}
	if view.Stale {
		r.add(verifyResultWarn, "observed_stale", "the observed state is stale: the server is offline or not reporting")
	}
	switch o.State {
	case "running":
	case "":
		r.add(verifyResultWarn, "observed_missing", "the observed state is empty")
	default:
		r.add(verifyResultFail, "not_running", fmt.Sprintf("the container is %s (should be running)", o.State))
	}
	if o.State == "running" {
		switch o.Health {
		case "unhealthy":
			r.add(verifyResultFail, "unhealthy", "the health check reports unhealthy")
		case "degraded":
			r.add(verifyResultWarn, "degraded", "the health check reports degraded: some replicas are unhealthy")
		case "starting":
			r.add(verifyResultWarn, "starting", "the container is still starting (health check not passed yet)")
		}
	}
	if o.Error != "" {
		r.addDetail(verifyResultFail, "recovery_gave_up", "automatic recovery gave up (see detail)", clip(o.Error, 200))
	}
	if short := processShortfalls(o); len(short) > 0 {
		// A service with processes is up only when every process has all its
		// replicas: name each one that does not.
		for _, p := range short {
			lvl := verifyResultWarn
			if p.ReplicasReady == 0 {
				lvl = verifyResultFail
			}
			r.add(lvl, "process_degraded", "process "+processReadiness(p))
		}
	} else if o.ReplicasDesired > 0 && o.ReplicasReady < o.ReplicasDesired && o.State == "running" {
		lvl := verifyResultWarn
		if o.ReplicasReady == 0 {
			lvl = verifyResultFail
		}
		r.add(lvl, "replicas_degraded", fmt.Sprintf("%d of %d replicas are ready", o.ReplicasReady, o.ReplicasDesired))
	}
	if n := out.Observed.RestartCount; n > 0 {
		recent := o.StartedAt != nil && now.Sub(*o.StartedAt) < verifyRecentStart
		if n >= verifyCrashLoopRestarts && recent {
			r.add(verifyResultFail, "crash_loop", fmt.Sprintf("%d container restarts and the newest start was %s: likely crash-looping", n, ago(now, *o.StartedAt)))
		} else {
			r.add(verifyResultWarn, "restarts", plural(n, "container restart", "container restarts")+" recorded")
		}
	}
}

// probe turns one probe result into reasons.
func (r *verifyReasons) probe(d services.DomainView, res ProbeResult) {
	host := d.Hostname
	switch {
	case res.Error != "":
		r.add(verifyResultFail, "domain_unreachable", fmt.Sprintf("%s was not reached: %s", host, res.Error))
	case res.RedirectedTo != "":
		r.add(verifyResultWarn, "domain_redirect_offhost", fmt.Sprintf("%s answered %d and redirects to %s, which leaves the host and was not followed", host, res.Status, res.RedirectedTo))
	case res.Status >= 500:
		r.add(verifyResultFail, "domain_5xx", fmt.Sprintf("%s returned %d at %s", host, res.Status, verifyPathOf(res.URL)))
	case res.Status == 401 && len(d.AuthUsers) > 0:
		// basic auth is on: answering 401 shows the service is reachable
	case res.Status == 404:
		r.add(verifyResultWarn, "domain_4xx", fmt.Sprintf("%s returned 404 at %s: the app has no handler there (try path to probe another URL) or the route is missing", host, verifyPathOf(res.URL)))
	case res.Status >= 400:
		r.add(verifyResultWarn, "domain_4xx", fmt.Sprintf("%s returned %d at %s", host, res.Status, verifyPathOf(res.URL)))
	case res.Status >= 300:
		r.add(verifyResultWarn, "domain_redirect", fmt.Sprintf("%s answered %d (redirect on the same host did not settle within the hop limit)", host, res.Status))
	}
	if res.Error == "" && res.LatencyMs > verifySlowMs {
		r.add(verifyResultWarn, "domain_slow", fmt.Sprintf("%s answered in %d ms", host, res.LatencyMs))
	}
	if d.DNSOk != nil && !*d.DNSOk {
		r.addDetail(verifyResultWarn, "dns_not_ok", fmt.Sprintf("DNS check for %s failed (see detail)", host), clip(d.StatusMessage, 200))
	}
	if d.Status == "error" {
		r.addDetail(verifyResultWarn, "domain_status", fmt.Sprintf("%s is in error state (see detail)", host), clip(d.StatusMessage, 200))
	}
}

func verifyPathOf(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:]
		}
	}
	return "/"
}

func verifyObserved(v services.ServiceView, full bool) VerifyObserved {
	o := VerifyObserved{
		DesiredState: v.DesiredState, Stale: v.Stale, ObservedAt: ptrTime(v.ObservedAt), CurrentDeployID: idPtr(v.CurrentDeploymentID),
		StagedChanges: v.HasStagedChanges, Replicas: []VerifyReplica{},
	}
	if v.LatestDeployment != nil {
		o.LatestDeployment = v.LatestDeployment.Status
	}
	obs := v.Observed
	if obs == nil {
		return o
	}
	o.Reported = true
	o.State, o.Health, o.Error = obs.State, obs.Health, untrustedText("observer.error", clip(obs.Error, 200))
	o.StartedAt = ptrTime(obs.StartedAt)
	o.ReplicasReady, o.ReplicasDesired = obs.ReplicasReady, obs.ReplicasDesired
	for _, p := range obs.Processes {
		o.Processes = append(o.Processes, VerifyProcess{Name: p.Name, State: p.State, Health: p.Health, ReplicasReady: p.ReplicasReady, ReplicasDesired: p.ReplicasDesired})
	}
	for _, r := range obs.Replicas {
		o.RestartCount += r.RestartCount
		if full {
			o.Replicas = append(o.Replicas, VerifyReplica{Index: r.Index, Process: r.Process, State: r.State, Health: r.Health, RestartCount: r.RestartCount, StartedAt: ptrTime(r.StartedAt)})
		}
	}
	return o
}

func verifySummary(o VerifyServiceOutput) string {
	var fails, warns []string
	for _, r := range o.Reasons {
		if r.Level == verifyResultFail {
			fails = append(fails, r.Message)
		} else {
			warns = append(warns, r.Message)
		}
	}
	how := "observed health only (no domains probed)"
	if o.DomainsProbed {
		how = plural(len(o.Probes), "domain probe", "domain probes") + " plus observed health"
	}
	switch o.Result {
	case verifyResultPass:
		return fmt.Sprintf("%s is up: %s passed.", o.Service, how)
	case verifyResultFail:
		return fmt.Sprintf("%s FAILED verification (%s): %s", o.Service, how, clip(strings.Join(fails, "; "), 400))
	}
	return fmt.Sprintf("%s verified with warnings (%s): %s", o.Service, how, clip(strings.Join(warns, "; "), 400))
}

func verifyNextTools(sel string, v services.ServiceView, o VerifyServiceOutput, canLogs bool) []NextTool {
	next := []NextTool{}
	svc := map[string]any{"service": sel}
	if o.Result != verifyResultPass {
		next = append(next, NextTool{Tool: "diagnose_service", Args: svc, Why: "root-cause the failing checks: deployments, variables, domains and the server in one call"})
	}
	if ld := v.LatestDeployment; ld != nil && verifyFailedStatus(ld.Status) {
		next = append(next, NextTool{Tool: "explain_failed_deployment", Args: map[string]any{"deployment": ld.ID.String()}, Why: "the latest deployment " + ld.Status})
	}
	if o.LogErrorCount > 0 && canLogs {
		next = append(next, NextTool{Tool: "tail_logs", Args: map[string]any{"service": sel, "grep": "error"}, Why: "read the error lines with surrounding context"})
	}
	for _, r := range o.Reasons {
		if r.Code == "dns_not_ok" || r.Code == "domain_status" {
			next = append(next, NextTool{Tool: "setup_domain", Args: svc, Why: "re-check DNS and get the exact record to create"})
			break
		}
	}
	if v.HasStagedChanges {
		next = append(next, NextTool{Tool: "review_changes", Args: svc, Why: "staged changes are not live; review them before apply_changes"})
	}
	return next
}

func verifyValidPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.ContainsAny(p, "\r\n\\ \t") && len(p) <= 512
}

func verifyActive(status string) bool {
	switch status {
	case "created", "queued", "preparing", "building", "releasing", "verifying":
		return true
	}
	return false
}

func verifyFailedStatus(status string) bool {
	switch status {
	case "failed", "rolled_back", "rollback_failed":
		return true
	}
	return false
}
