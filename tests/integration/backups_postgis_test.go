//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A PostGIS database is backed up and restored, into a new database and in
// place. The postgis/postgis image creates its extensions (postgis, topology,
// fuzzystrmatch, tiger geocoder) in the database it provisions, and a dump
// creates them again, so restoring into that database as it is fails with
// `schema "tiger" already exists`. The agent therefore restores into an empty
// scratch database made from template0 and swaps it in.
//
// It uses the image of TestRestoreFromDump (postgis/postgis:18-3.6, amd64 only,
// emulated on arm64 hosts), which that test pulls in the same tier already, so
// it has no extra gate.
func TestBackupRestorePostGIS(t *testing.T) {
	requireTier(t, tierFull)
	s := SharedStackN(t, 2)
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

	r := s.Do("POST", "/environments/"+s.EnvID+"/databases", map[string]any{
		"name": "gis-backup", "engine": "postgres", "version": "18", "flavor": "postgis", "serverId": s.ServerID,
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	src := Service{ID: r.JSON().Str("id"), Slug: r.JSON().Str("slug")}
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, src.ID)
	s.servicesMu.Unlock()
	s.deployStaged(src.ID)
	waitPostgres(t, s, src.ID)

	// The image's own database already holds the extensions and their schemas:
	// exactly what a restore of a dump of them collides with.
	for _, schema := range []string{"tiger", "topology"} {
		require.Equal(t, "1", pgExec(t, s, src.ID, "SELECT count(*) FROM pg_namespace WHERE nspname = '"+schema+"'"), "the image creates the %s schema", schema)
	}
	version := pgExec(t, s, src.ID, "SELECT postgis_lib_version()")
	require.NotEmpty(t, version)
	pgExec(t, s, src.ID, `CREATE TABLE places (id serial PRIMARY KEY, name text NOT NULL, geom geometry(Point,4326));
INSERT INTO places (name, geom) SELECT 'place ' || g, ST_SetSRID(ST_MakePoint(g, g), 4326) FROM generate_series(1, 100) g;
CREATE INDEX places_geom_idx ON places USING gist (geom)`)
	require.Equal(t, "100", pgExec(t, s, src.ID, "SELECT count(*) FROM places"))

	r = s.Do("POST", "/backup-destinations", map[string]any{"name": "gis-disk", "kind": "local", "localDir": "gis-itest"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	dest := r.JSON()
	b := s.backup(src.ID, dest.Str("id"))
	require.Equal(t, "succeeded", b.Str("status"), "backup: %v", b)
	require.Equal(t, "pg_custom", b.Str("format"))

	// ---- mode new: a sibling, whose own database already has the extensions
	pgExec(t, s, src.ID, "INSERT INTO places (name) VALUES ('written after the backup')")
	sib := s.restoreNew(b.Str("id"), "gis-restored")
	require.NotEqual(t, src.ID, sib.ID)
	waitPostgres(t, s, sib.ID)
	requireRestoredPostGIS(t, s, sib.ID, version)
	require.Equal(t, "101", pgExec(t, s, src.ID, "SELECT count(*) FROM places"), "the source database is untouched")

	// ---- mode in_place: over the source, which has the extensions as well --
	r = s.Do("POST", "/backups/"+b.Str("id")+"/restore", map[string]any{"mode": "in_place", "confirmSlug": src.Slug})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rs := s.waitRestore(r.JSON().Str("id"), 5*time.Minute)
	require.Equal(t, "succeeded", rs.Str("status"), "in-place restore: %v", rs)
	require.NotEmpty(t, rs.Str("preBackupId"), "a safety backup precedes an in-place restore")
	waitPostgres(t, s, src.ID)
	requireRestoredPostGIS(t, s, src.ID, version)
}

// requireRestoredPostGIS checks a database restored from the PostGIS backup: the
// rows and the geometry index, the extensions and their schemas, the PostGIS
// version, a spatial query, and no scratch database left behind.
func requireRestoredPostGIS(t *testing.T, s *Stack, serviceID, version string) {
	t.Helper()
	require.Equal(t, "100", pgExec(t, s, serviceID, "SELECT count(*) FROM places"), "the rows of the backup arrived, not the later one")
	require.Equal(t, "place 50", pgExec(t, s, serviceID, "SELECT name FROM places WHERE id = 50"))
	require.Equal(t, "1", pgExec(t, s, serviceID, "SELECT count(*) FROM pg_indexes WHERE indexname = 'places_geom_idx'"))
	require.Equal(t, version, pgExec(t, s, serviceID, "SELECT postgis_lib_version()"), "the PostGIS version")
	require.NotEmpty(t, pgExec(t, s, serviceID, "SELECT postgis_full_version()"))
	require.Equal(t, "7", pgExec(t, s, serviceID, "SELECT count(*) FROM places WHERE ST_DWithin(geom, ST_SetSRID(ST_MakePoint(10, 10), 4326), 4.3)"),
		"spatial queries work on the restored geometry")
	for _, ext := range []string{"postgis", "postgis_topology", "fuzzystrmatch", "postgis_tiger_geocoder"} {
		require.Equal(t, "1", pgExec(t, s, serviceID, "SELECT count(*) FROM pg_extension WHERE extname = '"+ext+"'"), "extension %s", ext)
	}
	for _, schema := range []string{"tiger", "topology"} {
		require.Equal(t, "1", pgExec(t, s, serviceID, "SELECT count(*) FROM pg_namespace WHERE nspname = '"+schema+"'"), "schema %s", schema)
	}
	require.Equal(t, "0", pgExec(t, s, serviceID, "SELECT count(*) FROM pg_database WHERE datname LIKE 'nc\\_%'"), "no scratch or replaced database is left behind")
}
