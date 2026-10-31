//go:build integration

package integration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// 1. enroll: the shared agent's server is online with facts; reusing its token
// fails; a second server (a connection-only agent, so no second Traefik owner)
// is revoked, its agent exits as revoked and a deploy on it is rejected.
func TestEnroll(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 0) // deploys nothing: no network

	srv := s.WaitServerOnline(30 * time.Second)
	facts := srv.Obj("facts")
	require.NotEmpty(t, facts.Str("os"), "facts: %v", facts)
	require.NotEmpty(t, facts.Str("dockerVersion"), "facts: %v", facts)
	require.NotZero(t, facts.Int("cpus"), "facts: %v", facts)
	require.Equal(t, "ok", srv.Obj("proxy").Str("status"))
	require.Equal(t, s.Agent.Identity.ServerID, s.ServerID)

	// An enrollment token is single-use.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	body, err := json.Marshal(agentprotocol.EnrollRequest{Token: s.EnrollTok, PublicKey: pub, Hostname: "again", Facts: agentprotocol.Facts{OS: "linux"}})
	require.NoError(t, err)
	res, err := http.Post(s.URL()+"/agent/v1/enroll", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode, "reusing an enrollment token")

	// Revoke: the agent is told to go away (close 4401) and exits as revoked.
	srv2, tok2 := s.EnrollmentToken("revocable")
	lite := StartLiteAgent(t, s.URL(), tok2)
	require.Equal(t, srv2, lite.Identity.ServerID)
	s.WaitServer(srv2, 30*time.Second)
	svc := s.NewService(SvcSpec{Name: "pre", ServerID: srv2, Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}})
	r := s.Do("POST", "/servers/"+srv2+"/revoke", nil)
	require.Truef(t, r.Status == http.StatusOK || r.Status == http.StatusNoContent, "revoke: %d %s", r.Status, r.Raw)
	select {
	case <-lite.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("agent did not exit after its server was revoked")
	}
	require.ErrorIs(t, lite.Err(), conn.ErrRevoked)

	r = s.Do("GET", "/servers/"+srv2, nil)
	require.Equal(t, "revoked", r.JSON().Str("status"))

	// A new deploy on a revoked server fails with "server revoked".
	r = s.Do("POST", "/services/"+svc.ID+"/deployments", map[string]any{})
	require.GreaterOrEqual(t, r.Status, 400, string(r.Raw))
	require.Contains(t, strings.ToLower(string(r.Raw)), "server revoked")

	// The shared server is untouched.
	require.Equal(t, "online", s.Do("GET", "/servers/"+s.ServerID, nil).JSON().Str("status"))
}

// 2. image deploy: succeeds, labeled container exists, observed state is healthy.
func TestImageDeploy(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	host := s.Host()
	svc := s.NewService(SvcSpec{Name: "web", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}, Domain: host})

	d := s.DeployOK(svc.ID)
	require.Equal(t, "rolling", d.Str("strategy"))

	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 1)
	c := cs[0]
	require.Equal(t, "true", c.Labels[agentprotocol.LabelManaged])
	require.Equal(t, svc.ID, c.Labels[agentprotocol.LabelService])
	require.Equal(t, d.Str("id"), c.Labels[agentprotocol.LabelDeployment])
	require.Equal(t, d.Str("releaseId"), c.Labels[agentprotocol.LabelRelease])
	require.Equal(t, s.EnvID, c.Labels[agentprotocol.LabelEnvironment])
	require.Equal(t, agentprotocol.ContainerName(svc.Slug, d.Str("id"), 0), c.Name)
	require.Equal(t, "0", c.Labels[agentprotocol.LabelReplica])

	require.True(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		o := s.Service(svc.ID).Obj("observed")
		return o.Str("state") == agentprotocol.StateRunning && o.Str("health") == agentprotocol.HealthHealthy
	}), "service never observed healthy: %v", s.Service(svc.ID).Obj("observed"))

	sv := s.Service(svc.ID)
	require.Equal(t, d.Str("id"), sv.Str("currentDeploymentId"))
	require.False(t, sv.Bool("hasStagedChanges"))
	NewProxyClient(host).WaitBody(t, "/", "v1", 20*time.Second)
}

// 3. bad image: the deployment fails and its logs carry the pull error; nothing runs.
func TestBadImage(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{Name: "nope", Image: app.Registry + "/nonexistent/nope:1"})

	d := s.WaitDeployment(s.Deploy(svc.ID), 2*time.Minute)
	require.Equal(t, "failed", d.Str("status"), "%v", d)
	logs := s.DeploymentLogs(d.Str("id"))
	require.Contains(t, strings.ToLower(logs), "pull failed", logs)
	require.Contains(t, d.Str("statusMessage"), "pull failed")
	require.Empty(t, s.Containers(svc.ID))
}
