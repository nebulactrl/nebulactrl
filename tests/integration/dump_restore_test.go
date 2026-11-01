//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// postgisImage is the image of the managed PostGIS flavor (postgis 18, 3.6).
const postgisImage = "postgis/postgis:18-3.6"

// pullEngine lets the harness agent use images that exist for amd64 only
// (postgis/postgis has no arm64 variant): when the registry has no manifest for
// the host's platform, the image is pulled as linux/amd64, which the daemon
// runs under emulation. On amd64 hosts it changes nothing.
type pullEngine struct{ *docker.SDKEngine }

func (p pullEngine) ImagePull(ctx context.Context, ref, registryAuth string, progress func(string)) error {
	err := p.SDKEngine.ImagePull(ctx, ref, registryAuth, progress)
	if err == nil || !strings.Contains(err.Error(), "no matching manifest") {
		return err
	}
	out, perr := exec.CommandContext(ctx, "docker", "pull", "--platform", "linux/amd64", ref).CombinedOutput()
	if perr != nil {
		return fmt.Errorf("%w; amd64 fallback: %v: %s", err, perr, out)
	}
	return nil
}

func wrapPull(e *docker.SDKEngine) docker.Engine { return pullEngine{e} }

// putFile uploads a file to a dump restore like the CLI and the web app do.
func (s *Stack) putFile(restoreID, token, name string, data []byte) Resp {
	s.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		s.Srv.URL+"/api/v1/dump-restores/"+restoreID+"/upload?fileName="+name, bytes.NewReader(data))
	require.NoError(s.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := (&http.Client{}).Do(req)
	require.NoError(s.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(s.t, err)
	return Resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
}

func (s *Stack) waitDumpStatus(id string, timeout time.Duration, want ...string) J {
	s.t.Helper()
	var v J
	ok := eventually(timeout, time.Second, func() bool {
		r := s.Do("GET", "/dump-restores/"+id, nil)
		if r.Status != http.StatusOK {
			return false
		}
		v = r.JSON()
		for _, w := range want {
			if v.Str("status") == w {
				return true
			}
		}
		return v.Str("status") == "failed"
	})
	require.True(s.t, ok, "dump restore %s did not reach %v within %s: %v", id, want, timeout, v)
	return v
}

// dockerOut runs a docker command and returns its stdout.
func dockerOut(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	require.NoError(t, cmd.Run(), "docker %s: %s", strings.Join(args, " "), errb.String())
	return out.Bytes()
}

// startPostGISSource starts a stand-alone PostGIS server (the "old" database a
// user migrates away from, owned by a user and a database name the managed
// target does not have) and returns a function that runs psql in it.
func startPostGISSource(t *testing.T) (psql func(sql string) string, dump func(args ...string) []byte) {
	t.Helper()
	const name = "nc-it-dump-src"
	_, _ = sh(context.Background(), "", "docker", "rm", "-f", name)
	_ = dockerOut(t, "run", "-d", "--name", name, "--platform", "linux/amd64",
		"-e", "POSTGRES_USER=coolifyuser", "-e", "POSTGRES_PASSWORD=srcpass", "-e", "POSTGRES_DB=coolify_app", postgisImage)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", "-v", name) })
	// The image's init script runs a temporary server that only listens on the
	// socket; a TCP connection proves the real server is up.
	require.True(t, eventually(3*time.Minute, 2*time.Second, func() bool {
		_, err := sh(context.Background(), "", "docker", "exec", "-e", "PGPASSWORD=srcpass", name,
			"psql", "-h", "127.0.0.1", "-U", "coolifyuser", "-d", "coolify_app", "-tAc", "select 1")
		return err == nil
	}), "the source PostGIS never accepted TCP connections")
	psql = func(sql string) string {
		return strings.TrimSpace(string(dockerOut(t, "exec", "-e", "PGPASSWORD=srcpass", name,
			"psql", "-U", "coolifyuser", "-d", "coolify_app", "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql)))
	}
	dump = func(args ...string) []byte {
		a := append([]string{"exec", "-e", "PGPASSWORD=srcpass", name, "pg_dump", "-U", "coolifyuser"}, args...)
		return dockerOut(t, append(a, "coolify_app")...)
	}
	return psql, dump
}

// TestRestoreFromDump restores dump files made on another PostGIS server into
// a managed PostGIS database through the upload path: custom format into a
// new database, then plain SQL (gzip) in place, then a broken file that must
// leave the live database untouched.
func TestRestoreFromDump(t *testing.T) {
	requireTier(t, tierFull)
	s := SharedStackN(t, 2)
	ctx := context.Background()
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.servicesMu.Lock()
		ids := append([]string(nil), s.serviceIDs...)
		s.servicesMu.Unlock()
		for _, id := range ids {
			short := id
			if len(short) > 8 {
				short = short[:8]
			}
			out, _ := sh(cctx, "", "docker", "volume", "ls", "-q", "--filter", "name=nc-vol-"+short)
			for _, v := range strings.Fields(out) {
				_, _ = sh(cctx, "", "docker", "volume", "rm", "-f", v)
			}
		}
	})

	// ---- the source: PostGIS with data, owners and grants of its own -------
	srcSQL, srcDump := startPostGISSource(t)
	srcSQL(`CREATE ROLE reporting LOGIN;
CREATE TABLE places (id serial PRIMARY KEY, name text NOT NULL, geom geometry(Point,4326));
INSERT INTO places (name, geom) SELECT 'place ' || g, ST_SetSRID(ST_MakePoint(g, g), 4326) FROM generate_series(1, 200) g;
CREATE INDEX places_geom_idx ON places USING gist (geom);
GRANT SELECT ON places TO reporting;
CREATE VIEW near_origin AS SELECT id, name FROM places WHERE ST_DWithin(geom, ST_SetSRID(ST_MakePoint(0, 0), 4326), 10);
CREATE FUNCTION place_count() RETURNS bigint LANGUAGE sql AS $$ SELECT count(*) FROM places $$`)
	require.Equal(t, "200", srcSQL("SELECT count(*) FROM places"))
	custom := srcDump("-Fc")
	plain := srcDump("-Fp")
	require.True(t, bytes.HasPrefix(custom, []byte("PGDMP")))
	require.Contains(t, string(plain), "OWNER TO coolifyuser", "the plain dump names the source's owner")
	require.Contains(t, string(plain), "GRANT SELECT ON TABLE public.places TO reporting")
	var gzPlain bytes.Buffer
	zw := gzip.NewWriter(&gzPlain)
	_, err := zw.Write(plain)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	// ---- the target: a managed PostGIS database ----------------------------
	r := s.Do("POST", "/environments/"+s.EnvID+"/databases", map[string]any{
		"name": "gis", "engine": "postgres", "version": "18", "flavor": "postgis", "serverId": s.ServerID,
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	target := Service{ID: r.JSON().Str("id"), Slug: r.JSON().Str("slug")}
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, target.ID)
	s.servicesMu.Unlock()
	s.deployStaged(target.ID)
	waitPostgres(t, s, target.ID)
	token := s.Token(policy.AllScopes, nil)

	// ---- 1. custom format into a NEW database ------------------------------
	r = s.Do("POST", "/services/"+target.ID+"/dump-restores", map[string]any{
		"mode": "new", "name": "gis-restored", "fileName": "coolify.pgdump", "sizeBytes": len(custom),
	}, "Idempotency-Key", "itest-dump-1")
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rid := r.JSON().Str("id")
	sibID := r.JSON().Str("targetServiceId")
	require.NotEmpty(t, sibID)
	require.NotEqual(t, target.ID, sibID)
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, sibID)
	s.servicesMu.Unlock()

	// The upload is refused until the new database runs.
	if rv := s.Do("GET", "/dump-restores/"+rid, nil).JSON(); rv.Str("status") == "provisioning" {
		res := s.putFile(rid, token, "x", custom)
		require.Equal(t, http.StatusConflict, res.Status, string(res.Raw))
	}
	aw := s.waitDumpStatus(rid, 6*time.Minute, "awaiting_upload")
	require.Equal(t, "awaiting_upload", aw.Str("status"), "restore: %v", aw)

	// A file that does not fit the engine is refused and changes nothing.
	res := s.putFile(rid, token, "mysql.sql", []byte("-- MySQL dump 10.13\nCREATE TABLE t (a int);\n"))
	require.Equal(t, http.StatusUnprocessableEntity, res.Status, string(res.Raw))
	require.Contains(t, string(res.Raw), "MySQL")
	require.Equal(t, "awaiting_upload", s.Do("GET", "/dump-restores/"+rid, nil).JSON().Str("status"))

	res = s.putFile(rid, token, "coolify.pgdump", custom)
	require.Equal(t, http.StatusAccepted, res.Status, string(res.Raw))
	done := s.waitDumpStatus(rid, 5*time.Minute, "succeeded")
	require.Equal(t, "succeeded", done.Str("status"), "restore failed: %s\nlog:\n%s", done.Str("error"), done.Str("log"))
	sum := sha256.Sum256(custom)
	require.Equal(t, hex.EncodeToString(sum[:]), done.Str("sha256"))
	require.Equal(t, "pg_custom", done.Str("format"))
	require.Contains(t, done.Str("log"), "switching")

	waitPostgres(t, s, sibID)
	require.Equal(t, "200", pgExec(t, s, sibID, "SELECT count(*) FROM places"), "the rows arrived")
	require.Equal(t, "place 150", pgExec(t, s, sibID, "SELECT name FROM places WHERE id = 150"))
	require.Equal(t, "1", pgExec(t, s, sibID, "SELECT count(*) FROM pg_extension WHERE extname = 'postgis'"), "the PostGIS extension is there")
	require.NotEmpty(t, pgExec(t, s, sibID, "SELECT postgis_full_version()"))
	require.Equal(t, "10", pgExec(t, s, sibID, "SELECT count(*) FROM places WHERE ST_DWithin(geom, ST_SetSRID(ST_MakePoint(5, 5), 4326), 7.1)"),
		"spatial queries work on the restored geometry")
	require.Equal(t, "200", pgExec(t, s, sibID, "SELECT place_count()"))
	require.NotEmpty(t, pgExec(t, s, sibID, "SELECT count(*) FROM near_origin"))
	require.Equal(t, "postgres", pgExec(t, s, sibID, "SELECT tableowner FROM pg_tables WHERE tablename = 'places'"),
		"objects are owned by the managed user, not by the source's coolifyuser")
	require.Equal(t, "1", pgExec(t, s, sibID, "SELECT count(*) FROM pg_indexes WHERE indexname = 'places_geom_idx'"))
	require.Equal(t, "0", pgExec(t, s, sibID, "SELECT count(*) FROM pg_database WHERE datname LIKE 'nc\\_%'"), "no scratch database is left behind")
	// The original database never changed.
	require.Equal(t, "0", pgExec(t, s, target.ID, "SELECT count(*) FROM information_schema.tables WHERE table_name = 'places'"))

	// ---- 2. gzip'd plain SQL, IN PLACE, after a safety backup --------------
	pgExec(t, s, sibID, "INSERT INTO places (name) VALUES ('written after the first restore')")
	require.Equal(t, "201", pgExec(t, s, sibID, "SELECT count(*) FROM places"))
	r = s.Do("POST", "/backup-destinations", map[string]any{"name": "dump-itest", "kind": "local", "localDir": "dump-itest"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	destID := r.JSON().Str("id")

	r = s.Do("POST", "/services/"+sibID+"/dump-restores", map[string]any{"mode": "in_place", "confirmSlug": "wrong", "destinationId": destID})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, "in place needs the typed slug: %s", r.Raw)
	slug := s.Service(sibID).Str("slug")
	r = s.Do("POST", "/services/"+sibID+"/dump-restores", map[string]any{
		"mode": "in_place", "confirmSlug": slug, "destinationId": destID, "fileName": "coolify.sql.gz",
	})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rid2 := r.JSON().Str("id")
	require.NotEmpty(t, r.JSON().Str("preBackupId"), "a safety backup is taken first")
	aw = s.waitDumpStatus(rid2, 3*time.Minute, "awaiting_upload")
	require.Equal(t, "awaiting_upload", aw.Str("status"), "restore: %v", aw)

	res = s.putFile(rid2, token, "coolify.sql.gz", gzPlain.Bytes())
	require.Equal(t, http.StatusAccepted, res.Status, string(res.Raw))
	done = s.waitDumpStatus(rid2, 5*time.Minute, "succeeded")
	require.Equal(t, "succeeded", done.Str("status"), "restore failed: %s\nlog:\n%s", done.Str("error"), done.Str("log"))
	require.True(t, done.Bool("gzip"))
	require.Equal(t, "sql", done.Str("format"))
	require.Contains(t, done.Str("log"), "skipped", "the dump's ownership and grant statements were skipped")
	waitPostgres(t, s, sibID)
	require.Equal(t, "200", pgExec(t, s, sibID, "SELECT count(*) FROM places"), "in place replaced the data: the later row is gone")
	require.Equal(t, "postgres", pgExec(t, s, sibID, "SELECT tableowner FROM pg_tables WHERE tablename = 'places'"))
	require.NotEmpty(t, pgExec(t, s, sibID, "SELECT postgis_full_version()"))

	// ---- 3. a broken file fails and leaves the live database alone --------
	r = s.Do("POST", "/services/"+sibID+"/dump-restores", map[string]any{"mode": "in_place", "confirmSlug": slug, "destinationId": destID})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rid3 := r.JSON().Str("id")
	s.waitDumpStatus(rid3, 3*time.Minute, "awaiting_upload")
	pgExec(t, s, sibID, "INSERT INTO places (name) VALUES ('must survive a failed restore')")
	broken := []byte("-- PostgreSQL database dump\nCREATE TABLE ok_table (a int);\nTHIS IS NOT SQL;\n")
	res = s.putFile(rid3, token, "broken.sql", broken)
	require.Equal(t, http.StatusAccepted, res.Status, string(res.Raw))
	failed := s.waitDumpStatus(rid3, 2*time.Minute, "succeeded")
	require.Equal(t, "failed", failed.Str("status"), "restore: %v", failed)
	require.Contains(t, failed.Str("error"), "syntax error")
	require.Equal(t, "201", pgExec(t, s, sibID, "SELECT count(*) FROM places"), "the live database was not touched")
	require.Equal(t, "0", pgExec(t, s, sibID, "SELECT count(*) FROM information_schema.tables WHERE table_name = 'ok_table'"))
	require.Equal(t, "0", pgExec(t, s, sibID, "SELECT count(*) FROM pg_database WHERE datname LIKE 'nc\\_%'"), "the scratch database was removed")

	// ---- the history lists all three, without the log ---------------------
	r = s.Do("GET", "/services/"+sibID+"/dump-restores", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON().Items(), 3)
	for _, it := range r.JSON().Items() {
		require.Empty(t, it.Str("log"))
	}

	// ---- no secret leaked into the audit log or the restore rows ----------
	var leaks int
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE to_jsonb(audit_events)::text LIKE '%srcpass%'`).Scan(&leaks))
	require.Zero(t, leaks)
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT count(*) FROM dump_restores WHERE to_jsonb(dump_restores)::text LIKE '%srcpass%'`).Scan(&leaks))
	require.Zero(t, leaks)
}
