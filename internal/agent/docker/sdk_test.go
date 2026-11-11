package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type obj = map[string]any

func TestNewSDKEngineFromEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("DOCKER_API_VERSION", "1.50")
	e, err := NewSDKEngine()
	require.NoError(t, err)
	require.NoError(t, e.Close())
}

func TestNewSDKEngineRejectsBadHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "bogus")
	e, err := NewSDKEngine()
	require.Error(t, err)
	assert.Nil(t, e)
	assert.Contains(t, err.Error(), "docker client")
}

func TestWrapNotFoundAndOtherErrors(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("POST /containers/gone/start", http.StatusNotFound, "No such container: gone")
	d.onError("POST /containers/sick/start", http.StatusInternalServerError, "boom")

	err := e.ContainerStart(context.Background(), "gone")
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "start container gone")
	assert.Contains(t, err.Error(), "No such container: gone")

	err = e.ContainerStart(context.Background(), "sick")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "start container sick")
	assert.Contains(t, err.Error(), "boom")
}

func TestWrapNilStaysNil(t *testing.T) {
	require.NoError(t, wrap(nil, "anything"))
}

func TestPing(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.on("GET /_ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("API-Version", fakeAPIVersion)
		_, _ = w.Write([]byte("OK"))
	})
	require.NoError(t, e.Ping(context.Background()))
	assert.Equal(t, "/_ping", d.requests()[0].Path)
}

func TestPingError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /_ping", http.StatusInternalServerError, "daemon is sick")
	err := e.Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker ping")
	assert.Contains(t, err.Error(), "daemon is sick")
}

func TestServerVersion(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /version", 200, obj{"Version": "28.1.1", "ApiVersion": "1.49"})
	v, err := e.ServerVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "28.1.1", v)
}

func TestServerVersionError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /version", 500, "nope")
	v, err := e.ServerVersion(context.Background())
	require.Error(t, err)
	assert.Empty(t, v)
	assert.Contains(t, err.Error(), "docker version")
}

func TestInfoMapsDaemonInfo(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /info", 200, obj{
		"ServerVersion": "28.1.1", "CgroupVersion": "2", "KernelVersion": "6.8.0",
		"NCPU": 8, "MemTotal": int64(16 << 30),
	})
	info, err := e.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Info{ServerVersion: "28.1.1", CgroupVersion: "2", KernelVersion: "6.8.0", NCPU: 8, MemTotal: 16 << 30}, info)
}

func TestInfoNegativeMemoryIsZero(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /info", 200, obj{"MemTotal": -1, "NCPU": 2})
	info, err := e.Info(context.Background())
	require.NoError(t, err)
	assert.Zero(t, info.MemTotal)
	assert.Equal(t, 2, info.NCPU)
}

func TestInfoError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /info", 500, "nope")
	_, err := e.Info(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker info")
}

// ---- networks ----

func TestEnsureNetworkExistingIsNotRecreated(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /networks/nebula", 200, obj{"Name": "nebula", "Id": "n1"})
	require.NoError(t, e.EnsureNetwork(context.Background(), "nebula", nil))
	assert.Equal(t, []string{"GET /networks/nebula"}, d.calls())
}

func TestEnsureNetworkCreatesWhenMissing(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/nebula", 404, "network nebula not found")
	d.onJSON("POST /networks/create", 201, obj{"Id": "n1"})

	require.NoError(t, e.EnsureNetwork(context.Background(), "nebula", map[string]string{"nebula.managed": "true"}))

	var body network.CreateRequest
	d.only("POST", "/networks/create").decode(t, &body)
	assert.Equal(t, "nebula", body.Name)
	assert.Equal(t, "bridge", body.Driver)
	assert.False(t, body.Internal)
	assert.Equal(t, map[string]string{"nebula.managed": "true"}, body.Labels)
	assert.Nil(t, body.IPAM, "no subnet requested leaves address allocation to the daemon")
}

func TestEnsureInternalNetworkWithSubnet(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/db", 404, "not found")
	d.onJSON("POST /networks/create", 201, obj{"Id": "n1"})

	require.NoError(t, e.EnsureInternalNetwork(context.Background(), "db", nil, WithSubnet("10.77.0.0/24")))

	var body network.CreateRequest
	d.only("POST", "/networks/create").decode(t, &body)
	assert.True(t, body.Internal)
	require.NotNil(t, body.IPAM)
	require.Len(t, body.IPAM.Config, 1)
	assert.Equal(t, "10.77.0.0/24", body.IPAM.Config[0].Subnet.String())
}

func TestEnsureNetworkInvalidSubnetSendsNothing(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/db", 404, "not found")

	err := e.EnsureNetwork(context.Background(), "db", nil, WithSubnet("10.77.0.0"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid subnet "10.77.0.0"`)
	assert.Empty(t, d.find("POST", "/networks/create"))
}

func TestEnsureNetworkInspectFailureIsReported(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/nebula", 500, "daemon down")
	err := e.EnsureNetwork(context.Background(), "nebula", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inspect network nebula")
	assert.Empty(t, d.find("POST", "/networks/create"))
}

func TestEnsureNetworkCreateRaceIsSuccess(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/nebula", 404, "not found")
	d.onError("POST /networks/create", 409, "network with name nebula already exists")
	require.NoError(t, e.EnsureNetwork(context.Background(), "nebula", nil))
}

func TestEnsureNetworkPoolOverlapIsAnError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/nebula", 404, "not found")
	d.onError("POST /networks/create", 409, "invalid pool request: Pool overlaps with other one on this address space")
	err := e.EnsureNetwork(context.Background(), "nebula", nil, WithSubnet("10.0.0.0/24"))
	require.Error(t, err)
	assert.True(t, IsPoolOverlap(err))
	assert.Contains(t, err.Error(), "create network nebula")
}

func TestEnsureNetworkCreateFailure(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /networks/nebula", 404, "not found")
	d.onError("POST /networks/create", 500, "bridge exploded")
	err := e.EnsureNetwork(context.Background(), "nebula", nil)
	require.Error(t, err)
	assert.False(t, IsPoolOverlap(err))
	assert.Contains(t, err.Error(), "bridge exploded")
}

func TestIsPoolOverlap(t *testing.T) {
	assert.False(t, IsPoolOverlap(nil))
	assert.False(t, IsPoolOverlap(errors.New("network already exists")))
	assert.True(t, IsPoolOverlap(errors.New("Pool overlaps with other one on this address space")))
	assert.True(t, IsPoolOverlap(errors.New("INVALID POOL: OVERLAP")))
}

func TestResolveNetworkOptions(t *testing.T) {
	assert.Equal(t, NetworkOptions{}, ResolveNetworkOptions())
	assert.Equal(t, NetworkOptions{Subnet: "10.1.0.0/16"}, ResolveNetworkOptions(WithSubnet("10.1.0.0/16")))
	assert.Equal(t, NetworkOptions{Subnet: "10.2.0.0/16"}, ResolveNetworkOptions(WithSubnet("10.1.0.0/16"), WithSubnet("10.2.0.0/16")),
		"the last option wins")
}

func TestNetworkDisconnect(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("POST /networks/net/disconnect", 200)
	require.NoError(t, e.NetworkDisconnect(context.Background(), "net", "ctr"))

	var body network.DisconnectRequest
	d.only("POST", "/networks/net/disconnect").decode(t, &body)
	assert.Equal(t, "ctr", body.Container)
}

func TestNetworkDisconnectToleratesAbsentAndNotConnected(t *testing.T) {
	cases := map[string]struct {
		status int
		msg    string
	}{
		"network or container missing": {404, "No such container: ctr"},
		"container not attached":       {500, "container ctr is not connected to network net"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, e := newFakeDaemon(t)
			d.onError("POST /networks/net/disconnect", c.status, c.msg)
			require.NoError(t, e.NetworkDisconnect(context.Background(), "net", "ctr"))
		})
	}
}

func TestNetworkDisconnectOtherErrorIsReported(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("POST /networks/net/disconnect", 500, "endpoint stuck")
	err := e.NetworkDisconnect(context.Background(), "net", "ctr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disconnect ctr from net")
	assert.Contains(t, err.Error(), "endpoint stuck")
}

func TestNetworkListFiltersAndMapsMembers(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /networks", 200, []obj{
		{"Id": "id-a", "Name": "nebula-a", "Internal": true, "Labels": obj{"nebula.managed": "true"}, "Created": "2025-03-04T05:06:07Z"},
		{"Id": "id-other", "Name": "bridge"},
		{"Id": "id-gone", "Name": "nebula-gone"},
		{"Id": "id-b", "Name": "nebula-b"},
	})
	d.onJSON("GET /networks/id-a", 200, obj{
		"Id": "id-a", "Name": "nebula-a",
		"Containers": obj{"c1": obj{}, "c2": obj{}},
		"IPAM":       obj{"Config": []obj{{"Subnet": "10.9.0.0/24"}, {"Subnet": "fd00::/64"}}},
	})
	d.onError("GET /networks/id-gone", 404, "removed meanwhile")
	d.onJSON("GET /networks/id-b", 200, obj{"Id": "id-b", "Name": "nebula-b", "IPAM": obj{"Config": []obj{{}}}})

	got, err := e.NetworkList(context.Background(), "nebula-", map[string]string{"nebula.managed": "true"})
	require.NoError(t, err)

	var filters map[string]map[string]bool
	require.NoError(t, json.Unmarshal([]byte(d.only("GET", "/networks").Query.Get("filters")), &filters))
	assert.Equal(t, map[string]map[string]bool{"label": {"nebula.managed=true": true}}, filters)

	require.Len(t, got, 2, "bridge has the wrong prefix; nebula-gone vanished between list and inspect")
	assert.Equal(t, "id-a", got[0].ID)
	assert.Equal(t, "nebula-a", got[0].Name)
	assert.True(t, got[0].Internal)
	assert.Equal(t, map[string]string{"nebula.managed": "true"}, got[0].Labels)
	assert.ElementsMatch(t, []string{"c1", "c2"}, got[0].Containers)
	assert.Equal(t, []string{"10.9.0.0/24", "fd00::/64"}, got[0].Subnets)
	assert.Equal(t, time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC), got[0].Created.UTC())
	assert.Equal(t, "nebula-b", got[1].Name)
	assert.Empty(t, got[1].Subnets, "an IPAM entry without a subnet is skipped")
	assert.Empty(t, got[1].Containers)
}

func TestNetworkListErrors(t *testing.T) {
	t.Run("list fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /networks", 500, "nope")
		_, err := e.NetworkList(context.Background(), "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list networks")
	})
	t.Run("inspect fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onJSON("GET /networks", 200, []obj{{"Id": "id-a", "Name": "a"}})
		d.onError("GET /networks/id-a", 500, "inspect broke")
		_, err := e.NetworkList(context.Background(), "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inspect network a")
	})
}

func TestNetworkRemove(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("DELETE /networks/net", 204)
	d.onError("DELETE /networks/gone", 404, "no such network")
	require.NoError(t, e.NetworkRemove(context.Background(), "net"))
	d.only("DELETE", "/networks/net")
	require.ErrorIs(t, e.NetworkRemove(context.Background(), "gone"), ErrNotFound)
}

// ---- volumes ----

func TestEnsureVolume(t *testing.T) {
	t.Run("existing is kept", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onJSON("GET /volumes/data", 200, obj{"Name": "data"})
		require.NoError(t, e.EnsureVolume(context.Background(), "data", nil))
		assert.Equal(t, []string{"GET /volumes/data"}, d.calls())
	})
	t.Run("missing is created with labels", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /volumes/data", 404, "no such volume")
		d.onJSON("POST /volumes/create", 201, obj{"Name": "data"})
		require.NoError(t, e.EnsureVolume(context.Background(), "data", map[string]string{"app": "x"}))
		var body struct {
			Name   string
			Labels map[string]string
		}
		d.only("POST", "/volumes/create").decode(t, &body)
		assert.Equal(t, "data", body.Name)
		assert.Equal(t, map[string]string{"app": "x"}, body.Labels)
	})
	t.Run("inspect failure", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /volumes/data", 500, "disk gone")
		err := e.EnsureVolume(context.Background(), "data", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inspect volume data")
		assert.Empty(t, d.find("POST", "/volumes/create"))
	})
	t.Run("create failure", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /volumes/data", 404, "no such volume")
		d.onError("POST /volumes/create", 500, "quota")
		err := e.EnsureVolume(context.Background(), "data", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create volume data")
	})
}

func TestRemoveVolumeForce(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("DELETE /volumes/data", 204)
	d.onError("DELETE /volumes/busy", 409, "volume is in use")
	require.NoError(t, e.RemoveVolume(context.Background(), "data", true))
	assert.Equal(t, "1", d.only("DELETE", "/volumes/data").Query.Get("force"))

	err := e.RemoveVolume(context.Background(), "busy", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove volume busy")
	assert.Empty(t, d.find("DELETE", "/volumes/busy")[0].Query.Get("force"))
}

func TestNetworkConnectSendsAliases(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("POST /networks/net/connect", 200)
	require.NoError(t, e.NetworkConnect(context.Background(), "net", "ctr", []string{"web", "api"}))
	var body network.ConnectRequest
	d.only("POST", "/networks/net/connect").decode(t, &body)
	assert.Equal(t, "ctr", body.Container)
	require.NotNil(t, body.EndpointConfig)
	assert.Equal(t, []string{"web", "api"}, body.EndpointConfig.Aliases)
	assert.Zero(t, body.EndpointConfig.GwPriority)
}

func TestNetworkConnectError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("POST /networks/net/connect", 404, "No such network: net")
	err := e.NetworkConnect(context.Background(), "net", "ctr", nil)
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "connect ctr to net")
}

// ---- images ----

func TestImagePullStreamsProgressAndSendsAuth(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.on("POST /images/create", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for _, m := range []obj{
			{"status": "Pulling from library/nginx", "id": "1.27"},
			{"status": "Downloading", "id": "abc", "progress": "[==>  ]", "progressDetail": obj{"current": 1}},
			{"status": "Pull complete", "id": "abc"},
			{"status": "Status: Downloaded newer image"},
			{"id": "ignored-empty-status"},
		} {
			_ = json.NewEncoder(w).Encode(m)
		}
	})
	var lines []string
	require.NoError(t, e.ImagePull(context.Background(), "nginx:1.27", "c2VjcmV0", func(s string) { lines = append(lines, s) }))

	req := d.only("POST", "/images/create")
	assert.Equal(t, "docker.io/library/nginx", req.Query.Get("fromImage"))
	assert.Equal(t, "1.27", req.Query.Get("tag"))
	assert.Equal(t, "c2VjcmV0", req.Header.Get("X-Registry-Auth"))
	assert.Equal(t, []string{
		"1.27: Pulling from library/nginx",
		"abc: Pull complete",
		"Status: Downloaded newer image",
	}, lines, "byte-progress updates and empty statuses are not reported")
}

func TestImagePullWithoutProgressCallbackOrAuth(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.on("POST /images/create", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(obj{"status": "ok"})
	})
	require.NoError(t, e.ImagePull(context.Background(), "alpine", "", nil))
	req := d.only("POST", "/images/create")
	assert.Equal(t, "latest", req.Query.Get("tag"))
	assert.Empty(t, req.Header.Get("X-Registry-Auth"))
}

func TestImagePullStreamedErrorMessage(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.on("POST /images/create", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(obj{"status": "Pulling"})
		_ = json.NewEncoder(w).Encode(obj{"errorDetail": obj{"message": "manifest unknown"}, "error": "manifest unknown"})
	})
	err := e.ImagePull(context.Background(), "alpine:nope", "", func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pull alpine:nope")
	assert.Contains(t, err.Error(), "manifest unknown")
}

func TestImagePullHTTPErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("POST /images/create", 404, "pull access denied, repository does not exist")
		require.ErrorIs(t, e.ImagePull(context.Background(), "nope/nope", "", nil), ErrNotFound)
	})
	t.Run("invalid reference", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		err := e.ImagePull(context.Background(), "UPPER CASE:::", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pull UPPER CASE:::")
		assert.Empty(t, d.requests())
	})
	t.Run("garbled stream", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.on("POST /images/create", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("this is not json\n"))
		})
		err := e.ImagePull(context.Background(), "alpine", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pull alpine")
	})
}

func TestImageInspectMapsFields(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /images/app:1/json", 200, obj{
		"Id":          "sha256:abc",
		"RepoTags":    []string{"app:1"},
		"RepoDigests": []string{"app@sha256:def"},
		"Created":     "2025-01-02T03:04:05.123456789Z",
		"Config": obj{
			"Labels":      obj{"org.x": "y"},
			"Entrypoint":  []string{"/app"},
			"Env":         []string{"PATH=/usr/bin", "MODE=prod"},
			"Cmd":         []string{"serve"},
			"Healthcheck": obj{"Test": []string{"CMD", "true"}},
		},
		"Metadata": obj{"LastTagTime": "2025-02-03T04:05:06Z"},
	})
	got, err := e.ImageInspect(context.Background(), "app:1")
	require.NoError(t, err)
	assert.Equal(t, "sha256:abc", got.ID)
	assert.Equal(t, []string{"app:1"}, got.RepoTags)
	assert.Equal(t, []string{"app@sha256:def"}, got.RepoDigests)
	assert.Equal(t, map[string]string{"org.x": "y"}, got.Labels)
	assert.Equal(t, []string{"/app"}, got.Entrypoint)
	assert.Equal(t, []string{"PATH=/usr/bin", "MODE=prod"}, got.Env)
	assert.Equal(t, []string{"serve"}, got.Cmd)
	assert.True(t, got.HasHealthcheck)
	assert.Equal(t, time.Date(2025, 1, 2, 3, 4, 5, 123456789, time.UTC), got.Created.UTC())
	assert.Equal(t, time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC), got.LastTagged.UTC())
}

func TestImageInspectHealthcheckDetection(t *testing.T) {
	cases := map[string]obj{
		"NONE disables the inherited check": {"Healthcheck": obj{"Test": []string{"NONE"}}},
		"empty test":                        {"Healthcheck": obj{}},
		"no healthcheck":                    {},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			d, e := newFakeDaemon(t)
			d.onJSON("GET /images/app/json", 200, obj{"Id": "sha256:abc", "Config": cfg})
			got, err := e.ImageInspect(context.Background(), "app")
			require.NoError(t, err)
			assert.False(t, got.HasHealthcheck)
		})
	}
}

func TestImageInspectWithoutConfigOrDates(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /images/app/json", 200, obj{"Id": "sha256:abc", "Created": "not a date"})
	got, err := e.ImageInspect(context.Background(), "app")
	require.NoError(t, err)
	assert.Equal(t, "sha256:abc", got.ID)
	assert.Nil(t, got.Labels)
	assert.False(t, got.HasHealthcheck)
	assert.True(t, got.Created.IsZero(), "an unparsable created date is left zero")
	assert.True(t, got.LastTagged.IsZero())
}

func TestImageInspectNotFound(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /images/ghost/json", 404, "No such image: ghost")
	_, err := e.ImageInspect(context.Background(), "ghost")
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "inspect image ghost")
}

func TestImageRemove(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("DELETE /images/app:1", 200, []obj{{"Untagged": "app:1"}})
	require.NoError(t, e.ImageRemove(context.Background(), "app:1", true))
	q := d.only("DELETE", "/images/app:1").Query
	assert.Equal(t, "1", q.Get("force"))
	assert.Empty(t, q.Get("noprune"), "children are pruned")

	d.onError("DELETE /images/ghost", 404, "No such image")
	require.ErrorIs(t, e.ImageRemove(context.Background(), "ghost", false), ErrNotFound)
}

// ---- containers ----

// createRequest is the wire body of POST /containers/create.
type createRequest struct {
	container.Config
	HostConfig       *container.HostConfig
	NetworkingConfig *network.NetworkingConfig
}

func fakeCreateOK(d *fakeDaemon, id string) {
	d.onJSON("POST /containers/create", 201, obj{"Id": id, "Warnings": []string{}})
}

func TestContainerCreateMapsSpec(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeCreateOK(d, "cid1")
	stopTimeout := 25
	spec := ContainerSpec{
		Name:          "web",
		Image:         "nginx:1.27",
		Env:           []string{"A=1"},
		Cmd:           []string{"nginx", "-g", "daemon off;"},
		Entrypoint:    []string{"/entry"},
		Labels:        map[string]string{"nebula.managed": "true"},
		StopTimeout:   &stopTimeout,
		StopSignal:    "SIGQUIT",
		Hostname:      "web-host",
		User:          "1000:1000",
		RestartPolicy: "unless-stopped",
		MemoryBytes:   256 << 20,
		NanoCPUs:      500_000_000,
		Healthcheck: &HealthSpec{
			Test: []string{"CMD", "curl", "-f", "localhost"}, Interval: 10 * time.Second,
			Timeout: 2 * time.Second, StartPeriod: 5 * time.Second, Retries: 3,
		},
		LogConfig:      &LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m"}},
		ExtraHosts:     []string{"db:10.0.0.5"},
		CapAdd:         []string{"net_admin"},
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		ShmSize:        64 << 20,
		Tmpfs:          map[string]string{"/tmp": "size=64m"},
		ReadonlyRootfs: true,
		Init:           true,
		Ulimits:        []Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
		DNS:            []string{"1.1.1.1", "2606:4700::1111"},
		Runtime:        "nvidia",
		Privileged:     true,
		PidMode:        "host",
		GroupAdd:       []string{"video"},
		Sysctls:        map[string]string{"net.ipv4.ip_forward": "1"},
		PidsLimit:      200,
		Ports: []PortPublish{
			{HostPort: 8080, ContainerPort: 80},
			{HostIP: "127.0.0.1", HostPort: 5353, ContainerPort: 53, Proto: "udp"},
		},
		Mounts: []VolumeMount{
			{Volume: "data", Target: "/data"},
			{HostPath: "/etc/ssl", Target: "/ssl", ReadOnly: true},
		},
		Networks: []NetworkAttachment{{Network: "nebula", Aliases: []string{"web"}}},
	}
	id, err := e.ContainerCreate(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "cid1", id)

	req := d.only("POST", "/containers/create")
	assert.Equal(t, "web", req.Query.Get("name"))
	var body createRequest
	req.decode(t, &body)

	assert.Equal(t, "nginx:1.27", body.Image)
	assert.Equal(t, []string{"A=1"}, body.Env)
	assert.Equal(t, []string{"nginx", "-g", "daemon off;"}, []string(body.Cmd))
	assert.Equal(t, []string{"/entry"}, []string(body.Entrypoint))
	assert.Equal(t, map[string]string{"nebula.managed": "true"}, body.Labels)
	require.NotNil(t, body.StopTimeout)
	assert.Equal(t, 25, *body.StopTimeout)
	assert.Equal(t, "SIGQUIT", body.StopSignal)
	assert.Equal(t, "web-host", body.Hostname)
	assert.Equal(t, "1000:1000", body.User)
	require.NotNil(t, body.Healthcheck)
	assert.Equal(t, []string{"CMD", "curl", "-f", "localhost"}, body.Healthcheck.Test)
	assert.Equal(t, 10*time.Second, body.Healthcheck.Interval)
	assert.Equal(t, 2*time.Second, body.Healthcheck.Timeout)
	assert.Equal(t, 5*time.Second, body.Healthcheck.StartPeriod)
	assert.Equal(t, 3, body.Healthcheck.Retries)

	h := body.HostConfig
	require.NotNil(t, h)
	assert.Equal(t, container.RestartPolicyUnlessStopped, h.RestartPolicy.Name)
	assert.Equal(t, int64(256<<20), h.Memory)
	assert.Equal(t, int64(500_000_000), h.NanoCPUs)
	assert.Equal(t, "json-file", h.LogConfig.Type)
	assert.Equal(t, map[string]string{"max-size": "10m"}, h.LogConfig.Config)
	assert.Equal(t, []string{"db:10.0.0.5"}, h.ExtraHosts)
	assert.Equal(t, []string{"CAP_NET_ADMIN"}, h.CapAdd, "the client normalises capability names")
	assert.Equal(t, []string{"ALL"}, h.CapDrop)
	assert.Equal(t, []string{"no-new-privileges"}, h.SecurityOpt)
	assert.Equal(t, int64(64<<20), h.ShmSize)
	assert.Equal(t, map[string]string{"/tmp": "size=64m"}, h.Tmpfs)
	assert.True(t, h.ReadonlyRootfs)
	require.NotNil(t, h.Init)
	assert.True(t, *h.Init)
	require.Len(t, h.Ulimits, 1)
	assert.Equal(t, container.Ulimit{Name: "nofile", Soft: 1024, Hard: 4096}, *h.Ulimits[0])
	require.Len(t, h.DNS, 2)
	assert.Equal(t, "1.1.1.1", h.DNS[0].String())
	assert.Equal(t, "2606:4700::1111", h.DNS[1].String())
	assert.Equal(t, "nvidia", h.Runtime)
	assert.True(t, h.Privileged)
	assert.Equal(t, container.PidMode("host"), h.PidMode)
	assert.Equal(t, []string{"video"}, h.GroupAdd)
	assert.Equal(t, map[string]string{"net.ipv4.ip_forward": "1"}, h.Sysctls)
	require.NotNil(t, h.PidsLimit)
	assert.Equal(t, int64(200), *h.PidsLimit)

	tcp80, _ := network.PortFrom(80, network.TCP)
	udp53, _ := network.PortFrom(53, network.UDP)
	assert.Contains(t, body.ExposedPorts, tcp80)
	assert.Contains(t, body.ExposedPorts, udp53)
	require.Len(t, h.PortBindings[tcp80], 1)
	assert.Equal(t, "8080", h.PortBindings[tcp80][0].HostPort)
	assert.False(t, h.PortBindings[tcp80][0].HostIP.IsValid())
	require.Len(t, h.PortBindings[udp53], 1)
	assert.Equal(t, "5353", h.PortBindings[udp53][0].HostPort)
	assert.Equal(t, "127.0.0.1", h.PortBindings[udp53][0].HostIP.String())

	require.Len(t, h.Mounts, 2)
	assert.Equal(t, "volume", string(h.Mounts[0].Type))
	assert.Equal(t, "data", h.Mounts[0].Source)
	assert.Equal(t, "/data", h.Mounts[0].Target)
	assert.False(t, h.Mounts[0].ReadOnly)
	assert.Equal(t, "bind", string(h.Mounts[1].Type))
	assert.Equal(t, "/etc/ssl", h.Mounts[1].Source)
	assert.True(t, h.Mounts[1].ReadOnly)

	assert.Equal(t, container.NetworkMode("nebula"), h.NetworkMode)
	require.NotNil(t, body.NetworkingConfig)
	assert.Equal(t, []string{"web"}, body.NetworkingConfig.EndpointsConfig["nebula"].Aliases)
	assert.Empty(t, d.find("POST", "/networks/nebula/connect"), "a single network is attached at create time")
}

func TestContainerCreateMinimalSpecOmitsOptionalSettings(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeCreateOK(d, "cid")
	_, err := e.ContainerCreate(context.Background(), ContainerSpec{Name: "m", Image: "busybox"})
	require.NoError(t, err)
	var body createRequest
	d.only("POST", "/containers/create").decode(t, &body)
	h := body.HostConfig
	require.NotNil(t, h)
	assert.Nil(t, h.Init, "Init is only sent when requested")
	assert.Nil(t, h.PidsLimit)
	assert.Empty(t, h.PortBindings)
	assert.Empty(t, h.Mounts)
	assert.Empty(t, h.Devices)
	assert.Empty(t, h.DeviceRequests)
	assert.Nil(t, body.Healthcheck)
	assert.Nil(t, body.NetworkingConfig)
	assert.Empty(t, h.NetworkMode)
}

func TestContainerCreateDevicesAndGPUs(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeCreateOK(d, "cid")
	_, err := e.ContainerCreate(context.Background(), ContainerSpec{
		Name: "gpu", Image: "cuda",
		Devices: []DeviceMapping{
			{HostPath: "/dev/fuse"},
			{HostPath: "/dev/dri/card0", ContainerPath: "/dev/card", Permissions: "r"},
		},
		GPUs: &GPURequest{Driver: "nvidia", Count: -1, Capabilities: []string{"compute", "gpu", "utility"}},
	})
	require.NoError(t, err)
	var body createRequest
	d.only("POST", "/containers/create").decode(t, &body)
	assert.Equal(t, []container.DeviceMapping{
		{PathOnHost: "/dev/fuse", PathInContainer: "/dev/fuse", CgroupPermissions: "rwm"},
		{PathOnHost: "/dev/dri/card0", PathInContainer: "/dev/card", CgroupPermissions: "r"},
	}, body.HostConfig.Devices)
	assert.Equal(t, []container.DeviceRequest{
		{Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu", "compute", "utility"}}},
	}, body.HostConfig.DeviceRequests)
}

func TestContainerCreateNetworkMode(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeCreateOK(d, "cid")
	_, err := e.ContainerCreate(context.Background(), ContainerSpec{Name: "h", Image: "x", NetworkMode: "host"})
	require.NoError(t, err)
	var body createRequest
	d.only("POST", "/containers/create").decode(t, &body)
	assert.Equal(t, container.NetworkMode("host"), body.HostConfig.NetworkMode)
	assert.Nil(t, body.NetworkingConfig)
}

func TestContainerCreateRejectsNetworkModeWithNetworks(t *testing.T) {
	d, e := newFakeDaemon(t)
	_, err := e.ContainerCreate(context.Background(), ContainerSpec{
		Name: "h", Image: "x", NetworkMode: "host", Networks: []NetworkAttachment{{Network: "n"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with networks")
	assert.Empty(t, d.requests())
}

func TestContainerCreateInvalidInputSendsNothing(t *testing.T) {
	cases := map[string]struct {
		spec ContainerSpec
		want string
	}{
		"dns":              {ContainerSpec{Image: "x", DNS: []string{"not-an-ip"}}, `invalid dns server "not-an-ip"`},
		"container port 0": {ContainerSpec{Image: "x", Ports: []PortPublish{{HostPort: 80}}}, "invalid container port 0"},
		"container port high": {ContainerSpec{Image: "x", Ports: []PortPublish{{HostPort: 80, ContainerPort: 70000}}},
			"invalid container port 70000"},
		"host ip": {ContainerSpec{Image: "x", Ports: []PortPublish{{HostIP: "nope", HostPort: 80, ContainerPort: 80}}}, `invalid host ip "nope"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, e := newFakeDaemon(t)
			_, err := e.ContainerCreate(context.Background(), c.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			assert.Empty(t, d.requests())
		})
	}
}

func TestContainerCreateMultipleNetworksConnectsTheRest(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeCreateOK(d, "cid9")
	d.onStatus("POST /networks/edge/connect", 200)
	d.onStatus("POST /networks/db/connect", 200)
	id, err := e.ContainerCreate(context.Background(), ContainerSpec{
		Name: "app", Image: "x",
		Networks: []NetworkAttachment{
			{Network: "primary", Aliases: []string{"app"}, GwPriority: 5},
			{Network: "edge", Aliases: []string{"app-edge"}, GwPriority: 100},
			{Network: "db"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "cid9", id)

	var body createRequest
	d.only("POST", "/containers/create").decode(t, &body)
	assert.Equal(t, container.NetworkMode("primary"), body.HostConfig.NetworkMode)
	require.Len(t, body.NetworkingConfig.EndpointsConfig, 1, "only the primary network goes in the create call")
	assert.Equal(t, 5, body.NetworkingConfig.EndpointsConfig["primary"].GwPriority)

	var edge network.ConnectRequest
	d.only("POST", "/networks/edge/connect").decode(t, &edge)
	assert.Equal(t, "cid9", edge.Container)
	assert.Equal(t, []string{"app-edge"}, edge.EndpointConfig.Aliases)
	assert.Equal(t, 100, edge.EndpointConfig.GwPriority)
	d.only("POST", "/networks/db/connect")
	assert.Equal(t, []string{"POST /containers/create", "POST /networks/edge/connect", "POST /networks/db/connect"}, d.calls())
}

func TestContainerCreateOldAPIDropsGwPriority(t *testing.T) {
	d, e := newFakeDaemonAt(t, "1.47")
	fakeCreateOK(d, "cid")
	d.onStatus("POST /networks/edge/connect", 200)
	_, err := e.ContainerCreate(context.Background(), ContainerSpec{
		Name: "app", Image: "x",
		Networks: []NetworkAttachment{{Network: "p", GwPriority: 5}, {Network: "edge", GwPriority: 100}},
	})
	require.NoError(t, err)
	var body createRequest
	d.only("POST", "/containers/create").decode(t, &body)
	assert.Zero(t, body.NetworkingConfig.EndpointsConfig["p"].GwPriority)
	var edge network.ConnectRequest
	d.only("POST", "/networks/edge/connect").decode(t, &edge)
	assert.Zero(t, edge.EndpointConfig.GwPriority)
}

func TestContainerCreateConnectFailureRemovesContainer(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeCreateOK(d, "cid9")
	d.onError("POST /networks/edge/connect", 404, "No such network: edge")
	d.onStatus("DELETE /containers/cid9", 204)
	id, err := e.ContainerCreate(context.Background(), ContainerSpec{
		Name: "app", Image: "x", Networks: []NetworkAttachment{{Network: "p"}, {Network: "edge"}},
	})
	require.ErrorIs(t, err, ErrNotFound)
	assert.Empty(t, id)
	rm := d.only("DELETE", "/containers/cid9")
	assert.Equal(t, "1", rm.Query.Get("force"), "the half-built container is force-removed")
}

func TestContainerCreateErrors(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("POST /containers/create", 404, "No such image: ghost")
	_, err := e.ContainerCreate(context.Background(), ContainerSpec{Name: "web", Image: "ghost"})
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "create container web")
}

func TestContainerLifecycleCalls(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("POST /containers/c1/start", 204)
	d.onStatus("POST /containers/c1/pause", 204)
	d.onStatus("POST /containers/c1/unpause", 204)
	d.onStatus("DELETE /containers/c1", 204)
	ctx := context.Background()
	require.NoError(t, e.ContainerStart(ctx, "c1"))
	require.NoError(t, e.ContainerPause(ctx, "c1"))
	require.NoError(t, e.ContainerUnpause(ctx, "c1"))
	require.NoError(t, e.ContainerRemove(ctx, "c1", true, true))
	assert.Equal(t, []string{"POST /containers/c1/start", "POST /containers/c1/pause", "POST /containers/c1/unpause", "DELETE /containers/c1"}, d.calls())
	rm := d.only("DELETE", "/containers/c1")
	assert.Equal(t, "1", rm.Query.Get("force"))
	assert.Equal(t, "1", rm.Query.Get("v"))
}

func TestContainerRemoveWithoutFlags(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("DELETE /containers/c1", 204)
	require.NoError(t, e.ContainerRemove(context.Background(), "c1", false, false))
	rm := d.only("DELETE", "/containers/c1")
	assert.Empty(t, rm.Query.Get("force"))
	assert.Empty(t, rm.Query.Get("v"))
}

func TestContainerLifecycleErrorsWrapNotFound(t *testing.T) {
	d, e := newFakeDaemon(t)
	for _, p := range []string{"start", "pause", "unpause", "stop"} {
		d.onError("POST /containers/ghost/"+p, 404, "No such container: ghost")
	}
	d.onError("DELETE /containers/ghost", 404, "No such container: ghost")
	d.onError("POST /containers/ghost/update", 404, "No such container: ghost")
	ctx := context.Background()
	for what, err := range map[string]error{
		"start":   e.ContainerStart(ctx, "ghost"),
		"pause":   e.ContainerPause(ctx, "ghost"),
		"unpause": e.ContainerUnpause(ctx, "ghost"),
		"stop":    e.ContainerStop(ctx, "ghost", nil),
		"remove":  e.ContainerRemove(ctx, "ghost", false, false),
		"update":  e.ContainerSetRestartPolicy(ctx, "ghost", "no"),
	} {
		require.ErrorIs(t, err, ErrNotFound, what)
		assert.Contains(t, err.Error(), "ghost", what)
	}
}

func TestContainerStopTimeout(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("POST /containers/c1/stop", 204)
	ctx := context.Background()
	require.NoError(t, e.ContainerStop(ctx, "c1", nil))
	t30 := 30 * time.Second
	require.NoError(t, e.ContainerStop(ctx, "c1", &t30))
	t0 := time.Duration(0)
	require.NoError(t, e.ContainerStop(ctx, "c1", &t0))
	tHalf := 1500 * time.Millisecond
	require.NoError(t, e.ContainerStop(ctx, "c1", &tHalf))

	reqs := d.find("POST", "/containers/c1/stop")
	require.Len(t, reqs, 4)
	assert.False(t, reqs[0].Query.Has("t"), "no timeout leaves the container's own stop timeout in force")
	assert.Equal(t, "30", reqs[1].Query.Get("t"))
	assert.Equal(t, "0", reqs[2].Query.Get("t"), "zero means SIGKILL right away, not the default")
	assert.Equal(t, "2", reqs[3].Query.Get("t"), "sub-second precision is rounded to whole seconds")
}

func TestContainerSetRestartPolicy(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("POST /containers/db/update", 200, obj{"Warnings": []string{}})
	require.NoError(t, e.ContainerSetRestartPolicy(context.Background(), "db", "no"))
	var body struct{ RestartPolicy container.RestartPolicy }
	d.only("POST", "/containers/db/update").decode(t, &body)
	assert.Equal(t, container.RestartPolicyDisabled, body.RestartPolicy.Name)
}

func inspectJSON() obj {
	return obj{
		"Id":           "cid",
		"Name":         "/web",
		"Image":        "sha256:img",
		"Created":      "2025-01-02T03:04:05.5Z",
		"RestartCount": 3,
		"State": obj{
			"Status": "running", "Running": true, "Restarting": false, "OOMKilled": true, "ExitCode": 137,
			"StartedAt": "2025-01-02T03:04:06Z", "FinishedAt": "2025-01-02T04:00:00Z",
			"Health": obj{"Status": "healthy"},
		},
		"Config": obj{
			"Image": "nginx:1.27", "Labels": obj{"a": "b"}, "Env": []string{"X=1"}, "Cmd": []string{"nginx"},
		},
		"HostConfig": obj{
			"RestartPolicy": obj{"Name": "unless-stopped"},
			"LogConfig":     obj{"Type": "json-file", "Config": obj{"max-size": "1m"}},
			"NetworkMode":   "nebula",
			"ExtraHosts":    []string{"h:1.2.3.4"},
			"Dns":           []string{"9.9.9.9"},
			"PortBindings": obj{
				"80/tcp": []obj{{"HostIp": "", "HostPort": "8080"}},
				"53/udp": []obj{{"HostIp": "127.0.0.1", "HostPort": "5353"}, {"HostIp": "0.0.0.0", "HostPort": "5354"}},
			},
		},
		"Mounts": []obj{
			{"Type": "volume", "Name": "data", "Source": "/var/lib/docker/volumes/data/_data", "Destination": "/data", "RW": true},
			{"Type": "bind", "Source": "/etc/ssl", "Destination": "/ssl", "RW": false},
		},
		"NetworkSettings": obj{"Networks": obj{
			"nebula": obj{"Aliases": []string{"web"}, "IPAddress": "10.0.0.2"},
			"empty":  obj{},
		}},
	}
}

func TestContainerInspectMapsEverything(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /containers/web/json", 200, inspectJSON())
	got, err := e.ContainerInspect(context.Background(), "web")
	require.NoError(t, err)

	assert.Equal(t, "cid", got.ID)
	assert.Equal(t, "web", got.Name, "the leading slash is trimmed")
	assert.Equal(t, "sha256:img", got.ImageID)
	assert.Equal(t, "nginx:1.27", got.Image)
	assert.Equal(t, map[string]string{"a": "b"}, got.Labels)
	assert.Equal(t, []string{"X=1"}, got.Env)
	assert.Equal(t, []string{"nginx"}, got.Cmd)
	assert.Equal(t, 3, got.RestartCount)
	assert.Equal(t, time.Date(2025, 1, 2, 3, 4, 5, 500_000_000, time.UTC), got.Created.UTC())
	assert.Equal(t, "running", got.State)
	assert.True(t, got.Running)
	assert.False(t, got.Restarting)
	assert.True(t, got.OOMKilled)
	assert.Equal(t, 137, got.ExitCode)
	assert.Equal(t, time.Date(2025, 1, 2, 3, 4, 6, 0, time.UTC), got.StartedAt.UTC())
	assert.Equal(t, time.Date(2025, 1, 2, 4, 0, 0, 0, time.UTC), got.FinishedAt.UTC())
	assert.Equal(t, "healthy", got.Health)
	assert.Equal(t, "unless-stopped", got.RestartMode)
	assert.Equal(t, LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m"}}, got.LogConfig)
	assert.Equal(t, "nebula", got.NetworkMode)
	assert.Equal(t, []string{"h:1.2.3.4"}, got.ExtraHosts)
	assert.Equal(t, []string{"9.9.9.9"}, got.DNS)
	assert.ElementsMatch(t, []PortPublish{
		{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
		{HostIP: "127.0.0.1", HostPort: 5353, ContainerPort: 53, Proto: "udp"},
		{HostPort: 5354, ContainerPort: 53, Proto: "udp"},
	}, got.Ports, "an unspecified host IP is reported as no host IP")
	assert.Equal(t, []MountInfo{
		{Type: "volume", Name: "data", Source: "/var/lib/docker/volumes/data/_data", Destination: "/data"},
		{Type: "bind", Source: "/etc/ssl", Destination: "/ssl", ReadOnly: true},
	}, got.Mounts)
	assert.Equal(t, map[string]ContainerNetwork{
		"nebula": {IP: "10.0.0.2", Aliases: []string{"web"}},
		"empty":  {},
	}, got.Networks)
}

func TestContainerInspectSparseResponse(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /containers/bare/json", 200, obj{
		"Id": "cid", "Name": "bare", "Created": "garbage",
		"State": obj{"Status": "exited", "StartedAt": "0001-01-01T00:00:00Z", "FinishedAt": "0001-01-01T00:00:00Z"},
	})
	got, err := e.ContainerInspect(context.Background(), "bare")
	require.NoError(t, err)
	assert.Equal(t, "bare", got.Name)
	assert.Equal(t, "exited", got.State)
	assert.True(t, got.Created.IsZero())
	assert.True(t, got.FinishedAt.IsZero(), "the daemon's year-1 placeholder means never finished")
	assert.Empty(t, got.Health)
	assert.Empty(t, got.Image)
	assert.Empty(t, got.Ports)
	assert.NotNil(t, got.Networks, "Networks is always a non-nil map")
	assert.Empty(t, got.Networks)
}

func TestContainerInspectNotFound(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /containers/ghost/json", 404, "No such container: ghost")
	_, err := e.ContainerInspect(context.Background(), "ghost")
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "inspect container ghost")
}

func TestContainerListFiltersAndInspects(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /containers/json", 200, []obj{{"Id": "c1"}, {"Id": "gone"}, {"Id": "c2"}})
	d.onJSON("GET /containers/c1/json", 200, obj{"Id": "c1", "Name": "/one"})
	d.onError("GET /containers/gone/json", 404, "No such container")
	d.onJSON("GET /containers/c2/json", 200, obj{"Id": "c2", "Name": "/two"})

	got, err := e.ContainerList(context.Background(), ListFilter{
		All:    true,
		Labels: map[string]string{"nebula.managed": "true", "nebula.service": ""},
		Names:  []string{"web", "db"},
	})
	require.NoError(t, err)
	require.Len(t, got, 2, "a container removed between list and inspect is skipped")
	assert.Equal(t, "one", got[0].Name)
	assert.Equal(t, "two", got[1].Name)

	q := d.only("GET", "/containers/json").Query
	assert.Equal(t, "1", q.Get("all"))
	var filters map[string]map[string]bool
	require.NoError(t, json.Unmarshal([]byte(q.Get("filters")), &filters))
	assert.Equal(t, map[string]map[string]bool{
		"label": {"nebula.managed=true": true, "nebula.service": true},
		"name":  {"web": true, "db": true},
	}, filters, "an empty label value filters on key presence")
}

func TestContainerListEmptyAndRunningOnly(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /containers/json", 200, []obj{})
	got, err := e.ContainerList(context.Background(), ListFilter{})
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
	q := d.only("GET", "/containers/json").Query
	assert.Empty(t, q.Get("all"))
	assert.Empty(t, q.Get("filters"))
}

func TestContainerListErrors(t *testing.T) {
	t.Run("list fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /containers/json", 500, "nope")
		_, err := e.ContainerList(context.Background(), ListFilter{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list containers")
	})
	t.Run("inspect fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onJSON("GET /containers/json", 200, []obj{{"Id": "c1"}})
		d.onError("GET /containers/c1/json", 500, "inspect broke")
		_, err := e.ContainerList(context.Background(), ListFilter{})
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNotFound)
		assert.True(t, strings.Contains(err.Error(), "inspect broke"))
	})
}
