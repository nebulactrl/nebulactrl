package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRe = regexp.MustCompile(`^[a-z0-9-]{0,64}$`)
)

// DestinationInput creates a destination.
type DestinationInput struct {
	Name       string
	Kind       string // s3 | local
	Endpoint   string
	Region     string
	Bucket     string
	Prefix     string
	PathStyle  bool
	CompatMode *bool // nil: on for custom endpoints, off for AWS
	AccessKey  string
	SecretKey  string
	LocalDir   string
}

// DestinationPatch updates a destination; nil fields are unchanged and empty
// keys keep the stored credentials.
type DestinationPatch struct {
	Name       *string
	Endpoint   *string
	Region     *string
	Bucket     *string
	Prefix     *string
	PathStyle  *bool
	CompatMode *bool
	AccessKey  *string
	SecretKey  *string
	LocalDir   *string
}

func validateDestination(kind, endpoint, region, bucket, prefix, localDir string) (string, error) {
	bad := func(msg string) error { return apierr.Unprocessable("validation_failed", msg) }
	switch kind {
	case agentprotocol.DestS3:
		if !bucketRe.MatchString(bucket) {
			return "", bad("bucket must be 3-63 lowercase letters, digits, dots or hyphens")
		}
		if !regionRe.MatchString(region) {
			return "", bad("region may only contain lowercase letters, digits and hyphens")
		}
		if endpoint != "" {
			u, err := url.Parse(endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
				return "", bad("endpoint must be an http(s) URL without credentials or path, e.g. https://s3.eu-central-1.example.com")
			}
			endpoint = strings.TrimSuffix(endpoint, "/")
		}
		if localDir != "" {
			return "", bad("localDir only applies to local destinations")
		}
	case DestBuiltin:
		if endpoint != "" || bucket != "" || region != "" || localDir != "" {
			return "", bad("the built-in storage takes no endpoint, region, bucket or directory")
		}
	case agentprotocol.DestLocal:
		if endpoint != "" || bucket != "" || region != "" {
			return "", bad("endpoint, region and bucket only apply to s3 destinations")
		}
		if localDir != "" {
			if err := agentprotocol.ValidObjectKey(localDir); err != nil {
				return "", bad("localDir must be a relative directory name without '..', e.g. nightly")
			}
		}
	default:
		return "", bad("kind must be s3 or local")
	}
	if prefix != "" {
		if err := agentprotocol.ValidObjectKey(prefix); err != nil {
			return "", bad("prefix must be a relative path without '..' or empty segments, e.g. nebulactrl/prod")
		}
	}
	return endpoint, nil
}

func cleanPrefix(p string) string { return strings.Trim(strings.TrimSpace(p), "/") }

// ListDestinations returns the workspace's destinations.
func (s *Service) ListDestinations(ctx context.Context) ([]DestinationView, error) {
	ws, err := s.workspace(ctx, policy.ScopeProjectsRead)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBackupDestinations(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list destinations: %w", err)
	}
	out := make([]DestinationView, len(rows))
	for i, d := range rows {
		u, err := s.q.CountBackupDestinationUsage(ctx, d.ID)
		if err != nil {
			return nil, fmt.Errorf("count destination usage: %w", err)
		}
		out[i] = destinationView(d, int(u.Schedules+u.VolumeConfigs), int(u.Backups+u.VolumeBackups))
	}
	return out, nil
}

// GetDestination returns one destination.
func (s *Service) GetDestination(ctx context.Context, id uuid.UUID) (DestinationView, error) {
	ws, err := s.workspace(ctx, policy.ScopeProjectsRead)
	if err != nil {
		return DestinationView{}, err
	}
	return s.destinationView(ctx, s.q, ws, id)
}

func (s *Service) destinationView(ctx context.Context, q *db.Queries, ws, id uuid.UUID) (DestinationView, error) {
	d, err := s.destination(ctx, q, ws, id)
	if err != nil {
		return DestinationView{}, err
	}
	u, err := q.CountBackupDestinationUsage(ctx, d.ID)
	if err != nil {
		return DestinationView{}, fmt.Errorf("count destination usage: %w", err)
	}
	return destinationView(d, int(u.Schedules+u.VolumeConfigs), int(u.Backups+u.VolumeBackups)), nil
}

// CreateDestination adds a destination. Admin only: it stores credentials.
func (s *Service) CreateDestination(ctx context.Context, in DestinationInput) (DestinationView, error) {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return DestinationView{}, err
	}
	if in.Kind == DestBuiltin {
		return DestinationView{}, errBuiltinReadOnly("created")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		return DestinationView{}, apierr.Unprocessable("validation_failed", "name is required (at most 80 characters)")
	}
	in.Prefix = cleanPrefix(in.Prefix)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	endpoint, err := validateDestination(in.Kind, in.Endpoint, in.Region, in.Bucket, in.Prefix, in.LocalDir)
	if err != nil {
		return DestinationView{}, err
	}
	destID := uuid.New()
	var accessEnc, secretEnc []byte
	compat := endpoint != ""
	if in.CompatMode != nil {
		compat = *in.CompatMode
	}
	if in.Kind == agentprotocol.DestS3 {
		if len(in.AccessKey) < 3 || len(in.SecretKey) < 3 {
			return DestinationView{}, apierr.Unprocessable("validation_failed", "accessKey and secretKey are required for s3 destinations")
		}
		if accessEnc, err = s.box.SealString(secretbox.BackupDestinationsAccessKey, destID.String(), in.AccessKey); err != nil {
			return DestinationView{}, fmt.Errorf("seal access key: %w", err)
		}
		if secretEnc, err = s.box.SealString(secretbox.BackupDestinationsSecretKey, destID.String(), in.SecretKey); err != nil {
			return DestinationView{}, fmt.Errorf("seal secret key: %w", err)
		}
	} else if in.AccessKey != "" || in.SecretKey != "" {
		return DestinationView{}, apierr.Unprocessable("validation_failed", "credentials only apply to s3 destinations")
	}
	var row db.BackupDestination
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertBackupDestination(ctx, db.InsertBackupDestinationParams{
			ID: destID, WorkspaceID: ws, Name: in.Name, Kind: in.Kind, Endpoint: endpoint, Region: in.Region, Bucket: in.Bucket,
			Prefix: in.Prefix, PathStyle: in.PathStyle, CompatMode: compat, AccessKeyEnc: accessEnc, SecretKeyEnc: secretEnc,
			LocalDir: in.LocalDir,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDestinationCreate, ResourceType: "backup_destination", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": row.Name, "kind": row.Kind, "bucket": row.Bucket, "endpoint": row.Endpoint},
		})
	})
	if services.IsUniqueViolation(err) {
		return DestinationView{}, apierr.Conflict("destination_conflict", "a destination named "+in.Name+" already exists")
	}
	if err != nil {
		return DestinationView{}, fmt.Errorf("create destination: %w", err)
	}
	s.publishWorkspace(ws, row.ID)
	return destinationView(row, 0, 0), nil
}

// UpdateDestination changes a destination. Credentials are replaced only when
// provided; any change clears the last test result.
func (s *Service) UpdateDestination(ctx context.Context, id uuid.UUID, p DestinationPatch) (DestinationView, error) {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return DestinationView{}, err
	}
	cur, err := s.destination(ctx, s.q, ws, id)
	if err != nil {
		return DestinationView{}, err
	}
	if cur.Kind == DestBuiltin {
		return DestinationView{}, errBuiltinReadOnly("changed")
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	name, endpoint, region, bucket, prefix, localDir := cur.Name, cur.Endpoint, cur.Region, cur.Bucket, cur.Prefix, cur.LocalDir
	set(&name, p.Name)
	set(&endpoint, p.Endpoint)
	set(&region, p.Region)
	set(&bucket, p.Bucket)
	set(&prefix, p.Prefix)
	set(&localDir, p.LocalDir)
	prefix = cleanPrefix(prefix)
	pathStyle, compat := cur.PathStyle, cur.CompatMode
	if p.PathStyle != nil {
		pathStyle = *p.PathStyle
	}
	if p.CompatMode != nil {
		compat = *p.CompatMode
	}
	if name == "" || len(name) > 80 {
		return DestinationView{}, apierr.Unprocessable("validation_failed", "name is required (at most 80 characters)")
	}
	if endpoint, err = validateDestination(cur.Kind, endpoint, region, bucket, prefix, localDir); err != nil {
		return DestinationView{}, err
	}
	var accessEnc, secretEnc []byte
	if p.AccessKey != nil && *p.AccessKey != "" {
		if cur.Kind != agentprotocol.DestS3 {
			return DestinationView{}, apierr.Unprocessable("validation_failed", "credentials only apply to s3 destinations")
		}
		if accessEnc, err = s.box.SealString(secretbox.BackupDestinationsAccessKey, id.String(), *p.AccessKey); err != nil {
			return DestinationView{}, err
		}
	}
	if p.SecretKey != nil && *p.SecretKey != "" {
		if cur.Kind != agentprotocol.DestS3 {
			return DestinationView{}, apierr.Unprocessable("validation_failed", "credentials only apply to s3 destinations")
		}
		if secretEnc, err = s.box.SealString(secretbox.BackupDestinationsSecretKey, id.String(), *p.SecretKey); err != nil {
			return DestinationView{}, err
		}
	}
	// Moving a destination would orphan the objects of its stored backups.
	if u, err := s.q.CountBackupDestinationUsage(ctx, id); err == nil && u.Backups+u.VolumeBackups+u.VolumeRepos > 0 {
		moved := endpoint != cur.Endpoint || bucket != cur.Bucket || prefix != cur.Prefix || localDir != cur.LocalDir
		if moved {
			return DestinationView{}, apierr.Conflict("destination_in_use",
				fmt.Sprintf("%d stored backup(s) live in this destination; endpoint, bucket, prefix and directory can not change. Create a new destination instead.", u.Backups+u.VolumeBackups+u.VolumeRepos))
		}
	}
	var row db.BackupDestination
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpdateBackupDestination(ctx, db.UpdateBackupDestinationParams{
			ID: id, WorkspaceID: ws, Name: name, Endpoint: endpoint, Region: region, Bucket: bucket, Prefix: prefix,
			PathStyle: pathStyle, CompatMode: compat, AccessKeyEnc: accessEnc, SecretKeyEnc: secretEnc, LocalDir: localDir,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDestinationUpdate, ResourceType: "backup_destination", ResourceID: id.String(),
			// Credential changes record that they happened, never the values.
			Metadata: map[string]any{"name": row.Name, "credentialsChanged": accessEnc != nil || secretEnc != nil},
		})
	})
	if services.IsUniqueViolation(err) {
		return DestinationView{}, apierr.Conflict("destination_conflict", "a destination named "+name+" already exists")
	}
	if err != nil {
		return DestinationView{}, fmt.Errorf("update destination: %w", err)
	}
	s.publishWorkspace(ws, id)
	return s.destinationView(ctx, s.q, ws, id)
}

// DeleteDestination removes a destination that no schedule or stored backup uses.
func (s *Service) DeleteDestination(ctx context.Context, id uuid.UUID) error {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return err
	}
	d, err := s.destination(ctx, s.q, ws, id)
	if err != nil {
		return err
	}
	if d.Kind == DestBuiltin {
		return errBuiltinReadOnly("deleted")
	}
	u, err := s.q.CountBackupDestinationUsage(ctx, id)
	if err != nil {
		return fmt.Errorf("count destination usage: %w", err)
	}
	if u.Schedules > 0 {
		return apierr.Conflict("destination_in_use", fmt.Sprintf("%d backup schedule(s) write to this destination; remove or move them first", u.Schedules))
	}
	if u.VolumeConfigs > 0 {
		return apierr.Conflict("destination_in_use", fmt.Sprintf("%d volume backup configuration(s) write to this destination; remove or move them first", u.VolumeConfigs))
	}
	if u.Backups > 0 || u.VolumeBackups > 0 {
		return apierr.Conflict("destination_in_use", fmt.Sprintf("%d stored backup(s) live in this destination; delete them first", u.Backups+u.VolumeBackups))
	}
	if u.VolumeRepos > 0 {
		return apierr.Conflict("destination_in_use", fmt.Sprintf("%d volume backup repositor(y/ies) live in this destination; they hold encrypted backup data and can not be removed here", u.VolumeRepos))
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeleteBackupDestination(ctx, db.DeleteBackupDestinationParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDestinationDelete, ResourceType: "backup_destination", ResourceID: id.String(),
			Metadata: map[string]any{"name": d.Name},
		})
	})
	if err != nil {
		// Backups that only record failed or skipped runs still reference it.
		return apierr.Conflict("destination_in_use", "backup history still references this destination")
	}
	s.publishWorkspace(ws, id)
	return nil
}

// destinationSecrets decrypts a destination's credentials for a command. A
// builtin destination has none of its own: the key of the team's slice is used.
func (s *Service) destinationSecrets(ctx context.Context, d db.BackupDestination) (map[string]string, error) {
	out := map[string]string{}
	if d.Kind == DestBuiltin {
		sl, secret, err := s.builtinSlice(ctx, d)
		if err != nil {
			return nil, err
		}
		out[agentprotocol.SecretBackupAccessKey] = sl.AccessKey
		out[agentprotocol.SecretBackupSecretKey] = secret
		return out, nil
	}
	if d.Kind != agentprotocol.DestS3 {
		return out, nil
	}
	ak, err := s.box.OpenString(secretbox.BackupDestinationsAccessKey, d.ID.String(), d.AccessKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt destination access key: %w", err)
	}
	sk, err := s.box.OpenString(secretbox.BackupDestinationsSecretKey, d.ID.String(), d.SecretKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt destination secret key: %w", err)
	}
	out[agentprotocol.SecretBackupAccessKey] = ak
	out[agentprotocol.SecretBackupSecretKey] = sk
	return out, nil
}

// objectPrefix is the key prefix of a destination.
func objectPrefix(d db.BackupDestination) string { return cleanPrefix(d.Prefix) }

// TestDestination asks the agent on serverID to write, read and delete a probe
// object in the destination, and waits briefly for the verdict.
func (s *Service) TestDestination(ctx context.Context, id, serverID uuid.UUID) (DestinationView, error) {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return DestinationView{}, err
	}
	d, err := s.destination(ctx, s.q, ws, id)
	if err != nil {
		return DestinationView{}, err
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return DestinationView{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return DestinationView{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status != "online" {
		return DestinationView{}, apierr.Conflict("server_offline", srv.Name+" is not online, so the destination can not be tested from it")
	}
	dest, err := s.destinationFor(ctx, d)
	if err != nil {
		return DestinationView{}, err
	}
	payload, _ := json.Marshal(agentprotocol.BackupTestPayload{Destination: dest, Prefix: objectPrefix(d)})
	started := s.now()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindBackupTest, Payload: payload}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDestinationTest, ResourceType: "backup_destination", ResourceID: id.String(),
			Metadata: map[string]any{"name": d.Name, "server": srv.Name},
		})
	})
	if err != nil {
		return DestinationView{}, fmt.Errorf("enqueue destination test: %w", err)
	}
	deadline := time.After(25 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.destinationView(ctx, s.q, ws, id)
		case <-deadline:
			return s.destinationView(ctx, s.q, ws, id)
		case <-tick.C:
			cur, err := s.q.GetBackupDestinationInternal(ctx, id)
			if err == nil && cur.LastTestAt != nil && !cur.LastTestAt.Before(started.Add(-time.Second)) {
				return s.destinationView(ctx, s.q, ws, id)
			}
		}
	}
}

// recordTest stores the outcome of a backup_test command.
func (s *Service) recordTest(ctx context.Context, destID uuid.UUID, ok bool, msg string) {
	if err := s.q.SetBackupDestinationTest(ctx, db.SetBackupDestinationTestParams{ID: destID, LastTestOk: &ok, LastTestMessage: msg}); err != nil {
		s.log.ErrorContext(ctx, "record destination test", "destination_id", destID, "err", err)
		return
	}
	if d, err := s.q.GetBackupDestinationInternal(ctx, destID); err == nil {
		s.publishWorkspace(d.WorkspaceID, destID)
	}
}
