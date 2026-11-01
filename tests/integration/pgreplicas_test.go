//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Postgres read replicas on ONE server, no mesh. They need Docker and the
// postgres image only. (replicas_test.go is the load-balancing/scaling suite;
// "replica" there means a container of one service, not a standby.)

// ---- helpers -----------------------------------------------------------------

// replicaState is the observed lifecycle state of a read replica.
const (
	replStreaming = "streaming"
	replLagging   = "lagging"
)

// replicationView returns the `replication` object of GET /services/{id}/database.
func (s *Stack) replicationView(serviceID string) J {
	s.t.Helper()
	r := s.Do("GET", "/services/"+serviceID+"/database", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON().Obj("replication")
}

// replicaItem returns the entry of the primary's replication.replicas with this
// service id (nil when it is not listed).
func (s *Stack) replicaItem(primaryID, replicaID string) J {
	s.t.Helper()
	arr, _ := s.replicationView(primaryID)["replicas"].([]any)
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok && J(m).Str("id") == replicaID {
			return J(m)
		}
	}
	return nil
}

// replicaByName finds the replica of primaryID whose slug is slug.
func (s *Stack) replicaByName(primaryID, slug string) J {
	s.t.Helper()
	arr, _ := s.replicationView(primaryID)["replicas"].([]any)
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok && J(m).Str("slug") == slug {
			return J(m)
		}
	}
	return nil
}

// addReplicaRaw requests a replica and returns the raw response.
func (s *Stack) addReplicaRaw(primaryID string, body map[string]any) Resp {
	s.t.Helper()
	return s.Do("POST", "/services/"+primaryID+"/database/replicas", body)
}

// addReplica creates a replica named name on serverID, waits for it to be
// listed, registers it for cleanup and returns it (it is not streaming yet).
func (s *Stack) addReplica(primary Service, serverID, name string, extra map[string]any) Service {
	s.t.Helper()
	body := map[string]any{"name": name, "serverId": serverID}
	for k, v := range extra {
		body[k] = v
	}
	r := s.addReplicaRaw(primary.ID, body)
	require.Equal(s.t, http.StatusAccepted, r.Status, string(r.Raw))
	id := r.JSON().Str("serviceId")
	require.NotEmpty(s.t, id, string(r.Raw))
	svc := Service{ID: id, Slug: s.Service(id).Str("slug")}
	require.Equal(s.t, slugOf(name), svc.Slug)
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, svc.ID)
	s.servicesMu.Unlock()
	return svc
}

// slugOf lowercases a test name that is already slug-shaped.
func slugOf(name string) string { return strings.ToLower(strings.ReplaceAll(name, " ", "-")) }

// waitReplica waits until the replica reports one of want; it fails at once when
// the saga ends in a state that never leaves by itself.
func (s *Stack) waitReplica(primaryID, replicaID string, timeout time.Duration, want ...string) J {
	s.t.Helper()
	var it J
	ok := eventually(timeout, 2*time.Second, func() bool {
		it = s.replicaItem(primaryID, replicaID)
		if it == nil {
			return false
		}
		st := it.Str("state")
		for _, w := range want {
			if st == w {
				return true
			}
		}
		switch st {
		case "failed", "needs_rebuild":
			s.t.Fatalf("replica %s ended in %q (want %v): %v", replicaID, st, want, it)
		}
		return false
	})
	require.Truef(s.t, ok, "replica %s did not reach %v within %s; last: %v\nreplica row: %v", replicaID, want, timeout, it, s.replicaRow(replicaID))
	return it
}

// waitStreaming waits for a replica to be streaming.
func (s *Stack) waitStreaming(primaryID, replicaID string) J {
	s.t.Helper()
	return s.waitReplica(primaryID, replicaID, 6*time.Minute, replStreaming)
}

// replicaRow is the database_replicas row for diagnostics and assertions.
func (s *Stack) replicaRow(replicaID string) map[string]any {
	row := map[string]any{}
	var slot, user, state, step, msg string
	var linkID, exposureID *string
	err := s.Pool.QueryRow(context.Background(),
		`SELECT slot_name, repl_user, state, step, message, link_id::text, exposure_id::text FROM database_replicas WHERE service_id = $1`, replicaID,
	).Scan(&slot, &user, &state, &step, &msg, &linkID, &exposureID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	row["slot"], row["user"], row["state"], row["step"], row["message"], row["linkId"], row["exposureId"] = slot, user, state, step, msg, linkID, exposureID
	return row
}

// replicaIdentity returns the replication slot and role of a replica.
func (s *Stack) replicaIdentity(t *testing.T, replicaID string) (slot, user string) {
	t.Helper()
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT slot_name, repl_user FROM database_replicas WHERE service_id = $1`, replicaID).Scan(&slot, &user))
	return slot, user
}

// inspectFmt is `docker inspect -f <format> <id>`.
func inspectFmt(t *testing.T, id, format string) string {
	t.Helper()
	out, err := sh(context.Background(), "", "docker", "inspect", "-f", format, id)
	require.NoError(t, err, out)
	return strings.TrimSpace(out)
}

// pgq asks the consumer app (through Traefik) to run a single-value query
// against the Postgres URL in env var envName (see tests/fixtures/app /pgq).
func pgq(c *ProxyClient, envName, query string) (int, string, error) {
	return c.Get("/pgq?env=" + url.QueryEscape(envName) + "&q=" + url.QueryEscape(query))
}

// waitPgq polls /pgq until it answers 200 with want.
func waitPgq(t *testing.T, c *ProxyClient, envName, query, want string, timeout time.Duration) {
	t.Helper()
	var (
		code int
		body string
		err  error
	)
	require.Truef(t, eventually(timeout, time.Second, func() bool {
		code, body, err = pgq(c, envName, query)
		return err == nil && code == http.StatusOK && body == want
	}), "pgq %s %q: want 200 %q, last %d %q err=%v", envName, query, want, code, body, err)
}

// networksOf lists the Docker networks a container is attached to and whether
// each is internal.
func networksOf(t *testing.T, containerID string) map[string]bool {
	t.Helper()
	names := strings.Fields(inspectFmt(t, containerID, `{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}`))
	out := map[string]bool{}
	for _, n := range names {
		out[n] = strings.TrimSpace(mustSh(t, "docker", "network", "inspect", "-f", "{{.Internal}}", n)) == "true"
	}
	return out
}

func mustSh(t testing.TB, name string, args ...string) string {
	t.Helper()
	out, err := sh(context.Background(), "", name, args...)
	require.NoError(t, err, out)
	return out
}

// dataVolume is the name of a database service's data volume.
func dataVolume(serviceID string) string { return agentprotocol.VolumeName(serviceID, "data") }

// replicationPassword reads POSTGRES_REPLICATION_PASSWORD from the replica's
// container environment (the only place it lives in the clear).
func replicationPassword(t *testing.T, containerID string) string {
	t.Helper()
	env := inspectFmt(t, containerID, `{{range .Config.Env}}{{println .}}{{end}}`)
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "POSTGRES_REPLICATION_PASSWORD="); ok {
			require.NotEmpty(t, v)
			return v
		}
	}
	t.Fatalf("POSTGRES_REPLICATION_PASSWORD is not in the replica container's environment")
	return ""
}

// replicaPair is a deployed primary with a streaming replica and a consumer.
type replicaPair struct {
	Primary, Replica, Consumer Service
	Client                     *ProxyClient
}

// newReplicaPair creates a Postgres primary `pg`, deploys it with a consumer
// `api` (DATABASE_URL = writer, RO_URL = reader, LEGACY_URL = the primary's own
// URL), seeds a table, adds the replica `rname` on the stack's server and waits
// until it streams. The consumer is deployed BEFORE the replica exists, so its
// reader URL falls back to the writer until the replica restages it.
func newReplicaPair(t *testing.T, s *Stack, app *fixtureState, rname string) replicaPair {
	t.Helper()
	pg := s.createDB("pg", "postgres", "17")
	api := s.NewService(SvcSpec{
		Name: "api", Image: app.AppImage, Domain: s.Host(),
		Vars: map[string]string{
			"VERSION":      "v1",
			"DATABASE_URL": "${{" + pg.Slug + ".DATABASE_WRITE_URL}}",
			"RO_URL":       "${{" + pg.Slug + ".DATABASE_READ_URL}}",
			"LEGACY_URL":   "${{" + pg.Slug + ".DATABASE_URL}}",
		},
	})
	s.deployStaged()
	waitPostgres(t, s, pg.ID)
	pgExec(t, s, pg.ID, "CREATE TABLE notes (id int primary key, body text); INSERT INTO notes SELECT g, 'row ' || g FROM generate_series(1, 1000) g")
	c := NewProxyClient(s.Host())
	waitConsumer(t, c, "/", "v1", 60*time.Second)

	r := s.addReplica(pg, s.ServerID, rname, nil)
	s.waitStreaming(pg.ID, r.ID)
	return replicaPair{Primary: pg, Replica: r, Consumer: api, Client: c}
}

// replicaCount is the number of rows of the seeded table on a database service.
func replicaCount(t *testing.T, s *Stack, serviceID string) int {
	t.Helper()
	n, err := strconv.Atoi(pgExec(t, s, serviceID, "SELECT count(*) FROM notes"))
	require.NoError(t, err)
	return n
}

// ---- tests -------------------------------------------------------------------

// A same-server replica end to end: create (prepare, clone, deploy), stream,
// lag, the writer/reader exports a consumer sees, a restart that keeps
// streaming (PGPASSWORD from the environment), a wiped data volume that makes
// the container exit 3 instead of running initdb, rebuild, and the guards that
// keep a primary with replicas from being deleted, upgraded or backed up.
func TestReplicaSameServerStreamsAndReads(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2) // environment + the internal network of the databases
	ctx := context.Background()

	pg := s.createDB("pg", "postgres", "17")
	api := s.NewService(SvcSpec{
		Name: "api", Image: app.AppImage, Domain: s.Host(),
		Vars: map[string]string{
			"VERSION":      "v1",
			"DATABASE_URL": "${{" + pg.Slug + ".DATABASE_WRITE_URL}}",
			"RO_URL":       "${{" + pg.Slug + ".DATABASE_READ_URL}}",
		},
	})
	s.deployStaged()
	waitPostgres(t, s, pg.ID)
	pgExec(t, s, pg.ID, "CREATE TABLE notes (id int primary key, body text); INSERT INTO notes SELECT g, 'row ' || g FROM generate_series(1, 1000) g")
	c := NewProxyClient(s.Host())
	waitConsumer(t, c, "/", "v1", 60*time.Second)
	// No replica yet: the reader falls back to the writer.
	waitPgq(t, c, "RO_URL", "select pg_is_in_recovery()::text", "false", 30*time.Second)

	// ---- plan, then create ---------------------------------------------------
	plan := s.addReplicaRaw(pg.ID, map[string]any{"name": "pg-r1", "serverId": s.ServerID, "dryRun": true})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	pl := plan.JSON().Obj("plan")
	require.True(t, pl.Bool("sameServer"), string(plan.Raw))
	require.Zero(t, pl.Int("devicesAdded"), "a same-server replica needs no mesh: %s", plan.Raw)
	require.Empty(t, pl["blockers"], string(plan.Raw))
	require.Nil(t, s.replicaByName(pg.ID, "pg-r1"), "a dry run creates nothing")

	r := s.addReplica(pg, s.ServerID, "pg-r1", nil)
	require.NotEqual(t, pg.ID, r.ID)
	it := s.waitStreaming(pg.ID, r.ID)
	slot, repl := s.replicaIdentity(t, r.ID)
	require.Regexp(t, `^nc_r_[0-9a-f]{8}$`, slot)
	require.Regexp(t, `^nebula_r_[0-9a-f]{8}$`, repl)
	require.Equal(t, s.ServerID, it.Str("serverId"))

	// A replica is an ordinary database service of the same environment.
	rinfo := s.Service(r.ID)
	require.Equal(t, "database", rinfo.Str("kind"))
	require.Equal(t, "postgres:17", rinfo.Obj("config").Str("image"))
	require.Equal(t, "replica", s.replicationView(r.ID).Str("role"))
	require.Equal(t, "primary", s.replicationView(pg.ID).Str("role"))
	require.Equal(t, pg.ID, s.replicationView(r.ID).Obj("primary").Str("id"))

	// ---- what the primary and the replica look like inside --------------------
	require.Equal(t, "t", pgExec(t, s, r.ID, "SELECT pg_is_in_recovery()"))
	require.Equal(t, "f", pgExec(t, s, pg.ID, "SELECT pg_is_in_recovery()"))
	require.Equal(t, "1", pgExec(t, s, pg.ID, "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming' AND application_name = '"+slot+"'"))
	require.Equal(t, "t", pgExec(t, s, pg.ID, "SELECT active FROM pg_replication_slots WHERE slot_name = '"+slot+"'"))
	require.Equal(t, "t", pgExec(t, s, pg.ID, "SELECT rolreplication FROM pg_roles WHERE rolname = '"+repl+"'"))
	// The managed pg_hba block (samenet, per-replica role) is what lets the standby in.
	require.Equal(t, "1", pgExec(t, s, pg.ID,
		"SELECT count(*) FROM pg_hba_file_rules WHERE 'replication' = ANY(database) AND '"+repl+"' = ANY(user_name)"))
	// The documented defaults the design relies on (five replicas leave headroom).
	senders, _ := strconv.Atoi(pgExec(t, s, pg.ID, "SHOW max_wal_senders"))
	slots, _ := strconv.Atoi(pgExec(t, s, pg.ID, "SHOW max_replication_slots"))
	require.GreaterOrEqual(t, senders, 10)
	require.GreaterOrEqual(t, slots, 10)
	require.Equal(t, "off", pgExec(t, s, r.ID, "SHOW hot_standby_feedback"))
	require.Equal(t, "30s", pgExec(t, s, r.ID, "SHOW max_standby_streaming_delay"))
	require.NotEqual(t, "", pgExec(t, s, pg.ID, "SHOW max_slot_wal_keep_size"))
	require.Equal(t, "streaming", pgExec(t, s, r.ID, "SELECT status FROM pg_stat_wal_receiver"))

	// The clone copied the data; new writes follow.
	require.Equal(t, 1000, replicaCount(t, s, r.ID))
	pgExec(t, s, pg.ID, "INSERT INTO notes SELECT g, 'more ' || g FROM generate_series(1001, 1100) g")
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool { return replicaCount(t, s, r.ID) == 1100 }),
		"the replica did not catch up: %d rows", replicaCount(t, s, r.ID))

	// ---- lag is shown ---------------------------------------------------------
	require.Truef(t, eventually(90*time.Second, 2*time.Second, func() bool {
		it = s.replicaItem(pg.ID, r.ID)
		_, hasLag := it["lagBytes"]
		return hasLag && it.Str("observedAt") != "" && it.Str("walStatus") != ""
	}), "no lag was observed within 90s: %v", it)
	require.Contains(t, []string{replStreaming, replLagging}, it.Str("state"))
	require.Equal(t, "reserved", it.Str("walStatus"))
	require.GreaterOrEqual(t, it.Int("lagBytes"), 0)

	// ---- network: the replica has no internet --------------------------------
	rc := s.RunningContainers(r.ID)
	require.Len(t, rc, 1)
	for name, internal := range networksOf(t, rc[0].ID) {
		require.Truef(t, internal, "replica is attached to the non-internal network %s: blockEgress must hold", name)
	}

	// ---- the replication password never travelled or got stored in clear ------
	pw := replicationPassword(t, rc[0].ID)
	require.Len(t, pw, 32)
	var leaked int
	require.NoError(t, s.Pool.QueryRow(ctx,
		"SELECT count(*) FROM agent_commands WHERE payload::text LIKE '%' || $1 || '%' OR result::text LIKE '%' || $1 || '%'", pw).Scan(&leaked))
	require.Zero(t, leaked, "the replication password must never be stored in agent_commands")
	require.Zero(t, strings.Count(s.dumpAllTables("sessions", "audit_events", "service_variables", "variable_versions", "database_rotations"), pw))
	require.NotContains(t, s.dumpTables([]string{"deployment_logs"}), pw, "the password is not in any deployment log")

	// ---- the consumer: membership restaged it, a deploy picks the replica up --
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool { return s.Service(api.ID).Bool("hasStagedChanges") }),
		"a new replica restages the consumers of ${{%s.DATABASE_READ_URL}}", pg.Slug)
	require.False(t, s.Service(pg.ID).Bool("hasStagedChanges"), "the primary is not restaged by a replica")
	s.deployStaged(api.ID)
	waitConsumer(t, c, "/", "v1", 60*time.Second)
	waitPgq(t, c, "RO_URL", "select pg_is_in_recovery()::text", "true", 30*time.Second)
	waitPgq(t, c, "DATABASE_URL", "select pg_is_in_recovery()::text", "false", 30*time.Second)
	// Writes go to the writer and reach the replica; the reader refuses them.
	waitPgq(t, c, "DATABASE_URL", "INSERT INTO notes VALUES (5001, 'via api') RETURNING 'ok'::text", "ok", 30*time.Second)
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool {
		return pgExec(t, s, r.ID, "SELECT count(*) FROM notes WHERE id = 5001") == "1"
	}), "the row written through DATABASE_WRITE_URL did not reach the replica")
	code, body, err := pgq(c, "RO_URL", "INSERT INTO notes VALUES (5002, 'nope') RETURNING 'ok'::text")
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, code)
	require.Contains(t, body, "read-only")

	// ---- a restart keeps streaming ---------------------------------------------
	started := inspectFmt(t, rc[0].ID, "{{.State.StartedAt}}")
	rr := s.Do("POST", "/services/"+r.ID+"/restart", nil)
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, rr.Status, string(rr.Raw))
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		cs := s.RunningContainers(r.ID)
		return len(cs) == 1 && inspectFmt(t, cs[0].ID, "{{.State.StartedAt}}") != started
	}), "the replica was not restarted")
	waitPostgres(t, s, r.ID)
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		return pgExec(t, s, r.ID, "SELECT coalesce((SELECT status FROM pg_stat_wal_receiver), '')") == "streaming"
	}), "the walreceiver did not reconnect after a restart (PGPASSWORD from the environment)")
	pgExec(t, s, pg.ID, "INSERT INTO notes VALUES (6001, 'after restart')")
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool {
		return pgExec(t, s, r.ID, "SELECT count(*) FROM notes WHERE id = 6001") == "1"
	}), "no replication after the restart")
	s.waitStreaming(pg.ID, r.ID)

	// ---- guards ---------------------------------------------------------------
	for _, tc := range []struct {
		name, method, path string
		body               any
		want               string
	}{
		{"delete primary", "DELETE", "/services/" + pg.ID + "?confirm=" + pg.Slug, nil, "replicas_exist"},
		{"upgrade primary", "POST", "/services/" + pg.ID + "/database/upgrade", map[string]any{"targetVersion": "18", "confirm": pg.Slug}, "replicas_exist"},
		{"rotate on replica", "POST", "/services/" + r.ID + "/database/rotate-credentials", map[string]any{}, "replica_read_only"},
		{"replica of a replica", "POST", "/services/" + r.ID + "/database/replicas", map[string]any{"name": "pg-r2", "serverId": s.ServerID}, "replica_not_primary"},
	} {
		resp := s.Do(tc.method, tc.path, tc.body)
		require.GreaterOrEqual(t, resp.Status, 400, "%s: %s", tc.name, resp.Raw)
		require.Contains(t, string(resp.Raw), tc.want, tc.name)
	}
	// The plan lists the blocker as text (the upgrade plan's blockers are plain
	// strings); the upgrade itself answers with the replicas_exist code above.
	plan2 := s.Do("POST", "/services/"+pg.ID+"/database/upgrade/plan", map[string]any{"targetVersion": "18"})
	require.Equal(t, http.StatusOK, plan2.Status, string(plan2.Raw))
	require.Contains(t, string(plan2.Raw), "This database has 1 read replica(s): remove them, upgrade, then add replicas again",
		"the upgrade plan names the blocker")

	// Backups run on the primary only.
	dest := s.Do("POST", "/backup-destinations", map[string]any{"name": "replica guard", "kind": "local", "localDir": "itest-replica"})
	require.Equal(t, http.StatusCreated, dest.Status, string(dest.Raw))
	bk := s.Do("POST", "/services/"+r.ID+"/backups", map[string]any{"destinationId": dest.JSON().Str("id")})
	require.GreaterOrEqual(t, bk.Status, 400, string(bk.Raw))
	require.Contains(t, string(bk.Raw), "backup_replica_unsupported")
	sc := s.Do("POST", "/services/"+r.ID+"/backup-schedules", map[string]any{"destinationId": dest.JSON().Str("id"), "cron": "@daily"})
	require.GreaterOrEqual(t, sc.Status, 400, string(sc.Raw))
	require.Contains(t, string(sc.Raw), "backup_replica_unsupported")

	// ---- an emptied data volume: exit 3, never initdb ------------------------
	cid := s.RunningContainers(r.ID)[0].ID
	mustSh(t, "docker", "stop", cid)
	vol := dataVolume(r.ID)
	mustSh(t, "docker", "run", "--rm", "-v", vol+":/v", "alpine:3", "sh", "-c", "find /v -mindepth 1 -delete")
	mustSh(t, "docker", "start", cid)
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		return inspectFmt(t, cid, "{{.State.ExitCode}}") == "3"
	}), "an empty replica data directory must exit with code 3, got %s", inspectFmt(t, cid, "{{.State.ExitCode}} {{.State.Status}}"))
	logs := mustSh(t, "docker", "logs", cid)
	require.Contains(t, logs, "replica data directory is empty")
	mustSh(t, "docker", "stop", cid)
	require.Empty(t, strings.TrimSpace(mustSh(t, "docker", "run", "--rm", "-v", vol+":/v", "alpine:3", "ls", "-A", "/v")),
		"the replica start command must write nothing (no initdb of a fresh, non-replicating primary)")

	// ---- rebuild: a fresh clone, streaming again -------------------------------
	var rb Resp
	require.Truef(t, eventually(3*time.Minute, 5*time.Second, func() bool {
		rb = s.Do("POST", "/services/"+r.ID+"/database/rebuild", map[string]any{})
		return rb.Status == http.StatusAccepted
	}), "rebuild was not accepted: HTTP %d %s", rb.Status, rb.Raw)
	_ = eventually(45*time.Second, time.Second, func() bool {
		it := s.replicaItem(pg.ID, r.ID)
		return it != nil && it.Str("state") != replStreaming
	})
	s.waitStreaming(pg.ID, r.ID)
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		cs := s.RunningContainers(r.ID)
		return len(cs) == 1 && pgExec(t, s, r.ID, "SELECT pg_is_in_recovery()") == "t"
	}), "the rebuilt replica is not a standby")
	// 1000 seeded + 100 + 5001 (written through the consumer) + 6001 (after the restart).
	require.Equal(t, 1102, replicaCount(t, s, r.ID))
}

// Manual switchover Promote on one server: the replica becomes the primary with
// the data intact, the old primary is fenced and stopped (data kept), consumers
// are restaged and rewritten to the new primary, the backup schedule moves, and
// the old primary can be rebuilt as a replica of the new one.
func TestReplicaSameServerPromoteSwitchover(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2)
	ctx := context.Background()
	p := newReplicaPair(t, s, app, "pg-r1")
	pg, r, api, c := p.Primary, p.Replica, p.Consumer, p.Client

	// A backup schedule on the primary: it follows the primary role.
	dest := s.Do("POST", "/backup-destinations", map[string]any{"name": "promote", "kind": "local", "localDir": "itest-promote"})
	require.Equal(t, http.StatusCreated, dest.Status, string(dest.Raw))
	sc := s.Do("POST", "/services/"+pg.ID+"/backup-schedules", map[string]any{"destinationId": dest.JSON().Str("id"), "cron": "@daily"})
	require.Equal(t, http.StatusCreated, sc.Status, string(sc.Raw))
	schedID := sc.JSON().Str("id")

	// The consumer reads the replica from now on.
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool { return s.Service(api.ID).Bool("hasStagedChanges") }), "consumer not restaged")
	s.deployStaged(api.ID)
	waitConsumer(t, c, "/", "v1", 60*time.Second)

	// ---- plan ------------------------------------------------------------------
	plan := s.Do("POST", "/services/"+r.ID+"/database/promote", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	pp := plan.JSON().Obj("plan")
	require.Equal(t, "switchover", pp.Str("mode"), string(plan.Raw))
	require.Equal(t, 1, pp.Int("schedulesMoving"), "the backup schedule follows the primary role: %s", plan.Raw)
	require.Empty(t, pp["blockers"], string(plan.Raw))
	require.Contains(t, string(plan.Raw), api.Slug, "the plan lists the consumers that reference the old primary")
	require.NotContains(t, strings.ToLower(string(plan.Raw)), "failover", "no failover wording in the API")

	// ---- confirmation is required ------------------------------------------------
	for _, body := range []map[string]any{{}, {"confirm": "wrong"}} {
		bad := s.Do("POST", "/services/"+r.ID+"/database/promote", body)
		require.GreaterOrEqual(t, bad.Status, 400, string(bad.Raw))
		require.Contains(t, string(bad.Raw), "promote_confirmation_required")
	}
	require.Equal(t, "t", pgExec(t, s, r.ID, "SELECT pg_is_in_recovery()"), "nothing happened yet")

	// ---- promote -------------------------------------------------------------------
	pgExec(t, s, pg.ID, "INSERT INTO notes VALUES (7001, 'last write on the old primary')")
	oldCID := s.RunningContainers(pg.ID)[0].ID
	pr := s.Do("POST", "/services/"+r.ID+"/database/promote", map[string]any{"confirm": r.Slug})
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, pr.Status, string(pr.Raw))
	var status, step, msg string
	require.Truef(t, eventually(4*time.Minute, 2*time.Second, func() bool {
		if err := s.Pool.QueryRow(ctx, `SELECT status, step, message FROM database_promotions WHERE replica_service_id = $1 ORDER BY started_at DESC LIMIT 1`, r.ID).Scan(&status, &step, &msg); err != nil {
			return false
		}
		return status != "running"
	}), "the promotion did not finish: %s/%s %s", status, step, msg)
	require.Equal(t, "succeeded", status, "promotion %s: %s", step, msg)

	// The new primary: writable, with every row including the last write on the old one.
	require.Equal(t, "f", pgExec(t, s, r.ID, "SELECT pg_is_in_recovery()"))
	require.Equal(t, "1", pgExec(t, s, r.ID, "SELECT count(*) FROM notes WHERE id = 7001"), "a switchover loses no committed write")
	require.Equal(t, 1001, replicaCount(t, s, r.ID))
	pgExec(t, s, r.ID, "INSERT INTO notes VALUES (7002, 'first write on the new primary')")

	// The old primary: stopped, fenced, data kept, never restarted by Docker.
	require.Equal(t, "false", inspectFmt(t, oldCID, "{{.State.Running}}"))
	require.Equal(t, "no", inspectFmt(t, oldCID, "{{.HostConfig.RestartPolicy.Name}}"))
	var fenced int
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT count(*) FROM database_fences WHERE service_id = $1`, pg.ID).Scan(&fenced))
	require.Equal(t, 1, fenced)
	require.Equal(t, "fenced", s.replicationView(pg.ID).Str("role"))
	require.Contains(t, mustSh(t, "docker", "volume", "ls", "-q", "--filter", "name="+dataVolume(pg.ID)), dataVolume(pg.ID), "the old primary's data is kept")
	// With no replicas left the new primary is a plain database: its role is
	// "standalone" (a "primary" is a database that has replicas).
	require.Equal(t, "standalone", s.replicationView(r.ID).Str("role"))
	var replicaRows int
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT count(*) FROM database_replicas WHERE service_id = $1`, r.ID).Scan(&replicaRows))
	require.Zero(t, replicaRows, "the promoted replica is no replica any more")

	// The schedule follows the primary role.
	var schedService string
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT service_id::text FROM backup_schedules WHERE id = $1`, schedID).Scan(&schedService))
	require.Equal(t, r.ID, schedService)

	// The consumer is restaged with its references pointing at the new primary, and
	// after a deploy LEGACY_URL (a reference to the OLD service's DATABASE_URL, whose
	// container is now stopped) reaches the new primary.
	require.True(t, s.Service(api.ID).Bool("hasStagedChanges"))
	s.deployStaged(api.ID)
	waitConsumer(t, c, "/", "v1", 60*time.Second)
	waitPgq(t, c, "LEGACY_URL", "select pg_is_in_recovery()::text", "false", 60*time.Second)
	waitPgq(t, c, "DATABASE_URL", "select count(*)::text from notes where id = 7002", "1", 30*time.Second)

	// The audit trail records the promotion and the fence.
	var audited int
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE project_id = $1 AND action IN ('database.promote', 'database.fence')`, s.ProjectID).Scan(&audited))
	require.GreaterOrEqual(t, audited, 2)

	// ---- the old primary comes back as a replica of the new one ---------------------
	rb := s.Do("POST", "/services/"+pg.ID+"/database/rebuild", map[string]any{"followServiceId": r.ID})
	require.Equal(t, http.StatusAccepted, rb.Status, string(rb.Raw))
	// The saga leaves `streaming` first (stale state right after the request), then comes back.
	_ = eventually(45*time.Second, time.Second, func() bool {
		it := s.replicaItem(r.ID, pg.ID)
		return it != nil && it.Str("state") != replStreaming
	})
	s.waitStreaming(r.ID, pg.ID)
	require.Equal(t, "t", pgExec(t, s, pg.ID, "SELECT pg_is_in_recovery()"))
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool {
		return pgExec(t, s, pg.ID, "SELECT count(*) FROM notes WHERE id = 7002") == "1"
	}), "the rebuilt old primary did not receive the new primary's writes")
}

// Deleting a replica drops its slot, its role and its pg_hba line on the
// primary (so a dead replica can never pin WAL) and removes the service.
func TestReplicaSameServerRemoveDropsSlot(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2)
	ctx := context.Background()
	p := newReplicaPair(t, s, app, "pg-r1")
	pg, r := p.Primary, p.Replica
	slot, repl := s.replicaIdentity(t, r.ID)
	require.Equal(t, "1", pgExec(t, s, pg.ID, "SELECT count(*) FROM pg_replication_slots WHERE slot_name = '"+slot+"'"))

	del := s.Do("DELETE", "/services/"+r.ID+"/database/replica", nil)
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent}, del.Status, string(del.Raw))
	require.Truef(t, eventually(3*time.Minute, 2*time.Second, func() bool {
		var n int
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM database_replicas WHERE service_id = $1`, r.ID).Scan(&n)
		return n == 0 && s.Do("GET", "/services/"+r.ID, nil).Status == http.StatusNotFound
	}), "the replica was not removed: %v", s.replicaRow(r.ID))

	require.Equal(t, "0", pgExec(t, s, pg.ID, "SELECT count(*) FROM pg_replication_slots WHERE slot_name = '"+slot+"'"), "the slot is dropped")
	require.Equal(t, "0", pgExec(t, s, pg.ID, "SELECT count(*) FROM pg_roles WHERE rolname = '"+repl+"'"), "the replication role is dropped")
	require.Equal(t, "0", pgExec(t, s, pg.ID,
		"SELECT count(*) FROM pg_hba_file_rules WHERE 'replication' = ANY(database) AND '"+repl+"' = ANY(user_name)"), "the pg_hba line is removed")
	require.Equal(t, "0", pgExec(t, s, pg.ID, "SELECT count(*) FROM pg_stat_replication"))
	require.Empty(t, s.RunningContainers(r.ID), "the replica container is removed")
	require.NotContains(t, mustSh(t, "docker", "volume", "ls", "-q", "--filter", "name="+dataVolume(r.ID)), dataVolume(r.ID), "a removed replica's volume goes with it")

	// The primary has no replicas again: it can be deleted/upgraded like any database,
	// and its reader falls back to the writer.
	require.Equal(t, "standalone", s.replicationView(pg.ID).Str("role"), "no replicas left: a plain database again")
	waitPgq(t, p.Client, "DATABASE_URL", "select pg_is_in_recovery()::text", "false", 30*time.Second)
}
