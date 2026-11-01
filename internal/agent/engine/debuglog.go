package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// maxDebugLines bounds the debug stream of one deployment: a chatty health
	// check must not grow the stored log without limit. The control plane
	// enforces the same cap again.
	maxDebugLines = 500
	// DebugTruncatedMarker is the last debug line sent once the cap is reached.
	DebugTruncatedMarker = "… debug log truncated at 500 lines"
	// maskedValue stands in for every environment value in a debug line.
	maskedValue = "•••"
)

// debugLog is the verbose trace of one deployment (stream "debug"): the docker
// calls it made, each probe attempt, route changes, cleanup and timings. It is
// redacted like every other stream (the batcher applies the redactor) and
// capped at maxDebugLines. Every method is safe on a nil receiver, so code
// that runs outside a deployment (scale, reconcile) calls it unconditionally.
type debugLog struct {
	b *logs.Batcher

	mu        sync.Mutex
	n         int
	truncated bool
}

// newDebugLog starts the debug stream of a deployment. Close flushes it.
func newDebugLog(p agentprotocol.DeployPayload, send func(agentprotocol.LogFrame), redact func(string) string) *debugLog {
	return &debugLog{b: logs.NewBatcher(agentprotocol.LogFrame{
		DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, Stream: agentprotocol.StreamDebug,
	}, send, redact)}
}

// Add queues one line (embedded newlines count as separate lines).
func (l *debugLog) Add(line string) {
	if l == nil {
		return
	}
	n := strings.Count(line, "\n") + 1
	l.mu.Lock()
	if l.truncated {
		l.mu.Unlock()
		return
	}
	if l.n+n > maxDebugLines {
		l.truncated = true
		l.mu.Unlock()
		l.b.Add(DebugTruncatedMarker)
		return
	}
	l.n += n
	l.mu.Unlock()
	l.b.Add(line)
}

// Close flushes the pending lines. Idempotent.
func (l *debugLog) Close() {
	if l != nil {
		l.b.Close()
	}
}

// debugf adds one line to the deployment's debug stream. A silent deploy (a
// scale of a finished deployment) leaves the history alone.
func (d *deploy) debugf(format string, a ...any) {
	if d.silent || d.dbg == nil {
		return
	}
	d.dbg.Add(fmt.Sprintf(format, a...))
}

// phase starts timing a deploy phase; the returned function records its
// duration for the summary line and the debug log. A process of a
// multi-process deploy names its phases after itself ("worker health").
func (d *deploy) phase(name string) func() {
	began := time.Now()
	if pr := d.proc(); pr != nil {
		name = pr.Name + " " + name
	}
	return func() {
		took := time.Since(began).Round(time.Millisecond)
		root := d.root()
		root.timingsMu.Lock()
		root.timings = append(root.timings, name+" "+took.String())
		root.timingsMu.Unlock()
		d.debugf("phase %s took %s", name, took)
	}
}

// timingSummary lists the recorded phases in the order they finished.
func (d *deploy) timingSummary() string {
	root := d.root()
	root.timingsMu.Lock()
	defer root.timingsMu.Unlock()
	return strings.Join(root.timings, ", ")
}

// envKeys lists the keys of KEY=VALUE entries, sorted. Values are never
// returned: a debug line names what the container is given, not what it holds.
func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// specSummary describes the docker create call of one container for the debug
// log. Environment values are masked; the start command is reduced to its
// argument count and program, since its arguments can carry a value.
func specSummary(spec docker.ContainerSpec) []string {
	var out []string
	out = append(out, fmt.Sprintf("create %s image=%s restart=%q stopTimeout=%s",
		spec.Name, shortID(spec.Image), spec.RestartPolicy, stopTimeoutLabel(spec.StopTimeout)))
	if keys := envKeys(spec.Env); len(keys) > 0 {
		masked := make([]string, len(keys))
		for i, k := range keys {
			masked[i] = k + "=" + maskedValue
		}
		out = append(out, fmt.Sprintf("  env (%d, values masked): %s", len(keys), strings.Join(masked, " ")))
	}
	if len(spec.Cmd) > 0 {
		out = append(out, fmt.Sprintf("  cmd: %d argument(s), program %q", len(spec.Cmd), spec.Cmd[0]))
	}
	for _, n := range spec.Networks {
		out = append(out, fmt.Sprintf("  network %s aliases=%s", n.Network, strings.Join(n.Aliases, ",")))
	}
	for _, m := range spec.Mounts {
		src := m.Volume
		if m.HostPath != "" {
			src = m.HostPath
		}
		out = append(out, fmt.Sprintf("  mount %s -> %s readOnly=%t", src, m.Target, m.ReadOnly))
	}
	if spec.MemoryBytes > 0 || spec.NanoCPUs > 0 {
		out = append(out, fmt.Sprintf("  limits memory=%dMiB cpus=%.2f", spec.MemoryBytes>>20, float64(spec.NanoCPUs)/1e9))
	}
	if len(spec.Labels) > 0 {
		out = append(out, fmt.Sprintf("  labels: %d (service=%s deployment=%s release=%s)", len(spec.Labels),
			shortID(spec.Labels[agentprotocol.LabelService]), shortID(spec.Labels[agentprotocol.LabelDeployment]), shortID(spec.Labels[agentprotocol.LabelRelease])))
	}
	return out
}

func stopTimeoutLabel(t *int) string {
	if t == nil {
		return "daemon default"
	}
	return fmt.Sprintf("%ds", *t)
}

// routeSummary says what a route table holds: how many routes and which hosts.
func routeSummary(routes []agentprotocol.Route) string {
	if len(routes) == 0 {
		return "0 routes"
	}
	var hosts []string
	for _, r := range routes {
		hosts = append(hosts, r.Hosts...)
	}
	sort.Strings(hosts)
	const show = 6
	more := ""
	if len(hosts) > show {
		more = fmt.Sprintf(" +%d more", len(hosts)-show)
		hosts = hosts[:show]
	}
	return fmt.Sprintf("%d routes (%s%s)", len(routes), strings.Join(hosts, ", "), more)
}

// proxyGeneration reads the generation of the proxy's live config when the
// proxy reports one ("n/a" otherwise: fakes and older managers).
func proxyGeneration(p Proxy) string {
	if g, ok := p.(interface{ Generation() int64 }); ok {
		return fmt.Sprintf("%d", g.Generation())
	}
	return "n/a"
}

// errLabel is "ok" or the error text.
func errLabel(err error) string {
	if err == nil {
		return "ok"
	}
	return "failed: " + err.Error()
}

// createLabel is the outcome of a container create call.
func createLabel(id string, err error) string {
	if err != nil {
		return "failed: " + err.Error()
	}
	return "created " + shortID(id)
}

// probeLabel is the outcome of one proxy probe.
func probeLabel(code int, err error) string {
	if err != nil {
		return "failed: " + err.Error()
	}
	if code >= 500 {
		return fmt.Sprintf("HTTP %d (fail: 5xx)", code)
	}
	return fmt.Sprintf("HTTP %d (pass)", code)
}

// healthResultLabel is the final verdict of one replica's health check.
func healthResultLabel(r health.Result) string {
	switch {
	case r.Healthy && r.Warning != "":
		return "healthy (" + r.Warning + ")"
	case r.Healthy:
		return "healthy"
	default:
		return "unhealthy: " + r.Reason
	}
}

// containerNames lists container names for the inventory line.
func containerNames(cs []docker.ContainerInfo) string {
	if len(cs) == 0 {
		return "none"
	}
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// attachmentNames lists the networks a container joins.
func attachmentNames(as []docker.NetworkAttachment) string {
	if len(as) == 0 {
		return "none"
	}
	names := make([]string, len(as))
	for i, a := range as {
		names[i] = a.Network
	}
	return strings.Join(names, ", ")
}

// sourceLabel names what a deploy ships, without credentials: an image
// reference with its pinned digest, or a git URL at a commit.
func sourceLabel(p agentprotocol.DeployPayload) string {
	switch {
	case p.Image != nil:
		return fmt.Sprintf("image %s (pinned %q)", p.Image.Ref, p.Image.Digest)
	case p.Git != nil:
		return fmt.Sprintf("git %s @ %s (token %t, ssh key %t)", p.Git.URL, shortSHA(p.Git.CommitSHA), p.Git.HasToken, p.Git.HasSSHKey)
	case p.Stack != nil:
		return fmt.Sprintf("compose stack, %d component(s)", len(p.Stack.Components))
	}
	return "none"
}

// debugErrf is debugf for a call that may have failed: a failure is logged at
// WARN, so the debug log's level filter finds it.
func (d *deploy) debugErrf(err error, format string, a ...any) {
	if err != nil {
		format = "WARN " + format
	}
	d.debugf(format, a...)
}

// leveled puts a trace line under a scope ("health nc-web-…"). A level word at
// the start of the line (WARN, ERROR) moves in front of the scope, where the
// log viewer reads it: "WARN health nc-web-…: probe …".
func leveled(line, scope string) string {
	for _, level := range []string{"WARN ", "ERROR "} {
		if rest, ok := strings.CutPrefix(line, level); ok {
			return level + scope + ": " + rest
		}
	}
	return scope + ": " + line
}

// unhealthy is a non-nil error for an unhealthy result, so it logs at WARN.
func unhealthy(r health.Result) error {
	if r.Healthy {
		return nil
	}
	return errors.New(r.Reason)
}

// probeFailure is a non-nil error for a proxy probe that failed or answered 5xx.
func probeFailure(code int, err error) error {
	if err != nil {
		return err
	}
	if code >= 500 {
		return fmt.Errorf("HTTP %d", code)
	}
	return nil
}
