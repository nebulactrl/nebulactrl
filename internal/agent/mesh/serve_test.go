package mesh

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestServeConfigGolden(t *testing.T) {
	cfg, err := ServeConfig([]ServeTarget{
		{MeshPort: 15001, Host: "nc-svc-99999999", ContainerPort: 5432}, // given out of order on purpose
		{MeshPort: 8080, Host: "nc-api-d1d1d1d1-0", ContainerPort: 8080},
	})
	require.NoError(t, err)
	golden(t, "serve_config", string(cfg))

	var parsed struct {
		TCP map[string]struct{ TCPForward string }
	}
	require.NoError(t, json.Unmarshal(cfg, &parsed))
	require.Equal(t, "nc-svc-99999999:5432", parsed.TCP["15001"].TCPForward)
	require.Equal(t, "nc-api-d1d1d1d1-0:8080", parsed.TCP["8080"].TCPForward)
}

func TestServeConfigIsDeterministicAndEmptyIsValid(t *testing.T) {
	a, _ := ServeConfig([]ServeTarget{{MeshPort: 2, Host: "b", ContainerPort: 1}, {MeshPort: 1, Host: "a", ContainerPort: 1}})
	b, _ := ServeConfig([]ServeTarget{{MeshPort: 1, Host: "a", ContainerPort: 1}, {MeshPort: 2, Host: "b", ContainerPort: 1}})
	require.Equal(t, string(a), string(b))

	empty, err := ServeConfig(nil)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(empty))
}

// A service forwards to nc-svc-<id8> when a running container carries the
// alias, else to the container's unique name (exposing never restarts it),
// and never to a slug.
func TestServeTargetsAliasThenContainerName(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	exps := serverSpec().Exposures
	svcLabels := func(id string) map[string]string {
		return map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: id}
	}

	// Nothing running yet: the alias, which resolves as soon as a deploy lands.
	got := m.serveTargets(context.Background(), exps)
	require.Equal(t, "nc-svc-99999999", got[0].Host)
	require.Equal(t, "nc-svc-88888888", got[1].Host)

	// Running, no alias (deployed before the exposure existed): the name.
	f.seed("nc-postgres-d1d1d1d1-0", true, svcLabels(svcPG))
	f.seed("nc-api-d2d2d2d2-0", true, svcLabels(svcAPI))
	got = m.serveTargets(context.Background(), exps)
	require.Equal(t, ServeTarget{MeshPort: 15001, Host: "nc-postgres-d1d1d1d1-0", ContainerPort: 5432}, got[0])
	require.Equal(t, ServeTarget{MeshPort: 8080, Host: "nc-api-d2d2d2d2-0", ContainerPort: 8080}, got[1])

	// The next deploy carries the alias: the target follows, without a restart.
	f.mu.Lock()
	f.find("nc-postgres-d1d1d1d1-0").Networks = map[string]docker.ContainerNetwork{
		"nebulactrl-env-aaaaaaaa": {IP: "10.0.0.5", Aliases: []string{"postgres", "nc-svc-99999999"}},
	}
	f.mu.Unlock()
	got = m.serveTargets(context.Background(), exps)
	require.Equal(t, "nc-svc-99999999", got[0].Host)
	require.Equal(t, "nc-api-d2d2d2d2-0", got[1].Host)

	// A stopped container is not a target.
	f.seed("nc-api-d3d3d3d3-0", false, svcLabels(svcAPI))
	got = m.serveTargets(context.Background(), exps)
	require.Equal(t, "nc-api-d2d2d2d2-0", got[1].Host)
}

// The config is written live to the watched /config; an unchanged config is not
// rewritten and nothing is recreated.
func TestServeConfigIsRewrittenLiveOnlyWhenItChanges(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	spec := serverSpec()
	st := sync1(t, m, keysFor(srvID), nil, provision(spec))
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)

	// create → copy serve.json → start: the config is in place at first start.
	c, cp, s := f.index("create "), f.index("copy "), f.index("start ")
	require.True(t, c >= 0 && c < cp && cp < s, "%v", f.events)
	require.Equal(t, "/config", f.copies[0].dir)
	require.Equal(t, "serve.json", f.copies[0].file)
	require.Contains(t, f.copies[0].content, `"15001"`)
	require.Contains(t, f.copies[0].content, `"TCPForward": "nc-svc-99999999:5432"`)

	// Same set again: no write.
	f.mark()
	sync1(t, m, keysFor(srvID), nil, provision(spec))
	require.Zero(t, f.count("copy "), "unchanged config is not rewritten")
	require.Zero(t, f.count("create "))
	require.Zero(t, f.count("remove "))

	// An exposure is added: rewritten in place, no recreate, no restart.
	spec.Exposures = append(spec.Exposures, agentprotocol.MeshExposure{
		ServiceID: "77777777-1111-2222-3333-444444444444", ServiceSlug: "cache", EnvironmentID: envA,
		Purpose: agentprotocol.MeshPurposeService, ContainerPort: 6379, MeshPort: 6379,
	})
	f.mark()
	st = sync1(t, m, nil, nil, spec)
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	require.Equal(t, 1, f.count("copy "))
	require.Contains(t, f.lastServe(), `"6379"`)
	require.Zero(t, f.count("create "))
	require.Zero(t, f.count("remove "))
	require.Zero(t, f.count("stop "))
	require.Zero(t, f.count("start "))

	// An exposure is dropped: gone from the config the same way.
	spec.Exposures = spec.Exposures[1:2]
	f.mark()
	sync1(t, m, nil, nil, spec)
	require.NotContains(t, f.lastServe(), `"15001"`)
	require.Zero(t, f.count("create "))
}

// A deploy that adds the alias is picked up by Refresh without a sync.
func TestRefreshRetargetsAfterADeploy(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	spec := serverSpec()
	f.seed("nc-postgres-d1d1d1d1-0", true, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: svcPG})
	sync1(t, m, keysFor(srvID), nil, provision(spec))
	require.Contains(t, f.lastServe(), `"TCPForward": "nc-postgres-d1d1d1d1-0:5432"`)

	f.mark()
	m.Refresh(context.Background())
	require.Zero(t, f.count("copy "), "nothing changed")

	f.mu.Lock()
	f.find("nc-postgres-d1d1d1d1-0").Networks = map[string]docker.ContainerNetwork{
		"nebulactrl-env-aaaaaaaa": {IP: "10.0.0.5", Aliases: []string{"nc-svc-99999999"}},
	}
	f.mu.Unlock()
	m.Refresh(context.Background())
	require.Equal(t, 1, f.count("copy "))
	require.Contains(t, f.lastServe(), `"TCPForward": "nc-svc-99999999:5432"`)
	require.Zero(t, f.count("create "))
}
