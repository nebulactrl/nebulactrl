// Package stats collects the host facts and heartbeat metrics the agent
// reports to the control plane.
package stats

import (
	"context"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Options configures a Collector.
type Options struct {
	Engine docker.Engine
	// StateDir selects the filesystem whose usage is reported as disk usage.
	StateDir string
	// HTTPPort and HTTPSPort are the host ports the proxy will bind; they are
	// probed for Facts.Port80Free / Port443Free.
	HTTPPort  int
	HTTPSPort int
	// ProxyStatus supplies the proxy health for heartbeats.
	ProxyStatus func() agentprotocol.ProxyHealth
	// TailnetStatus supplies the observed tailnet state for heartbeats (nil result = off).
	TailnetStatus func() *agentprotocol.TailnetStatus
	// TunnelStatus supplies the observed Cloudflare Tunnel connector for heartbeats (nil result = none).
	TunnelStatus func(ctx context.Context) *agentprotocol.TunnelStatus
	// AcmeStatuses supplies the observed DNS-01 wildcard and domain certificates for heartbeats.
	AcmeStatuses func() []agentprotocol.AcmeCertStatus
	// MeshStatus supplies the observed mesh nodes for heartbeats (nil result = the agent manages none).
	MeshStatus func(ctx context.Context) *agentprotocol.MeshStatus
	// EndpointStatus supplies the observed instance service listeners for heartbeats.
	EndpointStatus func() []agentprotocol.EndpointStatus
	// GeoStatus supplies the observed GeoIP database for heartbeats (nil result = geolocation off).
	GeoStatus func() *agentprotocol.GeoStatus
	// ComposeVersion probes `docker compose version --short` (compose.Runner's
	// Version); nil means the agent has no compose runner. It is called once,
	// on the first Facts call.
	ComposeVersion func(ctx context.Context) (string, error)
	// BuildxVersion probes the docker buildx plugin (build.BuildxVersion); nil
	// means the agent cannot build and push, and an error that the plugin is
	// missing. CapBuildPush is advertised when it succeeds. Probed once.
	BuildxVersion func(ctx context.Context) (string, error)
	// AgentContainer names the container the agent runs in (empty: the
	// hostname); the host helper of the emulation probe needs its image.
	AgentContainer string
	// Emulation replaces the binfmt_misc probe (tests): the foreign platforms
	// and whether the answer is final.
	Emulation func(ctx context.Context) ([]string, bool)
}

// Collector gathers facts and heartbeat samples.
type Collector struct {
	opt Options

	portsOnce sync.Once
	port80    bool
	port443   bool

	mu            sync.Mutex
	dockerVersion string

	composeOnce    sync.Once
	composeVersion string // "" when the plugin is absent or the probe failed

	buildxOnce sync.Once
	buildx     bool // the buildx plugin works: CapBuildPush

	emuMu   sync.Mutex
	emuDone bool
	emu     []string
}

// New returns a Collector and primes the CPU sampler so the first heartbeat has
// a meaningful delta.
func New(opt Options) *Collector {
	if opt.HTTPPort == 0 {
		opt.HTTPPort = 80
	}
	if opt.HTTPSPort == 0 {
		opt.HTTPSPort = 443
	}
	_, _ = cpu.Percent(0, false)
	return &Collector{opt: opt}
}

// probePorts checks once whether the proxy ports are free. It runs on the
// first Facts call, which happens before the proxy is bootstrapped, so the answer
// reflects the host and not our own proxy. Later calls reuse it.
func (c *Collector) probePorts() {
	c.portsOnce.Do(func() {
		c.port80 = portFree(c.opt.HTTPPort)
		c.port443 = portFree(c.opt.HTTPSPort)
	})
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func (c *Collector) diskPath() string {
	if c.opt.StateDir != "" {
		if _, err := os.Stat(c.opt.StateDir); err == nil {
			return c.opt.StateDir
		}
	}
	return "/"
}

// Facts returns the host capabilities. Docker-derived fields are best effort:
// when the daemon cannot be queried they stay empty.
func (c *Collector) Facts(ctx context.Context) agentprotocol.Facts {
	c.probePorts()
	f := agentprotocol.Facts{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUs:        runtime.NumCPU(),
		Port80Free:  c.port80,
		Port443Free: c.port443,
	}
	if hi, err := host.InfoWithContext(ctx); err == nil {
		if hi.Platform != "" {
			f.OS = hi.Platform
			if hi.PlatformVersion != "" {
				f.OS += " " + hi.PlatformVersion
			}
		}
		f.Kernel = hi.KernelVersion
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil && n > 0 {
		f.CPUs = n
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		f.MemTotal = vm.Total
	}
	if du, err := disk.UsageWithContext(ctx, c.diskPath()); err == nil {
		f.DiskTotal = du.Total
	}
	if c.opt.Engine != nil {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if info, err := c.opt.Engine.Info(dctx); err == nil {
			f.DockerVersion = info.ServerVersion
			f.CgroupVersion = info.CgroupVersion
			if f.Kernel == "" {
				f.Kernel = info.KernelVersion
			}
			c.setDockerVersion(info.ServerVersion)
		}
	}
	f.Capabilities = []string{agentprotocol.CapProxyV2, agentprotocol.CapServiceTailnet, agentprotocol.CapDNS01, agentprotocol.CapDNS01Lego, agentprotocol.CapTunnel, agentprotocol.CapDockerCleanup}
	f.Capabilities = append(f.Capabilities, agentprotocol.CapHostHardening)
	f.Capabilities = append(f.Capabilities, agentprotocol.CapHostPatching)
	f.Capabilities = append(f.Capabilities, agentprotocol.CapCancelDeploy, agentprotocol.CapDecommission)
	f.Capabilities = append(f.Capabilities, agentprotocol.CapMesh, agentprotocol.CapReplication, agentprotocol.CapProxyPorts)
	f.Capabilities = append(f.Capabilities, agentprotocol.CapBuildOptions, agentprotocol.CapUptime)
	// Several processes per service is pure engine behaviour, so every agent (a build-only one included) advertises it.
	f.Capabilities = append(f.Capabilities, agentprotocol.CapProcesses)
	if c.opt.EndpointStatus != nil { // a localproxy manager is wired: this agent can carry instance tunnels
		f.Capabilities = append(f.Capabilities, agentprotocol.CapInstanceTunnel)
	}
	if systemdHost() {
		f.Capabilities = append(f.Capabilities, agentprotocol.CapUpdateControlPlane)
	}
	if v := c.probeCompose(ctx); v != "" {
		f.ComposeVersion = v
		if compose.AtLeastMin(v) {
			f.Capabilities = append(f.Capabilities, agentprotocol.CapCompose)
		}
	}
	if c.probeBuildx(ctx) {
		f.Capabilities = append(f.Capabilities, agentprotocol.CapBuildPush)
		f.Emulation = c.emulation(ctx)
	}
	f.HostTailscale = DetectHostTailscale()
	f.HostSysctls = hostSysctls()
	f.TunDevice = TunDevice()
	return f
}

// probeBuildx reports once whether the docker buildx plugin works.
func (c *Collector) probeBuildx(ctx context.Context) bool {
	c.buildxOnce.Do(func() {
		if c.opt.BuildxVersion == nil {
			return
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err := c.opt.BuildxVersion(pctx)
		c.buildx = err == nil
	})
	return c.buildx
}

// probeCompose asks the compose plugin for its version once and caches the
// answer: a host without the plugin (or an unreadable answer) reports "".
func (c *Collector) probeCompose(ctx context.Context) string {
	c.composeOnce.Do(func() {
		if c.opt.ComposeVersion == nil {
			return
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if v, err := c.opt.ComposeVersion(pctx); err == nil {
			c.composeVersion = strings.TrimSpace(v)
		}
	})
	return c.composeVersion
}

// tailscaleCGNAT is the range Tailscale assigns node addresses from.
var tailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")

// DetectHostTailscale reports whether a host interface carries a Tailscale
// address (100.64.0.0/10), meaning the host itself is on a tailnet.
func DetectHostTailscale() *agentprotocol.HostTailscale {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return &agentprotocol.HostTailscale{}
	}
	return hostTailscaleFrom(addrs)
}

func hostTailscaleFrom(addrs []net.Addr) *agentprotocol.HostTailscale {
	out := &agentprotocol.HostTailscale{}
	for _, a := range addrs {
		var ip netip.Addr
		switch v := a.(type) {
		case *net.IPNet:
			ip, _ = netip.AddrFromSlice(v.IP)
		case *net.IPAddr:
			ip, _ = netip.AddrFromSlice(v.IP)
		}
		if ip = ip.Unmap(); ip.IsValid() && tailscaleCGNAT.Contains(ip) {
			out.Detected = true
			out.IPs = append(out.IPs, ip.String())
		}
	}
	return out
}

func (c *Collector) setDockerVersion(v string) {
	c.mu.Lock()
	c.dockerVersion = v
	c.mu.Unlock()
}

// Heartbeat samples current host load. Failures of individual probes leave the
// corresponding fields at their last known or zero value.
func (c *Collector) Heartbeat(ctx context.Context) agentprotocol.Heartbeat {
	hb := agentprotocol.Heartbeat{Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyStarting}}
	if pct, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pct) > 0 {
		hb.CPUPercent = pct[0]
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		hb.MemUsed, hb.MemTotal = vm.Used, vm.Total
	}
	if du, err := disk.UsageWithContext(ctx, c.diskPath()); err == nil {
		hb.DiskUsed, hb.DiskTotal = du.Used, du.Total
	}
	if c.opt.Engine != nil {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if v, err := c.opt.Engine.ServerVersion(dctx); err == nil {
			c.setDockerVersion(v)
		}
		cancel()
	}
	c.mu.Lock()
	hb.DockerVersion = c.dockerVersion
	c.mu.Unlock()
	if c.opt.ProxyStatus != nil {
		hb.Proxy = c.opt.ProxyStatus()
	}
	if c.opt.TailnetStatus != nil {
		hb.Tailnet = c.opt.TailnetStatus()
	}
	if c.opt.TunnelStatus != nil {
		tctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		hb.Tunnel = c.opt.TunnelStatus(tctx)
		cancel()
	}
	if c.opt.AcmeStatuses != nil {
		hb.AcmeCerts = c.opt.AcmeStatuses()
	}
	if c.opt.MeshStatus != nil {
		mctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		hb.Mesh = c.opt.MeshStatus(mctx)
		cancel()
	}
	if c.opt.EndpointStatus != nil {
		hb.Endpoints = c.opt.EndpointStatus()
	}
	if c.opt.GeoStatus != nil {
		hb.Geo = c.opt.GeoStatus()
	}
	return hb
}
