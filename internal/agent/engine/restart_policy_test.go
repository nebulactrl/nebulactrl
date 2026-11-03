package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRestartPolicyMapsToTheDockerSpec(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		retries      int
		wantPolicy   string
		wantRetries  int
	}{
		{"empty is always", "", 0, "unless-stopped", 0},
		{"always", agentprotocol.RestartAlways, 0, "unless-stopped", 0},
		{"on-failure defaults to 10 retries", agentprotocol.RestartOnFailure, 0, "on-failure", 10},
		{"on-failure with a cap", agentprotocol.RestartOnFailure, 3, "on-failure", 3},
		{"never", agentprotocol.RestartNever, 0, "no", 0},
		{"a stray cap never leaks to always", agentprotocol.RestartAlways, 5, "unless-stopped", 0},
		{"a stray cap never leaks to never", agentprotocol.RestartNever, 5, "no", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil)
			p := basePayload()
			p.RestartPolicy, p.RestartMaxRetries = tc.policy, tc.retries
			res, dr := r.deploy(p, nil)
			require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
			spec := r.dk.created[0]
			require.Equal(t, tc.wantPolicy, spec.RestartPolicy)
			require.Equal(t, tc.wantRetries, spec.RestartMaxRetries)
		})
	}
}

func TestRestartPolicyIsKeptByAReplicaAddedFromTheSpecLabel(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.RestartPolicy, p.RestartMaxRetries = agentprotocol.RestartOnFailure, 4
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	// The control plane wants a second replica; the agent rebuilds the spec
	// from the first replica's label.
	require.NoError(t, r.eng.Reconcile(context.Background(), desiredFor(p, 2)))
	r.eng.bg.Wait()

	require.Len(t, r.dk.created, 2)
	added := r.dk.created[1]
	require.Equal(t, replicaName(1), added.Name)
	require.Equal(t, "on-failure", added.RestartPolicy)
	require.Equal(t, 4, added.RestartMaxRetries)
}

func TestRestartPolicyRoundTripsThroughTheSpecLabel(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.RestartPolicy, p.RestartMaxRetries = agentprotocol.RestartNever, 0
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	info, err := r.dk.ContainerInspect(context.Background(), replicaName(0))
	require.NoError(t, err)
	got, ok := payloadFromContainer(info)
	require.True(t, ok)
	require.Equal(t, agentprotocol.RestartNever, got.RestartPolicy)

	// The default policy leaves no trace in the label.
	require.NotContains(t, specLabelOf(basePayload()), "restartPolicy")
}
