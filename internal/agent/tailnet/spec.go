// Package tailnet manages the per-service tailnet sidecar on an agent host: a
// pinned tailscale/tailscale container in userspace-networking mode that joins
// the service's environment network and fronts every replica through the
// environment alias (Serve HTTPS or plain HTTP, raw TCP forwards, optional
// Funnel). It has no capabilities, no TUN device, no published ports and never
// sees the Docker socket; the agent execs a fixed argv to read its status.
package tailnet

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Fixed paths and addresses inside the sidecar.
const (
	stateDir     = "/var/lib/tailscale"
	configDir    = "/config"
	serveFile    = "serve.json"
	healthAddr   = "127.0.0.1:9002"
	certDomain   = "${TS_CERT_DOMAIN}" // substituted by containerboot; "no-https" on tailnets without HTTPS
	memoryBytes  = 256 << 20
	healthPeriod = 10 * time.Second
)

var (
	slugRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Validate checks an ensure spec before anything touches Docker. Every value
// that ends up in an environment variable, a path or the serve config is
// checked here.
func Validate(spec agentprotocol.ServiceTailnetSpec) error {
	if spec.ServiceID == "" || spec.EnvironmentID == "" {
		return errors.New("serviceId and environmentId are required")
	}
	if !slugRe.MatchString(spec.ServiceSlug) {
		return fmt.Errorf("invalid service slug %q", spec.ServiceSlug)
	}
	if !hostnameRe.MatchString(spec.Hostname) {
		return fmt.Errorf("invalid tailnet hostname %q", spec.Hostname)
	}
	if !agentprotocol.ValidTailnetProvider(spec.Provider) {
		return fmt.Errorf("unknown tailnet provider %q", spec.Provider)
	}
	if spec.Provider == agentprotocol.TailnetProviderHeadscale {
		if !strings.HasPrefix(spec.LoginServer, "https://") && !strings.HasPrefix(spec.LoginServer, "http://") {
			return errors.New("a headscale tailnet needs an http(s) login server")
		}
		if strings.ContainsAny(spec.LoginServer, " \t\r\n\"'`$;&|<>") {
			return errors.New("invalid login server")
		}
		if spec.Funnel {
			return errors.New("headscale has no Funnel")
		}
	}
	if spec.Web && (spec.Port < 1 || spec.Port > 65535) {
		return errors.New("the web exposure needs the service port")
	}
	if spec.Funnel && !spec.Web {
		return errors.New("funnel needs the web exposure")
	}
	seen := map[int]bool{}
	for _, f := range spec.TCP {
		if f.Listen < 1 || f.Listen > 65535 || f.Target < 1 || f.Target > 65535 {
			return fmt.Errorf("tcp %d -> %d is out of range", f.Listen, f.Target)
		}
		if seen[f.Listen] {
			return fmt.Errorf("tcp listen port %d is used twice", f.Listen)
		}
		seen[f.Listen] = true
		if f.TLS && spec.Provider == agentprotocol.TailnetProviderHeadscale {
			return errors.New("headscale cannot terminate TLS")
		}
	}
	if spec.Web && seen[webPort(spec.Provider)] {
		return fmt.Errorf("tcp port %d collides with the web exposure", webPort(spec.Provider))
	}
	return nil
}

func webPort(provider string) int {
	if provider == agentprotocol.TailnetProviderHeadscale {
		return agentprotocol.TailnetHTTPPort
	}
	return agentprotocol.TailnetHTTPSPort
}

// ContainerSpec is the sidecar container. authKey is set only while
// provisioning; the node's identity then lives on the state volume (with
// TS_AUTH_ONCE a restart never logs in again). The spec fingerprint label never
// covers the key, nor the exposures (they are applied live through the watched
// serve config).
func ContainerSpec(spec agentprotocol.ServiceTailnetSpec, authKey string) docker.ContainerSpec {
	env := []string{
		"TS_USERSPACE=true",
		"TS_STATE_DIR=" + stateDir,
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false", // Docker's DNS must keep resolving the service alias
		"TS_HOSTNAME=" + spec.Hostname,
		"TS_SERVE_CONFIG=" + configDir + "/" + serveFile,
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=" + healthAddr,
	}
	if spec.Provider == agentprotocol.TailnetProviderHeadscale {
		// A tagged key carries the tags: never pass --advertise-tags with it.
		env = append(env, "TS_EXTRA_ARGS=--login-server="+spec.LoginServer)
	}
	nets := []docker.NetworkAttachment{{Network: agentprotocol.EnvNetwork(spec.EnvironmentID)}}
	if spec.BlockEgress {
		nets = append(nets, docker.NetworkAttachment{Network: agentprotocol.EnvInternalNetwork(spec.EnvironmentID)})
	}
	fp := []string{proxy.TailscaleImage}
	for _, n := range nets {
		fp = append(fp, n.Network)
	}
	hash := fingerprint(append(fp, env...)...)
	if authKey != "" {
		env = append(env, "TS_AUTHKEY="+authKey)
	}
	return docker.ContainerSpec{
		Name:  agentprotocol.TailnetContainerName(spec.ServiceID),
		Image: proxy.TailscaleImage,
		Env:   env,
		Labels: map[string]string{
			agentprotocol.LabelManaged:     "true",
			agentprotocol.LabelTailnet:     spec.ServiceID,
			agentprotocol.LabelEnvironment: spec.EnvironmentID,
			agentprotocol.LabelTailnetSpec: hash,
		},
		// The environment network (plus its internal one when the app blocks
		// egress): never the edge network or another environment.
		Networks: nets,
		Mounts: []docker.VolumeMount{
			{Volume: agentprotocol.TailnetStateVolumeName(spec.ServiceID), Target: stateDir},
			{Volume: agentprotocol.TailnetConfigVolumeName(spec.ServiceID), Target: configDir},
		},
		RestartPolicy: "unless-stopped",
		MemoryBytes:   memoryBytes,
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"no-new-privileges"},
		Healthcheck: &docker.HealthSpec{
			Test:     []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://" + healthAddr + "/healthz"},
			Interval: healthPeriod, Timeout: 3 * time.Second, StartPeriod: 30 * time.Second, Retries: 3,
		},
		LogConfig: &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": proxy.LogMaxSize, "max-file": proxy.LogMaxFile}},
	}
}

func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// ServeConfig renders the Serve configuration (ipn.ServeConfig JSON).
//
// Tailscale: HTTPS on 443 proxies to http://<slug>:<port> under the node's own
// certificate name (${TS_CERT_DOMAIN}, substituted by containerboot), TCP
// ports forward per connection (TerminateTLS with the same name when asked),
// and Funnel opens 443 to the internet.
//
// Headscale: no HTTPS; plain HTTP on 80. The Web key must be the node's real
// DNS name, so fqdn (from `tailscale status`) is required for the web
// exposure and the first rendering, before the node has a name, only carries
// the TCP forwards.
func ServeConfig(spec agentprotocol.ServiceTailnetSpec, fqdn string) ([]byte, error) {
	tcp := map[string]any{}
	web := map[string]any{}
	var funnel map[string]bool
	headscale := spec.Provider == agentprotocol.TailnetProviderHeadscale
	slug := spec.ServiceSlug

	for _, f := range spec.TCP {
		h := map[string]any{"TCPForward": slug + ":" + strconv.Itoa(f.Target)}
		if f.TLS && !headscale {
			h["TerminateTLS"] = certDomain
		}
		tcp[strconv.Itoa(f.Listen)] = h
	}
	if spec.Web {
		handlers := map[string]any{"Handlers": map[string]any{"/": map[string]any{"Proxy": "http://" + slug + ":" + strconv.Itoa(spec.Port)}}}
		switch {
		case headscale && fqdn != "":
			tcp[strconv.Itoa(agentprotocol.TailnetHTTPPort)] = map[string]any{"HTTP": true}
			web[fqdn+":"+strconv.Itoa(agentprotocol.TailnetHTTPPort)] = handlers
		case !headscale:
			tcp[strconv.Itoa(agentprotocol.TailnetHTTPSPort)] = map[string]any{"HTTPS": true}
			key := certDomain + ":" + strconv.Itoa(agentprotocol.TailnetHTTPSPort)
			web[key] = handlers
			if spec.Funnel {
				funnel = map[string]bool{key: true}
			}
		}
	}
	cfg := map[string]any{}
	if len(tcp) > 0 {
		cfg["TCP"] = tcp
	}
	if len(web) > 0 {
		cfg["Web"] = web
	}
	if funnel != nil {
		cfg["AllowFunnel"] = funnel
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// serveTar packs the serve config for CopyToContainer into /config.
func serveTar(cfg []byte) (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: serveFile, Mode: 0o644, Size: int64(len(cfg)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(cfg); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}
