package engine

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	tnKey      = "tskey-auth-ENGINETEST-SECRET-123456"
	tnOnline   = `{"BackendState":"Running","Self":{"ID":"nNODE1","DNSName":"web-prod.tail1.ts.net.","TailscaleIPs":["100.64.0.9"]}}`
	tnNeedsLog = `{"BackendState":"NeedsLogin"}`
	tnCtr      = "nc-ts-5e5e5e5e"
)

func tailnetSpec() *agentprotocol.ServiceTailnetSpec {
	return &agentprotocol.ServiceTailnetSpec{
		Mode: agentprotocol.TailnetEnsure, Provider: agentprotocol.TailnetProviderTailscale, Hostname: "web-prod",
		Web: true, Port: 80, Provision: true, IdentityHash: "id1", Tags: []string{"tag:nebula-svc"},
	}
}

func tailnetRig(t *testing.T, status string) *rig {
	t.Helper()
	r := newRig(t, nil)
	r.eng.tn.WaitRunning, r.eng.tn.Poll = 30*time.Millisecond, time.Millisecond
	r.dk.onExec = func(_ string, argv []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		if strings.Join(argv, " ") == "tailscale status --json --peers=false" {
			_, _ = io.WriteString(stdout, status)
		}
		return 0, nil
	}
	return r
}

func tailnetSecrets() map[string]string {
	return map[string]string{agentprotocol.SecretServiceTailnetKey: tnKey}
}

func (r *rig) hasEvent(status string) bool {
	return strings.Contains(strings.Join(r.snd.eventSteps(), ","), "tailnet:"+status)
}

func TestDeployEnsuresTheSidecarAfterTheAppIsLive(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	p := basePayload()
	p.Tailnet = tailnetSpec()

	res, dr := r.deploy(p, tailnetSecrets())
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.NotNil(t, dr.Tailnet)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, dr.Tailnet.State, dr.Tailnet.Message)
	require.Equal(t, "web-prod.tail1.ts.net", dr.Tailnet.FQDN)
	require.Equal(t, "nNODE1", dr.Tailnet.DeviceID)
	require.True(t, dr.Tailnet.Provisioned)

	// App first, sidecar second; the sidecar carries the key and the tailnet label.
	require.Len(t, r.dk.created, 2)
	app, side := r.dk.created[0], r.dk.created[1]
	require.Equal(t, newCtr, app.Name)
	require.Equal(t, tnCtr, side.Name)
	require.Contains(t, side.Env, "TS_AUTHKEY="+tnKey)
	require.Equal(t, svcID, side.Labels[agentprotocol.LabelTailnet])
	_, isReplica := side.Labels[agentprotocol.LabelService]
	require.False(t, isReplica)
	require.Equal(t, agentprotocol.EnvNetwork(envID), side.Networks[0].Network)

	require.True(t, r.hasEvent("started"))
	require.True(t, r.hasEvent("succeeded"))

	// The key never reaches logs, events or the result.
	require.NotContains(t, r.snd.allLogText(), tnKey)
	require.NotContains(t, dr.Message, tnKey)
	for _, e := range r.snd.events {
		require.NotContains(t, e.Message, tnKey)
	}
	raw, _ := json.Marshal(dr)
	require.NotContains(t, string(raw), tnKey)

	// The next observed report carries the sidecar state.
	r.eng.reportObserved(context.Background())
	require.Len(t, r.snd.observed, 1)
	var seen *agentprotocol.ServiceTailnetStatus
	for _, o := range r.snd.observed[0].Services {
		if o.ServiceID == svcID {
			seen = o.Tailnet
		}
	}
	require.NotNil(t, seen)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, seen.State)
}

func TestSidecarFailureNeverFailsTheDeployment(t *testing.T) {
	r := tailnetRig(t, tnNeedsLog)
	p := basePayload()
	p.Tailnet = tailnetSpec()

	res, dr := r.deploy(p, tailnetSecrets())
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, "the app is live; a tailnet problem is a warning")
	require.True(t, running(t, r.dk, newCtr))
	require.NotNil(t, dr.Tailnet)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, dr.Tailnet.State)
	require.Contains(t, dr.Message, "Warning: tailnet needs_attention")
	require.True(t, r.hasEvent("failed"))
	require.NotContains(t, dr.Message, tnKey)
}

func TestDeployWithoutATailnetSpecTouchesNoSidecar(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	_, dr := r.deploy(basePayload(), nil)
	require.Nil(t, dr.Tailnet)
	require.Len(t, r.dk.created, 1)
	require.False(t, r.hasEvent("started"))
}

func TestDisablingRemovesTheSidecarWithTheNextDeploy(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	p := basePayload()
	p.Tailnet = tailnetSpec()
	_, _ = r.deploy(p, tailnetSecrets())
	_, ok := r.dk.state(tnCtr)
	require.True(t, ok)

	p2 := basePayload()
	p2.DeploymentID = "d3d3d3d3-0000-4000-8000-000000000003"
	p2.Tailnet = &agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetRemove, ServiceID: svcID}
	res, dr := r.deploy(p2, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.NotNil(t, dr.Tailnet)
	require.True(t, dr.Tailnet.Removed)
	_, ok = r.dk.state(tnCtr)
	require.False(t, ok, "sidecar removed")
}

func TestRemoveServiceTakesTheSidecarWithIt(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	p := basePayload()
	p.Tailnet = tailnetSpec()
	_, _ = r.deploy(p, tailnetSecrets())

	raw, _ := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID, StopGraceSeconds: 1})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rm", Kind: agentprotocol.KindRemoveService, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	_, ok := r.dk.state(tnCtr)
	require.False(t, ok)
}

func TestSyncAndRemoveTailnetCommands(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	spec := tailnetSpec()
	spec.ServiceID, spec.ServiceSlug, spec.EnvironmentID = svcID, "web", envID
	raw, _ := json.Marshal(agentprotocol.SyncTailnetPayload{Tailnet: *spec})

	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{
		ID: "sync", Kind: agentprotocol.KindSyncTailnet, Payload: raw, Secrets: tailnetSecrets(),
	})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var st agentprotocol.ServiceTailnetStatus
	require.NoError(t, json.Unmarshal(res.Data, &st))
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State)
	require.True(t, st.Provisioned)
	require.NotContains(t, string(res.Data), tnKey)
	_, ok := r.dk.state(tnCtr)
	require.True(t, ok)

	// A bad payload fails the command instead of touching Docker.
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "bad", Kind: agentprotocol.KindSyncTailnet, Payload: json.RawMessage(`{"tailnet":{"mode":"remove"}}`)})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)

	rm, _ := json.Marshal(agentprotocol.RemoveTailnetPayload{ServiceID: svcID})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rm", Kind: agentprotocol.KindRemoveTailnet, Payload: rm})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.NoError(t, json.Unmarshal(res.Data, &st))
	require.True(t, st.Removed)
	_, ok = r.dk.state(tnCtr)
	require.False(t, ok)
}

func TestSidecarIsNotAnOrphanAndNotAReplica(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	p := basePayload()
	p.Tailnet = tailnetSpec()
	_, _ = r.deploy(p, tailnetSecrets())

	// An old sidecar must survive the orphan sweep (no service label) and never be counted as a replica.
	r.eng.cfg.OrphanMinAge = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, r.eng.sweepOrphans(context.Background()))
	_, ok := r.dk.state(tnCtr)
	require.True(t, ok)
	reps, err := r.eng.containersOf(context.Background(), svcID, true)
	require.NoError(t, err)
	for _, c := range reps {
		require.NotEqual(t, tnCtr, c.Name)
	}
}

func TestKindsAreAllowed(t *testing.T) {
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindSyncTailnet])
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindRemoveTailnet])
}
