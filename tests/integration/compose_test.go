//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Importing a Compose project stages every service with its references
// rewritten; deploying the staged result brings up a stack in which the app
// reaches its database through the rewritten reference: once through a managed
// Valkey (the URL gains the generated password) and once through a plain image
// service (host name only).
func TestComposeImportDeploysAndConnects(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2) // environment + the internal network of the managed database

	hostA, hostB := s.Host("managed"), s.Host("plain")
	compose := fmt.Sprintf(`
name: shop
services:
  web:
    image: %[1]s
    environment:
      VERSION: compose-v1
      REDIS_URL: redis://cache:6379/0
    ports: ["8080"]
    depends_on: [cache]
  cache:
    image: valkey/valkey:8
    volumes:
      - cachedata:/data
  web2:
    image: %[1]s
    environment:
      VERSION: compose-v2
      REDIS_URL: redis://store:6379/1
    ports: ["8080"]
    depends_on:
      store:
        condition: service_started
  store:
    image: valkey/valkey:7.2-alpine
volumes:
  cachedata:
`, app.AppImage)

	body := func(extra map[string]any) map[string]any {
		b := map[string]any{
			"source":   map[string]any{"compose": compose},
			"serverId": s.ServerID,
			"choices": map[string]any{"services": map[string]any{
				"web":  map[string]any{"domain": map[string]any{"enabled": true, "host": hostA}},
				"web2": map[string]any{"domain": map[string]any{"enabled": true, "host": hostB}},
			}},
		}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	// Review: a managed database for `cache`, a plain image for `store`, the references rewritten.
	plan := s.Do("POST", "/environments/"+s.EnvID+"/compose-imports/plan", body(nil))
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	var parsed struct {
		Installable bool `json:"installable"`
		Services    []struct {
			Key    string `json:"key"`
			Source struct {
				Type string `json:"type"`
			} `json:"source"`
		} `json:"services"`
		Rewrites []struct {
			Service string `json:"service"`
			Key     string `json:"key"`
			Kind    string `json:"kind"`
		} `json:"rewrites"`
	}
	require.NoError(t, json.Unmarshal(plan.Raw, &parsed))
	require.True(t, parsed.Installable, string(plan.Raw))
	kinds := map[string]string{}
	for _, sv := range parsed.Services {
		kinds[sv.Key] = sv.Source.Type
	}
	require.Equal(t, map[string]string{"web": "image", "cache": "managed", "web2": "image", "store": "image"}, kinds)
	rewrites := map[string]bool{}
	for _, r := range parsed.Rewrites {
		rewrites[r.Service+"."+r.Key+":"+r.Kind] = true
	}
	require.True(t, rewrites["web.REDIS_URL:host"], "%v", rewrites)
	require.True(t, rewrites["web.REDIS_URL:auth"], "the managed Valkey needs its password: %v", rewrites)
	require.True(t, rewrites["web2.REDIS_URL:host"], "%v", rewrites)
	require.False(t, rewrites["web2.REDIS_URL:auth"], "%v", rewrites)

	// Import: everything is staged, nothing runs.
	key := "compose-" + s.hostBase
	imp := s.Do("POST", "/environments/"+s.EnvID+"/compose-imports", body(nil), "Idempotency-Key", key)
	require.Equal(t, http.StatusCreated, imp.Status, string(imp.Raw))
	ids := importedServices(t, imp.Raw)
	require.Len(t, ids, 4)
	for slug, id := range ids {
		s.servicesMu.Lock()
		s.serviceIDs = append(s.serviceIDs, id)
		s.servicesMu.Unlock()
		require.True(t, s.Service(id).Bool("hasStagedChanges"), "%s is staged", slug)
	}
	require.Equal(t, "database", s.Service(ids["cache"]).Str("kind"))
	require.Equal(t, "app", s.Service(ids["store"]).Str("kind"))
	require.Equal(t, "valkey/valkey:7.2-alpine", s.Service(ids["store"]).Obj("config").Str("image"))

	// The same request again replays the stored response and creates nothing new.
	again := s.Do("POST", "/environments/"+s.EnvID+"/compose-imports", body(nil), "Idempotency-Key", key)
	require.Equal(t, http.StatusCreated, again.Status, string(again.Raw))
	require.Equal(t, ids, importedServices(t, again.Raw))
	list := s.Do("GET", "/environments/"+s.EnvID+"/services", nil)
	require.Len(t, list.JSON().Items(), 4)

	// Deploy the staged result in dependency order and let the apps talk to their databases.
	s.deployStaged()
	clientA, clientB := NewProxyClient(hostA), NewProxyClient(hostB)
	waitConsumer(t, clientA, "/kv", "kv ok", 60*time.Second)
	waitConsumer(t, clientB, "/kv", "kv ok", 60*time.Second)
	code, version, err := clientA.Get("/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "compose-v1", strings.TrimSpace(version))
	_, version, err = clientB.Get("/")
	require.NoError(t, err)
	require.Equal(t, "compose-v2", strings.TrimSpace(version))
}

// importedServices maps slug to id for the services of an import result.
func importedServices(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var res struct {
		Services []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"services"`
	}
	require.NoError(t, json.Unmarshal(raw, &res))
	out := map[string]string{}
	for _, sv := range res.Services {
		out[sv.Slug] = sv.ID
	}
	return out
}
