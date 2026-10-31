package gitproviders

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The payloads in testdata follow the providers' documented webhook examples:
//   GitHub  https://docs.github.com/en/webhooks/webhook-events-and-payloads#push
//   GitLab  https://docs.gitlab.com/user/project/integrations/webhook_events/#push-events
//   Gitea   https://docs.gitea.com/usage/webhooks
//   Bitbucket Cloud https://support.atlassian.com/bitbucket-cloud/docs/event-payloads/#Push
//   Bitbucket DC https://confluence.atlassian.com/bitbucketserver/event-payload-938025882.html

func payload(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

const secret = "whsec_test_0123456789"

func TestVerifyGitHubSignature(t *testing.T) {
	d := githubDriver{}
	body := payload(t, "github_push.json")
	sig := "sha256=" + hmacSHA256Hex(secret, body)

	require.True(t, d.verify(hdr("X-Hub-Signature-256", sig), body, secret))

	tampered := []byte(strings.Replace(string(body), "Fix API pagination", "Steal the keys", 1))
	require.False(t, d.verify(hdr("X-Hub-Signature-256", sig), tampered, secret), "a tampered body fails")
	require.False(t, d.verify(hdr("X-Hub-Signature-256", sig), body, "other-secret"), "another secret fails")
	require.False(t, d.verify(hdr(), body, secret), "unsigned fails")
	require.False(t, d.verify(hdr("X-Hub-Signature-256", sig), body, ""), "an empty secret never verifies")
	require.False(t, d.verify(hdr("X-Hub-Signature", "sha1=abcdef"), body, secret), "the legacy SHA-1 header alone is not accepted")
	require.False(t, d.verify(hdr("X-Hub-Signature-256", "sha256=zz"), body, secret))
}

func TestVerifyGitLabToken(t *testing.T) {
	d := gitlabDriver{}
	body := payload(t, "gitlab_push.json")
	require.True(t, d.verify(hdr("X-Gitlab-Token", secret), body, secret))
	require.False(t, d.verify(hdr("X-Gitlab-Token", secret+"x"), body, secret))
	require.False(t, d.verify(hdr(), body, secret))
	require.False(t, d.verify(hdr("X-Gitlab-Token", ""), body, ""), "an empty token never verifies")
}

func TestVerifyGiteaAndForgejoSignature(t *testing.T) {
	body := payload(t, "gitea_push.json")
	sig := hmacSHA256Hex(secret, body)
	tampered := []byte(strings.Replace(string(body), "acme/shop", "acme/evil", 1))
	for name, headers := range map[string]http.Header{
		"gitea":           hdr("X-Gitea-Signature", sig),
		"forgejo":         hdr("X-Forgejo-Signature", sig),
		"github-compat":   hdr("X-Hub-Signature-256", "sha256="+sig),
		"gitea+forgejo":   hdr("X-Gitea-Signature", sig, "X-Forgejo-Signature", sig),
		"uppercase-value": hdr("X-Gitea-Signature", strings.ToUpper(sig)),
	} {
		for _, p := range []Provider{Gitea, Forgejo} {
			d := driverFor(p)
			require.True(t, d.verify(headers, body, secret), name+"/"+string(p))
			require.False(t, d.verify(headers, tampered, secret), name+"/"+string(p)+" tampered")
			require.False(t, d.verify(headers, body, "nope"), name+"/"+string(p)+" wrong secret")
		}
	}
	require.False(t, giteaDriver{name: Gitea}.verify(hdr(), body, secret))
}

func TestVerifyBitbucketSignature(t *testing.T) {
	d := bitbucketDriver{}
	body := payload(t, "bitbucket_push.json")
	sig := "sha256=" + hmacSHA256Hex(secret, body)
	require.True(t, d.verify(hdr("X-Hub-Signature", sig), body, secret))
	require.False(t, d.verify(hdr("X-Hub-Signature", sig), append([]byte(nil), append(body, ' ')...), secret))
	require.False(t, d.verify(hdr("X-Hub-Signature", "sha1=abc"), body, secret), "only sha256 signatures count")
	require.False(t, d.verify(hdr("X-Hub-Signature", hmacSHA256Hex(secret, body)), body, secret), "the algorithm prefix is required")
	require.False(t, d.verify(hdr(), body, secret))
}

func TestDeliveryIDs(t *testing.T) {
	require.Equal(t, "g-1", githubDriver{}.deliveryID(hdr("X-GitHub-Delivery", "g-1")))
	require.Equal(t, "idem", gitlabDriver{}.deliveryID(hdr("Idempotency-Key", "idem", "X-Gitlab-Event-UUID", "uuid")))
	require.Equal(t, "uuid", gitlabDriver{}.deliveryID(hdr("X-Gitlab-Event-UUID", "uuid")))
	require.Equal(t, "t-1", giteaDriver{name: Gitea}.deliveryID(hdr("X-Gitea-Delivery", "t-1")))
	require.Equal(t, "f-1", giteaDriver{name: Forgejo}.deliveryID(hdr("X-Forgejo-Delivery", "f-1")))
	require.Equal(t, "b-1", bitbucketDriver{}.deliveryID(hdr("X-Request-UUID", "b-1")))
	require.Empty(t, githubDriver{}.deliveryID(hdr()))
}

func TestParseGitHubPush(t *testing.T) {
	p, err := githubDriver{}.parse(hdr("X-GitHub-Event", "push"), payload(t, "github_push.json"))
	require.NoError(t, err)
	require.Equal(t, kindPush, p.kind)
	require.Len(t, p.pushes, 1)
	ev := p.pushes[0]
	require.Equal(t, GitHub, ev.Provider)
	require.Equal(t, "octo-org/monorepo", ev.RepoFullName)
	require.Equal(t, "main", ev.Branch)
	require.Equal(t, "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", ev.CommitSHA)
	require.Equal(t, "Fix API pagination", ev.CommitMessage)
	require.Equal(t, "Mona Lisa", ev.Author)
	require.Equal(t, "https://github.com/octo-org/monorepo.git", ev.CloneURL)
	require.True(t, ev.PathsComplete)
	require.ElementsMatch(t, []string{"apps/web/src/cart.tsx", "apps/web/src/index.tsx", "README.md", "apps/api/old.go", "apps/api/handler.go"}, ev.ChangedPaths,
		"added, modified and removed files of every commit, once")
}

func TestParseGitHubPushIgnores(t *testing.T) {
	body := payload(t, "github_push.json")
	for name, mutate := range map[string]func(string) string{
		"tag":     func(s string) string { return strings.Replace(s, `"refs/heads/main"`, `"refs/tags/v1"`, 1) },
		"deleted": func(s string) string { return strings.Replace(s, `"deleted": false`, `"deleted": true`, 1) },
	} {
		p, err := githubDriver{}.parse(hdr("X-GitHub-Event", "push"), []byte(mutate(string(body))))
		require.NoError(t, err, name)
		require.Equal(t, kindIgnored, p.kind, name)
		require.NotEmpty(t, p.reason, name)
	}
	p, err := githubDriver{}.parse(hdr("X-GitHub-Event", "ping"), []byte(`{"zen":"Keep it logically awesome."}`))
	require.NoError(t, err)
	require.Equal(t, kindPing, p.kind)
	p, err = githubDriver{}.parse(hdr("X-GitHub-Event", "star"), []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, kindIgnored, p.kind)
	_, err = githubDriver{}.parse(hdr("X-GitHub-Event", "push"), []byte(`{not json`))
	require.Error(t, err)
}

func TestParseGitHubPushWithoutCommitsHasNoFileList(t *testing.T) {
	p, err := githubDriver{}.parse(hdr("X-GitHub-Event", "push"),
		[]byte(`{"ref":"refs/heads/feature","after":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678","created":true,"commits":[],"repository":{"full_name":"o/r"}}`))
	require.NoError(t, err)
	require.False(t, p.pushes[0].PathsComplete, "a new branch without commits cannot be filtered by path")
}

func TestParseGitHubPullRequest(t *testing.T) {
	body := payload(t, "github_pull_request.json")
	p, err := githubDriver{}.parse(hdr("X-GitHub-Event", "pull_request"), body)
	require.NoError(t, err)
	require.Equal(t, kindPullRequest, p.kind)
	pr := p.pr
	require.Equal(t, PRsynchronize, pr.Action)
	require.Equal(t, 42, pr.Number)
	require.Equal(t, "octo-org/monorepo", pr.RepoFullName)
	require.Equal(t, "dark-mode", pr.HeadBranch)
	require.Equal(t, "e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e", pr.HeadSHA)
	require.Equal(t, "main", pr.BaseBranch)
	require.Equal(t, "Add dark mode", pr.Title)
	require.Equal(t, "hubot", pr.Author)
	require.True(t, pr.FromFork)
	require.Equal(t, "https://github.com/contributor/monorepo.git", pr.HeadCloneURL)
	require.Equal(t, "https://github.com/octo-org/monorepo.git", pr.BaseCloneURL)
	require.Equal(t, "https://github.com/octo-org/monorepo/pull/42", pr.HTMLURL)

	closedMerged := strings.NewReplacer(`"action": "synchronize"`, `"action": "closed"`, `"merged": false`, `"merged": true`).Replace(string(body))
	p, err = githubDriver{}.parse(hdr("X-GitHub-Event", "pull_request"), []byte(closedMerged))
	require.NoError(t, err)
	require.Equal(t, PRmerged, p.pr.Action, "a merge is reported as merged, never as closed")

	closed := strings.Replace(string(body), `"action": "synchronize"`, `"action": "closed"`, 1)
	p, _ = githubDriver{}.parse(hdr("X-GitHub-Event", "pull_request"), []byte(closed))
	require.Equal(t, PRclosed, p.pr.Action)

	labeled := strings.Replace(string(body), `"action": "synchronize"`, `"action": "labeled"`, 1)
	p, _ = githubDriver{}.parse(hdr("X-GitHub-Event", "pull_request"), []byte(labeled))
	require.Equal(t, kindIgnored, p.kind)

	same := strings.Replace(string(body), `"full_name": "contributor/monorepo"`, `"full_name": "octo-org/monorepo"`, 1)
	p, _ = githubDriver{}.parse(hdr("X-GitHub-Event", "pull_request"), []byte(same))
	require.False(t, p.pr.FromFork)
}

func TestParseGitLabPush(t *testing.T) {
	p, err := gitlabDriver{}.parse(hdr("X-Gitlab-Event", "Push Hook"), payload(t, "gitlab_push.json"))
	require.NoError(t, err)
	require.Equal(t, kindPush, p.kind)
	ev := p.pushes[0]
	require.Equal(t, GitLab, ev.Provider)
	require.Equal(t, "mike/diaspora", ev.RepoFullName)
	require.Equal(t, "master", ev.Branch)
	require.Equal(t, "da1560886d4f094c3e6c9ef40349f7d38b5d27d7", ev.CommitSHA)
	require.Equal(t, "fixed readme", ev.CommitMessage)
	require.Equal(t, "GitLab dev user", ev.Author)
	require.Equal(t, "http://example.com/mike/diaspora.git", ev.CloneURL)
	require.True(t, ev.PathsComplete)
	require.ElementsMatch(t, []string{"CHANGELOG", "app/controller/application.rb"}, ev.ChangedPaths)

	more := strings.Replace(string(payload(t, "gitlab_push.json")), `"total_commits_count": 2`, `"total_commits_count": 57`, 1)
	p, _ = gitlabDriver{}.parse(hdr("X-Gitlab-Event", "Push Hook"), []byte(more))
	require.False(t, p.pushes[0].PathsComplete, "GitLab lists 20 commits at most: a longer push has an incomplete file list")
}

func TestParseGitLabMergeRequest(t *testing.T) {
	body := payload(t, "gitlab_merge_request.json")
	p, err := gitlabDriver{}.parse(hdr("X-Gitlab-Event", "Merge Request Hook"), body)
	require.NoError(t, err)
	require.Equal(t, kindPullRequest, p.kind)
	pr := p.pr
	require.Equal(t, PRsynchronize, pr.Action, "an update with oldrev means new commits")
	require.Equal(t, 7, pr.Number)
	require.Equal(t, "gitlab-org/gitlab-test", pr.RepoFullName)
	require.Equal(t, "ms-viewport", pr.HeadBranch)
	require.Equal(t, "da1560886d4f094c3e6c9ef40349f7d38b5d27d7", pr.HeadSHA)
	require.Equal(t, "master", pr.BaseBranch)
	require.True(t, pr.FromFork)
	require.Equal(t, "http://example.com/contrib/gitlab-test.git", pr.HeadCloneURL)

	noNewCommits := strings.Replace(string(body), `"oldrev": "6bf8d9f3c0a5a5d1a8a3d2c0f7b1f2a1e9d8c7b6",`, ``, 1)
	p, _ = gitlabDriver{}.parse(hdr("X-Gitlab-Event", "Merge Request Hook"), []byte(noNewCommits))
	require.Equal(t, kindIgnored, p.kind, "a title edit is not a synchronize")

	for action, want := range map[string]string{"open": PRopened, "reopen": PRreopened, "close": PRclosed, "merge": PRmerged} {
		b := strings.Replace(string(body), `"action": "update"`, `"action": "`+action+`"`, 1)
		p, _ = gitlabDriver{}.parse(hdr("X-Gitlab-Event", "Merge Request Hook"), []byte(b))
		require.Equal(t, want, p.pr.Action, action)
	}
}

func TestParseGiteaAndForgejo(t *testing.T) {
	for _, tc := range []struct {
		p      Provider
		header string
	}{{Gitea, "X-Gitea-Event"}, {Forgejo, "X-Forgejo-Event"}} {
		d := driverFor(tc.p)
		p, err := d.parse(hdr(tc.header, "push"), payload(t, "gitea_push.json"))
		require.NoError(t, err)
		require.Equal(t, kindPush, p.kind)
		ev := p.pushes[0]
		require.Equal(t, tc.p, ev.Provider)
		require.Equal(t, "acme/shop", ev.RepoFullName)
		require.Equal(t, "main", ev.Branch)
		require.Equal(t, "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", ev.CommitSHA)
		require.Equal(t, "Add services folder", ev.CommitMessage)
		require.Equal(t, "Ada Lovelace", ev.Author)
		require.True(t, ev.PathsComplete)
		require.ElementsMatch(t, []string{"services/billing/main.go", "go.mod"}, ev.ChangedPaths)

		p, err = d.parse(hdr(tc.header, "pull_request"), payload(t, "gitea_pull_request.json"))
		require.NoError(t, err)
		require.Equal(t, PRsynchronize, p.pr.Action, "Gitea's `synchronized`")
		require.Equal(t, 5, p.pr.Number)
		require.False(t, p.pr.FromFork)
		require.Equal(t, "faster", p.pr.HeadBranch)
		require.Equal(t, "0123456789abcdef0123456789abcdef01234567", p.pr.HeadSHA)
		require.Equal(t, "grace", p.pr.Author)
	}
}

func TestParseBitbucket(t *testing.T) {
	p, err := bitbucketDriver{}.parse(hdr("X-Event-Key", "repo:push"), payload(t, "bitbucket_push.json"))
	require.NoError(t, err)
	require.Equal(t, kindPush, p.kind)
	require.Len(t, p.pushes, 1, "the tag push is ignored")
	ev := p.pushes[0]
	require.Equal(t, "team/webapp", ev.RepoFullName)
	require.Equal(t, "main", ev.Branch)
	require.Equal(t, "709d658dc5b6d6afcd46049c2f332ee3f515a67d", ev.CommitSHA)
	require.Equal(t, "New commit 1", ev.CommitMessage)
	require.Equal(t, "Emma Tester", ev.Author)
	require.False(t, ev.PathsComplete, "Bitbucket's payload lists no files")

	p, err = bitbucketDriver{}.parse(hdr("X-Event-Key", "repo:refs_changed"), payload(t, "bitbucket_dc_refs_changed.json"))
	require.NoError(t, err)
	require.Len(t, p.pushes, 1, "the branch deletion is ignored")
	require.Equal(t, "TEAM/webapp", p.pushes[0].RepoFullName)
	require.Equal(t, "master", p.pushes[0].Branch)
	require.Equal(t, "178864a7d521b6f5e720b386b2c2b0ef8563e0dc", p.pushes[0].CommitSHA)

	p, _ = bitbucketDriver{}.parse(hdr("X-Event-Key", "pullrequest:created"), []byte(`{}`))
	require.Equal(t, kindIgnored, p.kind, "Bitbucket has no previews")
}

func TestRepoFullNameFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/octo-org/monorepo.git":     "octo-org/monorepo",
		"https://github.com/octo-org/monorepo":         "octo-org/monorepo",
		"https://gitlab.com/group/sub/proj.git":        "group/sub/proj",
		"git@github.com:octo-org/monorepo.git":         "octo-org/monorepo",
		"ssh://git@git.example.com:2222/acme/shop.git": "acme/shop",
		"git://host/app.git":                           "",
		"https://github.com/":                          "",
		"":                                             "",
	} {
		require.Equal(t, want, RepoFullNameFromURL(in), in)
	}
}

func TestHeaderRedaction(t *testing.T) {
	got := redactHeaders(hdr(
		"X-Hub-Signature-256", "sha256=deadbeef", "X-Gitlab-Token", "hunter2", "Authorization", "Bearer abc", "X-Forgejo-Signature", "x",
		"X-GitHub-Event", "push", "User-Agent", "GitHub-Hookshot/abc", "Idempotency-Key", "keep-me",
	))
	require.Equal(t, "[redacted]", got["X-Hub-Signature-256"])
	require.Equal(t, "[redacted]", got["X-Gitlab-Token"])
	require.Equal(t, "[redacted]", got["Authorization"])
	require.Equal(t, "[redacted]", got["X-Forgejo-Signature"])
	require.Equal(t, "push", got["X-Github-Event"])
	require.Equal(t, "GitHub-Hookshot/abc", got["User-Agent"])
	require.Equal(t, "keep-me", got["Idempotency-Key"])
}

func TestWatchPaths(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		changed  []string
		want     bool
	}{
		{"no patterns deploys everything", nil, []string{"README.md"}, true},
		{"directory prefix", []string{"apps/api"}, []string{"apps/api/handler.go"}, true},
		{"directory prefix miss", []string{"apps/api"}, []string{"apps/web/index.ts"}, false},
		{"similar sibling is not below", []string{"apps/api"}, []string{"apps/api-gateway/x.go"}, false},
		{"trailing slash", []string{"apps/api/"}, []string{"apps/api/x/y.go"}, true},
		{"double star", []string{"**/*.go"}, []string{"apps/api/deep/er/x.go"}, true},
		{"double star miss", []string{"**/*.go"}, []string{"README.md"}, false},
		{"single star", []string{"packages/*/src"}, []string{"packages/ui/src/button.tsx"}, true},
		{"single star does not cross", []string{"packages/*/src"}, []string{"packages/ui/lib/src/x.ts"}, false},
		{"exact file", []string{"Dockerfile"}, []string{"Dockerfile"}, true},
		{"leading slash and dot", []string{"/apps/api", "./go.mod"}, []string{"go.mod"}, true},
		{"exclude removes the only match", []string{"apps/api/**", "!**/*.md"}, []string{"apps/api/README.md"}, false},
		{"exclude leaves another match", []string{"apps/api/**", "!**/*.md"}, []string{"apps/api/README.md", "apps/api/x.go"}, true},
		{"excludes only: everything else deploys", []string{"!docs/**"}, []string{"main.go"}, true},
		{"excludes only: excluded path does not", []string{"!docs/**"}, []string{"docs/a.md"}, false},
		{"any of several changes", []string{"apps/api/**"}, []string{"README.md", "apps/web/a.ts", "apps/api/a.go"}, true},
		{"no changed files", []string{"apps/api"}, nil, false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, MatchWatchPaths(c.patterns, c.changed), c.name)
	}
}

func TestValidateWatchPaths(t *testing.T) {
	got, err := ValidateWatchPaths([]string{" apps/api/** ", "", "apps/api/**", "!**/*.md"})
	require.NoError(t, err)
	require.Equal(t, []string{"apps/api/**", "!**/*.md"}, got)
	for _, bad := range []string{"../secrets", "apps/../x", "a[", "bad\x00"} {
		_, err := ValidateWatchPaths([]string{bad})
		require.Error(t, err, bad)
	}
	_, err = ValidateWatchPaths(make([]string, MaxWatchPaths+1))
	require.Error(t, err)
}

func TestSkipToken(t *testing.T) {
	require.Equal(t, "[skip ci]", skipToken("Fix typo [skip ci]"))
	require.Equal(t, "[ci skip]", skipToken("[CI SKIP] docs"))
	require.Empty(t, skipToken("normal commit"))
}
