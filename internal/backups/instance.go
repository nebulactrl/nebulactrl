package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The control-plane ("instance") backup is a scheduled pg_dump of NebulaCtrl's
// own database to ONE instance-wide destination (an S3-compatible bucket or a
// directory on the control-plane host). Unlike database backups it runs on the
// control plane itself, not through an agent, but it reuses the destination
// model, the object stores (internal/agent/backup) and retention (Prune) of
// those.
//
// The instance secret key (NEBULACTRL_SECRET_KEY) is never part of a dump. It is
// the one thing a restore needs that the backup does not contain, so the
// settings carry a reminder the admin acknowledges once the key is stored
// elsewhere.

// Names of the instance backup feature.
const (
	// InstanceDir is the key directory control-plane backups live in, below the
	// destination's prefix.
	InstanceDir = "nebulactrl-instance"
	// InstanceManifestKind marks the manifest of a control-plane backup.
	InstanceManifestKind = "nebulactrl-instance"

	// DefaultInstanceSchedule is the default cron (UTC): daily at 03:30.
	DefaultInstanceSchedule = "30 3 * * *"
	// DefaultInstanceRetention is how many successful backups are kept.
	DefaultInstanceRetention = 14

	EventInstanceBackupUpdated = "instance.backup.updated"

	ActionInstanceBackupUpdate = "instance.backup.update"
	ActionInstanceBackupRun    = "instance.backup.run"
	ActionInstanceBackupTest   = "instance.backup.test"
	ActionInstanceBackupDone   = "instance.backup.succeeded"
	ActionInstanceBackupFailed = "instance.backup.failed"
	ActionInstanceKeyAck       = "instance.backup.key_acknowledged"
)

// Run triggers of an instance backup.
const (
	InstanceTriggerSchedule = "schedule"
	InstanceTriggerManual   = "manual"
)

// AdminChecker tells whether a user is the instance admin (the first account).
// *auth.Service implements it.
type AdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// InstanceDeps are the collaborators of an InstanceService. Pool, Jobs, Box and
// Admins may be nil for OpenAPI generation only.
type InstanceDeps struct {
	Pool *pgxpool.Pool
	Bus  *events.Bus
	Jobs *jobs.Manager
	Box  *secretbox.Box
	// Admins authorizes the instance admin.
	Admins AdminChecker
	// DatabaseURL is the control plane's own database (what pg_dump reads).
	DatabaseURL string
	// BackupDir is NEBULACTRL_BACKUP_DIR: the root of local destinations. Empty
	// disables local destinations.
	BackupDir string
	// KeyFingerprint identifies the instance secret key without revealing it
	// (see KeyFingerprint). It is shown in the UI and written to manifests so a
	// restore can check it holds the right key.
	KeyFingerprint string
	Tools          PGTools // zero value: pg_dump and pg_restore from PATH
	Logger         *slog.Logger
	// Builtin resolves the system bucket of the built-in S3 for a "builtin"
	// destination, and BuiltinS3URL is the internal URL the control plane reaches
	// it at (it does not go through the agent tunnel). Either empty: that
	// destination is unavailable. SetBuiltinStorage adds both later.
	Builtin      BuiltinStorage
	BuiltinS3URL string
}

// InstanceService implements the control-plane backup use cases.
type InstanceService struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	bus       *events.Bus
	jm        *jobs.Manager
	box       *secretbox.Box
	admins    AdminChecker
	dbURL     string
	backupDir string
	keyFP     string
	tools     PGTools
	log       *slog.Logger

	notifier atomic.Pointer[Notifier]
	builtin  atomic.Pointer[instanceBuiltin]
	now      func() time.Time
}

// instanceBuiltin is how the control plane reaches the built-in S3 itself.
type instanceBuiltin struct {
	storage BuiltinStorage
	s3URL   string
}

// NewInstanceService returns an InstanceService.
func NewInstanceService(d InstanceDeps) *InstanceService {
	s := &InstanceService{
		pool: d.Pool, bus: d.Bus, jm: d.Jobs, box: d.Box, admins: d.Admins, dbURL: d.DatabaseURL, backupDir: d.BackupDir,
		keyFP: d.KeyFingerprint, tools: d.Tools, log: d.Logger, now: time.Now,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	s.SetBuiltinStorage(d.Builtin, d.BuiltinS3URL)
	return s
}

// SetNotifier installs the hook told about finished control-plane backups.
func (s *InstanceService) SetNotifier(n Notifier) { s.notifier.Store(&n) }

// SetBuiltinStorage installs the resolver of the system bucket and the URL of
// the built-in S3 as the control plane reaches it. Either empty disables the
// "builtin" destination kind.
func (s *InstanceService) SetBuiltinStorage(b BuiltinStorage, s3URL string) {
	if b == nil || s3URL == "" {
		s.builtin.Store(nil)
		return
	}
	s.builtin.Store(&instanceBuiltin{storage: b, s3URL: s3URL})
}

// KeyFingerprint returns 12 hex characters that identify an instance secret key
// without revealing it (SHA-256 over a fixed label and the key; the key has 256
// bits of entropy, so the fingerprint can not be used to recover it).
func KeyFingerprint(key []byte) string {
	h := sha256.New()
	h.Write([]byte("nebulactrl instance key fingerprint\x00"))
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ---- views ------------------------------------------------------------------

// InstanceDestinationView is the destination of the control-plane backup.
// Credentials are never returned, only whether they are set.
type InstanceDestinationView struct {
	Kind            string     `json:"kind" enum:"s3,local,builtin" doc:"builtin is the system bucket of the built-in S3 on this control-plane host; it needs no endpoint, bucket or keys."`
	Endpoint        string     `json:"endpoint,omitempty"`
	Region          string     `json:"region,omitempty"`
	Bucket          string     `json:"bucket,omitempty"`
	Prefix          string     `json:"prefix,omitempty"`
	PathStyle       bool       `json:"pathStyle"`
	CompatMode      bool       `json:"compatMode"`
	AccessKeySet    bool       `json:"accessKeySet"`
	SecretKeySet    bool       `json:"secretKeySet"`
	LocalDir        string     `json:"localDir,omitempty"`
	Configured      bool       `json:"configured" doc:"The destination has everything a backup needs (a bucket and keys, or a usable local directory)."`
	OffSite         bool       `json:"offSite" doc:"False for a directory on the control-plane host and for the built-in storage: neither survives the loss of that host."`
	Where           string     `json:"where" doc:"Human-readable location, e.g. s3://bucket/prefix."`
	LastTestAt      *time.Time `json:"lastTestAt,omitempty"`
	LastTestOK      *bool      `json:"lastTestOk,omitempty"`
	LastTestMessage string     `json:"lastTestMessage,omitempty"`
}

// InstanceBackupView is the API view of the control-plane backup settings.
type InstanceBackupView struct {
	Enabled        bool                    `json:"enabled"`
	Schedule       string                  `json:"schedule" doc:"5-field cron expression, evaluated in UTC."`
	RetentionCount int                     `json:"retentionCount" doc:"Successful backups kept (the newest is never removed)."`
	Destination    InstanceDestinationView `json:"destination"`
	NotOffSite     bool                    `json:"notOffSite" doc:"The destination lives on this control-plane host, so a lost host loses the backups too. Add an external destination."`
	NextRunAt      *time.Time              `json:"nextRunAt,omitempty"`
	LastRun        *InstanceRunView        `json:"lastRun,omitempty" doc:"The most recent run of any outcome."`
	LastSuccessAt  *time.Time              `json:"lastSuccessAt,omitempty"`
	// KeyFingerprint identifies NEBULACTRL_SECRET_KEY without revealing it.
	KeyFingerprint    string     `json:"keyFingerprint"`
	KeyAcknowledgedAt *time.Time `json:"keyAcknowledgedAt,omitempty" doc:"When an admin confirmed the instance secret key is stored separately. The reminder card shows until then."`
	ToolsAvailable    bool       `json:"toolsAvailable" doc:"False when pg_dump is missing on the control-plane host: backups can not run."`
	ToolsMessage      string     `json:"toolsMessage,omitempty"`
	LocalDirEnabled   bool       `json:"localDirEnabled" doc:"False when NEBULACTRL_BACKUP_DIR is not set: local directory destinations are unavailable."`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// InstanceRunView is the API view of one control-plane backup run.
type InstanceRunView struct {
	ID            uuid.UUID  `json:"id"`
	Trigger       string     `json:"trigger" enum:"schedule,manual,pre_upgrade"`
	Status        string     `json:"status" enum:"running,succeeded,failed"`
	Destination   string     `json:"destination" doc:"Where the backup was written."`
	DestKind      string     `json:"destKind" enum:"s3,local,builtin"`
	ObjectKey     string     `json:"objectKey,omitempty"`
	SizeBytes     int64      `json:"sizeBytes"`
	SHA256        string     `json:"sha256,omitempty"`
	PGVersion     string     `json:"pgVersion,omitempty"`
	Error         string     `json:"error,omitempty"`
	ActorLabel    string     `json:"actorLabel,omitempty"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	ObjectDeleted bool       `json:"objectDeleted" doc:"Retention removed the stored objects; the row stays as history."`
}

func runView(b db.InstanceBackup) InstanceRunView {
	return InstanceRunView{
		ID: b.ID, Trigger: b.Trigger, Status: b.Status, Destination: b.DestLabel, DestKind: b.DestKind, ObjectKey: b.ObjectKey,
		SizeBytes: b.SizeBytes, SHA256: b.Sha256, PGVersion: b.PgVersion, Error: b.Error, ActorLabel: b.ActorLabel,
		StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, ObjectDeleted: b.ObjectDeletedAt != nil,
	}
}

// ---- destination helpers ----------------------------------------------------

// instanceFingerprint identifies a destination location. Retention only prunes
// backups of the current location.
func instanceFingerprint(c db.InstanceBackupSetting) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.DestKind, c.Endpoint, c.Region, c.Bucket, cleanPrefix(c.Prefix), c.LocalDir}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// instanceLabel is the human-readable location of a destination.
func (s *InstanceService) instanceLabel(c db.InstanceBackupSetting) string {
	if c.DestKind == DestBuiltin {
		l := "built-in storage on this control-plane host"
		if p := cleanPrefix(c.Prefix); p != "" {
			l += " (" + p + ")"
		}
		return l
	}
	if c.DestKind == agentprotocol.DestS3 {
		l := "s3://" + c.Bucket
		if p := cleanPrefix(c.Prefix); p != "" {
			l += "/" + p
		}
		if c.Endpoint != "" {
			l += " (" + strings.TrimPrefix(strings.TrimPrefix(c.Endpoint, "https://"), "http://") + ")"
		}
		return l
	}
	dir := s.backupDir
	if dir == "" {
		dir = "(NEBULACTRL_BACKUP_DIR not set)"
	}
	if c.LocalDir != "" {
		dir = filepath.Join(dir, filepath.FromSlash(c.LocalDir))
	}
	return "directory " + dir
}

// instanceConfigured reports whether c has everything a backup needs.
func (s *InstanceService) instanceConfigured(c db.InstanceBackupSetting) bool {
	if c.DestKind == agentprotocol.DestLocal {
		return s.backupDir != ""
	}
	if c.DestKind == DestBuiltin {
		return s.builtin.Load() != nil
	}
	return c.Bucket != "" && len(c.AccessKeyEnc) > 0 && len(c.SecretKeyEnc) > 0
}

func (s *InstanceService) destinationView(c db.InstanceBackupSetting) InstanceDestinationView {
	return InstanceDestinationView{
		Kind: c.DestKind, Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, Prefix: c.Prefix, PathStyle: c.PathStyle,
		CompatMode: c.CompatMode, AccessKeySet: len(c.AccessKeyEnc) > 0, SecretKeySet: len(c.SecretKeyEnc) > 0, LocalDir: c.LocalDir,
		Configured: s.instanceConfigured(c), OffSite: c.DestKind == agentprotocol.DestS3, Where: s.instanceLabel(c),
		LastTestAt: c.LastTestAt, LastTestOK: c.LastTestOk, LastTestMessage: c.LastTestMessage,
	}
}

// store opens the object store of c. Local destinations live below the backup
// directory of the control-plane host.
func (s *InstanceService) store(ctx context.Context, c db.InstanceBackupSetting) (backup.Store, error) {
	if c.DestKind == DestBuiltin {
		return s.builtinStore(ctx)
	}
	dest := agentprotocol.BackupDestination{
		Kind: c.DestKind, Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, PathStyle: c.PathStyle, CompatMode: c.CompatMode, LocalDir: c.LocalDir,
	}
	secrets := map[string]string{}
	if c.DestKind == agentprotocol.DestS3 {
		if s.box == nil {
			return nil, errors.New("the control plane has no secret key to open the destination credentials with")
		}
		ak, err := s.box.OpenString(secretbox.InstanceBackupAccessKey, secretbox.InstanceRow, c.AccessKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt destination access key: %w", err)
		}
		sk, err := s.box.OpenString(secretbox.InstanceBackupSecretKey, secretbox.InstanceRow, c.SecretKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt destination secret key: %w", err)
		}
		secrets[agentprotocol.SecretBackupAccessKey], secrets[agentprotocol.SecretBackupSecretKey] = ak, sk
	}
	return backup.NewStoreFactory(s.backupDir)(dest, secrets)
}

// secretValues are the credentials that must never reach a message or log.
func (s *InstanceService) secretValues(ctx context.Context, c db.InstanceBackupSetting) []string {
	var out []string
	if c.DestKind == DestBuiltin {
		if b := s.builtin.Load(); b != nil {
			if ak, sk, _, err := b.storage.SystemAccessKey(ctx); err == nil {
				for _, v := range []string{ak, sk} {
					if len(v) >= 4 {
						out = append(out, v)
					}
				}
			}
		}
	}
	if s.box == nil {
		return out
	}
	for _, a := range []struct {
		col secretbox.Column
		enc []byte
	}{{secretbox.InstanceBackupAccessKey, c.AccessKeyEnc}, {secretbox.InstanceBackupSecretKey, c.SecretKeyEnc}} {
		if len(a.enc) == 0 {
			continue
		}
		if v, err := s.box.OpenString(a.col, secretbox.InstanceRow, a.enc); err == nil && len(v) >= 4 {
			out = append(out, v)
		}
	}
	if conn, err := ParsePGURL(s.dbURL); err == nil && len(conn.Password) >= 4 {
		out = append(out, conn.Password)
	}
	return out
}

func scrubSecrets(msg string, secrets []string) string {
	for _, v := range secrets {
		msg = strings.ReplaceAll(msg, v, "••••••")
	}
	return clip(msg)
}

// instanceObjectKey is the key of a backup started at t.
func instanceObjectKey(prefix string, t time.Time, id uuid.UUID) string {
	t = t.UTC()
	name := fmt.Sprintf("nebulactrl-%s-%s.dump", t.Format("20060102T150405Z"), id.String()[:8])
	return path.Join(cleanPrefix(prefix), InstanceDir, t.Format("2006"), t.Format("01"), name)
}

// ---- authorization ----------------------------------------------------------

// authorize allows the instance admin (the first account; a token counts
// through its creator and needs the admin scope).
func (s *InstanceService) authorize(ctx context.Context) (*auth.Principal, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return nil, apierr.Forbidden()
	}
	if s.admins == nil {
		return nil, apierr.Forbidden()
	}
	ok, err := s.admins.IsInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

// publish tells the instance administrators' feeds the instance backup changed.
func (s *InstanceService) publish() {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: EventInstanceBackupUpdated, ResourceID: "instance", Instance: true})
	}
}

// ---- settings ---------------------------------------------------------------

func (s *InstanceService) view(ctx context.Context, q *db.Queries, c db.InstanceBackupSetting) (InstanceBackupView, error) {
	v := InstanceBackupView{
		Enabled: c.Enabled, Schedule: c.Schedule, RetentionCount: int(c.RetentionCount), Destination: s.destinationView(c),
		NextRunAt: c.NextRunAt, KeyFingerprint: s.keyFP, KeyAcknowledgedAt: c.KeyAckAt, LocalDirEnabled: s.backupDir != "",
		UpdatedAt: c.UpdatedAt, NotOffSite: c.DestKind == DestBuiltin,
	}
	if err := s.tools.Available(); err != nil {
		v.ToolsMessage = err.Error()
	} else {
		v.ToolsAvailable = true
	}
	runs, err := q.ListInstanceBackups(ctx, 1)
	if err != nil {
		return v, fmt.Errorf("list instance backups: %w", err)
	}
	if len(runs) > 0 {
		rv := runView(runs[0])
		v.LastRun = &rv
	}
	last, err := q.GetLastSucceededInstanceBackup(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, fmt.Errorf("last instance backup: %w", err)
	}
	if err == nil {
		t := last.StartedAt
		if last.FinishedAt != nil {
			t = *last.FinishedAt
		}
		v.LastSuccessAt = &t
	}
	return v, nil
}

// Get returns the control-plane backup settings (instance admin only).
func (s *InstanceService) Get(ctx context.Context) (InstanceBackupView, error) {
	if _, err := s.authorize(ctx); err != nil {
		return InstanceBackupView{}, err
	}
	c, err := s.q.GetInstanceBackupSettings(ctx)
	if err != nil {
		return InstanceBackupView{}, fmt.Errorf("instance backup settings: %w", err)
	}
	return s.view(ctx, s.q, c)
}

// InstanceBackupPatch is a partial update; nil fields are unchanged. The keys
// are write-only: nil or empty keeps the stored ones.
type InstanceBackupPatch struct {
	Enabled        *bool
	Schedule       *string
	RetentionCount *int
	Kind           *string
	Endpoint       *string
	Region         *string
	Bucket         *string
	Prefix         *string
	PathStyle      *bool
	CompatMode     *bool
	AccessKey      *string
	SecretKey      *string
	LocalDir       *string
}

// Update changes the settings (instance admin only). Enabling needs a complete
// destination and a valid schedule.
func (s *InstanceService) Update(ctx context.Context, p InstanceBackupPatch) (InstanceBackupView, error) {
	pr, err := s.authorize(ctx)
	if err != nil {
		return InstanceBackupView{}, err
	}
	cur, err := s.q.GetInstanceBackupSettings(ctx)
	if err != nil {
		return InstanceBackupView{}, fmt.Errorf("instance backup settings: %w", err)
	}
	bad := func(msg string) error { return apierr.Unprocessable("validation_failed", msg) }
	trim := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}

	kind, endpoint, region, bucket, prefix, localDir := cur.DestKind, cur.Endpoint, cur.Region, cur.Bucket, cur.Prefix, cur.LocalDir
	trim(&kind, p.Kind)
	trim(&endpoint, p.Endpoint)
	trim(&region, p.Region)
	trim(&bucket, p.Bucket)
	trim(&prefix, p.Prefix)
	trim(&localDir, p.LocalDir)
	prefix = cleanPrefix(prefix)
	clearCreds := false
	if kind != cur.DestKind {
		// A switch starts the destination afresh: the other kind's fields and
		// credentials do not carry over.
		clearCreds = true
		if kind == agentprotocol.DestLocal || kind == DestBuiltin {
			endpoint, region, bucket = "", "", ""
		}
		if kind != agentprotocol.DestLocal {
			localDir = ""
		}
	}
	pathStyle, compat := cur.PathStyle, cur.CompatMode
	if p.PathStyle != nil {
		pathStyle = *p.PathStyle
	}
	if p.CompatMode != nil {
		compat = *p.CompatMode
	}
	if endpoint, err = validateDestination(kind, endpoint, region, bucket, prefix, localDir); err != nil {
		return InstanceBackupView{}, err
	}
	if kind == agentprotocol.DestLocal && s.backupDir == "" && (p.Kind != nil || (p.Enabled != nil && *p.Enabled)) {
		return InstanceBackupView{}, apierr.Unprocessable("backup_dir_not_configured",
			"a local directory needs NEBULACTRL_BACKUP_DIR on the control plane; set it (and mount a volume there) or use an S3-compatible bucket")
	}

	if kind == DestBuiltin && p.Kind != nil && s.builtin.Load() == nil {
		return InstanceBackupView{}, apierr.Unprocessable("builtin_storage_unavailable",
			"the built-in storage is not available on this control plane; use an S3-compatible bucket or a local directory")
	}

	var accessEnc, secretEnc []byte
	for _, k := range []struct {
		in  *string
		col secretbox.Column
		out *[]byte
	}{{p.AccessKey, secretbox.InstanceBackupAccessKey, &accessEnc}, {p.SecretKey, secretbox.InstanceBackupSecretKey, &secretEnc}} {
		if k.in == nil || *k.in == "" {
			continue
		}
		if kind != agentprotocol.DestS3 {
			return InstanceBackupView{}, bad("credentials only apply to s3 destinations")
		}
		if s.box == nil {
			return InstanceBackupView{}, apierr.Unprocessable("no_secret_key", "the control plane has no secret key to store the credentials with")
		}
		if *k.out, err = s.box.SealString(k.col, secretbox.InstanceRow, *k.in); err != nil {
			return InstanceBackupView{}, fmt.Errorf("seal credentials: %w", err)
		}
	}

	schedule, retention, enabled := cur.Schedule, int(cur.RetentionCount), cur.Enabled
	if p.Schedule != nil {
		schedule = strings.TrimSpace(*p.Schedule)
	}
	if p.RetentionCount != nil {
		retention = *p.RetentionCount
	}
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	if retention < 1 || retention > 365 {
		return InstanceBackupView{}, bad("retentionCount must be 1-365")
	}
	now := s.now()
	next, err := nextRun(schedule, "UTC", now)
	if err != nil {
		return InstanceBackupView{}, err
	}

	arg := db.UpdateInstanceBackupSettingsParams{
		Enabled: enabled, DestKind: kind, Endpoint: endpoint, Region: region, Bucket: bucket, Prefix: prefix, PathStyle: pathStyle,
		CompatMode: compat, AccessKeyEnc: accessEnc, SecretKeyEnc: secretEnc, LocalDir: localDir, Schedule: schedule,
		RetentionCount: int32(retention), ClearCreds: clearCreds,
	}
	// Anything that changes where or how the destination is reached invalidates its test.
	arg.ClearTest = kind != cur.DestKind || endpoint != cur.Endpoint || region != cur.Region || bucket != cur.Bucket ||
		prefix != cur.Prefix || localDir != cur.LocalDir || pathStyle != cur.PathStyle || compat != cur.CompatMode ||
		accessEnc != nil || secretEnc != nil
	switch {
	case !enabled:
		arg.NextRunAt = nil
	case cur.Enabled && cur.NextRunAt != nil && schedule == cur.Schedule:
		arg.NextRunAt = cur.NextRunAt // keep the pending slot
	default:
		arg.NextRunAt = &next
	}

	// Validate the destination as it will be stored.
	probe := cur
	probe.DestKind, probe.Endpoint, probe.Region, probe.Bucket, probe.Prefix, probe.LocalDir = kind, endpoint, region, bucket, prefix, localDir
	probe.AccessKeyEnc, probe.SecretKeyEnc = cur.AccessKeyEnc, cur.SecretKeyEnc
	if clearCreds {
		probe.AccessKeyEnc, probe.SecretKeyEnc = nil, nil
	}
	if accessEnc != nil {
		probe.AccessKeyEnc = accessEnc
	}
	if secretEnc != nil {
		probe.SecretKeyEnc = secretEnc
	}
	if enabled && !s.instanceConfigured(probe) {
		if kind == agentprotocol.DestS3 {
			return InstanceBackupView{}, apierr.Unprocessable("destination_incomplete", "an S3 destination needs a bucket, an access key and a secret key before backups can be enabled")
		}
		if kind == DestBuiltin {
			return InstanceBackupView{}, apierr.Unprocessable("builtin_storage_unavailable", "the built-in storage is not available on this control plane")
		}
		return InstanceBackupView{}, apierr.Unprocessable("destination_incomplete", "choose a destination before enabling backups")
	}

	var row db.InstanceBackupSetting
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpdateInstanceBackupSettings(ctx, arg)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionInstanceBackupUpdate, ResourceType: "instance_backup", ResourceID: "instance", WorkspaceID: pr.WorkspaceID,
			// Credential changes record that they happened, never the values.
			Metadata: map[string]any{
				"enabled": row.Enabled, "schedule": row.Schedule, "retention": row.RetentionCount, "kind": row.DestKind,
				"destination": s.instanceLabel(row), "credentialsChanged": accessEnc != nil || secretEnc != nil,
			},
		})
	})
	if err != nil {
		return InstanceBackupView{}, fmt.Errorf("update instance backup settings: %w", err)
	}
	s.publish()
	return s.view(ctx, s.q, row)
}

// AcknowledgeKey records that an admin stored the instance secret key
// separately from the backups; the reminder card then disappears.
func (s *InstanceService) AcknowledgeKey(ctx context.Context) (InstanceBackupView, error) {
	pr, err := s.authorize(ctx)
	if err != nil {
		return InstanceBackupView{}, err
	}
	var row db.InstanceBackupSetting
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).AckInstanceBackupKey(ctx, &pr.UserID)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionInstanceKeyAck, ResourceType: "instance_backup", ResourceID: "instance", WorkspaceID: pr.WorkspaceID,
			Metadata: map[string]any{"keyFingerprint": s.keyFP},
		})
	})
	if err != nil {
		return InstanceBackupView{}, fmt.Errorf("acknowledge instance key: %w", err)
	}
	s.publish()
	return s.view(ctx, s.q, row)
}

// Test writes, reads back and deletes a probe object in the destination from
// the control plane itself, and stores the verdict on the settings.
func (s *InstanceService) Test(ctx context.Context) (InstanceBackupView, error) {
	pr, err := s.authorize(ctx)
	if err != nil {
		return InstanceBackupView{}, err
	}
	c, err := s.q.GetInstanceBackupSettings(ctx)
	if err != nil {
		return InstanceBackupView{}, fmt.Errorf("instance backup settings: %w", err)
	}
	if !s.instanceConfigured(c) {
		return InstanceBackupView{}, apierr.Unprocessable("destination_incomplete", "set up the destination before testing it")
	}
	ok, msg := true, "Wrote, read back and deleted a probe object."
	if err := s.probe(ctx, c); err != nil {
		ok, msg = false, scrubSecrets(err.Error(), s.secretValues(ctx, c))
	}
	var row db.InstanceBackupSetting
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.SetInstanceBackupTest(ctx, db.SetInstanceBackupTestParams{LastTestOk: &ok, LastTestMessage: msg}); err != nil {
			return err
		}
		var err error
		if row, err = q.GetInstanceBackupSettings(ctx); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionInstanceBackupTest, ResourceType: "instance_backup", ResourceID: "instance", WorkspaceID: pr.WorkspaceID,
			Metadata: map[string]any{"ok": ok, "destination": s.instanceLabel(c)},
		})
	})
	if err != nil {
		return InstanceBackupView{}, fmt.Errorf("record destination test: %w", err)
	}
	s.publish()
	return s.view(ctx, s.q, row)
}

// probe writes, reads and deletes a small object below the instance directory.
func (s *InstanceService) probe(ctx context.Context, c db.InstanceBackupSetting) error {
	st, err := s.store(ctx, c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := uuid.NewString()
	key := path.Join(cleanPrefix(c.Prefix), InstanceDir, ".nebulactrl-probe-"+id)
	want := "nebulactrl instance backup probe " + id
	if err := st.Put(ctx, key, strings.NewReader(want)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	defer func() { _ = st.Delete(context.WithoutCancel(ctx), key) }()
	r, err := st.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read back: %w", err)
	}
	defer r.Close()
	buf := make([]byte, len(want)+1)
	n, _ := r.Read(buf)
	if string(buf[:n]) != want {
		return errors.New("read back a different object than the one written")
	}
	return nil
}

// ListRuns returns the newest control-plane backup runs.
func (s *InstanceService) ListRuns(ctx context.Context, limit int) ([]InstanceRunView, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.ListInstanceBackups(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list instance backups: %w", err)
	}
	out := make([]InstanceRunView, len(rows))
	for i, b := range rows {
		out[i] = runView(b)
	}
	return out, nil
}

// RunNow starts a backup immediately, whether or not the schedule is enabled.
// It returns the running run; the dump happens in a background job.
func (s *InstanceService) RunNow(ctx context.Context) (InstanceRunView, error) {
	pr, err := s.authorize(ctx)
	if err != nil {
		return InstanceRunView{}, err
	}
	c, err := s.q.GetInstanceBackupSettings(ctx)
	if err != nil {
		return InstanceRunView{}, fmt.Errorf("instance backup settings: %w", err)
	}
	if !s.instanceConfigured(c) {
		return InstanceRunView{}, apierr.Unprocessable("destination_incomplete", "set up the destination before running a backup")
	}
	if err := s.tools.Available(); err != nil {
		return InstanceRunView{}, apierr.Unprocessable("pg_tools_missing", err.Error())
	}
	var row db.InstanceBackup
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if row, err = s.startRun(ctx, tx, c, InstanceTriggerManual, pr.Label); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionInstanceBackupRun, ResourceType: "instance_backup", ResourceID: row.ID.String(), WorkspaceID: pr.WorkspaceID,
			Metadata: map[string]any{"trigger": InstanceTriggerManual, "destination": row.DestLabel},
		})
	})
	if services.IsUniqueViolation(err) {
		return InstanceRunView{}, apierr.Conflict("backup_in_progress", "a control-plane backup is already running")
	}
	if err != nil {
		return InstanceRunView{}, fmt.Errorf("start instance backup: %w", err)
	}
	s.publish()
	return runView(row), nil
}
