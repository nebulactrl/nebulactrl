package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// proxyConfigurer is the part of proxy.Manager that takes the server-level
// proxy configuration. It is optional on Config.Proxy so proxies without proxy
// v2 support (and most test fakes) need no stub; a command that carries a proxy
// configuration then fails instead of being silently dropped.
type proxyConfigurer interface {
	SetProxyConfig(cfg agentprotocol.ProxyConfig)
	SetTailnetAuthKey(key string)
	LastApply() agentprotocol.ProxyConfigureResult
}

// resolveRoutes fills the agent-side resolved fields (basic-auth users) of a
// route table from the command's secrets. A domain whose secret is missing
// fails the whole table: it is never applied with an unprotected domain.
func resolveRoutes(routes []agentprotocol.Route, secrets map[string]string) ([]agentprotocol.Route, error) {
	out, _, err := proxy.ResolveSecrets(routes, nil, secrets)
	return out, err
}

// keepLiveRoutes protects the routes of services this agent has already moved
// past the control plane's view. A route table the control plane sends
// (sync_routes, a replacing proxy_configure) is computed from its database when
// the command is queued, and the database only learns of a new deployment or
// replica count when the agent's result arrives. A table queued by any other
// change in that window (a domain, maintenance, another service's route) is
// therefore older than what this agent has done:
//
//   - it names the previous containers of a service that a deploy or scale has
//     just switched. Applied as sent it puts them back into the upstreams, and
//     the retire step stops them right after: a sustained 502 of a rolling
//     deploy or a scale-down.
//   - it has no route at all for a service whose first deployment has just been
//     routed. Applied as sent it removes the route (404 pages) and lets the
//     proxy leave the service's network (502 until the next push).
//
// For every managed route whose service is mid-command, or was deployed or
// scaled here to a replica set other than the one the table names, the live
// table's upstreams, networks and health path (one unit with the replica set)
// win; everything else in the incoming route (hosts, domains, maintenance) is
// applied as sent. A managed route of such a service that the table lacks stays
// as it is live: this agent's routes for a deployed service change through its
// own deploy, scale and remove commands, never by absence from a table.
// External upstreams and services this agent knows nothing about are always the
// control plane's. This is the rule Reconcile applies to its own frames ("local
// state wins").
//
// cur is the proxy's live table; the caller holds routeMu.
func (e *Engine) keepLiveRoutes(table, cur []agentprotocol.Route) []agentprotocol.Route {
	if len(cur) == 0 {
		return table
	}
	live := make(map[string]agentprotocol.Route, len(cur))
	for _, r := range cur {
		live[r.ServiceID] = r
	}
	sent := make(map[string]bool, len(table))
	for _, r := range table {
		sent[r.ServiceID] = true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []agentprotocol.Route // copied on the first change
	for i, r := range table {
		lr, ok := live[r.ServiceID]
		if !ok || len(r.Upstreams) == 0 || len(lr.Upstreams) == 0 ||
			!managedUpstreams(r.Upstreams) || !managedUpstreams(lr.Upstreams) || slices.Equal(r.Upstreams, lr.Upstreams) {
			continue
		}
		owner := ownerOf(r)
		if e.inflight[owner] == 0 && !supersedesTable(e.local[owner], r) {
			continue
		}
		if out == nil {
			out = slices.Clone(table)
		}
		e.log.Info("route table keeps the live upstreams: the control plane's view is older than this agent's",
			"service_id", owner, "sent", r.Upstreams, "live", lr.Upstreams, "in_flight", e.inflight[owner] > 0)
		r.Upstreams, r.Networks, r.HealthPath = slices.Clone(lr.Upstreams), slices.Clone(lr.Networks), lr.HealthPath
		out[i] = r
	}
	for _, lr := range cur {
		if sent[lr.ServiceID] || len(lr.Upstreams) == 0 || !managedUpstreams(lr.Upstreams) {
			continue
		}
		owner := ownerOf(lr)
		if _, deployed := e.local[owner]; !deployed && e.inflight[owner] == 0 {
			continue
		}
		if out == nil {
			out = slices.Clone(table)
		}
		e.log.Info("route table keeps a live route it lacks: the control plane's view is older than this agent's",
			"service_id", owner, "route", lr.ServiceID, "in_flight", e.inflight[owner] > 0)
		out = append(out, lr)
	}
	if out == nil {
		return table
	}
	return out
}

// ownerOf is the service whose commands own a route: the compose stack of a
// stack route, else the route's own service.
func ownerOf(r agentprotocol.Route) string {
	if r.Stack != "" {
		return r.Stack
	}
	return r.ServiceID
}

// managedUpstreams reports whether every upstream is a managed container
// ("<name>:<port>" with the container prefix). Addresses of external routes
// are the control plane's to change at any time.
func managedUpstreams(ups []string) bool {
	for _, u := range ups {
		host, _, err := net.SplitHostPort(u)
		if err != nil || !strings.HasPrefix(host, agentprotocol.ContainerPrefix) {
			return false
		}
	}
	return true
}

// supersedesTable reports whether the replica set this agent last deployed
// (loc) differs from the managed containers a route table names, so the agent's
// view is the newer one: the control plane records a deployment only after the
// agent has applied it. For a service with processes the route is compared
// with the replicas of the process that owns it. An unknown service (loc is
// zero), a compose stack (no container names) or a route no process owns never
// supersedes.
func supersedesTable(loc desiredEntry, r agentprotocol.Route) bool {
	want := loc.namesOfRoute(r)
	if len(want) == 0 {
		return false
	}
	hosts := make([]string, len(r.Upstreams))
	for i, u := range r.Upstreams {
		hosts[i], _, _ = net.SplitHostPort(u)
	}
	slices.Sort(hosts)
	names := slices.Clone(want)
	slices.Sort(names)
	return !slices.Equal(hosts, names)
}

// installProxyConfig resolves the certificate keys of cfg from the command's
// secrets and hands the configuration (and the one-time tailnet auth key) to
// the proxy; the next Apply uses it. A nil cfg leaves the configuration alone.
func (e *Engine) installProxyConfig(cfg *agentprotocol.ProxyConfig, secrets map[string]string) error {
	if cfg == nil {
		return nil
	}
	pc, ok := e.cfg.Proxy.(proxyConfigurer)
	if !ok {
		return errors.New("this agent's proxy does not accept a proxy configuration")
	}
	_, resolved, err := proxy.ResolveSecrets(nil, cfg, secrets)
	if err != nil {
		return err
	}
	pc.SetProxyConfig(*resolved)
	if t := resolved.Tailnet; t != nil && t.Provision {
		pc.SetTailnetAuthKey(secrets[agentprotocol.SecretTailnetAuthKey])
	}
	return nil
}

// lastApply is the proxy's report of its last Apply (zero when unsupported).
func (e *Engine) lastApply() agentprotocol.ProxyConfigureResult {
	if pc, ok := e.cfg.Proxy.(proxyConfigurer); ok {
		return pc.LastApply()
	}
	return agentprotocol.ProxyConfigureResult{}
}

// proxyResult builds the result frame of sync_routes and proxy_configure. Its
// Data is a ProxyConfigureResult, whose outcome/message keys also decode as a
// DeployResult.
func proxyResult(id, outcome, msg string, rep agentprotocol.ProxyConfigureResult) agentprotocol.ResultFrame {
	rep.Outcome, rep.Message = outcome, msg
	data, _ := json.Marshal(rep)
	f := agentprotocol.ResultFrame{CommandID: id, Outcome: outcome, Data: data}
	if outcome != agentprotocol.OutcomeSucceeded {
		f.Error = msg
	}
	return f
}

func (e *Engine) handleProxyConfigure(_ context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.ProxyConfigurePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid proxy_configure payload: "+err.Error())
	}
	if p.ACMEEmail != "" {
		e.cfg.Proxy.SetACMEEmail(p.ACMEEmail)
	}
	e.applyTrafficSetting(p.TrafficAnalytics)
	if err := e.installProxyConfig(&p.Proxy, cmd.Secrets); err != nil {
		return failedResult(cmd.ID, "proxy configuration rejected: "+err.Error())
	}

	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	replace := p.ReplaceRoutes || p.Routes != nil
	// The configuration always applies; a table older than one already applied
	// does not, the agent's own table stays.
	superseded := false
	if replace {
		if old, newest := e.tableSuperseded(p.TableSeq); old {
			e.log.Info("proxy_configure route table superseded by a newer one", "table_seq", p.TableSeq, "newest", newest)
			replace, superseded = false, true
		}
	}
	routes := p.Routes
	if !replace {
		routes = e.cfg.Proxy.Current()
	}
	if routes == nil {
		routes = []agentprotocol.Route{}
	}
	routes, err := resolveRoutes(routes, cmd.Secrets)
	if err != nil {
		return failedResult(cmd.ID, "route table rejected: "+err.Error())
	}
	if replace {
		routes = e.keepLiveRoutes(routes, e.cfg.Proxy.Current())
	}
	if err := e.cfg.Proxy.Apply(routes); err != nil {
		return proxyResult(cmd.ID, agentprotocol.OutcomeFailed, err.Error(), e.lastApply())
	}
	if replace {
		e.setRoutes(routes)
		e.noteTable(p.TableSeq)
	}
	rep := e.lastApply()
	msg := "Proxy configuration applied"
	if rep.Restarted {
		msg += "; the proxy was restarted"
	}
	if superseded {
		msg += "; its route table was superseded by a newer one and not applied"
	}
	return proxyResult(cmd.ID, agentprotocol.OutcomeSucceeded, msg, rep)
}

// syncVerify is one post-apply check of a route-only deployment.
type syncVerify struct {
	route agentprotocol.Route
	host  string // http: Host header
	path  string // http: probe path
}

func (e *Engine) handleSyncRoutes(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.SyncRoutesPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid sync_routes payload: "+err.Error())
	}
	routeOnly := p.DeploymentID != ""
	event := func(step, status, msg string) {
		if routeOnly {
			e.cfg.Sender.Event(agentprotocol.EventFrame{DeploymentID: p.DeploymentID, Step: step, Status: status, Message: msg})
		}
	}
	reject := func(msg string) agentprotocol.ResultFrame {
		event(agentprotocol.StepRoute, agentprotocol.StepFailed, msg)
		return proxyResult(cmd.ID, agentprotocol.OutcomeFailed, msg, agentprotocol.ProxyConfigureResult{})
	}

	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	// A table older than one already applied is not applied: it would undo what
	// the newer one changed (a route-only deployment applies its own table).
	if old, newest := e.tableSuperseded(p.TableSeq); old && !routeOnly {
		e.log.Info("sync_routes superseded by a newer route table", "table_seq", p.TableSeq, "newest", newest)
		return proxyResult(cmd.ID, agentprotocol.OutcomeSucceeded,
			fmt.Sprintf("Superseded by a newer route table (%d, this one is %d): not applied", newest, p.TableSeq), e.lastApply())
	}
	if p.ACMEEmail != "" {
		e.cfg.Proxy.SetACMEEmail(p.ACMEEmail)
	}
	e.applyTrafficSetting(p.TrafficAnalytics)
	if err := e.installProxyConfig(p.Proxy, cmd.Secrets); err != nil {
		return reject("Proxy configuration rejected: " + err.Error())
	}
	if p.Routes == nil {
		p.Routes = []agentprotocol.Route{}
	}
	routes, err := resolveRoutes(p.Routes, cmd.Secrets)
	if err != nil {
		return reject("Route table rejected: " + err.Error())
	}

	prev := e.cfg.Proxy.Current()
	routes = e.keepLiveRoutes(routes, prev)
	event(agentprotocol.StepRoute, agentprotocol.StepStarted, "Updating proxy routes")
	if err := e.cfg.Proxy.Apply(routes); err != nil {
		return reject("Proxy rejected the route table: " + err.Error())
	}
	e.noteTable(p.TableSeq)
	event(agentprotocol.StepRoute, agentprotocol.StepSucceeded, "")
	e.setRoutes(routes)
	msg := fmt.Sprintf("Applied %d route(s)", len(routes))
	if !routeOnly {
		return proxyResult(cmd.ID, agentprotocol.OutcomeSucceeded, msg, e.lastApply())
	}

	checks := verifyTargets(routes, prev)
	if len(checks) == 0 {
		event(agentprotocol.StepVerify, agentprotocol.StepSucceeded, "Nothing to verify (no http or tcp route changed)")
		return proxyResult(cmd.ID, agentprotocol.OutcomeSucceeded, msg, e.lastApply())
	}
	event(agentprotocol.StepVerify, agentprotocol.StepStarted, fmt.Sprintf("Verifying %d route(s) through the proxy", len(checks)))
	var last string
	for try := 1; try <= e.cfg.VerifyTries; try++ {
		if last = e.verifyRoutes(ctx, checks); last == "" {
			event(agentprotocol.StepVerify, agentprotocol.StepSucceeded, "Reachable through the proxy")
			return proxyResult(cmd.ID, agentprotocol.OutcomeSucceeded, msg, e.lastApply())
		}
		e.log.Info("route verification failed", "attempt", try, "of", e.cfg.VerifyTries, "reason", last)
		if try < e.cfg.VerifyTries {
			select {
			case <-ctx.Done():
				return proxyResult(cmd.ID, agentprotocol.OutcomeFailed, "agent stopped during verification", e.lastApply())
			case <-time.After(e.cfg.VerifyInterval):
			}
		}
	}
	event(agentprotocol.StepVerify, agentprotocol.StepFailed, "Verification through the proxy failed: "+last)

	// Withdraw the new table: the previous one is restored before anything else changes.
	fail := "Verification through the proxy failed: " + last
	if err := e.cfg.Proxy.Apply(prev); err != nil {
		return proxyResult(cmd.ID, agentprotocol.OutcomeRollbackFailed, fail+"\nRestoring the previous routes also failed: "+err.Error(), e.lastApply())
	}
	e.setRoutes(prev)
	return proxyResult(cmd.ID, agentprotocol.OutcomeRolledBack, fail+"\nRolled back to the previous routes.", e.lastApply())
}

// verifyTargets lists what a route-only deployment checks: the http and tcp
// routes that changed against the previous table, or, when none changed (a
// re-deploy), every external and tcp/udp route. udp has no check, and routes
// with VerifySkip are exempt (upstreams that legitimately answer 5xx).
func verifyTargets(table, prev []agentprotocol.Route) []syncVerify {
	changed := func(r agentprotocol.Route) bool {
		for _, o := range prev {
			if o.ServiceID == r.ServiceID && o.Protocol == r.Protocol {
				return !reflect.DeepEqual(o, r)
			}
		}
		return true
	}
	pick := func(all bool) []syncVerify {
		var out []syncVerify
		for _, r := range table {
			if r.VerifySkip || r.Protocol == agentprotocol.ProtoUDP {
				continue
			}
			if all {
				if r.Kind != agentprotocol.RouteKindExternal && !r.IsL4() {
					continue
				}
			} else if !changed(r) {
				continue
			}
			if r.IsL4() {
				out = append(out, syncVerify{route: r})
				continue
			}
			host, prefix, ok := verifyTarget(r)
			if !ok {
				continue
			}
			path := r.HealthPath
			if path == "" {
				path = "/"
			}
			out = append(out, syncVerify{route: r, host: host, path: prefix + path})
		}
		return out
	}
	if out := pick(false); len(out) > 0 {
		return out
	}
	return pick(true)
}

// verifyRoutes runs every check once and returns the first failure ("" = all
// passed). http: the probe entrypoint answers the route's host with status <
// 500 (it checks routing and the upstream, not access rules). tcp: the
// published listen port accepts a connection.
func (e *Engine) verifyRoutes(ctx context.Context, checks []syncVerify) string {
	for _, c := range checks {
		if c.route.IsL4() {
			if err := e.dialL4(ctx, c.route.ListenPort); err != nil {
				return fmt.Sprintf("tcp port %d: %v", c.route.ListenPort, err)
			}
			continue
		}
		code, err := e.cfg.Proxy.Probe(ctx, c.host, c.path)
		switch {
		case err != nil:
			return fmt.Sprintf("%s%s: %v", c.host, c.path, err)
		case code >= 500:
			return fmt.Sprintf("%s%s: HTTP %d", c.host, c.path, code)
		}
	}
	return ""
}

// dialL4 connects once to a tcp entrypoint: through the proxy container's edge
// address, else the host's published port.
func (e *Engine) dialL4(ctx context.Context, port int) error {
	var addrs []string
	if info, err := e.cfg.Docker.ContainerInspect(ctx, agentprotocol.ProxyContainer); err == nil {
		if ep, ok := info.Networks[agentprotocol.EdgeNetwork]; ok && ep.IP != "" {
			addrs = append(addrs, net.JoinHostPort(ep.IP, strconv.Itoa(port)))
		}
	}
	addrs = append(addrs, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	dial := e.cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	}
	var last error
	for _, a := range addrs {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		c, err := dial(dctx, "tcp", a)
		cancel()
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
	}
	return last
}
