package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	sshTestURL  = "ssh://git@git.example.com/acme/shop.git"
	sshTestSHA  = "0123456789abcdef0123456789abcdef01234567"
	sshTestPin  = "git.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	sshTestPEM  = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\nQyNTUxOQAAACDjKqp5Fc65tEnRulDqKiy7Gm4B-canary-key-body\n-----END OPENSSH PRIVATE KEY-----\n"
	sshTestBody = "QyNTUxOQAAACDjKqp5Fc65tEnRulDqKiy7Gm4B-canary-key-body"
)

func TestSSHGitSourcePassesTheDeployKeyAndPinnedHostKeysToTheBuilder(t *testing.T) {
	r := newRig(t, nil)
	// A build tool that echoes the key (a misbehaving Dockerfile) must not leak it.
	r.bld.out = []string{"cloning", "key line: " + sshTestBody}
	p := basePayload()
	p.Image = nil
	p.Git = &agentprotocol.GitSource{
		URL: sshTestURL, CommitSHA: sshTestSHA, DockerfilePath: "Dockerfile", BuildContext: ".",
		GitSSH: agentprotocol.GitSSH{HasSSHKey: true, KnownHosts: []string{sshTestPin}},
	}
	res, dr := r.deploy(p, map[string]string{agentprotocol.SecretGitSSHKey: sshTestPEM})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.bld.calls, 1)
	require.Equal(t, sshTestPEM, r.bld.calls[0].SSHKey)
	require.Equal(t, []string{sshTestPin}, r.bld.calls[0].KnownHosts)
	require.Empty(t, r.bld.calls[0].Token, "the HTTPS token does not apply to an SSH URL")

	require.NotContains(t, r.snd.allLogText(), sshTestBody)
	require.Contains(t, r.snd.allLogText(), "cloning")
}

func TestSSHGitSourceWithoutADeployKeyFailsBeforeBuilding(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Image = nil
	p.Git = &agentprotocol.GitSource{URL: sshTestURL, CommitSHA: sshTestSHA, DockerfilePath: "Dockerfile", BuildContext: "."}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "deploy key")
	require.Empty(t, r.bld.calls)
}
