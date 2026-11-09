package engine

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const ownerVol = "nc-vol-5e5e5e5e-dist"

// ownerExec answers the owner helper: stat reports current ("uid:gid") and
// chown exits with chownCode. Every exec's argv is recorded in *calls.
func ownerExec(current string, chownCode int, calls *[][]string) func(string, []string, docker.ExecSpec, io.Writer, io.Writer) (int, error) {
	return func(_ string, argv []string, _ docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
		*calls = append(*calls, slices.Clone(argv))
		switch argv[0] {
		case "stat":
			_, _ = io.WriteString(stdout, current+"\n")
		case "chown":
			if chownCode != 0 {
				_, _ = io.WriteString(stderr, "chown: /v: Operation not permitted\n")
			}
			return chownCode, nil
		}
		return 0, nil
	}
}

func chowns(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if c[0] == "chown" {
			out = append(out, c)
		}
	}
	return out
}

func TestVolumeOwnerChownDecision(t *testing.T) {
	tests := []struct {
		name    string
		owner   string
		current string
		want    []string // argv of the chown, nil when none must run
	}{
		{"no owner never touches the volume", "", "0:0", nil},
		{"root-owned volume is chowned to uid:gid", "10001:10001", "0:0", []string{"chown", "10001:10001", "/v"}},
		{"already owned by uid:gid", "10001:10001", "10001:10001", nil},
		{"only the gid differs", "10001:10001", "10001:0", []string{"chown", "10001:10001", "/v"}},
		{"only the uid differs", "10001:10001", "0:10001", []string{"chown", "10001:10001", "/v"}},
		{"uid form chowns a differing uid", "10001", "0:0", []string{"chown", "10001", "/v"}},
		{"uid form ignores the group", "10001", "10001:0", nil},
		{"root is a valid owner", "0:0", "10001:10001", []string{"chown", "0:0", "/v"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dk := newFakeDocker()
			var calls [][]string
			dk.onExec = ownerExec(tc.current, 0, &calls)

			mounts, err := ensureVolumeMounts(context.Background(), dk, svcID,
				[]agentprotocol.VolumeMount{{Name: ownerVol, MountPath: "/dist", Owner: tc.owner}}, nil)
			require.NoError(t, err)
			require.Equal(t, []docker.VolumeMount{{Volume: ownerVol, Target: "/dist"}}, mounts)

			if tc.owner == "" {
				require.Empty(t, dk.created, "no helper for a volume without an owner")
				require.Empty(t, calls)
				return
			}
			require.Len(t, dk.created, 1)
			require.Equal(t, [][]string{{"stat", "-c", "%u:%g", "/v"}}, calls[:1], "the current owner is read first")
			if tc.want == nil {
				require.Empty(t, chowns(calls))
			} else {
				require.Equal(t, [][]string{tc.want}, chowns(calls))
				require.NotContains(t, tc.want, "-R", "the chown is never recursive")
			}
			require.Empty(t, dk.containers, "the helper is removed")
		})
	}
}

func TestVolumeOwnerHelperIsConfined(t *testing.T) {
	dk := newFakeDocker()
	var calls [][]string
	dk.onExec = ownerExec("0:0", 0, &calls)

	_, err := ensureVolumeMounts(context.Background(), dk, svcID,
		[]agentprotocol.VolumeMount{{Name: ownerVol, MountPath: "/dist", Owner: "10001:10001"}}, nil)
	require.NoError(t, err)

	require.Len(t, dk.created, 1)
	h := dk.created[0]
	require.Equal(t, agentprotocol.ResticImage, h.Image, "the pinned helper image")
	require.Contains(t, dk.pulled, agentprotocol.ResticImage, "pulled when missing")
	require.Equal(t, agentprotocol.VolumeOwnerHelperPrefix+ownerVol, h.Name)
	require.Equal(t, "none", h.NetworkMode)
	require.Empty(t, h.Networks)
	require.Equal(t, []string{"ALL"}, h.CapDrop)
	require.Equal(t, []string{"CHOWN"}, h.CapAdd)
	require.True(t, h.ReadonlyRootfs)
	require.Equal(t, []docker.VolumeMount{{Volume: ownerVol, Target: "/v"}}, h.Mounts)
	require.Equal(t, agentprotocol.HelperVolumeOwner, h.Labels[agentprotocol.LabelHelper])
	require.NotContains(t, h.Labels, agentprotocol.LabelManaged, "the orphan sweep and cleanup must leave the helper alone")
}

func TestVolumeOwnerFailureNamesTheVolume(t *testing.T) {
	dk := newFakeDocker()
	var calls [][]string
	dk.onExec = ownerExec("0:0", 1, &calls)

	_, err := ensureVolumeMounts(context.Background(), dk, svcID,
		[]agentprotocol.VolumeMount{{Name: ownerVol, MountPath: "/dist", Owner: "10001:10001"}}, nil)
	require.Error(t, err)
	for _, want := range []string{ownerVol, "/dist", "10001:10001", "Operation not permitted"} {
		require.Contains(t, err.Error(), want)
	}
	require.Empty(t, dk.containers, "the helper is removed after a failure too")
}

func TestVolumeOwnerFailsTheDeployment(t *testing.T) {
	r := newRig(t, nil)
	var calls [][]string
	r.dk.onExec = ownerExec("0:0", 1, &calls)
	p := basePayload()
	p.Volumes = []agentprotocol.VolumeMount{{Name: ownerVol, MountPath: "/dist", Owner: "10001:10001"}}

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.True(t, strings.Contains(dr.Message, ownerVol), dr.Message)
	require.Equal(t, []string{agentprotocol.VolumeOwnerHelperPrefix + ownerVol}, r.dk.started, "only the helper ran: the app container never starts on a volume it cannot write to")
}

func TestVolumeOwnerOfStatOutput(t *testing.T) {
	uid, gid, err := volumeOwnerOf("10001:10002\n")
	require.NoError(t, err)
	require.Equal(t, [2]int{10001, 10002}, [2]int{uid, gid})
	for _, bad := range []string{"", "10001", "a:b", "1:", ":1"} {
		_, _, err := volumeOwnerOf(bad)
		require.Error(t, err, bad)
	}
}
