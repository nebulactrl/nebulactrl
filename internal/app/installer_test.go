package app_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	installdeploy "github.com/nebulactrl/nebulactrl/deploy/install"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/localserver"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestAcceptWorkloadsGuardsPlacement(t *testing.T) {
	h := newHarness(t)
	h.setup()
	rig := h.fleetRig()
	a := h.newOnlineServer("a", 8*gib, 1*gib)
	b := h.newOnlineServer("b", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	onB := h.newServiceOn(env, "on-b", b, nil)
	onA := h.newServiceOn(env, "on-a", a, nil)
	rig.deployAndSucceed(onB, b)

	// Accept workloads is on by default.
	r := h.do("GET", "/servers/"+b, "", nil)
	require.Equal(t, true, r.JSON()["acceptWorkloads"])
	require.Equal(t, false, r.JSON()["controlPlane"])

	r = h.do("PATCH", "/servers/"+b, "", map[string]any{"acceptWorkloads": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["acceptWorkloads"])

	// Creating a service there is refused.
	r = h.do("POST", "/environments/"+env+"/services", "", map[string]any{
		"name": "new", "sourceType": "image", "serverId": b, "config": map[string]any{"image": "traefik/whoami:latest", "port": 80},
	})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "server_not_accepting_workloads", r.JSON()["code"])
	require.Contains(t, string(r.Raw), "does not accept workloads")

	// So is moving one onto it.
	g := h.do("GET", "/services/"+onA, "", nil)
	gen := int64(g.JSON()["generation"].(float64))
	r = h.do("PATCH", "/services/"+onA, "", map[string]any{"serverId": b}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "server_not_accepting_workloads", r.JSON()["code"])

	// What already runs there keeps deploying.
	r = h.do("POST", "/services/"+onB+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))

	// The drain planner does not pick it as a target either.
	r = h.do("POST", "/servers/"+a+"/drain/plan", "", map[string]any{"targetServerId": b})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "server_not_accepting_workloads", r.JSON()["code"])

	// Turning it back on makes it schedulable again; the change is audited.
	r = h.do("PATCH", "/servers/"+b, "", map[string]any{"acceptWorkloads": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = h.do("PATCH", "/services/"+onA, "", map[string]any{"serverId": b}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'server.update' AND metadata ? 'acceptWorkloads'`).Scan(&n))
	require.Equal(t, 2, n)
}

func TestSetupTokenGuardsFirstRun(t *testing.T) {
	h := newHarnessCfg(t, func(c *config.Config) { c.Installer.SetupToken = "tok-4f9c1d" })
	body := map[string]any{"email": "admin@example.com", "password": testPassword, "name": "Admin"}

	r := h.do("GET", "/setup", "", nil)
	require.Equal(t, true, r.JSON()["needsSetup"])
	require.Equal(t, true, r.JSON()["tokenRequired"])

	r = h.do("POST", "/setup", "", body)
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	require.Equal(t, "setup_token_invalid", r.JSON()["code"])

	body["setupToken"] = "wrong"
	r = h.do("POST", "/setup", "", body)
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))

	body["setupToken"] = "tok-4f9c1d"
	r = h.do("POST", "/setup", "", body)
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	// Once set up, the token no longer matters and no longer shows as required.
	r = h.do("GET", "/setup", "", nil)
	require.Equal(t, false, r.JSON()["needsSetup"])
	require.Equal(t, false, r.JSON()["tokenRequired"])
	delete(body, "setupToken")
	r = h.do("POST", "/setup", "", body)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
}

func TestSetupNeedsNoTokenByDefault(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/setup", "", nil)
	require.Equal(t, false, r.JSON()["tokenRequired"])
	h.setup()
}

// The control-plane host's agent enrolls before the admin account exists, and
// Setup adopts the team the installer's server already belongs to.
func TestLocalServerEnrollsBeforeSetupAndSetupAdoptsTheTeam(t *testing.T) {
	const token = "nct_enroll_installertesttokenaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	h := newHarnessCfg(t, func(c *config.Config) {
		c.Installer = config.Installer{LocalEnrollToken: token, LocalServerName: "cp-host", LocalServerIP: "203.0.113.9", ACMEEmail: "ops@example.com"}
	})
	ctx := context.Background()
	ens := localserver.New(h.app.Pool, h.app.Bus, h.app.Config, nil)
	require.True(t, ens.Enabled())

	done, err := ens.Ensure(ctx)
	require.NoError(t, err)
	require.False(t, done, "still waiting for the agent")
	done, err = ens.Ensure(ctx) // idempotent
	require.NoError(t, err)
	require.False(t, done)

	q := db.New(h.app.Pool)
	teams, err := h.app.Pool.Query(ctx, `SELECT count(*) FROM workspaces`)
	require.NoError(t, err)
	require.True(t, teams.Next())
	var nTeams int
	require.NoError(t, teams.Scan(&nTeams))
	teams.Close()
	require.Equal(t, 1, nTeams)
	srv, err := q.GetControlPlaneServer(ctx)
	require.NoError(t, err)
	require.Equal(t, "cp-host", srv.Name)
	require.Equal(t, "pending", srv.Status)
	require.Equal(t, "ops@example.com", *srv.AcmeEmail)
	require.Equal(t, "203.0.113.9", *srv.PublicIp)
	require.True(t, srv.AcceptWorkloads)

	// The agent redeems the token with no user in the system.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	enroll := func() *http.Response {
		b, _ := json.Marshal(agentprotocol.EnrollRequest{Token: token, PublicKey: pub, Hostname: "cp-host", Facts: agentprotocol.Facts{}})
		res, err := http.Post(h.srv.URL+"/agent/v1/enroll", "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		return res
	}
	res := enroll()
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()
	res = enroll() // single use
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	res.Body.Close()

	// The admin then creates the account: the team the server belongs to is theirs.
	r := h.do("POST", "/setup", "", map[string]any{"email": "admin@example.com", "password": testPassword, "name": "Admin", "teamName": "Acme"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	var teamID uuid.UUID
	var teamName string
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT id, name FROM workspaces`).Scan(&teamID, &teamName))
	require.Equal(t, "Acme", teamName, "setup renames the bootstrap team")
	require.Equal(t, teamID, srv.WorkspaceID)
	var role string
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT role FROM memberships WHERE workspace_id = $1`, teamID).Scan(&role))
	require.Equal(t, "owner", role)

	// The admin sees the server, flagged as the control-plane host.
	r = h.do("GET", "/servers/"+srv.ID.String(), "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["controlPlane"])
	require.Equal(t, "cp-host", r.JSON()["name"])
}

func TestLocalServerIsNotRecreatedAfterRevoke(t *testing.T) {
	const token = "nct_enroll_installertesttokenbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	h := newHarnessCfg(t, func(c *config.Config) { c.Installer.LocalEnrollToken = token })
	ctx := context.Background()
	ens := localserver.New(h.app.Pool, h.app.Bus, h.app.Config, nil)
	_, err := ens.Ensure(ctx)
	require.NoError(t, err)
	srv, err := db.New(h.app.Pool).GetControlPlaneServer(ctx)
	require.NoError(t, err)
	require.Equal(t, localserver.DefaultName, srv.Name)

	_, err = h.app.Pool.Exec(ctx, `UPDATE servers SET status = 'revoked' WHERE id = $1`, srv.ID)
	require.NoError(t, err)
	done, err := ens.Ensure(ctx)
	require.NoError(t, err)
	require.True(t, done)
	var n int
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT count(*) FROM servers`).Scan(&n))
	require.Equal(t, 1, n, "a revoked control-plane server is never recreated")
}

func TestDashboardSystemRouteOnTheControlPlaneServerOnly(t *testing.T) {
	h := newHarnessCfg(t, func(c *config.Config) {
		c.Installer.LocalEnrollToken = "nct_enroll_ccccccccccccccccccccccccccccccccccccccccccccc"
	})
	h.setup()
	systemroute.Set(systemroute.Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"})
	t.Cleanup(func() { systemroute.Set(systemroute.Config{}) })
	ctx := context.Background()

	cp := h.newOnlineServer("cp", 8*gib, 1*gib)
	other := h.newOnlineServer("other", 8*gib, 1*gib)
	_, err := h.app.Pool.Exec(ctx, `UPDATE servers SET is_control_plane = true WHERE id = $1`, uuid.MustParse(cp))
	require.NoError(t, err)

	q := db.New(h.app.Pool)
	routes, err := domains.ServerRoutes(ctx, q, uuid.MustParse(cp), uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	rt := routes[0]
	require.Equal(t, systemroute.ServiceID, rt.ServiceID)
	require.Equal(t, []string{"nebula.example.com"}, rt.Hosts)
	require.Equal(t, []string{"nebulactrl:8080"}, rt.Upstreams)
	require.Equal(t, agentprotocol.RouteKindExternal, rt.Kind)
	require.Equal(t, agentprotocol.TLSAuto, rt.Domains[0].TLSMode)

	routes, err = domains.ServerRoutes(ctx, q, uuid.MustParse(other), uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, routes, "only the control-plane host serves the dashboard")

	// The hostname is the dashboard's: no service domain may take it.
	env := h.newEnv("Shop")
	svc := h.newServiceOn(env, "web", cp, nil)
	g := h.do("GET", "/services/"+svc, "", nil)
	gen := int64(g.JSON()["generation"].(float64))
	r := h.do("POST", "/services/"+svc+"/domains", "", map[string]any{"hostname": "nebula.example.com"}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "domain_conflict", r.JSON()["code"])
}

func TestControlPlaneServesTheInstaller(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/install.sh", "/uninstall.sh", "/install-agent.sh"} {
		res, err := http.Get(h.srv.URL + p)
		require.NoError(t, err)
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		res.Body.Close()
		require.Equal(t, http.StatusOK, res.StatusCode, p)
		require.Contains(t, res.Header.Get("Content-Type"), "text/x-shellscript", p)
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"), p)
		require.True(t, bytes.HasPrefix(buf.Bytes(), []byte("#!/")), p)
		if p == "/install.sh" {
			require.Contains(t, buf.String(), "NC_ASSET_EOF_0", "assets are inlined")
			require.NotContains(t, buf.String(), "NEBULACTRL_SECRET_KEY=base64", "no instance secret")
			// apps/site serves `go run ./tests/install/assemble`, which prints Script():
			// the same bytes, so the public /install.sh and this one never differ.
			require.Equal(t, installdeploy.Script(), buf.String(), "served bytes are installdeploy.Script()")
		}
	}
}
