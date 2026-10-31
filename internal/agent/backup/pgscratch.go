package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// pgScratch is the scratch-database half of a PostgreSQL restore, shared by the
// stored-backup restore (Runner) and the uploaded-file restore (DumpRunner).
//
// A dump creates what it needs itself, extensions and their schemas included
// (PostGIS: postgis, topology, tiger geocoder with their schemas), so it must be restored into
// an EMPTY database. The database a postgres image provisions is not empty: the
// PostGIS image creates its extensions in it at first start, and a restore
// into it fails with `schema "tiger" already exists`. So the dump goes into a
// scratch database created from template0 and is swapped in under the target's
// name only when it restored completely: a failed or abandoned restore leaves
// the target exactly as it was.
type pgScratch struct {
	ex        Execer
	container string
	user, pw  string
	database  string // the target's name
	names     dumpNames
	secrets   map[string]string
}

func newPGScratch(ex Execer, container, user, pw, database, restoreID string, secrets map[string]string) *pgScratch {
	return &pgScratch{ex: ex, container: container, user: user, pw: pw, database: database, names: namesFor(restoreID, database), secrets: secrets}
}

// sql runs one statement with psql against db and returns its trimmed output.
func (s *pgScratch) sql(ctx context.Context, db, stmt string) (string, error) {
	var out bytes.Buffer
	errs := &lineSink{}
	code, err := s.ex.ContainerExec(ctx, s.container,
		[]string{"psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "--username", s.user, "--dbname", db, "-c", stmt},
		docker.ExecSpec{Env: []string{"PGPASSWORD=" + s.pw}}, &out, errs)
	errs.flush()
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := scrub(errs.String(), s.secrets)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", code)
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// prepare creates the empty scratch database.
func (s *pgScratch) prepare(ctx context.Context) error {
	scratch := quoteIdent(s.names.scratch)
	for _, stmt := range []string{
		"DROP DATABASE IF EXISTS " + scratch + " WITH (FORCE)",
		"CREATE DATABASE " + scratch + " TEMPLATE template0 OWNER " + quoteIdent(s.user),
	} {
		if _, err := s.sql(ctx, s.names.maint, stmt); err != nil {
			return err
		}
	}
	return nil
}

// discard drops the scratch database; it works after ctx was canceled.
func (s *pgScratch) discard(ctx context.Context) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	_, _ = s.sql(cctx, s.names.maint, "DROP DATABASE IF EXISTS "+quoteIdent(s.names.scratch)+" WITH (FORCE)")
}

// analyze refreshes the planner statistics of the restored data. A failure is
// returned for logging only: the restore is kept.
func (s *pgScratch) analyze(ctx context.Context) error {
	_, err := s.sql(ctx, s.names.scratch, "ANALYZE")
	return err
}

// switchOver gives the scratch database the target's name: new connections to
// the target are refused, the open ones are ended, the target is renamed away
// and dropped once the scratch database holds its name. On failure the target
// is put back and the scratch database is dropped.
func (s *pgScratch) switchOver(ctx context.Context, logf func(string)) error {
	n := s.names
	db, scratch, old := quoteIdent(s.database), quoteIdent(n.scratch), quoteIdent(n.old)
	exists, err := s.sql(ctx, n.maint, "SELECT 1 FROM pg_database WHERE datname = "+quoteLit(s.database))
	if err != nil {
		s.discard(ctx)
		return fmt.Errorf("could not check the live database: %v", err)
	}
	if exists != "1" {
		if _, err := s.sql(ctx, n.maint, "ALTER DATABASE "+scratch+" RENAME TO "+db); err != nil {
			s.discard(ctx)
			return fmt.Errorf("could not switch over: %v", err)
		}
		return nil
	}
	reallow := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_, _ = s.sql(cctx, n.maint, "ALTER DATABASE "+db+" WITH ALLOW_CONNECTIONS true")
	}
	if _, err := s.sql(ctx, n.maint, "ALTER DATABASE "+db+" WITH ALLOW_CONNECTIONS false"); err != nil {
		s.discard(ctx)
		return fmt.Errorf("could not stop new connections to %s: %v", s.database, err)
	}
	var renamed bool
	var lastErr error
	for attempt := 0; attempt < 5 && !renamed; attempt++ {
		if _, err := s.sql(ctx, n.maint, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = "+quoteLit(s.database)+" AND pid <> pg_backend_pid()"); err != nil {
			lastErr = err
		}
		if _, err := s.sql(ctx, n.maint, "ALTER DATABASE "+db+" RENAME TO "+old); err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		renamed = true
	}
	if !renamed {
		reallow()
		s.discard(ctx)
		return fmt.Errorf("could not switch over: %v", lastErr)
	}
	if _, err := s.sql(ctx, n.maint, "ALTER DATABASE "+scratch+" RENAME TO "+db); err != nil {
		// Put the live database back.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		_, _ = s.sql(cctx, n.maint, "ALTER DATABASE "+old+" RENAME TO "+db)
		cancel()
		reallow()
		s.discard(ctx)
		return fmt.Errorf("could not switch over: %v", err)
	}
	logf("dropping the replaced database")
	if _, err := s.sql(ctx, n.maint, "DROP DATABASE IF EXISTS "+old+" WITH (FORCE)"); err != nil {
		logf("could not drop the replaced database " + n.old + " (drop it by hand): " + err.Error())
	}
	return nil
}
