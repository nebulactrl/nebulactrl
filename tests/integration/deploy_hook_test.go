//go:build integration

package integration

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hookPost calls a deploy hook the way `curl -X POST <url>` does: no body, no
// content type, no cookies. A 429 is retried after its Retry-After, as a CI
// job's retrying client would: the hook allows a burst of 3 and then one call
// per 10 s, and how many calls fit depends on how fast the deploys in between
// finish. The limiter itself is covered by the deployhooks unit tests.
func hookPost(t *testing.T, url string) (int, J) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, nil)
		require.NoError(t, err)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		raw, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		require.NoError(t, err)
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			wait, perr := strconv.Atoi(res.Header.Get("Retry-After"))
			require.NoError(t, perr, "a 429 names its Retry-After")
			time.Sleep(time.Duration(wait) * time.Second)
			continue
		}
		return res.StatusCode, Resp{Status: res.StatusCode, Raw: raw}.JSON()
	}
}

// The deploy hook: POST /hooks/deploy/<serviceId>/<secret> deploys an image
// service like the Deploy button (staged changes included, trigger
// deploy_hook), ?tag= deploys another tag of the same repository as a one-off
// release, a wrong secret and a foreign repository are refused.
func TestDeployHookDeploysImageService(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	host := s.Host()
	svc := s.NewService(SvcSpec{Name: "hooked", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}, Domain: host})

	// Off until enabled: the URL does not exist yet.
	code, _ := hookPost(t, s.URL()+"/hooks/deploy/"+svc.ID+"/ndh_nothing")
	require.Equal(t, http.StatusNotFound, code)

	r := s.Do("PUT", "/services/"+svc.ID+"/deploy-hook", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	url := r.JSON().Str("url")
	prefix := s.URL() + "/hooks/deploy/" + svc.ID + "/ndh_"
	require.True(t, strings.HasPrefix(url, prefix), url)
	secret := strings.TrimPrefix(url, s.URL()+"/hooks/deploy/"+svc.ID+"/")

	// The URL is never returned again.
	again := s.Do("GET", "/services/"+svc.ID+"/deploy-hook", nil)
	require.Equal(t, http.StatusOK, again.Status)
	require.True(t, again.JSON().Bool("enabled"))
	require.NotContains(t, string(again.Raw), secret)

	// A wrong secret is a 404 and starts nothing.
	code, _ = hookPost(t, s.URL()+"/hooks/deploy/"+svc.ID+"/"+secret+"x")
	require.Equal(t, http.StatusNotFound, code)
	require.Empty(t, s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items())

	// 1. A plain call deploys.
	code, body := hookPost(t, url)
	require.Equal(t, http.StatusAccepted, code, "%v", body)
	require.Equal(t, "deploying", body.Str("status"))
	d := s.WaitDeployment(body.Str("deploymentId"), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "deployment: %v\nlogs:\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.Equal(t, "deploy_hook", d.Str("trigger"))
	require.Len(t, s.RunningContainers(svc.ID), 1)
	NewProxyClient(host).WaitBody(t, "/", "v1", 30*time.Second)

	// 2. Staged changes ride along, exactly like a manual deploy.
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	require.True(t, s.Service(svc.ID).Bool("hasStagedChanges"))
	code, body = hookPost(t, url)
	require.Equal(t, http.StatusAccepted, code, "%v", body)
	d = s.WaitDeployment(body.Str("deploymentId"), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "deployment: %v\nlogs:\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.False(t, s.Service(svc.ID).Bool("hasStagedChanges"))
	NewProxyClient(host).WaitBody(t, "/", "v2", 30*time.Second)

	// 3. ?tag= deploys another tag of the same repository, once: the release
	// records it, the service keeps its configured image.
	extra := app.Registry + "/nebulactrl-test-app:hook1"
	ctx := context.Background()
	_, err := sh(ctx, "", "docker", "tag", app.AppImage, extra)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rmi", extra) })
	_, err = sh(ctx, "", "docker", "push", extra)
	require.NoError(t, err)

	code, body = hookPost(t, url+"?tag=hook1")
	require.Equal(t, http.StatusAccepted, code, "%v", body)
	require.Equal(t, extra, body.Str("image"))
	d = s.WaitDeployment(body.Str("deploymentId"), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "deployment: %v\nlogs:\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.Equal(t, extra, d.Obj("release").Obj("source").Str("image"))
	require.Equal(t, app.AppImage, s.Service(svc.ID).Obj("config").Str("image"), "the configured image is untouched")
	require.False(t, s.Service(svc.ID).Bool("hasStagedChanges"))

	// A tag outside the service's repository is refused before anything starts.
	code, body = hookPost(t, url+"?tag=other/image:1")
	require.Equal(t, http.StatusUnprocessableEntity, code, "%v", body)
	require.Equal(t, "image_outside_repository", body.Str("code"))

	// Every call that got past the secret is in the audit log.
	audit := s.Do("GET", "/audit-events?limit=200", nil)
	require.Equal(t, http.StatusOK, audit.Status, string(audit.Raw))
	n := 0
	for _, e := range audit.JSON().Items() {
		if e.Str("action") == "deployment.hook" && e.Str("resourceId") == svc.ID {
			n++
		}
	}
	require.GreaterOrEqual(t, n, 4)
	require.NotContains(t, string(audit.Raw), secret)

	// Rotating kills the old URL.
	rot := s.Do("POST", "/services/"+svc.ID+"/deploy-hook/rotate", nil)
	require.Equal(t, http.StatusOK, rot.Status, string(rot.Raw))
	code, _ = hookPost(t, url)
	require.Equal(t, http.StatusNotFound, code)
}
