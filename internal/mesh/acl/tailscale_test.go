package acl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// userPolicy is a tailnet policy with comments, a user's own rules, an outdated
// NebulaCtrl rule (literal tailnet IPs only) and a tagOwners block.
const userPolicy = `// Our tailnet policy. Do not break it.
{
  // who owns which tag
  "tagOwners": {
    "tag:prod": ["autogroup:admin"],
  },
  "acls": [
    // everybody reaches the wiki
    {"action": "accept", "src": ["*"], "dst": ["tag:prod:443"]},
    // a user rule over literal IPs with a proto: never ours
    {"action": "accept", "proto": "tcp", "src": ["100.64.0.50"], "dst": ["100.64.0.51:22"]},
    // an old NebulaCtrl rule of a node that was re-provisioned
    {"action": "accept", "src": ["100.64.0.99"], "dst": ["100.64.0.2:15000"]},
  ],
}
`

func ownedState() State {
	return State{
		Nodes: nodes(), Links: []Link{replLink("l1", env1, "100.64.0.10")}, Exposures: []Exposure{exp("x1", env1, "replication", 15000)},
		Grants: []Grant{{LinkID: "l1", ExposureID: "x1", RefID: "r1", Source: "replica"}},
	}
}

func TestIsOursOwnershipRule(t *testing.T) {
	known := map[string]bool{"100.64.0.10": true, "100.64.0.2": true}
	for _, tc := range []struct {
		name string
		rule Rule
		ours bool
	}{
		{"known ips", Rule{Action: "accept", Src: []string{"100.64.0.10"}, Dst: []string{"100.64.0.2:15000"}}, true},
		{"stale literal tailnet ips", Rule{Action: "accept", Src: []string{"100.64.0.99"}, Dst: []string{"100.64.0.3:15000"}}, true},
		{"a tag", Rule{Action: "accept", Src: []string{"tag:prod"}, Dst: []string{"100.64.0.2:15000"}}, false},
		{"wildcard source", Rule{Action: "accept", Src: []string{"*"}, Dst: []string{"100.64.0.2:15000"}}, false},
		{"public ip", Rule{Action: "accept", Src: []string{"8.8.8.8"}, Dst: []string{"100.64.0.2:15000"}}, false},
		{"member group", Rule{Action: "accept", Src: []string{"autogroup:member"}, Dst: []string{"100.64.0.2:15000"}}, false},
		{"deny", Rule{Action: "deny", Src: []string{"100.64.0.10"}, Dst: []string{"100.64.0.2:15000"}}, false},
	} {
		require.Equal(t, tc.ours, IsOurs(tc.rule, known), tc.name)
	}
}

func TestPatchHuJSONReplacesOnlyOurRulesAndKeepsEverythingElse(t *testing.T) {
	st := ownedState()
	known := map[string]bool{"100.64.0.10": true, "100.64.0.2": true, "100.64.0.1": true}
	out, err := PatchHuJSON([]byte(userPolicy), Generate(st), "tag:nebula-mesh", []string{"autogroup:admin"}, known)
	require.NoError(t, err)
	s := string(out)

	// Comments and the user's rules survive.
	require.Contains(t, s, "// Our tailnet policy. Do not break it.")
	require.Contains(t, s, "// who owns which tag")
	require.Contains(t, s, "// everybody reaches the wiki")
	require.Contains(t, s, `"dst": ["tag:prod:443"]`)
	require.Regexp(t, `"proto":\s+"tcp"`, s, "a rule with a proto is never ours")
	require.Contains(t, s, "100.64.0.51:22")
	// The outdated NebulaCtrl rule is gone, the generated one is in.
	require.NotContains(t, s, "100.64.0.99")
	require.Contains(t, s, "100.64.0.10")
	require.Contains(t, s, "100.64.0.2:15000")
	// The mesh tag is owned; the user's entry stays.
	require.Contains(t, s, "tag:nebula-mesh")
	require.Contains(t, s, "tag:prod")

	golden(t, "tailscale_patched", out)

	// Idempotent: patching the result again changes nothing.
	again, err := PatchHuJSON(out, Generate(st), "tag:nebula-mesh", []string{"autogroup:admin"}, known)
	require.NoError(t, err)
	require.Equal(t, string(out), string(again))
}

func TestPatchHuJSONKeepsAnExistingTagOwnersEntry(t *testing.T) {
	doc := `{"tagOwners": {"tag:nebula-mesh": ["group:ops"]}, "acls": []}`
	out, err := PatchHuJSON([]byte(doc), Generate(ownedState()), "tag:nebula-mesh", []string{"autogroup:admin"}, nil)
	require.NoError(t, err)
	require.Contains(t, string(out), "group:ops")
	require.NotContains(t, string(out), "autogroup:admin", "the user's owners are never replaced")
}

func TestPatchHuJSONCreatesMissingSections(t *testing.T) {
	out, err := PatchHuJSON(nil, Generate(ownedState()), "tag:nebula-mesh", []string{"autogroup:admin"}, nil)
	require.NoError(t, err)
	require.Contains(t, string(out), "tagOwners")
	require.Contains(t, string(out), "100.64.0.2:15000")

	out, err = PatchHuJSON([]byte(`{"hosts": {"a": "1.2.3.4"}}`), Generate(State{}), "tag:nebula-mesh", []string{"autogroup:admin"}, nil)
	require.NoError(t, err)
	require.Contains(t, string(out), `"acls"`)
	require.Contains(t, string(out), `"hosts"`)
}

func TestPatchHuJSONRejectsInvalidPolicy(t *testing.T) {
	_, err := PatchHuJSON([]byte(`{"acls": [`), Generate(State{}), "tag:nebula-mesh", nil, nil)
	require.Error(t, err)
}

func TestContainsAndStaleRules(t *testing.T) {
	p := Generate(ownedState())
	known := map[string]bool{"100.64.0.10": true, "100.64.0.2": true}
	missing, err := ContainsRules([]byte(userPolicy), p)
	require.NoError(t, err)
	require.Len(t, missing, 1, "the generated rule is not in the user's policy")
	stale, err := StaleRules([]byte(userPolicy), p, known)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, []string{"100.64.0.99"}, stale[0].Src)

	patched, err := PatchHuJSON([]byte(userPolicy), p, "tag:nebula-mesh", []string{"autogroup:admin"}, known)
	require.NoError(t, err)
	missing, err = ContainsRules(patched, p)
	require.NoError(t, err)
	require.Empty(t, missing)
	stale, err = StaleRules(patched, p, known)
	require.NoError(t, err)
	require.Empty(t, stale)
	require.False(t, strings.Contains(string(patched), "100.64.0.99"))
}
