package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestDockerOptionsConfigValidation(t *testing.T) {
	base := Config{Image: "nginx:1", Port: 80}
	ok := base
	ok.Docker = &agentprotocol.DockerOptions{CapAdd: []string{"cap_net_bind_service", "NET_BIND_SERVICE"}}
	require.Equal(t, []string{"NET_BIND_SERVICE"}, ok.WithDefaults().Docker.CapAdd, "the same capability twice is stored once")

	ok.Docker = &agentprotocol.DockerOptions{CapAdd: []string{"cap_net_bind_service"}, ShmSizeMB: 64, Init: true}
	c := ok.WithDefaults()
	require.NoError(t, c.Validate(SourceImage))
	require.Equal(t, []string{"NET_BIND_SERVICE"}, c.Docker.CapAdd)

	empty := base
	empty.Docker = &agentprotocol.DockerOptions{}
	require.Nil(t, empty.WithDefaults().Docker, "an empty set is no set, so it never shows as a staged change")

	for name, mut := range map[string]func(*Config){
		"capability": func(c *Config) { c.Docker = &agentprotocol.DockerOptions{CapAdd: []string{"SYS_MODULE"}} },
		"nproc": func(c *Config) {
			c.Docker = &agentprotocol.DockerOptions{Ulimits: []agentprotocol.Ulimit{{Name: "nproc", Soft: 1, Hard: 1}}}
		},
		"sysctl": func(c *Config) {
			c.Docker = &agentprotocol.DockerOptions{Sysctls: map[string]string{"vm.max_map_count": "262144"}}
		},
		"dns name": func(c *Config) { c.Docker = &agentprotocol.DockerOptions{DNS: []string{"dns.example.com"}} },
		"user":     func(c *Config) { c.Docker = &agentprotocol.DockerOptions{User: "a b"} },
		"reserved label": func(c *Config) {
			c.Docker = &agentprotocol.DockerOptions{Labels: map[string]string{"nebulactrl.x": "1"}}
		},
		"bad stop signal": func(c *Config) { c.Docker = &agentprotocol.DockerOptions{StopSignal: "SIGFOO"} },
		"tmpfs vs file": func(c *Config) {
			c.Docker = &agentprotocol.DockerOptions{Tmpfs: []agentprotocol.TmpfsMount{{Path: "/etc/app.conf"}}}
			c.Files = []FileMount{{Path: "/etc/app.conf", Content: "x"}}
		},
	} {
		c := base
		mut(&c)
		require.ErrorIs(t, c.WithDefaults().Validate(SourceImage), ErrInvalidConfig, name)
	}
}

func TestDockerOptionsDiffShowsAsStagedChange(t *testing.T) {
	a := Config{Image: "nginx:1", Port: 80, Docker: &agentprotocol.DockerOptions{ShmSizeMB: 64}}.WithDefaults()
	b := Config{Image: "nginx:1", Port: 80, Docker: &agentprotocol.DockerOptions{ShmSizeMB: 128, Init: true}}.WithDefaults()
	var fields []string
	for _, d := range DiffConfig("config", a, b) {
		fields = append(fields, d.Field)
	}
	require.ElementsMatch(t, []string{"config.docker.shmSizeMb", "config.docker.init"}, fields)
	require.Empty(t, DiffConfig("config", a, a))
}

func TestRouteConfigPublicDatabaseAndDenyValidation(t *testing.T) {
	id := uuid.New()
	svc := &id
	rc := func(mut func(*RouteConfig)) *RouteConfig {
		r := &RouteConfig{
			Protocol: agentprotocol.ProtoTCP, ListenPort: 15432, AllowCIDRs: []string{"10.0.0.0/8"},
			Upstreams: []RouteUpstream{{ServiceID: svc, Port: 5432}}, PublicDatabase: true,
		}
		mut(r)
		return r
	}
	require.NoError(t, rc(func(*RouteConfig) {}).Validate())
	require.NoError(t, rc(func(r *RouteConfig) { r.DenyCIDRs = []string{"10.9.0.0/16"} }).Validate())
	require.NoError(t, rc(func(r *RouteConfig) { r.AllowCIDRs, r.OpenToInternet = []string{"0.0.0.0/0"}, true }).Validate())

	for name, r := range map[string]*RouteConfig{
		"no allow-list":       rc(func(r *RouteConfig) { r.AllowCIDRs, r.OpenToInternet = nil, true }),
		"open without typed":  rc(func(r *RouteConfig) { r.AllowCIDRs = []string{"::/0"} }),
		"deny covers allow":   rc(func(r *RouteConfig) { r.DenyCIDRs = []string{"10.0.0.0/8"} }),
		"external upstream":   rc(func(r *RouteConfig) { r.Upstreams = []RouteUpstream{{Address: "10.1.1.1:5432"}} }),
		"two upstreams":       rc(func(r *RouteConfig) { r.Upstreams = append(r.Upstreams, RouteUpstream{ServiceID: svc, Port: 1}) }),
		"udp":                 rc(func(r *RouteConfig) { r.Protocol = agentprotocol.ProtoUDP }),
		"http":                rc(func(r *RouteConfig) { r.Protocol, r.ListenPort, r.AllowCIDRs = agentprotocol.ProtoHTTP, 0, nil }),
		"bad deny":            rc(func(r *RouteConfig) { r.DenyCIDRs = []string{"x"} }),
		"too many deny rules": rc(func(r *RouteConfig) { r.DenyCIDRs = make([]string, 201) }),
	} {
		require.ErrorIs(t, r.Validate(), ErrInvalidConfig, name)
	}
}

func TestCheckDockerPlacement(t *testing.T) {
	code := func(err error) (int, string) {
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae)
		return ae.Status, ae.Code
	}
	for name, o := range map[string]*agentprotocol.DockerOptions{
		"devices":    {Devices: []agentprotocol.DeviceMapping{{HostPath: "/dev/dri/renderD128"}}},
		"gpus":       {GPUs: &agentprotocol.GPURequest{Count: -1, Driver: "nvidia"}},
		"runtime":    {Runtime: "runsc"},
		"privileged": {Privileged: true},
		"pidHost":    {PidHost: true},
		"seccomp":    {SeccompUnconfined: true},
		"apparmor":   {ApparmorUnconfined: true},
		"NET_ADMIN":  {CapAdd: []string{"NET_ADMIN"}},
		"SYS_ADMIN":  {CapAdd: []string{"sys_admin"}},
	} {
		cfg := Config{Image: "x:1", Docker: o}.WithDefaults()
		require.NoError(t, cfg.validateDocker(), "%s: well-formed, only the placement matters", name)
		require.NoError(t, CheckDockerPlacement(cfg, true), name)
		err := CheckDockerPlacement(cfg, false)
		status, c := code(err)
		require.Equal(t, http.StatusUnprocessableEntity, status, name)
		require.Equal(t, "requires_unrestricted_server", c, name)
		require.Equal(t, CodeRequiresUnrestricted, c)
	}
	// The ungated tier and a service without options pass on a restricted server.
	for name, o := range map[string]*agentprotocol.DockerOptions{
		"none": nil, "hardening": {CapDrop: []string{"ALL"}, NoNewPrivileges: true, ReadOnlyRootfs: true, PidsLimit: 100},
		"sysctls": {Sysctls: map[string]string{"net.core.somaxconn": "1024"}}, "caps": {CapAdd: []string{"NET_BIND_SERVICE"}},
	} {
		require.NoError(t, CheckDockerPlacement(Config{Image: "x:1", Docker: o}, false), name)
	}
}

func TestCheckDockerFilesVersusReadOnlyRootfs(t *testing.T) {
	cfg := Config{Image: "x:1", Docker: &agentprotocol.DockerOptions{ReadOnlyRootfs: true}, Files: []FileMount{{Path: "/etc/app.conf", Content: "x"}}}
	require.ErrorIs(t, CheckDockerFiles(cfg, nil), ErrInvalidConfig)
	require.ErrorIs(t, CheckDockerFiles(cfg, []db.Volume{{MountPath: "/data"}}), ErrInvalidConfig)
	require.NoError(t, CheckDockerFiles(cfg, []db.Volume{{MountPath: "/etc"}}), "the file is on the volume")
	cfg.Docker.ReadOnlyRootfs = false
	require.NoError(t, CheckDockerFiles(cfg, nil), "a writable root takes files anywhere")
	require.NoError(t, CheckDockerFiles(Config{Image: "x:1", Files: cfg.Files}, nil))
}

func TestHostRequirementWarnings(t *testing.T) {
	cfg := Config{Image: "elasticsearch:8", Docker: &agentprotocol.DockerOptions{HostSysctls: map[string]int64{"vm.max_map_count": 262144, "fs.file-max": 100}}}
	facts := func(v map[string]int64) *db.Server {
		raw, err := json.Marshal(agentprotocol.Facts{OS: "debian", HostSysctls: v})
		require.NoError(t, err)
		return &db.Server{Name: "edge", Facts: raw}
	}

	require.Empty(t, HostRequirementWarnings(Config{Image: "x:1"}, nil), "no requirements, no warnings")
	require.Empty(t, HostRequirementWarnings(cfg, facts(map[string]int64{"vm.max_map_count": 262144, "fs.file-max": 1000})), "satisfied")

	w := HostRequirementWarnings(cfg, facts(map[string]int64{"vm.max_map_count": 65530, "fs.file-max": 1000}))
	require.Len(t, w, 1)
	require.Contains(t, w[0], "needs vm.max_map_count >= 262144")
	require.Contains(t, w[0], "edge has 65530")
	require.Contains(t, w[0], "sysctl -w vm.max_map_count=262144")
	require.Contains(t, w[0], "/etc/sysctl.d/")

	// An agent that did not report the value, or no server yet: a warning that says it cannot tell.
	for name, srv := range map[string]*db.Server{"unreported": facts(nil), "no server": nil} {
		w = HostRequirementWarnings(cfg, srv)
		require.Len(t, w, 2, name)
		require.Contains(t, w[0], "has not reported", name) // sorted: fs.file-max first
	}
	require.False(t, errors.Is(nil, ErrInvalidConfig))
}

func TestDockerOptionsCatalogLists(t *testing.T) {
	require.NotContains(t, agentprotocol.SafeCapabilities, "NET_ADMIN")
	require.Equal(t, []string{"NET_ADMIN", "SYS_ADMIN"}, agentprotocol.UnrestrictedCapabilities)
	for _, k := range agentprotocol.AllowedSysctls {
		require.NotContains(t, agentprotocol.HostSysctlKeys, k, "a key is either container-namespaced or a host requirement")
	}
}
