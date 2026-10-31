// Package health runs deployment health checks from the agent, outside the
// container, so images need neither curl nor wget.
//
// A check passes after SuccessThreshold consecutive successful probes. It
// fails (unhealthy) when the deadline passes, the container exits, or the
// container restarts.
package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// unit is one "second" of the spec; tests shrink it.
var unit = time.Second

// Check types.
const (
	TypeHTTP   = "http"
	TypeTCP    = "tcp"
	TypeDocker = "docker"
	TypeExec   = "exec" // the container HEALTHCHECK installed by the agent; judged like docker
	TypeNone   = "none"
)

// noneStableSeconds is how long a container must run without restarting to
// count as healthy under the "none" check.
const noneStableSeconds = 10

// Target describes what to check.
type Target struct {
	Spec agentprotocol.HealthcheckSpec
	// DefaultPort is used when Spec.Port is 0.
	DefaultPort int
	// Addrs are candidate hosts (container IP first, then its DNS name). The
	// first that answers is remembered; the rest are fallbacks for hosts where
	// the container IP is not routable from the agent (Docker Desktop).
	Addrs []string
	// Inspect reports the container's current state.
	Inspect func(ctx context.Context) (docker.ContainerInfo, error)
	// OneShot runs to completion: the container exiting with code 0 is healthy.
	OneShot bool
	// Trace, when set, receives one line per probe attempt (address, status
	// code, latency, error) for the deployment's debug log.
	Trace func(line string)
}

// Result is the outcome of Wait.
type Result struct {
	Healthy bool
	// Reason explains an unhealthy result.
	Reason string
	// Warning is set for a healthy result that deserves a plan-level caveat.
	Warning string
	// Addr is the address that answered, when known.
	Addr string
}

type spec struct {
	typ        string
	path       string
	port       int
	interval   time.Duration
	timeout    time.Duration
	startAfter time.Duration
	deadline   time.Duration
	threshold  int
	// https makes an http probe use TLS (set by Probe.Once only; deploy-time
	// checks always probe plain http).
	https bool
}

func normalize(t Target) (spec, string) {
	h := t.Spec
	s := spec{typ: h.Type, path: h.Path, port: h.Port}
	if s.port == 0 {
		s.port = t.DefaultPort
	}
	if s.path == "" {
		s.path = "/"
	} else if !strings.HasPrefix(s.path, "/") {
		s.path = "/" + s.path
	}
	pos := func(v, def int) time.Duration {
		if v <= 0 {
			v = def
		}
		return time.Duration(v) * unit
	}
	s.interval = pos(h.IntervalSeconds, 2)
	s.timeout = pos(h.TimeoutSeconds, 3)
	s.deadline = pos(h.DeadlineSeconds, 120)
	if h.StartPeriodSeconds > 0 {
		s.startAfter = time.Duration(h.StartPeriodSeconds) * unit
	}
	s.threshold = h.SuccessThreshold
	if s.threshold <= 0 {
		s.threshold = 2
	}
	warn := ""
	switch s.typ {
	case TypeHTTP, TypeTCP:
		if s.port <= 0 {
			warn = "No port configured: falling back to the running-for-10s check"
			s.typ = TypeNone
		}
	case TypeDocker:
	case TypeExec:
		s.typ = TypeDocker
	case TypeNone, "":
		s.typ = TypeNone
	default:
		warn = fmt.Sprintf("Unknown health check type %q: falling back to the running-for-10s check", s.typ)
		s.typ = TypeNone
	}
	return s, warn
}

// Wait blocks until the container is healthy, unhealthy, or ctx ends.
func Wait(ctx context.Context, t Target) Result {
	s, warn := normalize(t)
	deadline := time.NewTimer(s.deadline)
	defer deadline.Stop()

	started := time.Now()
	first, err := t.Inspect(ctx)
	if err != nil {
		return Result{Reason: "inspect container: " + err.Error()}
	}
	baseRestarts := first.RestartCount
	t.tracef("check %s port=%d path=%s interval=%s timeout=%s deadline=%s threshold=%d start-period=%s targets=%s",
		s.typ, s.port, s.path, s.interval, s.timeout, s.deadline, s.threshold, s.startAfter, strings.Join(t.Addrs, ","))

	if s.startAfter > 0 {
		select {
		case <-ctx.Done():
			return Result{Reason: ctx.Err().Error()}
		case <-time.After(s.startAfter):
		}
	}

	hc := &http.Client{
		Timeout:       s.timeout,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer hc.CloseIdleConnections()

	var (
		okRun     int
		preferred = -1
		lastFail  string
	)
	stable := noneStableSeconds * unit
	for {
		info, err := t.Inspect(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			return Result{Reason: ctx.Err().Error()}
		case err != nil:
			return Result{Reason: "inspect container: " + err.Error()}
		case t.OneShot && !info.Running && !info.Restarting && info.State == "exited" && info.ExitCode == 0:
			return Result{Healthy: true}
		case !info.Running && !info.Restarting:
			return Result{Reason: exitReason(info)}
		case info.Restarting || info.RestartCount > baseRestarts:
			return Result{Reason: "container restarted (crash loop)"}
		}

		var (
			passed bool
			addr   string
		)
		switch s.typ {
		case TypeNone:
			// A one-shot is healthy only once it has exited cleanly.
			passed = !t.OneShot && time.Since(started) >= stable
			if passed {
				return Result{Healthy: true, Warning: noneWarning(warn)}
			}
		case TypeDocker:
			t.tracef("docker health status %q", info.Health)
			switch info.Health {
			case "healthy":
				passed = true
			case "":
				// The image defines no HEALTHCHECK: use the running-for-10s rule.
				warn = "Image defines no HEALTHCHECK: container considered healthy after running 10s"
				if time.Since(started) >= stable {
					return Result{Healthy: true, Warning: warn}
				}
			default:
				lastFail = "docker health status is " + info.Health
			}
		case TypeHTTP, TypeTCP:
			passed, addr, preferred, lastFail, _ = probeAddrs(ctx, hc, s, t.Addrs, preferred, t.Trace)
		}

		if s.typ != TypeNone {
			if passed {
				okRun++
				t.tracef("pass %d/%d consecutive", okRun, s.threshold)
				if okRun >= s.threshold {
					return Result{Healthy: true, Warning: warn, Addr: addr}
				}
			} else {
				okRun = 0
			}
		}

		select {
		case <-ctx.Done():
			return Result{Reason: ctx.Err().Error()}
		case <-deadline.C:
			msg := fmt.Sprintf("health check did not pass within %s", s.deadline.Round(unit))
			if lastFail != "" {
				msg += " (last failure: " + lastFail + ")"
			}
			return Result{Reason: msg}
		case <-time.After(s.interval):
		}
	}
}

func noneWarning(w string) string {
	if w != "" {
		return w
	}
	return "No health check configured: container considered healthy after running 10s without restart"
}

func exitReason(info docker.ContainerInfo) string {
	msg := fmt.Sprintf("container exited with code %d", info.ExitCode)
	if info.OOMKilled {
		msg += " (out of memory)"
	}
	if info.State != "" && info.State != "exited" {
		msg += " [state " + info.State + "]"
	}
	return msg
}

// tracef reports one line to Trace, when set.
func (t Target) tracef(format string, a ...any) {
	if t.Trace != nil {
		t.Trace(fmt.Sprintf(format, a...))
	}
}

// probeAddrs tries the preferred address first, then the others, and returns
// whether a probe passed plus the updated preferred index and how long the
// passing probe took. Every attempt is reported to trace (nil: none) with its
// latency.
func probeAddrs(ctx context.Context, hc *http.Client, s spec, addrs []string, preferred int, trace func(string)) (ok bool, addr string, pref int, fail string, took time.Duration) {
	say := func(format string, a ...any) {
		if trace != nil {
			trace(fmt.Sprintf(format, a...))
		}
	}
	order := make([]int, 0, len(addrs))
	if preferred >= 0 && preferred < len(addrs) {
		order = append(order, preferred)
	}
	for i := range addrs {
		if i != preferred {
			order = append(order, i)
		}
	}
	var fails []string
	for _, i := range order {
		host := addrs[i]
		hp := net.JoinHostPort(host, strconv.Itoa(s.port))
		var (
			reached bool
			err     error
		)
		began := time.Now()
		switch s.typ {
		case TypeTCP:
			err = dialTCP(ctx, hp, s.timeout)
			reached = err == nil
			if reached {
				say("probe tcp %s: connected in %s", hp, latency(began))
			} else {
				say("WARN probe tcp %s: failed after %s: %v", hp, latency(began), err)
			}
		default:
			var code int
			code, err = httpGet(ctx, hc, s.https, hp, s.path)
			reached = err == nil && code >= 200 && code < 400
			switch {
			case err != nil:
				say("WARN probe GET http://%s%s: failed after %s: %v", hp, s.path, latency(began), err)
			case reached:
				say("probe GET http://%s%s: HTTP %d in %s (pass)", hp, s.path, code, latency(began))
			default:
				say("WARN probe GET http://%s%s: HTTP %d in %s (fail: expected 200-399)", hp, s.path, code, latency(began))
			}
			if err == nil && !reached {
				err = fmt.Errorf("HTTP %d from %s%s", code, hp, s.path)
				// The address answered; no point trying fallbacks.
				return false, host, i, err.Error(), 0
			}
		}
		if reached {
			return true, host, i, "", latency(began)
		}
		if ctx.Err() != nil {
			return false, "", preferred, ctx.Err().Error(), 0
		}
		fails = append(fails, err.Error())
	}
	return false, "", preferred, strings.Join(fails, "; "), 0
}

// latency is the time since began, to the millisecond.
func latency(began time.Time) time.Duration {
	return time.Since(began).Round(time.Millisecond)
}

func dialTCP(ctx context.Context, hostPort string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return err
	}
	return c.Close()
}

func httpGet(ctx context.Context, hc *http.Client, https bool, hostPort, path string) (int, error) {
	scheme := "http"
	if https {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+hostPort+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
