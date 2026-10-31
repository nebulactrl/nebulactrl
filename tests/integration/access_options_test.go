//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// valkeyPing authenticates against a Valkey server and PINGs it over a raw RESP
// connection. It returns the server's two replies joined by a space ("OK PONG"),
// or an error when the connection is closed or refused first.
func valkeyPing(addr, password string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c, "*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n*1\r\n$4\r\nPING\r\n", len(password), password); err != nil {
		return "", err
	}
	r := bufio.NewReader(c)
	var out []string
	for range 2 {
		line, err := r.ReadString('\n')
		if err != nil {
			return strings.Join(out, " "), err
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "+")))
	}
	return strings.Join(out, " "), nil
}

// setPublicPort PUTs the database's public port rules.
func (s *Stack) setPublicPort(dbID string, body map[string]any) Resp {
	s.t.Helper()
	return s.Do("PUT", "/services/"+dbID+"/database/public-port", body)
}

// requireRefused asserts that the proxy listens on addr but closes the
// connection of this client before the database answers (an IP rule at work, not
// a closed port).
func requireRefused(t *testing.T, addr, password, why string) {
	t.Helper()
	require.NoError(t, waitTCP(context.Background(), addr), "the proxy must be listening on %s (%s)", addr, why)
	for range 3 {
		got, err := valkeyPing(addr, password)
		require.Errorf(t, err, "%s: the connection must be closed by the proxy, got %q", why, got)
		require.NotContains(t, got, "PONG", why)
		time.Sleep(300 * time.Millisecond)
	}
}

func requireConnects(t *testing.T, addr, password, why string) {
	t.Helper()
	require.Truef(t, eventually(30*time.Second, 500*time.Millisecond, func() bool {
		got, err := valkeyPing(addr, password)
		return err == nil && got == "OK PONG"
	}), "%s: expected the database to answer on %s", why, addr)
}

// TestPublicDatabasePortAndDockerOptions covers the network and container
// quick wins end to end on real Docker:
//
//   - a managed Valkey gets a public port with a mandatory allow-list; a client
//     the list excludes is closed by the proxy, one it includes connects, and a
//     deny list beats even a confirmed 0.0.0.0/0 allow;
//   - a service with shmSize, capAdd NET_BIND_SERVICE, an init process, a stop
//     signal, a ulimit, a tmpfs and a label shows every one in `docker inspect`.
func TestPublicDatabasePortAndDockerOptions(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	s := ExclusiveStack(t)

	kv := s.createDB("cache", "valkey", "9")
	opts := s.NewService(SvcSpec{
		Name: "optsapp", Image: app.AppImage, Vars: map[string]string{"VERSION": "opts-v1"},
		Config: map[string]any{"docker": map[string]any{
			"capAdd":     []string{"net_bind_service"},
			"shmSizeMb":  192,
			"init":       true,
			"stopSignal": "SIGQUIT",
			"labels":     map[string]string{"com.example.team": "payments"},
			"ulimits":    []map[string]any{{"name": "nofile", "soft": 2048, "hard": 4096}},
			"tmpfs":      []map[string]any{{"path": "/scratch", "sizeMb": 16, "mode": "1777"}},
			"extraHosts": []string{"legacy.internal:10.9.8.7"},
		}},
	})
	s.deployStaged()

	// ---- Docker options reach the container ----
	cs := s.RunningContainers(opts.ID)
	require.Len(t, cs, 1)
	out, err := sh(context.Background(), "", "docker", "inspect", cs[0].ID)
	require.NoError(t, err)
	var inspect []struct {
		Config struct {
			Labels     map[string]string
			StopSignal string
		}
		HostConfig struct {
			ShmSize    int64
			CapAdd     []string
			Init       *bool
			Tmpfs      map[string]string
			ExtraHosts []string
			Ulimits    []struct {
				Name       string
				Soft, Hard int64
			}
			Privileged  bool
			NetworkMode string
			PidMode     string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(out), &inspect), out)
	require.Len(t, inspect, 1)
	hc := inspect[0].HostConfig
	require.Equal(t, int64(192*1024*1024), hc.ShmSize)
	// Docker reports capabilities with the CAP_ prefix.
	require.Len(t, hc.CapAdd, 1)
	require.Equal(t, "NET_BIND_SERVICE", strings.TrimPrefix(hc.CapAdd[0], "CAP_"))
	require.NotNil(t, hc.Init)
	require.True(t, *hc.Init)
	require.Equal(t, "SIGQUIT", inspect[0].Config.StopSignal)
	require.Equal(t, "payments", inspect[0].Config.Labels["com.example.team"])
	require.Equal(t, opts.ID, inspect[0].Config.Labels["nebulactrl.service"], "managed labels stay in charge")
	require.Equal(t, "size=16m,mode=1777", hc.Tmpfs["/scratch"])
	require.Contains(t, hc.ExtraHosts, "legacy.internal:10.9.8.7")
	require.Len(t, hc.Ulimits, 1)
	require.Equal(t, "nofile", hc.Ulimits[0].Name)
	require.EqualValues(t, 2048, hc.Ulimits[0].Soft)
	require.EqualValues(t, 4096, hc.Ulimits[0].Hard)
	require.False(t, hc.Privileged)
	require.NotEqual(t, "host", hc.NetworkMode)
	require.NotEqual(t, "host", hc.PidMode)

	// Forbidden options are refused and nothing is staged.
	before := s.serviceETag(opts.ID)
	r := s.Do("PATCH", "/services/"+opts.ID, map[string]any{"config": map[string]any{
		"image": app.AppImage, "port": 8080,
		"docker": map[string]any{"capAdd": []string{"SYS_MODULE"}},
	}}, "If-Match", before)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	r = s.Do("PATCH", "/services/"+opts.ID, map[string]any{"config": map[string]any{
		"image": app.AppImage, "port": 8080,
		"docker": map[string]any{"capAdd": []string{"SYS_ADMIN"}},
	}}, "If-Match", before)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "requires_unrestricted_server")
	require.Equal(t, before, s.serviceETag(opts.ID))

	// ---- public database port ----
	rr := s.Do("POST", "/services/"+kv.ID+"/database/reveal", map[string]any{"key": "REDIS_URL"})
	require.Equal(t, http.StatusOK, rr.Status, string(rr.Raw))
	u, err := url.Parse(rr.JSON().Str("value"))
	require.NoError(t, err)
	password, _ := u.User.Password()
	require.NotEmpty(t, password)

	listen, err := freePort()
	require.NoError(t, err)
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(listen))

	// The allow-list is mandatory, and a range that admits everyone needs the slug typed.
	r = s.setPublicPort(kv.ID, map[string]any{"listenPort": listen, "allowCidrs": []string{}})
	require.GreaterOrEqual(t, r.Status, 400, string(r.Raw))
	r = s.setPublicPort(kv.ID, map[string]any{"listenPort": listen, "allowCidrs": []string{"0.0.0.0/0"}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "open_confirmation_required")
	r = s.setPublicPort(kv.ID, map[string]any{"listenPort": 443, "allowCidrs": []string{"203.0.113.0/24"}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))

	// An allow-list that excludes this client: the proxy listens and hangs up.
	r = s.setPublicPort(kv.ID, map[string]any{"listenPort": listen, "allowCidrs": []string{"203.0.113.0/24"}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	pp := r.JSON()
	require.True(t, pp.Bool("enabled"))
	require.True(t, pp.Bool("staged"))
	require.True(t, pp.Bool("newEntrypoint"), "a new port recreates the proxy: the UI must be able to warn")
	routeID := pp.Str("routeId")
	require.NotEmpty(t, routeID)
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, routeID)
	s.servicesMu.Unlock()

	// Staged: nothing listens yet.
	_, derr := net.DialTimeout("tcp", addr, time.Second)
	require.Error(t, derr, "the port is not published before the route is deployed")

	plan := s.Do("POST", "/services/"+routeID+"/deployments/plan", map[string]any{})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.Contains(t, strings.ToLower(string(plan.Raw)), "restart", "the plan warns about the proxy restart: %s", plan.Raw)

	s.DeployOK(routeID)
	requireRefused(t, addr, password, "client 127.0.0.1 is outside 203.0.113.0/24")

	// An allow-list that includes the client (the Docker gateway is private).
	r = s.setPublicPort(kv.ID, map[string]any{"listenPort": listen, "allowCidrs": []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "100.64.0.0/10", "198.18.0.0/15", "fc00::/7", "::1/128"}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, routeID, r.JSON().Str("routeId"), "the same route is edited in place")
	s.DeployOK(routeID)
	requireConnects(t, addr, password, "client is inside the private ranges")

	// The external connection string needs the server's public address.
	r = s.Do("POST", "/services/"+kv.ID+"/database/public-port/connection", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "no_public_address")
	r = s.Do("PATCH", "/servers/"+s.ServerID, map[string]any{"publicIp": "127.0.0.1"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = s.Do("POST", "/services/"+kv.ID+"/database/public-port/connection", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	ext, err := url.Parse(r.JSON().Str("value"))
	require.NoError(t, err)
	require.Equal(t, addr, ext.Host)
	extPW, _ := ext.User.Password()
	require.Equal(t, password, extPW)
	got, err := valkeyPing(ext.Host, extPW)
	require.NoError(t, err)
	require.Equal(t, "OK PONG", got, "the revealed connection string works as printed")
	var revealed int
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_events WHERE action = 'secret.reveal' AND resource_id = $1 AND metadata->>'public' = 'true'", kv.ID).Scan(&revealed))
	require.Equal(t, 1, revealed)

	// Deny first: a confirmed 0.0.0.0/0 allow still loses to the deny list.
	r = s.setPublicPort(kv.ID, map[string]any{"listenPort": listen, "allowCidrs": []string{"0.0.0.0/0"}, "confirmOpen": kv.Slug,
		"denyCidrs": []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "100.64.0.0/10", "198.18.0.0/15"}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("openToAll"))
	s.DeployOK(routeID)
	requireRefused(t, addr, password, "the deny list covers the client although the allow-list admits everyone")

	// Closing the port removes the listener's rules at once.
	r = s.Do("DELETE", "/services/"+kv.ID+"/database/public-port", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.False(t, r.JSON().Bool("enabled"))
	require.Truef(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return false
		}
		return true
	}), "the public port must close after the route is deleted")
}
