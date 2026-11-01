//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const stackCanary = "sekret-canary-12345"

// removeStackArtifacts removes the compose project's containers, network and
// volumes. The harness removes containers by service label only; compose also
// creates a project network and named volumes.
func removeStackArtifacts(serviceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	project := agentprotocol.StackProject(serviceID)
	filter := "label=com.docker.compose.project=" + project
	if ids, err := sh(ctx, "", "docker", "ps", "-aq", "--filter", filter); err == nil && ids != "" {
		_, _ = sh(ctx, "", "docker", append([]string{"rm", "-f"}, strings.Fields(ids)...)...)
	}
	if vols, err := sh(ctx, "", "docker", "volume", "ls", "-q", "--filter", filter); err == nil && vols != "" {
		_, _ = sh(ctx, "", "docker", append([]string{"volume", "rm", "-f"}, strings.Fields(vols)...)...)
	}
	if nets, err := sh(ctx, "", "docker", "network", "ls", "-q", "--filter", filter); err == nil && nets != "" {
		_, _ = sh(ctx, "", "docker", append([]string{"network", "rm"}, strings.Fields(nets)...)...)
	}
}

// createStack creates a stack service in the stack's environment.
func (s *Stack) createStack(name, compose string, extraCfg map[string]any) Resp {
	s.t.Helper()
	cfg := map[string]any{"stack": map[string]any{"compose": compose, "waitSeconds": 120}, "stopGraceSeconds": 5}
	for k, v := range extraCfg {
		cfg[k] = v
	}
	return s.Do("POST", "/environments/"+s.EnvID+"/services", map[string]any{
		"name": name, "kind": "stack", "sourceType": "compose", "serverId": s.ServerID, "config": cfg,
	})
}

// trackStack registers a created stack for cleanup and returns its ID.
func (s *Stack) trackStack(r Resp) string {
	s.t.Helper()
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	id := r.JSON().Str("id")
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, id)
	s.servicesMu.Unlock()
	s.t.Cleanup(func() { removeStackArtifacts(id) })
	return id
}

func componentNames(cs []J) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Str("name"))
	}
	return out
}

func jsonItems(v any) []J {
	arr, _ := v.([]any)
	out := make([]J, 0, len(arr))
	for _, e := range arr {
		m, _ := e.(map[string]any)
		out = append(out, J(m))
	}
	return out
}

// A two-service Compose stack runs through the API: the web component
// publishes a host port in the file (never bound on the host) and is routed by
// Traefik through a domain; the cache component is reached by the web
// component over the stack's own network; a variable change is staged, deployed
// (recreate) and rolled back; the secret canary never reaches the logs, the
// command payload or the agent's stack files.
func TestComposeStackDeploysRoutesAndRollsBack(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	host := s.Host("shop")

	compose := fmt.Sprintf(`
services:
  web:
    image: %s
    ports: ["18080:8080"]
    environment:
      VERSION: ${APP_VERSION}
      REDIS_URL: redis://cache:6379/0
      CANARY: ${STACK_TOKEN}
    depends_on:
      cache:
        condition: service_started
  cache:
    image: valkey/valkey:8
    volumes:
      - cachedata:/data
volumes:
  cachedata:
`, app.AppImage)

	// The live check lists the components and finds nothing to refuse.
	chk := s.Do("POST", "/environments/"+s.EnvID+"/stacks/check", map[string]any{"compose": compose, "serverId": s.ServerID})
	require.Equal(t, http.StatusOK, chk.Status, string(chk.Raw))
	cj := chk.JSON()
	require.Equal(t, []string{"cache", "web"}, componentNames(jsonItems(cj["components"])))
	require.Empty(t, jsonItems(cj["violations"]), string(chk.Raw))

	r := s.createStack("shop", compose, nil)
	id := s.trackStack(r)
	require.Equal(t, []string{"cache", "web"}, componentNames(jsonItems(r.JSON().Obj("config").Obj("stack")["components"])))
	require.Equal(t, "stack", r.JSON().Str("kind"))
	s.SetVars(id, map[string]string{"APP_VERSION": "stack-v1"}, map[string]string{"STACK_TOKEN": stackCanary})

	// Domains name a component and a container port.
	noComp := s.Do("POST", "/services/"+id+"/domains", map[string]any{"hostname": host})
	require.Equal(t, http.StatusUnprocessableEntity, noComp.Status, string(noComp.Raw))
	dom := s.Do("POST", "/services/"+id+"/domains", map[string]any{"hostname": host, "component": "web", "targetPort": 8080})
	require.Equal(t, http.StatusCreated, dom.Status, string(dom.Raw))
	require.Equal(t, "web", dom.JSON().Str("component"))

	// Deploy: recreate, one deployment for the whole stack.
	d1 := s.DeployOK(id)
	require.Equal(t, "recreate", d1.Str("strategy"))
	require.NotEmpty(t, d1.Str("releaseId"))
	client := NewProxyClient(host)
	client.WaitBody(t, "/", "stack-v1", 90*time.Second)
	client.WaitBody(t, "/kv", "kv ok", 60*time.Second) // web reaches cache by name inside the stack

	// Containers: fixed names, labels, no host port, everything under the compose project.
	short := agentprotocol.Short(id)
	byComponent := map[string]string{}
	for _, c := range s.RunningContainers(id) {
		byComponent[c.Labels[agentprotocol.LabelComponent]] = c.Name
		require.Equal(t, "true", c.Labels[agentprotocol.LabelManaged])
		require.Equal(t, d1.Str("id"), c.Labels[agentprotocol.LabelDeployment])
	}
	require.Equal(t, map[string]string{"web": "nc-" + short + "-web", "cache": "nc-" + short + "-cache"}, byComponent)
	bindings, err := sh(context.Background(), "", "docker", "inspect", "-f", "{{json .HostConfig.PortBindings}}", "nc-"+short+"-web")
	require.NoError(t, err)
	require.Contains(t, []string{"{}", "null"}, bindings, "the file's ports: must not be published on the host")
	_, dialErr := net.DialTimeout("tcp", "127.0.0.1:18080", time.Second)
	require.Error(t, dialErr, "host port 18080 must not be bound")

	// Observed state: one replica per component.
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		obs := s.Service(id).Obj("observed")
		reps := jsonItems(obs["replicas"])
		if len(reps) != 2 {
			return false
		}
		for _, rp := range reps {
			if rp.Str("state") != "running" || rp.Str("component") == "" {
				return false
			}
		}
		return obs.Str("state") == "running"
	}), "observed: %v", s.Service(id).Obj("observed"))

	// The secret canary is nowhere the control plane or the agent persists.
	require.NotContains(t, s.DeploymentLogs(d1.Str("id")), stackCanary)
	var payloads string
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		"SELECT coalesce(string_agg(payload::text, ' '), '') FROM agent_commands WHERE deployment_id = $1", d1.Str("id")).Scan(&payloads))
	require.NotEmpty(t, payloads)
	require.NotContains(t, payloads, stackCanary)
	if s.StateDir != "" {
		files, _ := filepath.Glob(filepath.Join(s.StateDir, agentprotocol.StackDirName, short, "*", "*"))
		require.NotEmpty(t, files, "the agent keeps the stack's files under %s", s.StateDir)
		for _, f := range files {
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			require.NotContains(t, string(raw), stackCanary, f)
		}
	}

	// Metrics: the stack is summed, the last sample lists each component.
	require.True(t, eventually(90*time.Second, 3*time.Second, func() bool {
		m := s.Do("GET", "/services/"+id+"/metrics?range=1h", nil)
		if m.Status != http.StatusOK {
			return false
		}
		pts := jsonItems(m.JSON()["points"])
		return len(pts) > 0 && len(jsonItems(pts[len(pts)-1]["replicas"])) == 2
	}), "metrics never listed both components")

	// A variable change is staged; the deploy recreates the stack with it.
	s.SetVars(id, map[string]string{"APP_VERSION": "stack-v2"}, nil)
	require.True(t, s.Service(id).Bool("hasStagedChanges"))
	d2 := s.DeployOK(id)
	require.NotEqual(t, d1.Str("id"), d2.Str("id"))
	client.WaitBody(t, "/", "stack-v2", 90*time.Second)
	client.WaitBody(t, "/kv", "kv ok", 60*time.Second)

	// Rollback restores the previous release (compose file, variables and the recorded images); data is not touched.
	rb := s.Do("POST", "/deployments/"+d1.Str("id")+"/rollback", map[string]any{})
	require.Equal(t, http.StatusAccepted, rb.Status, string(rb.Raw))
	rd := s.WaitDeployment(rb.JSON().Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", rd.Str("status"), "rollback: %v\n%s", rd, s.DeploymentLogs(rd.Str("id")))
	client.WaitBody(t, "/", "stack-v1", 90*time.Second)

	// Restart keeps the stack serving.
	rs := s.Do("POST", "/services/"+id+"/restart", map[string]any{})
	require.Equal(t, http.StatusAccepted, rs.Status, string(rs.Raw))
	client.WaitBody(t, "/", "stack-v1", 90*time.Second)

	// Delete removes containers, network and (asked to) volumes.
	slug := s.Service(id).Str("slug")
	del := s.Do("DELETE", "/services/"+id+"?confirm="+slug+"&deleteVolumes=true", nil)
	require.Equal(t, http.StatusNoContent, del.Status, string(del.Raw))
	require.True(t, eventually(90*time.Second, time.Second, func() bool {
		out, err := sh(context.Background(), "", "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=nc-"+short)
		return err == nil && out == ""
	}), "stack containers were not removed")
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		out, err := sh(context.Background(), "", "docker", "volume", "ls", "-q", "--filter", "label=com.docker.compose.project=nc-"+short)
		return err == nil && out == ""
	}), "stack volumes were not removed with deleteVolumes")
}

// The allow-list refuses what hands a container the host, Unrestricted
// (instance admin, per server, audited) lifts exactly that, and the flag is read
// again at every deploy.
func TestComposeStackAllowListAndUnrestricted(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	s := ExclusiveStack(t)

	privileged := fmt.Sprintf(`
services:
  web:
    image: %s
    privileged: true
`, app.AppImage)
	socket := fmt.Sprintf(`
services:
  web:
    image: %s
    volumes: ["/var/run/docker.sock:/var/run/docker.sock"]
`, app.AppImage)
	build := `
services:
  web:
    build: .
`
	violationKeys := func(raw Resp) []string {
		var keys []string
		for _, v := range jsonItems(raw.JSON().Obj("details")["violations"]) {
			keys = append(keys, v.Str("key"))
		}
		return keys
	}

	// The live check and create both refuse, with the violation list.
	chk := s.Do("POST", "/environments/"+s.EnvID+"/stacks/check", map[string]any{"compose": privileged, "serverId": s.ServerID})
	require.Equal(t, http.StatusOK, chk.Status, string(chk.Raw))
	vs := jsonItems(chk.JSON()["violations"])
	require.Len(t, vs, 1, string(chk.Raw))
	require.Equal(t, "privileged", vs[0].Str("key"))
	require.Equal(t, "restricted", vs[0].Str("kind"))
	require.False(t, chk.JSON().Bool("unrestricted"))

	for name, file := range map[string]string{"privileged": privileged, "socket": socket} {
		r := s.createStack(name, file, nil)
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "%s: %s", name, r.Raw)
		require.Equal(t, "invalid_compose", r.JSON().Str("code"), string(r.Raw))
		require.NotEmpty(t, violationKeys(r), string(r.Raw))
	}
	r := s.createStack("build", build, nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))

	// The server's Compose card: plugin found, restricted by default.
	st := s.Do("GET", "/servers/"+s.ServerID+"/unrestricted", nil)
	require.Equal(t, http.StatusOK, st.Status, string(st.Raw))
	require.False(t, st.JSON().Bool("unrestricted"))
	require.True(t, st.JSON().Bool("available"), string(st.Raw))
	require.NotEmpty(t, st.JSON().Str("composeVersion"))

	// Enabling needs the server's name typed.
	name := s.Do("GET", "/servers/"+s.ServerID, nil).JSON().Str("name")
	bad := s.Do("PUT", "/servers/"+s.ServerID+"/unrestricted", map[string]any{"unrestricted": true, "confirm": "nope"})
	require.Equal(t, http.StatusUnprocessableEntity, bad.Status, string(bad.Raw))
	on := s.Do("PUT", "/servers/"+s.ServerID+"/unrestricted", map[string]any{"unrestricted": true, "confirm": name})
	require.Equal(t, http.StatusOK, on.Status, string(on.Raw))
	require.True(t, on.JSON().Bool("unrestricted"))
	var audited int
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_events WHERE action = 'server.unrestricted.enable' AND resource_id = $1", s.ServerID).Scan(&audited))
	require.Equal(t, 1, audited)

	// Now the privileged stack is accepted and runs privileged; build: stays unsupported.
	require.Equal(t, http.StatusUnprocessableEntity, s.createStack("build2", build, nil).Status)
	id := s.trackStack(s.createStack("priv", privileged, nil))
	s.DeployOK(id)
	short := agentprotocol.Short(id)
	priv, err := sh(context.Background(), "", "docker", "inspect", "-f", "{{.HostConfig.Privileged}}", "nc-"+short+"-web")
	require.NoError(t, err)
	require.Equal(t, "true", priv)

	// Switching it off does not stop the stack, but the next deploy of a stack that needs it fails
	// before anything changes, and the running containers stay.
	off := s.Do("PUT", "/servers/"+s.ServerID+"/unrestricted", map[string]any{"unrestricted": false})
	require.Equal(t, http.StatusOK, off.Status, string(off.Raw))
	require.False(t, off.JSON().Bool("unrestricted"))
	plan := s.Do("POST", "/services/"+id+"/deployments/plan", map[string]any{})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	s.SetVars(id, map[string]string{"TOUCH": "1"}, nil)
	dep := s.Do("POST", "/services/"+id+"/deployments", map[string]any{})
	if dep.Status == http.StatusAccepted {
		d := s.WaitDeployment(dep.JSON().Str("id"), 2*time.Minute)
		require.Equal(t, "failed", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
		require.Contains(t, d.Str("statusMessage"), "privileged")
	} else {
		require.Equal(t, http.StatusUnprocessableEntity, dep.Status, string(dep.Raw))
		require.Contains(t, string(dep.Raw), "privileged")
	}
	require.Len(t, s.RunningContainers(id), 1, "the running stack is untouched")
}
