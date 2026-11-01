package agentprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildServerKindsAreRegisteredAndPayloadsCarryNoSecrets(t *testing.T) {
	require.True(t, AllowedKinds[KindBuildImage])
	require.True(t, AllowedKinds[KindRegistryCheck])
	require.Equal(t, "build_image", KindBuildImage)
	require.Equal(t, "registry_check", KindRegistryCheck)
	require.Equal(t, "build.push", CapBuildPush)
	require.Equal(t, "push", StepPush)
	require.Equal(t, "registry_push_auth", SecretRegistryPushAuth)

	raw, err := json.Marshal(BuildImagePayload{
		DeploymentID: "d", ServiceID: "s", ProjectID: "p",
		Git:      GitSource{URL: "https://example.com/r.git", CommitSHA: "0123456789abcdef", HasToken: true},
		Platform: "linux/arm64", Repository: "reg.example.com/acme/web", Tag: "r-1a2b3c4d",
		Builder: "nebulactrl-buildkit-0a1b2c3d", BuildArgKeys: []string{"A"}, BuildSecretKeys: []string{"B"}, Insecure: true,
	})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"deploymentId", "serviceId", "projectId", "git", "platform", "repository", "tag", "builder", "buildArgKeys", "buildSecretKeys", "insecure"} {
		require.Contains(t, m, k)
	}
	require.NotContains(t, m, "cacheRef", "empty optional fields are omitted")
	require.NotContains(t, string(raw), "password")
}

func TestBuildResultAndImageSourceRoundTrip(t *testing.T) {
	res := BuildImageResult{Outcome: OutcomeSucceeded, Digest: "sha256:abc", Repository: "r", Tag: "t", Platform: "linux/amd64", SizeBytes: 5,
		Build: &BuildInfo{Method: MethodDockerfile}}
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var back BuildImageResult
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, res, back)

	src := ImageSource{Ref: "reg.example.com/acme/web@sha256:abc", Digest: "sha256:abc", HasRegistryAuth: true,
		BuiltFrom: &BuiltFrom{ReleaseID: "rel", CommitSHA: "0123456", BuildServerID: "srv"}}
	raw, err = json.Marshal(src)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"builtFrom":{"releaseId":"rel","commitSha":"0123456","buildServerId":"srv"}`)
	var s2 ImageSource
	require.NoError(t, json.Unmarshal(raw, &s2))
	require.Equal(t, src, s2)

	raw, _ = json.Marshal(ImageSource{Ref: "x"})
	require.NotContains(t, string(raw), "builtFrom")

	raw, _ = json.Marshal(Facts{Emulation: []string{"linux/arm64"}})
	require.Contains(t, string(raw), `"emulation":["linux/arm64"]`)
	raw, _ = json.Marshal(Facts{})
	require.NotContains(t, string(raw), "emulation")
}
