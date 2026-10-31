package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appconfig "github.com/nebulactrl/nebulactrl/internal/config"
)

func TestServicesDeployHook(t *testing.T) {
	e := newEnvWith(t, func(c *appconfig.Config) { c.PublicURL = "http://nebula.example.com" })
	e.login()
	svcID := e.seed()

	// Off by default.
	r := e.mustRun("", "--json", "services", "deploy-hook", svcID)
	off := decode[map[string]any](t, r.stdout)
	require.Equal(t, false, off["enabled"])
	require.Equal(t, "http://nebula.example.com/hooks/deploy/"+svcID+"/<secret>", off["urlPattern"])

	// Enabling prints the URL once; reading it again does not.
	r = e.mustRun("", "services", "deploy-hook", svcID, "--enable")
	require.Contains(t, r.stdout, "Deploy hook:  on")
	var url string
	for _, l := range strings.Split(r.stdout, "\n") {
		if rest, ok := strings.CutPrefix(l, "URL:          "); ok {
			url = rest
		}
	}
	prefix := "http://nebula.example.com/hooks/deploy/" + svcID + "/ndh_"
	require.True(t, strings.HasPrefix(url, prefix), r.stdout)
	secret := strings.TrimPrefix(url, "http://nebula.example.com/hooks/deploy/"+svcID+"/")
	r = e.mustRun("", "services", "deploy-hook", svcID)
	require.NotContains(t, r.all(), secret)
	require.Contains(t, r.stdout, "<secret>")

	// The URL deploys the service for real: a deploy_hook deployment appears.
	resp, err := http.Post(e.srv.URL+"/hooks/deploy/"+svcID+"/"+secret, "application/json", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	r = e.mustRun("", "--json", "deployments", "ls", svcID)
	require.Contains(t, r.stdout, `"trigger": "deploy_hook"`)

	// Rotating replaces the secret; the old URL is dead.
	r = e.mustRun("", "--json", "services", "deploy-hook", svcID, "--rotate")
	rotated := decode[map[string]any](t, r.stdout)
	newURL, _ := rotated["url"].(string)
	require.NotEmpty(t, newURL)
	require.NotContains(t, newURL, secret)
	resp, err = http.Post(e.srv.URL+"/hooks/deploy/"+svcID+"/"+secret, "application/json", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Disabling turns it off; flags exclude each other.
	e.mustRun("", "services", "deploy-hook", svcID, "--disable")
	r = e.mustRun("", "--json", "services", "deploy-hook", svcID)
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["enabled"])
	r = e.run("", "services", "deploy-hook", svcID, "--enable", "--rotate")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.all(), "at most one")
}
