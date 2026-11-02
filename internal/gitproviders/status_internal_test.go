package gitproviders

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateForDeploymentMapsEveryDeploymentStatus(t *testing.T) {
	for status, want := range map[string]string{
		"queued":          StatePending,
		"building":        StatePending,
		"releasing":       StatePending,
		"verifying":       StatePending,
		"succeeded":       StateSuccess,
		"failed":          StateFailure,
		"rolled_back":     StateFailure,
		"rollback_failed": StateFailure,
		"canceled":        StateError,
		"something-new":   StatePending,
	} {
		require.Equal(t, want, StateForDeployment(status), status)
	}
}

func TestDescribeDeploymentSaysWhatHappenedAndFallsBackWithoutAMessage(t *testing.T) {
	for _, c := range []struct{ status, message, want string }{
		{"succeeded", "ignored", "Deployed"},
		{"failed", "health check failed", "health check failed"},
		{"failed", "  ", "Deployment failed"},
		{"rolled_back", "port 80 refused", "Rolled back: port 80 refused"},
		{"rolled_back", "", "Rolled back: the new release did not become healthy"},
		{"rollback_failed", "", "Rollback failed: manual recovery is needed"},
		{"rollback_failed", "disk full", "Rollback failed: disk full"},
		{"canceled", "", "Deployment canceled"},
		{"building", "", "Building"},
		{"releasing", "", "Deploying"},
		{"verifying", "", "Deploying"},
		{"queued", "", "Queued"},
	} {
		require.Equal(t, c.want, describeDeployment(c.status, c.message), c.status+"/"+c.message)
	}
}

func TestSmallTextHelpers(t *testing.T) {
	require.Equal(t, "bffeb74", shortSHA("bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"))
	require.Equal(t, "abc", shortSHA("abc"))
	require.Equal(t, "x", orDefault("  x ", "d"))
	require.Equal(t, "d", orDefault("   ", "d"))

	require.Equal(t, "abc", truncate("abc", 3))
	require.Equal(t, "ab…", truncate("abcdef", 3), "the cut keeps the limit, with an ellipsis")
	require.Equal(t, "héé…", truncate("héééé", 4), "multi-byte characters are not split")

	require.Equal(t, "Fix the bug", firstLine("  Fix the bug\n\nlong body"))
	require.Equal(t, "one", firstLine("one"))
	require.Equal(t, "git.example.com:8443", hostOf("https://git.example.com:8443/x"))
	require.Empty(t, hostOf("://bad"))
}

func TestAPIClientScrubsCredentialsFromErrors(t *testing.T) {
	c := &apiClient{secrets: []string{"glpat-supersecret", "tiny"}}

	var ae *apiError
	err := c.scrub(&apiError{Status: 401, Message: "bad token glpat-supersecret for tiny"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 401, ae.Status)
	require.Equal(t, "bad token •••••• for tiny", ae.Message, "short secrets are left alone: they would mangle ordinary words")

	err = c.scrub(errors.New("dial https://oauth2:glpat-supersecret@host failed"))
	require.EqualError(t, err, "dial https://oauth2:••••••@host failed")

	plain := errors.New("connection refused")
	require.Same(t, plain, c.scrub(plain), "an error without a secret is returned as is")
	require.NoError(t, c.scrub(nil))
}

func TestBitbucketCloudAndDataCenterURLs(t *testing.T) {
	d := bitbucketDriver{}
	require.Equal(t, "https://bitbucket.org/team/webapp.git", d.cloneURL("https://bitbucket.org/", "team/webapp"))
	require.Equal(t, "https://bitbucket.org/team/webapp", d.repoURL("https://bitbucket.org", "team/webapp"))

	// Data Center: project keys are upper case in the UI, the clone path is lower case.
	require.Equal(t, "https://git.corp.example.com/scm/team/webapp.git", d.cloneURL("https://git.corp.example.com", "TEAM/webapp"))
	require.Equal(t, "https://git.corp.example.com/projects/TEAM/repos/webapp", d.repoURL("https://git.corp.example.com", "TEAM/webapp"))
	require.Equal(t, "https://git.corp.example.com/lonely", d.repoURL("https://git.corp.example.com", "lonely"))

	require.Equal(t, Bitbucket, d.provider())
	require.Equal(t, "x-token-auth", d.cloneUsername())
	require.Equal(t, "https://git.corp.example.com", d.apiBase("https://git.corp.example.com/"))
	require.Equal(t, "Bearer t0k", d.authHeader("t0k", AuthToken))
}

func TestGiteaAndGitHubDriverBasics(t *testing.T) {
	g := giteaDriver{}
	require.Equal(t, "oauth2", g.cloneUsername())
	require.Equal(t, "token abc", g.authHeader("abc", AuthToken), "personal access tokens use the token scheme")
	require.Equal(t, "Bearer abc", g.authHeader("abc", AuthOAuth))
	require.Equal(t, "https://git.example.com/api/v1", g.apiBase("https://git.example.com/"))

	require.Equal(t, GitHub, githubDriver{}.provider())
	require.Equal(t, "Alice A", gtUser{Name: "Alice A", Username: "a", Login: "al"}.display(), "the full name wins")
	require.Equal(t, "a", gtUser{Username: "a", Login: "al"}.display())
	require.Equal(t, "al", gtUser{Login: "al"}.display())
	require.Empty(t, gtUser{}.display())
}

func TestProviderValid(t *testing.T) {
	for _, p := range Providers {
		require.True(t, p.Valid(), string(p))
	}
	require.False(t, Provider("svn").Valid())
	require.False(t, Provider("").Valid())
}
