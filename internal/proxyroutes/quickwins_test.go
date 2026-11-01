package proxyroutes_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestTCPRouteDenyList(t *testing.T) {
	f := newFixture(t)
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:5432"}}
	allow, deny := []string{"10.0.0.0/8"}, []string{"10.1.0.0/16"}
	port := 25432
	in := proxyroutes.CreateInput{Name: "pg", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: &port, AllowCIDRs: &allow, DenyCIDRs: &deny}}
	v, err := f.routes.Create(f.admin, f.env.ID, in)
	require.NoError(t, err)
	rc := f.reloadService(v.ID)
	require.Equal(t, deny, services.DecodeConfig(rc.Config).Route.DenyCIDRs)

	// The built table carries the deny list to the agent.
	f.markDeployed(rc)
	table, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, [16]byte{})
	require.NoError(t, err)
	var found bool
	for _, r := range table {
		if r.ServiceID == v.ID.String() {
			found = true
			require.Equal(t, allow, r.AllowCIDRs)
			require.Equal(t, deny, r.DenyCIDRs)
		}
	}
	require.True(t, found, "route in the table: %v", table)

	for name, mut := range map[string]func(*proxyroutes.RouteFields){
		"bad deny cidr":     func(r *proxyroutes.RouteFields) { r.DenyCIDRs = &[]string{"nope"} },
		"deny covers allow": func(r *proxyroutes.RouteFields) { r.DenyCIDRs = &[]string{"10.0.0.0/7"} },
	} {
		bad := in
		bad.Name = name
		bad.RouteFields.ListenPort = new(26000 + len(name))
		mut(&bad.RouteFields)
		_, err := f.routes.Create(f.admin, f.env.ID, bad)
		_, code := apiCode(t, err)
		require.Equal(t, "invalid_config", code, name)
	}

	// A deny-only tcp route still needs the explicit open-to-internet.
	denyOnly := proxyroutes.CreateInput{Name: "deny only", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(27000), DenyCIDRs: &deny}}
	_, err = f.routes.Create(f.admin, f.env.ID, denyOnly)
	_, code := apiCode(t, err)
	require.Equal(t, "invalid_config", code)
	denyOnly.OpenToInternet = new(true)
	_, err = f.routes.Create(f.admin, f.env.ID, denyOnly)
	require.NoError(t, err)

	// UDP and HTTP routes cannot enforce a deny list.
	udp := proxyroutes.CreateInput{Name: "dns", ServerID: f.server.ID, Protocol: "udp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(5353), DenyCIDRs: &deny, OpenToInternet: new(true)}}
	_, err = f.routes.Create(f.admin, f.env.ID, udp)
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_config", code)
	httpRoute := proxyroutes.CreateInput{Name: "web", ServerID: f.server.ID, Protocol: "http",
		Domains:     []proxyroutes.DomainInput{{Hostname: "lan.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, DenyCIDRs: &deny}}
	_, err = f.routes.Create(f.admin, f.env.ID, httpRoute)
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_config", code)
}

func TestDomainAccessDenyList(t *testing.T) {
	f := newFixture(t)
	web := f.web("site")
	d, err := f.doms.Create(f.admin, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	gen := f.reloadService(web.ID).Generation

	// Deny-only is a valid rule set; so is allow + deny.
	for _, a := range []agentprotocol.Access{
		{DenyCIDRs: []string{"203.0.113.0/24", "2001:db8::/32"}},
		{AllowCIDRs: []string{"10.0.0.0/8"}, DenyCIDRs: []string{"10.1.0.0/16"}},
	} {
		v, g, err := f.doms.Update(f.admin, d.ID, gen, domains.UpdateInput{Rule: domains.RuleInput{Options: &agentprotocol.RouteOptions{Access: &a}}})
		require.NoError(t, err)
		gen = g
		require.Equal(t, a.DenyCIDRs, v.Options.Access.DenyCIDRs)
	}

	for name, a := range map[string]agentprotocol.Access{
		"empty":             {},
		"bad deny":          {DenyCIDRs: []string{"nope"}},
		"deny covers allow": {AllowCIDRs: []string{"10.0.0.0/8"}, DenyCIDRs: []string{"10.0.0.0/8"}},
	} {
		_, _, err := f.doms.Update(f.admin, d.ID, gen, domains.UpdateInput{Rule: domains.RuleInput{Options: &agentprotocol.RouteOptions{Access: &a}}})
		status, code := apiCode(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, status, name)
		require.Equal(t, "invalid_options", code, name)
	}
}

func TestDockerOptionsConfigAndAdvancedCapabilities(t *testing.T) {
	f := newFixture(t)
	cfg := services.Config{Image: "nginx:1", Port: 80, Docker: &agentprotocol.DockerOptions{
		CapAdd: []string{"net_bind_service"}, ShmSizeMB: 128, Labels: map[string]string{"com.example.team": "a"},
		Ulimits: []agentprotocol.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}}, Init: true, StopSignal: "sigquit",
		Tmpfs: []agentprotocol.TmpfsMount{{Path: "/tmp/x", SizeMB: 16}},
	}}
	v, err := f.svcs.Create(f.admin, f.env.ID, services.CreateInput{Name: "web", SourceType: services.SourceImage, Config: cfg, ServerID: &f.server.ID})
	require.NoError(t, err)
	require.Equal(t, []string{"NET_BIND_SERVICE"}, v.Config.Docker.CapAdd, "capability names are normalized")
	require.Equal(t, "SIGQUIT", v.Config.Docker.StopSignal)

	// Changing an option stages the service.
	row := f.reloadService(v.ID)
	f.markDeployed(row)
	row = f.reloadService(v.ID)
	next := services.DecodeConfig(row.Config)
	next.Docker.ShmSizeMB = 256
	v2, err := f.svcs.Update(f.admin, v.ID, row.Generation, services.UpdateInput{Config: &next})
	require.NoError(t, err)
	require.Greater(t, v2.Generation, row.Generation)
	require.True(t, v2.HasStagedChanges)
	require.Equal(t, 256, v2.Config.Docker.ShmSizeMB)

	// Forbidden options are refused with a validation error.
	for name, o := range map[string]*agentprotocol.DockerOptions{
		"cap":       {CapAdd: []string{"SYS_MODULE"}},
		"label":     {Labels: map[string]string{"traefik.http.routers.x.rule": "Host(`a`)"}},
		"tmpfs":     {Tmpfs: []agentprotocol.TmpfsMount{{Path: "/dev"}}},
		"extrahost": {ExtraHosts: []string{"a:b"}},
	} {
		bad := services.Config{Image: "nginx:1", Port: 80, Docker: o}
		_, err := f.svcs.Create(f.admin, f.env.ID, services.CreateInput{Name: name, SourceType: services.SourceImage, Config: bad})
		status, code := apiCode(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, status, name)
		require.Equal(t, "invalid_config", code, name)
	}

	// NET_ADMIN / SYS_ADMIN need an unrestricted server (the instance admin's per-server switch).
	adv := services.Config{Image: "nginx:1", Port: 80, Docker: &agentprotocol.DockerOptions{CapAdd: []string{"NET_ADMIN"}}}
	_, err = f.svcs.Create(f.admin, f.env.ID, services.CreateInput{Name: "vpn", SourceType: services.SourceImage, Config: adv, ServerID: &f.server.ID})
	_, code := apiCode(t, err)
	require.Equal(t, "requires_unrestricted_server", code)

	// Without a server yet the placement is not known, so it is not refused; the catalog is public.
	pol, err := f.svcs.DockerOptionsPolicy(f.admin)
	require.NoError(t, err)
	require.Contains(t, pol.SafeCapabilities, "NET_BIND_SERVICE")
	require.NotContains(t, pol.SafeCapabilities, "SYS_ADMIN")
	require.Equal(t, []string{"NET_ADMIN", "SYS_ADMIN"}, pol.UnrestrictedCapabilities)

	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET unrestricted = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	vpn, err := f.svcs.Create(f.admin, f.env.ID, services.CreateInput{Name: "vpn", SourceType: services.SourceImage, Config: adv, ServerID: &f.server.ID})
	require.NoError(t, err)
	require.Equal(t, []string{"NET_ADMIN"}, vpn.Config.Docker.CapAdd)

	// Switching it off again blocks the next deploy's placement check, not the safe service.
	require.NoError(t, services.CheckDockerPlacement(services.DecodeConfig(f.reloadService(vpn.ID).Config), true))
	require.Error(t, services.CheckDockerPlacement(services.DecodeConfig(f.reloadService(vpn.ID).Config), false))
	require.NoError(t, services.CheckDockerPlacement(services.DecodeConfig(f.reloadService(v.ID).Config), false))

	// Moving a service that needs it to a restricted server is refused (422).
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "restricted"})
	require.NoError(t, err)
	row = f.reloadService(vpn.ID)
	_, err = f.svcs.Update(f.admin, vpn.ID, row.Generation, services.UpdateInput{ServerID: &other.ID})
	_, code = apiCode(t, err)
	require.Equal(t, "requires_unrestricted_server", code)
}
