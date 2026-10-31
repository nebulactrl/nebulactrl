package docker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
)

// SDKEngine is the moby-client backed Engine.
type SDKEngine struct {
	cli *client.Client
}

var _ Engine = (*SDKEngine)(nil)

// NewSDKEngine connects to the daemon described by the standard DOCKER_HOST /
// DOCKER_API_VERSION environment variables and negotiates the API version.
func NewSDKEngine() (*SDKEngine, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &SDKEngine{cli: cli}, nil
}

// Close releases the SDK client.
func (e *SDKEngine) Close() error { return e.cli.Close() }

func wrap(err error, what string) error {
	if err == nil {
		return nil
	}
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %s: %v", ErrNotFound, what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (e *SDKEngine) Ping(ctx context.Context) error {
	_, err := e.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	return wrap(err, "docker ping")
}

func (e *SDKEngine) ServerVersion(ctx context.Context) (string, error) {
	v, err := e.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return "", wrap(err, "docker version")
	}
	return v.Version, nil
}

func (e *SDKEngine) Info(ctx context.Context) (Info, error) {
	res, err := e.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, wrap(err, "docker info")
	}
	i := res.Info
	mem := uint64(0)
	if i.MemTotal > 0 {
		mem = uint64(i.MemTotal)
	}
	return Info{
		ServerVersion: i.ServerVersion,
		CgroupVersion: i.CgroupVersion,
		KernelVersion: i.KernelVersion,
		NCPU:          i.NCPU,
		MemTotal:      mem,
	}, nil
}

func (e *SDKEngine) EnsureNetwork(ctx context.Context, name string, labels map[string]string, opts ...NetworkOption) error {
	return e.ensureNetwork(ctx, name, labels, false, ResolveNetworkOptions(opts...))
}

func (e *SDKEngine) EnsureInternalNetwork(ctx context.Context, name string, labels map[string]string, opts ...NetworkOption) error {
	return e.ensureNetwork(ctx, name, labels, true, ResolveNetworkOptions(opts...))
}

func (e *SDKEngine) ensureNetwork(ctx context.Context, name string, labels map[string]string, internal bool, o NetworkOptions) error {
	if _, err := e.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return wrap(err, "inspect network "+name)
	}
	create := client.NetworkCreateOptions{Driver: "bridge", Labels: labels, Internal: internal}
	if o.Subnet != "" {
		p, err := netip.ParsePrefix(o.Subnet)
		if err != nil {
			return fmt.Errorf("create network %s: invalid subnet %q: %w", name, o.Subnet, err)
		}
		create.IPAM = &network.IPAM{Config: []network.IPAMConfig{{Subnet: p}}}
	}
	_, err := e.cli.NetworkCreate(ctx, name, create)
	if err != nil && cerrdefs.IsConflict(err) && !IsPoolOverlap(err) { // raced with another creator
		return nil
	}
	return wrap(err, "create network "+name)
}

func (e *SDKEngine) NetworkDisconnect(ctx context.Context, net, ctr string) error {
	_, err := e.cli.NetworkDisconnect(ctx, net, client.NetworkDisconnectOptions{Container: ctr})
	if err == nil || cerrdefs.IsNotFound(err) {
		return nil
	}
	// "container X is not connected to network Y" is reported as a plain error.
	if strings.Contains(err.Error(), "is not connected") {
		return nil
	}
	return wrap(err, "disconnect "+ctr+" from "+net)
}

func (e *SDKEngine) NetworkList(ctx context.Context, prefix string, labels map[string]string) ([]NetworkInfo, error) {
	filters := client.Filters{}
	for k, v := range labels {
		filters = filters.Add("label", k+"="+v)
	}
	res, err := e.cli.NetworkList(ctx, client.NetworkListOptions{Filters: filters})
	if err != nil {
		return nil, wrap(err, "list networks")
	}
	var out []NetworkInfo
	for _, s := range res.Items {
		if !strings.HasPrefix(s.Name, prefix) {
			continue
		}
		insp, err := e.cli.NetworkInspect(ctx, s.ID, client.NetworkInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) { // removed between list and inspect
				continue
			}
			return nil, wrap(err, "inspect network "+s.Name)
		}
		ni := NetworkInfo{ID: s.ID, Name: s.Name, Internal: s.Internal, Labels: s.Labels, Created: s.Created}
		for id := range insp.Network.Containers {
			ni.Containers = append(ni.Containers, id)
		}
		for _, c := range insp.Network.IPAM.Config {
			if c.Subnet.IsValid() {
				ni.Subnets = append(ni.Subnets, c.Subnet.String())
			}
		}
		out = append(out, ni)
	}
	return out, nil
}

func (e *SDKEngine) NetworkRemove(ctx context.Context, name string) error {
	_, err := e.cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
	return wrap(err, "remove network "+name)
}

func (e *SDKEngine) EnsureVolume(ctx context.Context, name string, labels map[string]string) error {
	if _, err := e.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return wrap(err, "inspect volume "+name)
	}
	_, err := e.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels})
	return wrap(err, "create volume "+name)
}

func (e *SDKEngine) RemoveVolume(ctx context.Context, name string, force bool) error {
	_, err := e.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force})
	return wrap(err, "remove volume "+name)
}

func (e *SDKEngine) NetworkConnect(ctx context.Context, net, ctr string, aliases []string) error {
	return e.networkConnect(ctx, net, ctr, aliases, 0)
}

func (e *SDKEngine) networkConnect(ctx context.Context, net, ctr string, aliases []string, gwPriority int) error {
	_, err := e.cli.NetworkConnect(ctx, net, client.NetworkConnectOptions{
		Container:      ctr,
		EndpointConfig: &network.EndpointSettings{Aliases: aliases, GwPriority: e.gwPriority(gwPriority)},
	})
	return wrap(err, "connect "+ctr+" to "+net)
}

// GwPriorityAPIVersion is the first Engine API version (Docker 28) whose
// endpoint settings carry GwPriority.
const GwPriorityAPIVersion = "1.48"

// gwPriority returns want when the negotiated API version knows GwPriority and
// 0 (the unset value) otherwise: an older daemon picks the default gateway by
// network name instead, which the managed network names are chosen to make
// harmless.
func (e *SDKEngine) gwPriority(want int) int {
	if want != 0 && versions.GreaterThanOrEqualTo(e.cli.ClientVersion(), GwPriorityAPIVersion) {
		return want
	}
	return 0
}

func (e *SDKEngine) ImagePull(ctx context.Context, ref, registryAuth string, progress func(string)) error {
	rc, err := e.cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: registryAuth})
	if err != nil {
		return wrap(err, "pull "+ref)
	}
	defer rc.Close()
	for msg, err := range rc.JSONMessages(ctx) {
		if err != nil {
			return wrap(err, "pull "+ref)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if progress != nil && msg.Status != "" && msg.Progress == nil {
			line := msg.Status
			if msg.ID != "" {
				line = msg.ID + ": " + line
			}
			progress(line)
		}
	}
	return nil
}

func (e *SDKEngine) ImageInspect(ctx context.Context, ref string) (ImageInfo, error) {
	res, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageInfo{}, wrap(err, "inspect image "+ref)
	}
	info := ImageInfo{ID: res.ID, RepoTags: res.RepoTags, RepoDigests: res.RepoDigests}
	if res.Config != nil {
		info.Labels = res.Config.Labels
		info.Entrypoint = res.Config.Entrypoint
		info.HasHealthcheck = res.Config.Healthcheck != nil && len(res.Config.Healthcheck.Test) > 0 &&
			res.Config.Healthcheck.Test[0] != "NONE"
	}
	if t, err := time.Parse(time.RFC3339Nano, res.Created); err == nil {
		info.Created = t
	}
	info.LastTagged = res.Metadata.LastTagTime
	return info, nil
}

func (e *SDKEngine) ImageRemove(ctx context.Context, ref string, force bool) error {
	_, err := e.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: force, PruneChildren: true})
	return wrap(err, "remove image "+ref)
}

func portOf(p PortPublish) (network.Port, error) {
	proto := network.IPProtocol(p.Proto)
	if proto == "" {
		proto = network.TCP
	}
	if p.ContainerPort <= 0 || p.ContainerPort > 65535 {
		return network.Port{}, fmt.Errorf("invalid container port %d", p.ContainerPort)
	}
	port, ok := network.PortFrom(uint16(p.ContainerPort), proto)
	if !ok {
		return network.Port{}, fmt.Errorf("invalid protocol %q", p.Proto)
	}
	return port, nil
}

func (e *SDKEngine) ContainerCreate(ctx context.Context, spec ContainerSpec) (string, error) {
	cfg := &container.Config{
		Image:       spec.Image,
		Env:         spec.Env,
		Cmd:         spec.Cmd,
		Entrypoint:  spec.Entrypoint,
		Labels:      spec.Labels,
		StopTimeout: spec.StopTimeout,
	}
	if h := spec.Healthcheck; h != nil {
		cfg.Healthcheck = &container.HealthConfig{
			Test: h.Test, Interval: h.Interval, Timeout: h.Timeout, StartPeriod: h.StartPeriod, Retries: h.Retries,
		}
	}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(spec.RestartPolicy)},
		Resources:     container.Resources{Memory: spec.MemoryBytes, NanoCPUs: spec.NanoCPUs},
	}
	if spec.LogConfig != nil {
		host.LogConfig = container.LogConfig{Type: spec.LogConfig.Type, Config: spec.LogConfig.Config}
	}
	host.ExtraHosts = spec.ExtraHosts
	host.CapAdd = spec.CapAdd
	host.CapDrop = spec.CapDrop
	host.SecurityOpt = spec.SecurityOpt
	host.ShmSize = spec.ShmSize
	host.Tmpfs = spec.Tmpfs
	host.ReadonlyRootfs = spec.ReadonlyRootfs
	if spec.Init {
		host.Init = &spec.Init
	}
	for _, u := range spec.Ulimits {
		host.Ulimits = append(host.Ulimits, &container.Ulimit{Name: u.Name, Soft: u.Soft, Hard: u.Hard})
	}
	cfg.StopSignal = spec.StopSignal
	for _, d := range spec.DNS {
		ip, err := netip.ParseAddr(d)
		if err != nil {
			return "", fmt.Errorf("invalid dns server %q: %w", d, err)
		}
		host.DNS = append(host.DNS, ip)
	}
	for _, d := range spec.Devices {
		inside, perms := d.ContainerPath, d.Permissions
		if inside == "" {
			inside = d.HostPath
		}
		if perms == "" {
			perms = "rwm"
		}
		host.Devices = append(host.Devices, container.DeviceMapping{PathOnHost: d.HostPath, PathInContainer: inside, CgroupPermissions: perms})
	}
	if g := spec.GPUs; g != nil {
		// Like `docker run --gpus`: "gpu" plus the requested driver capabilities, as one AND list.
		caps := []string{"gpu"}
		for _, c := range g.Capabilities {
			if c != "gpu" {
				caps = append(caps, c)
			}
		}
		host.DeviceRequests = append(host.DeviceRequests, container.DeviceRequest{Driver: g.Driver, Count: g.Count, Capabilities: [][]string{caps}})
	}
	host.Runtime = spec.Runtime
	host.Privileged = spec.Privileged
	host.PidMode = container.PidMode(spec.PidMode)
	host.GroupAdd = spec.GroupAdd
	host.Sysctls = spec.Sysctls
	if spec.PidsLimit > 0 {
		limit := spec.PidsLimit
		host.PidsLimit = &limit
	}
	cfg.Hostname = spec.Hostname
	cfg.User = spec.User
	if len(spec.Ports) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		host.PortBindings = network.PortMap{}
		for _, p := range spec.Ports {
			port, err := portOf(p)
			if err != nil {
				return "", err
			}
			b := network.PortBinding{HostPort: strconv.Itoa(p.HostPort)}
			if p.HostIP != "" {
				ip, err := netip.ParseAddr(p.HostIP)
				if err != nil {
					return "", fmt.Errorf("invalid host ip %q: %w", p.HostIP, err)
				}
				b.HostIP = ip
			}
			cfg.ExposedPorts[port] = struct{}{}
			host.PortBindings[port] = append(host.PortBindings[port], b)
		}
	}
	for _, m := range spec.Mounts {
		if m.HostPath != "" {
			host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeBind, Source: m.HostPath, Target: m.Target, ReadOnly: m.ReadOnly})
			continue
		}
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Volume, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	var netCfg *network.NetworkingConfig
	switch {
	case spec.NetworkMode != "":
		if len(spec.Networks) > 0 {
			return "", fmt.Errorf("container %s: networkMode %q cannot be combined with networks", spec.Name, spec.NetworkMode)
		}
		host.NetworkMode = container.NetworkMode(spec.NetworkMode)
	case len(spec.Networks) > 0:
		// Only the primary network is attached at create time: attaching several
		// endpoints at once needs API 1.44 (Docker 25); the rest are connected
		// below, before the container starts.
		host.NetworkMode = container.NetworkMode(spec.Networks[0].Network)
		netCfg = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			spec.Networks[0].Network: {Aliases: spec.Networks[0].Aliases, GwPriority: e.gwPriority(spec.Networks[0].GwPriority)},
		}}
	}
	res, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: cfg, HostConfig: host, NetworkingConfig: netCfg, Name: spec.Name,
	})
	if err != nil {
		return "", wrap(err, "create container "+spec.Name)
	}
	for _, n := range spec.Networks[min(1, len(spec.Networks)):] {
		if err := e.networkConnect(ctx, n.Network, res.ID, n.Aliases, n.GwPriority); err != nil {
			_, _ = e.cli.ContainerRemove(context.WithoutCancel(ctx), res.ID, client.ContainerRemoveOptions{Force: true})
			return "", err
		}
	}
	return res.ID, nil
}

func (e *SDKEngine) ContainerStart(ctx context.Context, id string) error {
	_, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return wrap(err, "start container "+id)
}

func (e *SDKEngine) ContainerPause(ctx context.Context, id string) error {
	_, err := e.cli.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return wrap(err, "pause container "+id)
}

func (e *SDKEngine) ContainerUnpause(ctx context.Context, id string) error {
	_, err := e.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return wrap(err, "unpause container "+id)
}

func (e *SDKEngine) ContainerStop(ctx context.Context, id string, timeout *time.Duration) error {
	opts := client.ContainerStopOptions{}
	if timeout != nil {
		s := int(timeout.Round(time.Second) / time.Second)
		opts.Timeout = &s
	}
	_, err := e.cli.ContainerStop(ctx, id, opts)
	return wrap(err, "stop container "+id)
}

func (e *SDKEngine) ContainerRemove(ctx context.Context, id string, force, removeVolumes bool) error {
	_, err := e.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: force, RemoveVolumes: removeVolumes})
	return wrap(err, "remove container "+id)
}

func (e *SDKEngine) ContainerInspect(ctx context.Context, idOrName string) (ContainerInfo, error) {
	res, err := e.cli.ContainerInspect(ctx, idOrName, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, wrap(err, "inspect container "+idOrName)
	}
	return infoFromInspect(res.Container), nil
}

func (e *SDKEngine) ContainerList(ctx context.Context, f ListFilter) ([]ContainerInfo, error) {
	filters := client.Filters{}
	for k, v := range f.Labels {
		if v == "" {
			filters = filters.Add("label", k)
		} else {
			filters = filters.Add("label", k+"="+v)
		}
	}
	for _, n := range f.Names {
		filters = filters.Add("name", n)
	}
	res, err := e.cli.ContainerList(ctx, client.ContainerListOptions{All: f.All, Filters: filters})
	if err != nil {
		return nil, wrap(err, "list containers")
	}
	out := make([]ContainerInfo, 0, len(res.Items))
	for _, s := range res.Items {
		ci, err := e.ContainerInspect(ctx, s.ID)
		if err != nil {
			if errors.Is(err, ErrNotFound) { // removed between list and inspect
				continue
			}
			return nil, err
		}
		out = append(out, ci)
	}
	return out, nil
}

func infoFromInspect(c container.InspectResponse) ContainerInfo {
	ci := ContainerInfo{
		ID:           c.ID,
		Name:         strings.TrimPrefix(c.Name, "/"),
		ImageID:      c.Image,
		RestartCount: c.RestartCount,
		Networks:     map[string]ContainerNetwork{},
	}
	if c.Config != nil {
		ci.Image = c.Config.Image
		ci.Labels = c.Config.Labels
		ci.Env = c.Config.Env
		ci.Cmd = c.Config.Cmd
	}
	if t, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
		ci.Created = t
	}
	if s := c.State; s != nil {
		ci.State = string(s.Status)
		ci.Running = s.Running
		ci.Restarting = s.Restarting
		ci.OOMKilled = s.OOMKilled
		ci.ExitCode = s.ExitCode
		if t, err := time.Parse(time.RFC3339Nano, s.StartedAt); err == nil {
			ci.StartedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, s.FinishedAt); err == nil && t.Year() > 1 {
			ci.FinishedAt = t
		}
		if s.Health != nil {
			ci.Health = string(s.Health.Status)
		}
	}
	if h := c.HostConfig; h != nil {
		ci.RestartMode = string(h.RestartPolicy.Name)
		ci.LogConfig = LogConfig{Type: h.LogConfig.Type, Config: h.LogConfig.Config}
		ci.NetworkMode = string(h.NetworkMode)
		ci.ExtraHosts = h.ExtraHosts
		for _, d := range h.DNS {
			ci.DNS = append(ci.DNS, d.String())
		}
		for port, bindings := range h.PortBindings {
			for _, b := range bindings {
				hp, _ := strconv.Atoi(b.HostPort)
				p := PortPublish{HostPort: hp, ContainerPort: int(port.Num()), Proto: string(port.Proto())}
				if b.HostIP.IsValid() && !b.HostIP.IsUnspecified() {
					p.HostIP = b.HostIP.String()
				}
				ci.Ports = append(ci.Ports, p)
			}
		}
	}
	for _, m := range c.Mounts {
		ci.Mounts = append(ci.Mounts, MountInfo{
			Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW,
		})
	}
	if ns := c.NetworkSettings; ns != nil {
		for name, ep := range ns.Networks {
			if ep == nil {
				continue
			}
			cn := ContainerNetwork{Aliases: ep.Aliases}
			if ep.IPAddress.IsValid() {
				cn.IP = ep.IPAddress.String()
			}
			ci.Networks[name] = cn
		}
	}
	return ci
}
