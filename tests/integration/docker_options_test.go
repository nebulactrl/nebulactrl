//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// dockerInspect is the part of `docker inspect` the Docker options tests read.
type dockerInspect struct {
	Config struct {
		Hostname string
		User     string
	}
	HostConfig struct {
		ReadonlyRootfs bool
		CapAdd         []string
		CapDrop        []string
		SecurityOpt    []string
		PidsLimit      *int64
		Tmpfs          map[string]string
		Sysctls        map[string]string
		GroupAdd       []string
		DNS            []string
		Privileged     bool
		PidMode        string
		Devices        []struct{ PathOnHost, PathInContainer, CgroupPermissions string }
	}
}

func (s *Stack) inspectService(serviceID string) dockerInspect {
	s.t.Helper()
	cs := s.RunningContainers(serviceID)
	require.Len(s.t, cs, 1)
	out, err := sh(context.Background(), "", "docker", "inspect", cs[0].ID)
	require.NoError(s.t, err)
	var all []dockerInspect
	require.NoError(s.t, json.Unmarshal([]byte(out), &all), out)
	require.Len(s.t, all, 1)
	return all[0]
}

// TestHardeningDockerOptions deploys a service with a read-only root
// filesystem, a tmpfs, every capability dropped, a pids limit, a namespaced
// sysctl, a user and a DNS server, and checks each in `docker inspect`. The
// hostname defaults to the service slug. Then the unrestricted tier: refused
// on a restricted server (422 requires_unrestricted_server, nothing staged),
// accepted once an instance admin switched the server to unrestricted, and
// applied to the container (device, unconfined seccomp).
func TestHardeningDockerOptions(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	s := ExclusiveStack(t)

	hardened := s.NewService(SvcSpec{
		Name: "hardened", Image: app.AppImage, Vars: map[string]string{"VERSION": "hardened-v1"},
		Config: map[string]any{"docker": map[string]any{
			"readOnlyRootfs":  true,
			"tmpfs":           []map[string]any{{"path": "/tmp", "sizeMb": 8, "mode": "1777"}},
			"capDrop":         []string{"ALL"},
			"noNewPrivileges": true,
			"pidsLimit":       128,
			"sysctls":         map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
			"user":            "65534:65534",
			"groupAdd":        []string{"44"},
			"dns":             []string{"1.1.1.1"},
		}},
	})
	plain := s.NewService(SvcSpec{Name: "plain", Image: app.AppImage, Vars: map[string]string{"VERSION": "plain-v1"}})
	s.deployStaged()

	hc := s.inspectService(hardened.ID)
	require.True(t, hc.HostConfig.ReadonlyRootfs, "read-only root filesystem")
	require.Equal(t, "8m", sizeOf(hc.HostConfig.Tmpfs["/tmp"]), "tmpfs %v", hc.HostConfig.Tmpfs)
	require.Len(t, hc.HostConfig.CapDrop, 1)
	require.Equal(t, "ALL", strings.TrimPrefix(hc.HostConfig.CapDrop[0], "CAP_"))
	require.NotNil(t, hc.HostConfig.PidsLimit)
	require.EqualValues(t, 128, *hc.HostConfig.PidsLimit)
	require.Contains(t, hc.HostConfig.SecurityOpt, "no-new-privileges:true")
	require.Equal(t, "0", hc.HostConfig.Sysctls["net.ipv4.ip_unprivileged_port_start"])
	require.Equal(t, "65534:65534", hc.Config.User)
	require.Equal(t, []string{"44"}, hc.HostConfig.GroupAdd)
	require.Equal(t, []string{"1.1.1.1"}, hc.HostConfig.DNS)
	require.Equal(t, hardened.Slug, hc.Config.Hostname, "the hostname defaults to the service slug")
	require.False(t, hc.HostConfig.Privileged)

	pc := s.inspectService(plain.ID)
	require.Equal(t, plain.Slug, pc.Config.Hostname, "every service gets its slug as hostname, not the container ID")
	require.False(t, pc.HostConfig.ReadonlyRootfs)

	// ---- the unrestricted tier ----
	before := s.serviceETag(plain.ID)
	patch := func(docker map[string]any) Resp {
		return s.Do("PATCH", "/services/"+plain.ID, map[string]any{"config": map[string]any{
			"image": app.AppImage, "port": 8080, "docker": docker,
		}}, "If-Match", s.serviceETag(plain.ID))
	}
	for name, docker := range map[string]map[string]any{
		"devices":    {"devices": []map[string]any{{"hostPath": "/dev/null", "containerPath": "/dev/hostnull", "permissions": "rw"}}},
		"gpus":       {"gpus": map[string]any{"count": -1, "driver": "nvidia"}},
		"privileged": {"privileged": true},
		"pidHost":    {"pidHost": true},
		"seccomp":    {"seccompUnconfined": true},
		"NET_ADMIN":  {"capAdd": []string{"NET_ADMIN"}},
	} {
		r := patch(docker)
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "%s: %s", name, r.Raw)
		require.Contains(t, string(r.Raw), "requires_unrestricted_server", name)
	}
	require.Equal(t, before, s.serviceETag(plain.ID), "nothing was staged")

	name := s.Do("GET", "/servers/"+s.ServerID, nil).JSON().Str("name")
	on := s.Do("PUT", "/servers/"+s.ServerID+"/unrestricted", map[string]any{"unrestricted": true, "confirm": name})
	require.Equal(t, http.StatusOK, on.Status, string(on.Raw))
	require.True(t, on.JSON().Bool("unrestricted"))

	r := patch(map[string]any{
		"devices":           []map[string]any{{"hostPath": "/dev/null", "containerPath": "/dev/hostnull", "permissions": "rw"}},
		"seccompUnconfined": true,
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	s.DeployOK(plain.ID)
	uc := s.inspectService(plain.ID)
	require.Len(t, uc.HostConfig.Devices, 1)
	require.Equal(t, "/dev/null", uc.HostConfig.Devices[0].PathOnHost)
	require.Equal(t, "/dev/hostnull", uc.HostConfig.Devices[0].PathInContainer)
	require.Equal(t, "rw", uc.HostConfig.Devices[0].CgroupPermissions)
	require.Contains(t, uc.HostConfig.SecurityOpt, "seccomp=unconfined")

	// Switching it off blocks the next deploy of the service that needs it; the running container stays.
	off := s.Do("PUT", "/servers/"+s.ServerID+"/unrestricted", map[string]any{"unrestricted": false})
	require.Equal(t, http.StatusOK, off.Status, string(off.Raw))
	plan := s.Do("POST", "/services/"+plain.ID+"/deployments/plan", map[string]any{})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.Contains(t, string(plan.Raw), "requires_unrestricted_server")
	s.SetVars(plain.ID, map[string]string{"TOUCH": "1"}, nil)
	dep := s.Do("POST", "/services/"+plain.ID+"/deployments", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, dep.Status, string(dep.Raw))
	require.Contains(t, string(dep.Raw), "requires_unrestricted_server")
	s.inspectService(plain.ID) // still exactly one running container
}

// sizeOf extracts the size= option of a Docker tmpfs option string.
func sizeOf(opt string) string {
	for _, p := range strings.Split(opt, ",") {
		if v, ok := strings.CutPrefix(p, "size="); ok {
			return v
		}
	}
	return opt
}
