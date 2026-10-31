package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type scanBuilder struct {
	got   build.EnvScanRequest
	files []agentprotocol.EnvFileScan
	cfg   map[string][]byte
	err   error
}

func (b *scanBuilder) Build(context.Context, build.Request, func(string)) (string, error) {
	return "", errors.New("not used")
}

func (b *scanBuilder) ScanEnv(_ context.Context, req build.EnvScanRequest) (build.EnvScanOutcome, error) {
	b.got = req
	return build.EnvScanOutcome{Commit: "abc1234def", Files: b.files, ConfigFiles: b.cfg}, b.err
}

func envScanCommand(t *testing.T, p agentprotocol.EnvScanPayload, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-1", Kind: agentprotocol.KindEnvScan, Payload: raw, Secrets: secrets}
}

func TestEnvScanReturnsFilesAndPassesTheSendTimeToken(t *testing.T) {
	sb := &scanBuilder{files: []agentprotocol.EnvFileScan{{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "A", Value: "1"}}}}}
	e := &Engine{cfg: Config{Builder: sb}}
	res := e.HandleCommand(context.Background(), envScanCommand(t,
		agentprotocol.EnvScanPayload{GitURL: "https://git.example.com/acme/shop.git", Branch: "main", BuildContext: "api", HasToken: true},
		map[string]string{agentprotocol.SecretGitToken: "tok-sekret-12345", agentprotocol.SecretGitUsername: "git-user"}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.EnvScanResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, "abc1234def", out.Commit)
	require.Len(t, out.Files, 1)
	require.Equal(t, build.EnvScanRequest{
		GitURL: "https://git.example.com/acme/shop.git", Branch: "main", BuildContext: "api", Token: "tok-sekret-12345", TokenUser: "git-user",
	}, sb.got)
}

func TestEnvScanIgnoresTokensTheCommandDoesNotDeclare(t *testing.T) {
	sb := &scanBuilder{}
	e := &Engine{cfg: Config{Builder: sb}}
	res := e.HandleCommand(context.Background(), envScanCommand(t,
		agentprotocol.EnvScanPayload{GitURL: "https://git.example.com/acme/shop.git"},
		map[string]string{agentprotocol.SecretGitToken: "stray"}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Empty(t, sb.got.Token, "an anonymous scan stays anonymous")
}

func TestEnvScanReturnsRequestedConfigFilesAndRefusesBadPaths(t *testing.T) {
	sb := &scanBuilder{cfg: map[string][]byte{"nebula.toml": []byte("schema = 1\n")}}
	e := &Engine{cfg: Config{Builder: sb}}
	res := e.HandleCommand(context.Background(), envScanCommand(t, agentprotocol.EnvScanPayload{
		GitURL: "https://git.example.com/acme/shop.git", Branch: "main", ConfigPaths: []string{"nebula.toml"}, ConfigOnly: true,
	}, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.EnvScanResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, map[string][]byte{"nebula.toml": []byte("schema = 1\n")}, out.ConfigFiles)
	require.Equal(t, []string{"nebula.toml"}, sb.got.ConfigPaths)
	require.True(t, sb.got.ConfigOnly)

	for _, p := range []agentprotocol.EnvScanPayload{
		{GitURL: "https://x/y.git", ConfigPaths: []string{"../secret"}},
		{GitURL: "https://x/y.git", ConfigPaths: []string{"/etc/passwd"}},
		{GitURL: "https://x/y.git", ConfigPaths: make([]string, agentprotocol.ScanMaxConfigPaths+1)},
	} {
		sb.got = build.EnvScanRequest{}
		res := e.HandleCommand(context.Background(), envScanCommand(t, p, nil))
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
		require.Empty(t, sb.got.GitURL, "nothing is fetched for a refused payload")
	}
}

func TestEnvScanErrorsAreRedacted(t *testing.T) {
	sb := &scanBuilder{err: errors.New("clone https://x-access-token:tok-sekret-12345@git.example.com failed")}
	e := &Engine{cfg: Config{Builder: sb}}
	res := e.HandleCommand(context.Background(), envScanCommand(t,
		agentprotocol.EnvScanPayload{GitURL: "https://git.example.com/acme/shop.git", HasToken: true},
		map[string]string{agentprotocol.SecretGitToken: "tok-sekret-12345"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.NotContains(t, res.Error, "tok-sekret-12345")
}

func TestEnvScanNeedsABuilderAndAValidPayload(t *testing.T) {
	res := (&Engine{}).HandleCommand(context.Background(), envScanCommand(t, agentprotocol.EnvScanPayload{GitURL: "https://x/y.git"}, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "builder not configured")

	e := &Engine{cfg: Config{Builder: &scanBuilder{}}}
	res = e.HandleCommand(context.Background(), agentprotocol.Command{ID: "c", Kind: agentprotocol.KindEnvScan, Payload: []byte("{")})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "invalid env_scan payload")
}
