package gitssh

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in       string
		user     string
		host     string
		port     int
		path     string
		repo     string
		hostName string
	}{
		{"git@github.com:org/repo.git", "git", "github.com", 22, "org/repo.git", "org/repo", "github.com"},
		{"github.com:org/repo", "git", "github.com", 22, "org/repo", "org/repo", "github.com"},
		{"deploy@git.example.com:team/sub/app.git", "deploy", "git.example.com", 22, "team/sub/app.git", "team/sub/app", "git.example.com"},
		{"git@[2001:db8::1]:org/repo.git", "git", "2001:db8::1", 22, "org/repo.git", "org/repo", "2001:db8::1"},
		{"ssh://git@github.com/org/repo.git", "git", "github.com", 22, "/org/repo.git", "org/repo", "github.com"},
		{"ssh://github.com/org/repo.git", "git", "github.com", 22, "/org/repo.git", "org/repo", "github.com"},
		{"ssh://git@git.example.com:2222/org/repo.git", "git", "git.example.com", 2222, "/org/repo.git", "org/repo", "[git.example.com]:2222"},
		{"ssh://git@127.0.0.1:2200/app.git", "git", "127.0.0.1", 2200, "/app.git", "app", "[127.0.0.1]:2200"},
		{"ssh://git@[::1]:2200/app.git", "git", "::1", 2200, "/app.git", "app", "[::1]:2200"},
		{"  ssh://git@gitlab.com/g/p.git  ", "git", "gitlab.com", 22, "/g/p.git", "g/p", "gitlab.com"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			ep, err := ParseURL(c.in)
			require.NoError(t, err)
			require.Equal(t, c.user, ep.User)
			require.Equal(t, c.host, ep.Host)
			require.Equal(t, c.port, ep.Port)
			require.Equal(t, c.path, ep.Path)
			require.Equal(t, c.repo, ep.Repo())
			require.Equal(t, c.hostName, ep.HostName())
			require.True(t, IsSSHURL(c.in))
		})
	}
}

func TestParseURLRejects(t *testing.T) {
	cases := map[string]string{
		"":                                  "empty",
		"https://github.com/org/repo.git":   "not an ssh://",
		"git://github.com/org/repo.git":     "not an ssh://",
		"http://x/y":                        "not an ssh://",
		"ssh://git@github.com":              "no repository path",
		"ssh://git@github.com/":             "no repository path",
		"ssh://git:secret@github.com/o/r":   "passwords",
		"ssh://git@github.com:99999/o/r":    "invalid port",
		"ssh://git@github.com:0/o/r":        "invalid port",
		"ssh://git@bad host/o/r":            "invalid URL",
		"ssh://git@-oProxyCommand=x/o/r":    "invalid host",
		"ssh://bad user@host/o/r":           "invalid URL",
		"ssh://git@host/o/../../etc/passwd": "invalid repository path",
		"git@github.com":                    "not an SSH URL",
		"just-a-string":                     "not an SSH URL",
		"git@github.com:":                   "not an SSH URL",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ParseURL(in)
			require.ErrorContains(t, err, want)
			require.False(t, IsSSHURL(in))
		})
	}
}

func TestLooksLikeSSH(t *testing.T) {
	for in, want := range map[string]bool{
		"ssh://git@github.com/o/r":      true,
		"ssh://":                        true,
		"git@github.com:o/r.git":        true,
		"https://github.com/o/r.git":    false,
		"git://github.com/o/r.git":      false,
		"http://localhost:3000/o/r.git": false,
		"":                              false,
	} {
		require.Equal(t, want, LooksLikeSSH(in), in)
	}
}

func TestDeployKeyLinkFor(t *testing.T) {
	cases := []struct{ url, provider, link string }{
		{"git@github.com:acme/shop.git", "github", "https://github.com/acme/shop/settings/keys/new"},
		{"ssh://git@ssh.github.com:443/acme/shop.git", "github", "https://github.com/acme/shop/settings/keys/new"},
		{"git@gitlab.com:acme/group/shop.git", "gitlab", "https://gitlab.com/acme/group/shop/-/settings/repository#js-deploy-keys-settings"},
		{"git@bitbucket.org:acme/shop.git", "bitbucket", "https://bitbucket.org/acme/shop/admin/access-keys/"},
		{"git@codeberg.org:acme/shop.git", "forgejo", "https://codeberg.org/acme/shop/settings/keys"},
		{"git@gitea.com:acme/shop.git", "gitea", "https://gitea.com/acme/shop/settings/keys"},
	}
	for _, c := range cases {
		ep, err := ParseURL(c.url)
		require.NoError(t, err)
		l, ok := DeployKeyLinkFor(ep)
		require.True(t, ok, c.url)
		require.Equal(t, c.provider, l.Provider)
		require.Equal(t, c.link, l.URL)
	}
	// A host that is not a known public instance gets no guessed link.
	ep, err := ParseURL("git@git.example.com:acme/shop.git")
	require.NoError(t, err)
	_, ok := DeployKeyLinkFor(ep)
	require.False(t, ok)
}
