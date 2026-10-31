//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// createDB creates a managed database through the API (staged).
func (s *Stack) createDB(name, engine, version string) Service {
	s.t.Helper()
	r := s.Do("POST", "/environments/"+s.EnvID+"/databases", map[string]any{
		"name": name, "engine": engine, "version": version, "serverId": s.ServerID,
	})
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	svc := Service{ID: r.JSON().Str("id"), Slug: r.JSON().Str("slug")}
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, svc.ID)
	s.servicesMu.Unlock()
	return svc
}

// deployStaged deploys the staged services (all when ids is empty) and waits
// for every deployment of the batch; it requires them all to succeed.
func (s *Stack) deployStaged(ids ...string) {
	s.t.Helper()
	body := map[string]any{}
	if len(ids) > 0 {
		body["serviceIds"] = ids
	}
	r := s.Do("POST", "/environments/"+s.EnvID+"/deploy-staged", body)
	require.Equal(s.t, http.StatusAccepted, r.Status, string(r.Raw))
	// Wait for every deployment of the batch before judging any: a failed test
	// must not leave a sibling still being created behind its cleanup.
	var done []J
	for _, it := range r.JSON().Items() {
		done = append(done, s.WaitDeployment(it.Str("id"), 4*time.Minute))
	}
	for _, d := range done {
		require.Equal(s.t, "succeeded", d.Str("status"), "deployment: %v\nlogs:\n%s", d, s.DeploymentLogs(d.Str("id")))
	}
}

// waitConsumer polls the consumer until path answers want (or a prefix of it).
func waitConsumer(t *testing.T, c *ProxyClient, path, want string, timeout time.Duration) {
	t.Helper()
	var code int
	var body string
	require.Truef(t, eventually(timeout, 500*time.Millisecond, func() bool {
		var err error
		code, body, err = c.Get(path)
		return err == nil && code == http.StatusOK && strings.HasPrefix(body, want)
	}), "GET %s: last answer %d %q, want 200 %q", path, code, body, want)
}

// waitConsumerFails polls until path answers a non-200 (the old password stopped working).
func waitConsumerFails(t *testing.T, c *ProxyClient, path string, timeout time.Duration) string {
	t.Helper()
	var body string
	require.Truef(t, eventually(timeout, 500*time.Millisecond, func() bool {
		code, b, err := c.Get(path)
		body = b
		return err == nil && code != http.StatusOK
	}), "GET %s kept answering 200", path)
	return body
}

// A consumer reads DATABASE_URL / REDIS_URL through ${{db.…}} references and
// connects to a managed Postgres and Valkey; after a credential rotation it
// works again once it is redeployed with the new connection string.
func TestManagedDatabasesConnectAndRotate(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2) // environment + the internal network of the databases

	pg := s.createDB("pg", "postgres", "17")
	kv := s.createDB("cache", "valkey", "9")
	consumer := s.NewService(SvcSpec{
		Name: "api", Image: app.AppImage, Domain: s.Host(),
		Vars: map[string]string{
			"VERSION":      "v1",
			"DATABASE_URL": "${{" + pg.Slug + ".DATABASE_URL}}",
			"REDIS_URL":    "${{" + kv.Slug + ".REDIS_URL}}",
		},
	})

	// The databases come first (a consumer waits for the services it references).
	s.deployStaged()
	pgInfo := s.Service(pg.ID)
	require.Equal(t, "database", pgInfo.Str("kind"))
	require.Equal(t, "postgres:17", pgInfo.Obj("config").Str("image"))
	require.Equal(t, "valkey/valkey:9", s.Service(kv.ID).Obj("config").Str("image"))

	api := NewProxyClient(s.Host())
	waitConsumer(t, api, "/db", "db ok postgres", 60*time.Second)
	waitConsumer(t, api, "/kv", "kv ok", 30*time.Second)

	// Info: the agent reports version, size and connections.
	r := s.Do("POST", "/services/"+pg.ID+"/database/refresh", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	require.Truef(t, eventually(30*time.Second, 500*time.Millisecond, func() bool {
		return s.Do("GET", "/services/"+pg.ID+"/database", nil).JSON().Obj("info").Str("version") != ""
	}), "no database info arrived: %s", s.Do("GET", "/services/"+pg.ID+"/database", nil).Raw)
	info := s.Do("GET", "/services/"+pg.ID+"/database", nil).JSON().Obj("info")
	require.True(t, strings.HasPrefix(info.Str("version"), "17."), "%v", info)
	require.Positive(t, info.Int("sizeBytes"))

	// Reveal: audited, uncached, and the only way to read the connection string.
	ro := s.Token([]string{"projects:read", "servers:read"}, nil)
	rr := s.DoToken(ro, "POST", "/services/"+pg.ID+"/database/reveal", map[string]any{"key": "DATABASE_URL"})
	require.Equal(t, http.StatusForbidden, rr.Status, string(rr.Raw))
	rr = s.Do("POST", "/services/"+pg.ID+"/database/reveal", map[string]any{"key": "DATABASE_URL"})
	require.Equal(t, http.StatusOK, rr.Status, string(rr.Raw))
	oldURL := rr.JSON().Str("value")
	require.True(t, strings.HasPrefix(oldURL, "postgres://postgres:"), oldURL)
	var audited int
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_events WHERE action = 'secret.reveal' AND project_id = $1", s.ProjectID).Scan(&audited))
	require.Equal(t, 1, audited)

	// Rotate the Postgres password: the exec runs inside the container, the old
	// password stops working at once and the consumer is staged.
	rot := s.Do("POST", "/services/"+pg.ID+"/database/rotate-credentials", map[string]any{})
	require.Equal(t, http.StatusOK, rot.Status, string(rot.Raw))
	require.Equal(t, "pending", rot.JSON().Str("status"))
	require.Truef(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		return s.Do("GET", "/services/"+pg.ID+"/database", nil).JSON().Obj("rotation").Str("status") == "applied"
	}), "rotation did not apply: %s", s.Do("GET", "/services/"+pg.ID+"/database", nil).Raw)
	waitConsumerFails(t, api, "/db", 20*time.Second)
	require.True(t, s.Service(consumer.ID).Bool("hasStagedChanges"), "the consumer of ${{pg.DATABASE_URL}} is staged")
	require.False(t, s.Service(pg.ID).Bool("hasStagedChanges"), "the database is not restaged by an exec rotation")

	// The new password never travelled in a command payload or a deployment log.
	newURL := s.Do("POST", "/services/"+pg.ID+"/database/reveal", map[string]any{"key": "DATABASE_URL"}).JSON().Str("value")
	require.NotEqual(t, oldURL, newURL)
	u, err := url.Parse(newURL)
	require.NoError(t, err)
	newPW, _ := u.User.Password()
	require.Len(t, newPW, 32)
	var leaked int
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM agent_commands WHERE payload::text LIKE '%' || $1 || '%' OR result::text LIKE '%' || $1 || '%'", newPW).Scan(&leaked))
	require.Zero(t, leaked, "the rotated password must never be stored in agent_commands")
	require.Zero(t, strings.Count(s.dumpAllTables("sessions", "audit_events", "service_variables", "variable_versions", "database_rotations"), newPW))

	// Redeploy the consumer: it now connects with the new password.
	s.deployStaged(consumer.ID)
	waitConsumer(t, api, "/db", "db ok postgres", 60*time.Second)
	waitConsumer(t, api, "/kv", "kv ok", 30*time.Second)

	// Valkey reads its password at start: rotation restages the database and
	// its consumer; deploying both (database first) restores the connection.
	rot = s.Do("POST", "/services/"+kv.ID+"/database/rotate-credentials", map[string]any{})
	require.Equal(t, http.StatusOK, rot.Status, string(rot.Raw))
	require.Equal(t, "applied", rot.JSON().Str("status"))
	require.True(t, s.Service(kv.ID).Bool("hasStagedChanges"))
	require.True(t, s.Service(consumer.ID).Bool("hasStagedChanges"))
	s.deployStaged()
	waitConsumer(t, api, "/kv", "kv ok", 90*time.Second)
	waitConsumer(t, api, "/db", "db ok postgres", 30*time.Second)
}
