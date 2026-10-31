package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/db/migrations"
	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Restore of a control-plane backup. It runs from the command line
// (`nebulactrl-server restore`), never inside the server: the server needs a
// database to start, and a restore is exactly what you do when there is none.
// So it reads nothing from the (empty) database: the source is named on the
// command line and its credentials come from the environment.

// RestoreSource names where control-plane backups are stored.
type RestoreSource struct {
	// Dir is a local directory (a destination's root, or any parent of it). Set
	// it, or Bucket.
	Dir string

	// S3.
	Endpoint   string
	Region     string
	Bucket     string
	Prefix     string // only below this key prefix
	PathStyle  bool
	CompatMode bool
	AccessKey  string
	SecretKey  string
}

// ParseRestoreSource turns the --from argument into a source: `s3://bucket[/prefix]`,
// `file:///dir` or a plain directory path. S3 settings that a URL can not carry
// are added by the caller afterwards.
func ParseRestoreSource(from string) (RestoreSource, error) {
	from = strings.TrimSpace(from)
	switch {
	case from == "":
		return RestoreSource{}, errors.New("--from is required: an s3://bucket/prefix URL or a directory")
	case strings.HasPrefix(from, "s3://"):
		u, err := url.Parse(from)
		if err != nil || u.Host == "" {
			return RestoreSource{}, fmt.Errorf("invalid S3 URL %q: use s3://bucket/optional/prefix", from)
		}
		return RestoreSource{Bucket: u.Host, Prefix: strings.Trim(u.Path, "/")}, nil
	case strings.HasPrefix(from, "file://"):
		u, err := url.Parse(from)
		if err != nil || u.Path == "" {
			return RestoreSource{}, fmt.Errorf("invalid file URL %q: use file:///absolute/directory", from)
		}
		return RestoreSource{Dir: u.Path}, nil
	default:
		return RestoreSource{Dir: from}, nil
	}
}

// Store opens the source.
func (src RestoreSource) Store() (backup.Store, error) {
	if src.Bucket == "" {
		if src.Dir == "" {
			return nil, errors.New("no backup source given")
		}
		st, err := backup.NewLocalStore(src.Dir, "")
		if err != nil {
			return nil, err
		}
		return st, nil
	}
	return backup.NewS3Store(agentprotocol.BackupDestination{
		Kind: agentprotocol.DestS3, Endpoint: src.Endpoint, Region: src.Region, Bucket: src.Bucket, PathStyle: src.PathStyle,
		CompatMode: src.CompatMode || src.Endpoint != "",
	}, src.AccessKey, src.SecretKey)
}

// RemoteBackup is a control-plane backup found in a store.
type RemoteBackup struct {
	Manifest    InstanceManifest
	ManifestKey string
}

// manifestSuffix ends the key of a control-plane backup manifest.
const manifestSuffix = ".dump.manifest.json"

// isInstanceManifestKey reports whether key is the manifest of a control-plane
// backup: below a nebulactrl-instance directory.
func isInstanceManifestKey(key string) bool {
	if !strings.HasSuffix(key, manifestSuffix) {
		return false
	}
	for _, seg := range strings.Split(path.Dir(key), "/") {
		if seg == InstanceDir {
			return true
		}
	}
	return false
}

// FindBackups lists the control-plane backups in st below prefix, newest first.
// A backup counts only when its manifest (written last) exists and reads.
func FindBackups(ctx context.Context, st backup.Store, prefix string) ([]RemoteBackup, error) {
	l, ok := st.(backup.Lister)
	if !ok {
		return nil, errors.New("this store can not list its objects")
	}
	objs, err := l.List(ctx, strings.Trim(prefix, "/"))
	if err != nil {
		return nil, err
	}
	var out []RemoteBackup
	for _, o := range objs {
		if !isInstanceManifestKey(o.Key) {
			continue
		}
		r, err := st.Get(ctx, o.Key)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", o.Key, err)
		}
		var m InstanceManifest
		err = json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m)
		_ = r.Close()
		if err != nil || m.Kind != InstanceManifestKind || m.ObjectKey == "" || m.SHA256 == "" || len(m.BackupID) < 8 {
			continue // not ours, or damaged: never offered for a restore
		}
		out = append(out, RemoteBackup{Manifest: m, ManifestKey: o.Key})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Manifest.CreatedAt.After(out[j].Manifest.CreatedAt) })
	return out, nil
}

// SelectBackup returns the newest backup created at or before at; a zero at
// selects the newest. A non-empty id selects exactly that backup (full id or its
// first 8 characters).
func SelectBackup(found []RemoteBackup, at time.Time, id string) (RemoteBackup, error) {
	if id != "" {
		for _, b := range found {
			if b.Manifest.BackupID == id || (len(id) >= 8 && strings.HasPrefix(b.Manifest.BackupID, id)) {
				return b, nil
			}
		}
		return RemoteBackup{}, fmt.Errorf("no backup with id %q was found", id)
	}
	for _, b := range found {
		if at.IsZero() || !b.Manifest.CreatedAt.After(at) {
			return b, nil
		}
	}
	if len(found) == 0 {
		return RemoteBackup{}, errors.New("no control-plane backups were found at the source")
	}
	oldest := found[len(found)-1].Manifest.CreatedAt
	return RemoteBackup{}, fmt.Errorf("no backup at or before %s; the oldest one is from %s", at.UTC().Format(time.RFC3339), oldest.UTC().Format(time.RFC3339))
}

// ParseAt parses --at: "latest" (or empty) or an RFC 3339 timestamp, a date and
// time without zone (UTC) or a plain date (end of that UTC day).
func ParseAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "latest") {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		return t.Add(24*time.Hour - time.Second), nil
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q: use RFC 3339 (2026-10-27T03:30:00Z), \"2026-10-27 03:30\" (UTC), a date, or \"latest\"", s)
}

// ErrDatabaseNotEmpty is returned when a restore target already holds tables.
var ErrDatabaseNotEmpty = errors.New("the target database is not empty")

// CheckEmptyDatabase fails with ErrDatabaseNotEmpty when the database behind
// dsn holds any user table, view, sequence or foreign table.
func CheckEmptyDatabase(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to the target database: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var n int
	var first string
	err = conn.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(n.nspname || '.' || c.relname), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')`).Scan(&n, &first)
	if err != nil {
		return fmt.Errorf("inspect the target database: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: it already contains %d object(s), for example %s. Restore into a new, empty database (CREATE DATABASE nebulactrl)", ErrDatabaseNotEmpty, n, first)
	}
	return nil
}

// RestoreOptions drives RestoreInstance.
type RestoreOptions struct {
	Source RestoreSource
	// At selects the newest backup created at or before it; zero = newest.
	At time.Time
	// BackupID selects one backup exactly (overrides At).
	BackupID string
	// DatabaseURL is the empty target database.
	DatabaseURL string
	// TmpDir holds the downloaded dump while it is verified (default: the
	// system temp directory). It needs room for one dump.
	TmpDir string
	Tools  PGTools
	// Out receives progress lines (nil = discard).
	Out io.Writer
}

// RestoreResult describes a finished restore.
type RestoreResult struct {
	Backup RemoteBackup
	Tables int // user tables in the restored database
}

func (o RestoreOptions) logf(format string, a ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", a...)
	}
}

// RestoreInstance restores a control-plane backup into an empty database. The
// dump is downloaded and its SHA-256 verified before pg_restore sees a byte;
// pg_restore then runs in a single transaction, so a failed restore leaves the
// database empty.
func RestoreInstance(ctx context.Context, o RestoreOptions) (RestoreResult, error) {
	var res RestoreResult
	conn, err := ParsePGURL(o.DatabaseURL)
	if err != nil {
		return res, err
	}
	if err := o.Tools.Available(); err != nil {
		return res, err
	}
	st, err := o.Source.Store()
	if err != nil {
		return res, err
	}
	o.logf("Looking for backups ...")
	found, err := FindBackups(ctx, st, o.Source.Prefix)
	if err != nil {
		return res, err
	}
	b, err := SelectBackup(found, o.At, o.BackupID)
	if err != nil {
		return res, err
	}
	res.Backup = b
	m := b.Manifest
	o.logf("Backup %s from %s (%s, %s, PostgreSQL %s)", m.BackupID[:8], m.CreatedAt.UTC().Format(time.RFC3339), HumanBytes(m.SizeBytes), m.Format, m.EngineVersion)
	if m.Format != agentprotocol.FormatPGCustom {
		return res, fmt.Errorf("unsupported backup format %q", m.Format)
	}
	if latest := latestMigration(); m.SchemaVersion > latest {
		o.logf("Warning: this backup has database schema version %d but this binary knows up to %d. Run the same or a newer nebulactrl-server afterwards.", m.SchemaVersion, latest)
	}
	if err := CheckEmptyDatabase(ctx, o.DatabaseURL); err != nil {
		return res, err
	}

	tmp, err := os.CreateTemp(o.TmpDir, "nebulactrl-restore-*.dump")
	if err != nil {
		return res, fmt.Errorf("create temporary file for the dump: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = tmp.Close()
		return res, err
	}
	o.logf("Downloading and verifying ...")
	if err := downloadVerified(ctx, st, m, tmp); err != nil {
		_ = tmp.Close()
		return res, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return res, err
	}
	o.logf("Restoring into %s on %s ...", conn.Database, conn.Host)
	err = o.Tools.RestoreFrom(ctx, conn, tmp)
	_ = tmp.Close()
	if err != nil {
		return res, fmt.Errorf("restore failed (the database was left empty): %w", err)
	}

	pg, err := pgx.Connect(ctx, o.DatabaseURL)
	if err != nil {
		return res, fmt.Errorf("connect after restore: %w", err)
	}
	defer pg.Close(context.WithoutCancel(ctx))
	if err := pg.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r', 'p')`).Scan(&res.Tables); err != nil {
		return res, fmt.Errorf("count restored tables: %w", err)
	}
	o.logf("Restored %d tables.", res.Tables)
	if m.KeyFingerprint != "" {
		o.logf("Start the control plane with the SAME NEBULACTRL_SECRET_KEY as the instance that wrote this backup (key fingerprint %s). The key is not in the backup.", m.KeyFingerprint)
	} else {
		o.logf("Start the control plane with the SAME NEBULACTRL_SECRET_KEY as the instance that wrote this backup. The key is not in the backup.")
	}
	return res, nil
}

// downloadVerified copies the dump of m into f and checks size and SHA-256.
func downloadVerified(ctx context.Context, st backup.Store, m InstanceManifest, f *os.File) error {
	r, err := st.Get(ctx, m.ObjectKey)
	if err != nil {
		return fmt.Errorf("open the dump %s: %w", m.ObjectKey, err)
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		return fmt.Errorf("download the dump: %w", err)
	}
	if n != m.SizeBytes {
		return fmt.Errorf("the dump is %d bytes but its manifest says %d: it is incomplete or damaged", n, m.SizeBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		return fmt.Errorf("the dump's SHA-256 is %s but its manifest says %s: it is damaged", got, m.SHA256)
	}
	return f.Sync()
}

// latestMigration is the newest migration version this binary knows.
func latestMigration() int64 {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0
	}
	var latest int64
	for _, e := range entries {
		name := filepath.Base(e.Name())
		if i := strings.IndexByte(name, '_'); i > 0 {
			if v, err := strconv.ParseInt(name[:i], 10, 64); err == nil && v > latest {
				latest = v
			}
		}
	}
	return latest
}

// HumanBytes formats a byte count (1.5 MiB).
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
