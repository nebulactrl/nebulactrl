package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ErrNoNetworkProbe is returned by a Probe for a check type that has no
// network probe (docker, exec, none) or no port to probe.
var ErrNoNetworkProbe = errors.New("health check has no network probe")

// Probe is one http or tcp probe of a health check spec, run once. It is the
// building block of periodic monitoring: unlike Wait it does not inspect a
// container, wait for a start period or demand consecutive successes.
type Probe struct {
	Spec agentprotocol.HealthcheckSpec
	// DefaultPort is used when Spec.Port is 0.
	DefaultPort int
	// Addrs are tried in order, as in Wait (container IP first, DNS name as
	// the fallback). An address that answers with a bad HTTP status fails the
	// probe without trying the rest.
	Addrs []string
	// TLS, when set, makes an http probe use https with this client
	// configuration (an external upstream that speaks TLS). A tcp probe only
	// connects and ignores it.
	TLS *tls.Config
}

// Once runs the probe and reports whether it passed and how long the passing
// attempt took. The port is Spec.Port, else DefaultPort; the path defaults to
// "/" and the timeout to 3 s, like Wait. A failed probe returns the failure of
// every address tried as err.
func (p Probe) Once(ctx context.Context) (ok bool, took time.Duration, err error) {
	s, _ := normalize(Target{Spec: p.Spec, DefaultPort: p.DefaultPort})
	if s.typ != TypeHTTP && s.typ != TypeTCP {
		return false, 0, fmt.Errorf("%w: type %q, port %d", ErrNoNetworkProbe, p.Spec.Type, s.port)
	}
	if len(p.Addrs) == 0 {
		return false, 0, errors.New("no address to probe")
	}
	s.https = p.TLS != nil
	hc := &http.Client{
		Timeout:       s.timeout,
		Transport:     &http.Transport{DisableKeepAlives: true, TLSClientConfig: p.TLS},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer hc.CloseIdleConnections()
	passed, _, _, fail, answered := probeAddrs(ctx, hc, s, p.Addrs, -1, nil)
	if !passed {
		return false, 0, errors.New(fail)
	}
	return true, answered, nil
}

// ProbeOnce runs one plain-http or tcp probe of spec against addrs; see Probe.
func ProbeOnce(ctx context.Context, spec agentprotocol.HealthcheckSpec, defaultPort int, addrs []string) (ok bool, took time.Duration, err error) {
	return Probe{Spec: spec, DefaultPort: defaultPort, Addrs: addrs}.Once(ctx)
}
