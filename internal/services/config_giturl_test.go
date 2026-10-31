package services

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitURLValidationAcceptsSSHForms(t *testing.T) {
	for _, u := range []string{
		"https://github.com/acme/shop.git",
		"http://localhost:3000/acme/shop.git",
		"git://127.0.0.1:9418/app.git",
		"ssh://git@github.com/acme/shop.git",
		"ssh://git@git.example.com:2222/acme/shop.git",
		"git@github.com:acme/shop.git",
		"git.example.com:acme/shop",
	} {
		c := Config{GitURL: u, GitBranch: "main"}.WithDefaults()
		require.NoError(t, c.Validate(SourceGit), u)
	}
}

func TestGitURLValidationRejectsBadSSHAndOtherSchemes(t *testing.T) {
	cases := map[string]string{
		"ssh://git@github.com":            "no repository path",
		"ssh://git:pw@github.com/a/b.git": "passwords",
		"ssh://git@host:99999/a/b.git":    "invalid port",
		"file:///etc/passwd":              "gitUrl must be",
		"ftp://example.com/a.git":         "gitUrl must be",
		"just-some-text":                  "gitUrl must be",
	}
	for u, want := range cases {
		c := Config{GitURL: u, GitBranch: "main"}.WithDefaults()
		require.ErrorContains(t, c.Validate(SourceGit), want, u)
	}
}
