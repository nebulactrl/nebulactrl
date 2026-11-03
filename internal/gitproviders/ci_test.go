package gitproviders

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// ciServer answers GET requests from testdata files: routes maps a URL path to
// a fixture name, or to "403" / "404" / "ratelimit" for an error answer.
type ciServer struct {
	*apiClient
	mu    sync.Mutex
	paths []string
}

func newCIServer(t *testing.T, routes map[string]string) *ciServer {
	t.Helper()
	cs := &ciServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.paths = append(cs.paths, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		cs.mu.Unlock()
		target, ok := routes[r.URL.EscapedPath()]
		switch {
		case !ok:
			http.Error(w, "not found", http.StatusNotFound)
		case target == "404":
			http.Error(w, "not found", http.StatusNotFound)
		case target == "403":
			http.Error(w, "Resource not accessible by integration", http.StatusForbidden)
		case target == "ratelimit":
			w.Header().Set("Retry-After", "60")
			http.Error(w, "slow down", http.StatusForbidden)
		default:
			raw, err := os.ReadFile("testdata/" + target)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(raw)
		}
	}))
	t.Cleanup(srv.Close)
	cs.apiClient = &apiClient{hc: srv.Client(), base: srv.URL}
	return cs
}

const ciSHA = "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"

func TestGitHubReadCI(t *testing.T) {
	const base = "/repos/acme/shop/commits/" + ciSHA
	run := func(t *testing.T, checkRuns, status string) (CIResult, error) {
		t.Helper()
		cs := newCIServer(t, map[string]string{base + "/check-runs": checkRuns, base + "/status": status})
		return githubDriver{}.readCI(context.Background(), cs.apiClient, "acme/shop", ciSHA)
	}
	tests := []struct {
		name, checkRuns, status string
		want                    CIResult
	}{
		{"every check passes; our own failing check is ignored", "github_check_runs_success.json", "github_status_success.json", CIResult{State: "success"}},
		{"nothing reported", "github_check_runs_none.json", "github_status_empty.json", CIResult{State: "none"}},
		{"a timed out check run fails", "github_check_runs_failure.json", "github_status_empty.json", CIResult{State: "failure", Summary: "unit tests"}},
		{"a running check run is pending", "github_check_runs_pending.json", "github_status_empty.json", CIResult{State: "pending", Summary: "unit tests"}},
		{"a failing legacy status fails", "github_check_runs_success.json", "github_status_failure.json", CIResult{State: "failure", Summary: "ci/legacy"}},
		{"a pending legacy status is pending", "github_check_runs_none.json", "github_status_pending.json", CIResult{State: "pending", Summary: "ci/legacy"}},
		{"failure beats pending", "github_check_runs_pending.json", "github_status_failure.json", CIResult{State: "failure", Summary: "ci/legacy"}},
		{"only a legacy status passing is success", "github_check_runs_none.json", "github_status_success.json", CIResult{State: "success"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(t, tc.checkRuns, tc.status)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	t.Run("a 403 on check runs is a permission error", func(t *testing.T) {
		_, err := run(t, "403", "github_status_empty.json")
		require.ErrorIs(t, err, errCIPermission)
	})
	t.Run("a rate limited 403 is not a permission error", func(t *testing.T) {
		_, err := run(t, "ratelimit", "github_status_empty.json")
		require.Error(t, err)
		require.NotErrorIs(t, err, errCIPermission)
	})
	t.Run("another failure is returned as is", func(t *testing.T) {
		_, err := run(t, "404", "github_status_empty.json")
		require.Error(t, err)
		require.True(t, isNotFound(err))
	})
	t.Run("the check runs request asks for 100 per page", func(t *testing.T) {
		cs := newCIServer(t, map[string]string{base + "/check-runs": "github_check_runs_none.json", base + "/status": "github_status_empty.json"})
		_, err := githubDriver{}.readCI(context.Background(), cs.apiClient, "acme/shop", ciSHA)
		require.NoError(t, err)
		require.Contains(t, cs.paths[0], "per_page=100")
	})
}

func TestGitLabReadCI(t *testing.T) {
	path := "/projects/acme%2Fshop/pipelines"
	for name, tc := range map[string]struct {
		fixture string
		want    CIResult
	}{
		"success": {"gitlab_pipelines_success.json", CIResult{State: "success"}},
		"failed":  {"gitlab_pipelines_failed.json", CIResult{State: "failure", Summary: "pipeline #4712 failed"}},
		"running": {"gitlab_pipelines_running.json", CIResult{State: "pending", Summary: "pipeline #4713 running"}},
	} {
		t.Run(name, func(t *testing.T) {
			cs := newCIServer(t, map[string]string{path: tc.fixture})
			got, err := gitlabDriver{}.readCI(context.Background(), cs.apiClient, "acme/shop", ciSHA)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Contains(t, cs.paths[0], "sha="+ciSHA)
			require.Contains(t, cs.paths[0], "order_by=id")
			require.Contains(t, cs.paths[0], "per_page=1")
		})
	}
	t.Run("no pipeline is none", func(t *testing.T) {
		cs := newCIServer(t, map[string]string{path: "gitlab_pipelines_none.json"})
		got, err := gitlabDriver{}.readCI(context.Background(), cs.apiClient, "acme/shop", ciSHA)
		require.NoError(t, err)
		require.Equal(t, CIResult{State: "none"}, got)
	})
}

func TestGiteaReadCI(t *testing.T) {
	path := "/repos/acme/shop/commits/" + ciSHA + "/status"
	for name, tc := range map[string]struct {
		fixture string
		want    CIResult
	}{
		"all success; our own failing status ignored": {"gitea_status_success.json", CIResult{State: "success"}},
		"a failure beats a pending status":            {"gitea_status_failure.json", CIResult{State: "failure", Summary: "ci/test"}},
		"a pending status":                            {"gitea_status_pending.json", CIResult{State: "pending", Summary: "ci/test"}},
		"only our own status is none":                 {"gitea_status_only_ours.json", CIResult{State: "none"}},
		"no status is none":                           {"gitea_status_empty.json", CIResult{State: "none"}},
	} {
		for _, p := range []Provider{Gitea, Forgejo} {
			t.Run(name+"/"+string(p), func(t *testing.T) {
				cs := newCIServer(t, map[string]string{path: tc.fixture})
				got, err := giteaDriver{name: p}.readCI(context.Background(), cs.apiClient, "acme/shop", ciSHA)
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			})
		}
	}
}

func TestBitbucketCannotReadCI(t *testing.T) {
	_, err := bitbucketDriver{}.readCI(context.Background(), &apiClient{}, "acme/shop", ciSHA)
	require.ErrorIs(t, err, errCINotSupported)
}
