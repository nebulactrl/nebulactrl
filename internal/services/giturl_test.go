package services

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitURLWithCredentialsIsRefused(t *testing.T) {
	for _, u := range []string{
		"https://user:tok@github.com/acme/shop.git",
		"https://ghp_token@github.com/acme/shop.git",
		"http://oauth2:glpat@git.example.com/a/b.git",
		"git://u:p@git.example.com/a/b.git",
	} {
		c := Config{GitURL: u, GitBranch: "main"}.WithDefaults()
		err := c.Validate(SourceGit)
		require.ErrorContains(t, err, "must not contain credentials", u)
		require.ErrorContains(t, err, "token field", u)
	}
	// The SSH login is not a credential.
	c := Config{GitURL: "ssh://git@github.com/acme/shop.git", GitBranch: "main"}.WithDefaults()
	require.NoError(t, c.Validate(SourceGit))
}

func TestRedactGitURL(t *testing.T) {
	require.Equal(t, "https://github.com/a/b.git", RedactGitURL("https://user:tok@github.com/a/b.git"))
	require.Equal(t, "Cloning http://git.example.com/a.git failed", RedactGitURL("Cloning http://x-access-token:abc@git.example.com/a.git failed"))
	require.Equal(t, "ssh://git@github.com/a/b.git", RedactGitURL("ssh://git@github.com/a/b.git"))
	require.Equal(t, "https://github.com/a/b.git", RedactGitURL("https://github.com/a/b.git"))
}

func TestSameGitRepo(t *testing.T) {
	require.True(t, SameGitRepo("https://github.com/Acme/Shop.git", "http://github.com/acme/shop"))
	require.True(t, SameGitRepo("https://git.example.com:443/a/b.git", "https://git.example.com/a/b/"))
	require.True(t, SameGitRepo("git@github.com:acme/shop.git", "https://github.com/acme/shop.git"))
	require.False(t, SameGitRepo("http://attacker.example/acme/shop.git", "https://github.com/acme/shop.git"))
	require.False(t, SameGitRepo("https://github.com/acme/other.git", "https://github.com/acme/shop.git"))
	require.False(t, SameGitRepo("https://git.example.com:8443/a/b.git", "https://git.example.com/a/b.git"))
	require.False(t, SameGitRepo("", ""))
}
