package acl

import (
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run with -update")
	require.Equal(t, string(want), string(got))
}

// Fixed ids keep the goldens readable.
const (
	srvA, srvB = "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"
	env1, env2 = "e1e1e1e1-0000-0000-0000-000000000001", "e2e2e2e2-0000-0000-0000-000000000002"
)

func nodes() []Node {
	return []Node{{ServerID: srvA, IPs: []string{"fd7a:115c:a1e0::1", "100.64.0.1"}}, {ServerID: srvB, IPs: []string{"100.64.0.2"}}}
}

func base() Base {
	return Base{
		TagOwners: map[string][]string{"tag:nebula-mesh": {"nebulactrl@"}, "tag:nebula-svc": {"nebulactrl@"}},
		Rules:     []Rule{{Action: "accept", Src: []string{"autogroup:member"}, Dst: []string{"tag:nebula-svc:*"}}},
	}
}

func replLink(id, env, ip string) Link {
	return Link{ID: id, EnvironmentID: env, ServerID: srvA, RemoteServerID: srvB, Purpose: "replication", IPs: []string{ip}, Label: "replication link " + id}
}

func svcLink(id, env, ip string) Link {
	return Link{ID: id, EnvironmentID: env, ServerID: srvA, RemoteServerID: srvB, Purpose: "service", IPs: []string{ip}, Label: "service link " + id}
}

func exp(id, env, purpose string, port int) Exposure {
	return Exposure{ID: id, ServerID: srvB, ServiceID: "5e5e5e5e-" + id, EnvironmentID: env, Purpose: purpose, MeshPort: port}
}

func render(t *testing.T, st State) []byte {
	t.Helper()
	out, err := Generate(st).Document(base())
	require.NoError(t, err)
	return out
}

func TestGoldenEmpty(t *testing.T) {
	st := State{Nodes: nodes()}
	golden(t, "empty", render(t, st))
	require.Empty(t, Generate(st).Rules)
	require.NotNil(t, Generate(st).Rules, "the policy always has an explicit acls array (default deny)")
}

func TestGoldenOneLinkOnePort(t *testing.T) {
	st := State{
		Nodes: nodes(), Links: []Link{replLink("l1", env1, "100.64.0.10")}, Exposures: []Exposure{exp("x1", env1, "replication", 15000)},
		Grants: []Grant{{LinkID: "l1", ExposureID: "x1", RefID: "r1", Source: "replica"}},
	}
	golden(t, "one_link", render(t, st))
	p := Generate(st)
	require.Equal(t, []Rule{{Action: "accept", Src: []string{"100.64.0.10"}, Dst: []string{"100.64.0.2:15000"}}}, p.Rules)
	require.Equal(t, "100.64.0.1", firstIPv4(nodes()[0].IPs), "IPv4 only")
}

func TestGoldenTwoEnvironmentsOnOneServerPair(t *testing.T) {
	st := State{
		Nodes: nodes(),
		Links: []Link{svcLink("l1", env1, "100.64.0.10"), svcLink("l2", env2, "100.64.0.11")},
		Exposures: []Exposure{
			exp("x1", env1, "service", 8080), exp("x2", env2, "service", 8080), exp("x3", env2, "service", 15001),
		},
		Grants: []Grant{
			{LinkID: "l1", ExposureID: "x1", RefID: "c1", Source: "reference"},
			{LinkID: "l2", ExposureID: "x2", RefID: "c2", Source: "edge"},
			{LinkID: "l2", ExposureID: "x3", RefID: "c2", Source: "reference"},
			// A link of environment 1 granted an exposure of environment 2: structurally impossible.
			{LinkID: "l1", ExposureID: "x3", RefID: "c1", Source: "reference"},
		},
	}
	golden(t, "two_envs", render(t, st))
	p := Generate(st)
	require.Len(t, p.Rules, 2)
	for _, r := range p.Rules {
		if r.Src[0] == "100.64.0.10" {
			require.Equal(t, []string{"100.64.0.2:8080"}, r.Dst, "environment 1's link never reaches environment 2's port")
		}
	}
}

func TestGoldenIPChange(t *testing.T) {
	st := State{
		Nodes: nodes(), Links: []Link{replLink("l1", env1, "100.64.0.10")}, Exposures: []Exposure{exp("x1", env1, "replication", 15000)},
		Grants: []Grant{{LinkID: "l1", ExposureID: "x1", RefID: "r1", Source: "replica"}},
	}
	before := render(t, st)
	st.Links[0].IPs = []string{"100.64.0.77"} // re-provisioned: a new tailnet address
	after := render(t, st)
	require.NotEqual(t, string(before), string(after))
	golden(t, "ip_change", after)
	require.Contains(t, string(after), "100.64.0.77")
	require.NotContains(t, string(after), "100.64.0.10")
}

func TestGoldenGrantRemoval(t *testing.T) {
	st := State{
		Nodes: nodes(), Links: []Link{svcLink("l1", env1, "100.64.0.10")},
		Exposures: []Exposure{exp("x1", env1, "service", 8080), exp("x2", env1, "service", 5432)},
		Grants: []Grant{
			{LinkID: "l1", ExposureID: "x1", RefID: "c1", Source: "reference"},
			{LinkID: "l1", ExposureID: "x2", RefID: "c1", Source: "reference"},
		},
	}
	require.Equal(t, []string{"100.64.0.2:5432", "100.64.0.2:8080"}, Generate(st).Rules[0].Dst, "ports sorted")
	st.Grants = st.Grants[:1]
	golden(t, "grant_removed", render(t, st))
	st.Grants = nil
	require.Empty(t, Generate(st).Rules, "a link without a grant opens nothing")
}

// A replication link and a service link between the same two servers produce two
// rules with two different source addresses, and neither can reach the other's port.
func TestGoldenServiceAndReplicationLinksOnOnePair(t *testing.T) {
	st := State{
		Nodes: nodes(),
		Links: []Link{replLink("lr", env1, "100.64.0.10"), svcLink("ls", env1, "100.64.0.20")},
		Exposures: []Exposure{
			exp("xr", env1, "replication", 15000), exp("xs", env1, "service", 8080),
		},
		Grants: []Grant{
			{LinkID: "lr", ExposureID: "xr", RefID: "replica", Source: "replica"},
			{LinkID: "ls", ExposureID: "xs", RefID: "consumer", Source: "reference"},
			{LinkID: "ls", ExposureID: "xr", RefID: "consumer", Source: "reference"}, // wrong purpose: ignored
			{LinkID: "lr", ExposureID: "xs", RefID: "replica", Source: "replica"},    // wrong purpose: ignored
		},
	}
	golden(t, "service_and_replication", render(t, st))
	p := Generate(st)
	require.Len(t, p.Rules, 2)
	require.NotEqual(t, p.Rules[0].Src, p.Rules[1].Src)
	for _, r := range p.Rules {
		require.Len(t, r.Dst, 1, "each link reaches exactly its own purpose's port")
	}
}

func TestExposureOnAnotherServerThanTheLinkTargetIsIgnored(t *testing.T) {
	x := exp("x1", env1, "replication", 15000)
	x.ServerID = srvA
	st := State{
		Nodes: nodes(), Links: []Link{replLink("l1", env1, "100.64.0.10")}, Exposures: []Exposure{x},
		Grants: []Grant{{LinkID: "l1", ExposureID: "x1", RefID: "r", Source: "replica"}},
	}
	require.Empty(t, Generate(st).Rules)
}

func TestGenerateIsDeterministicUnderShuffling(t *testing.T) {
	st := State{
		Nodes: nodes(),
		Links: []Link{
			svcLink("l1", env1, "100.64.0.10"), svcLink("l2", env2, "100.64.0.11"), replLink("l3", env1, "100.64.0.12"), replLink("l4", env2, "100.64.0.13"),
		},
		Exposures: []Exposure{exp("x1", env1, "service", 8080), exp("x2", env2, "service", 8080), exp("x3", env1, "replication", 15000), exp("x4", env2, "replication", 15001)},
		Grants: []Grant{
			{LinkID: "l1", ExposureID: "x1", RefID: "c1", Source: "reference"}, {LinkID: "l1", ExposureID: "x1", RefID: "c2", Source: "edge"},
			{LinkID: "l2", ExposureID: "x2", RefID: "c3", Source: "reference"}, {LinkID: "l3", ExposureID: "x3", RefID: "r1", Source: "replica"},
			{LinkID: "l4", ExposureID: "x4", RefID: "r2", Source: "replica"},
		},
	}
	want := string(render(t, st))
	wantSnippet := Generate(st).Snippet("tag:nebula-mesh", []string{"autogroup:admin"})
	rng := rand.New(rand.NewPCG(7, 7))
	for range 50 {
		s := st
		s.Nodes = append([]Node(nil), st.Nodes...)
		s.Links = append([]Link(nil), st.Links...)
		s.Exposures = append([]Exposure(nil), st.Exposures...)
		s.Grants = append([]Grant(nil), st.Grants...)
		rng.Shuffle(len(s.Nodes), func(a, b int) { s.Nodes[a], s.Nodes[b] = s.Nodes[b], s.Nodes[a] })
		rng.Shuffle(len(s.Links), func(a, b int) { s.Links[a], s.Links[b] = s.Links[b], s.Links[a] })
		rng.Shuffle(len(s.Exposures), func(a, b int) { s.Exposures[a], s.Exposures[b] = s.Exposures[b], s.Exposures[a] })
		rng.Shuffle(len(s.Grants), func(a, b int) { s.Grants[a], s.Grants[b] = s.Grants[b], s.Grants[a] })
		require.Equal(t, want, string(render(t, s)))
		require.Equal(t, wantSnippet, Generate(s).Snippet("tag:nebula-mesh", []string{"autogroup:admin"}))
	}
}

func TestGoldenSnippet(t *testing.T) {
	st := State{
		Nodes: nodes(), Links: []Link{svcLink("l1", env1, "100.64.0.10")}, Exposures: []Exposure{exp("x1", env1, "service", 8080)},
		Grants: []Grant{{LinkID: "l1", ExposureID: "x1", RefID: "c1", Source: "reference"}},
	}
	golden(t, "snippet", []byte(Generate(st).Snippet("tag:nebula-mesh", []string{"autogroup:admin"})))
}

func TestHash(t *testing.T) {
	a, b := Hash([]byte("x")), Hash([]byte("y"))
	require.Len(t, a, 64)
	require.NotEqual(t, a, b)
	require.True(t, strings.EqualFold(a, a))
}
