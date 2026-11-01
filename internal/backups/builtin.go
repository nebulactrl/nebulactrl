package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DestBuiltin is the destination kind of the team's slice of the built-in
// storage on the control-plane host. The row holds no credentials, bucket or
// endpoint: the control plane resolves them from the slice whenever it builds a
// command. Agents never see this kind; they get an ordinary s3 destination
// whose endpoint is the agent's local tunnel endpoint.
const DestBuiltin = "builtin"

const (
	// builtinRegion is what the built-in S3 answers to.
	builtinRegion = "us-east-1"
	// quotaMarker is what the quota gate puts in its answer when the slice is full.
	quotaMarker = "nebulactrl-quota-exceeded"
)

// builtinEndpoint is the address of the built-in S3 on a server: the agent's
// loopback listener, carried to the control plane over the agent's link.
var builtinEndpoint = fmt.Sprintf("http://127.0.0.1:%d", agentprotocol.LocalPortS3)

// BuiltinStorage resolves the built-in S3. *instsvc.Service implements it.
type BuiltinStorage interface {
	// SliceForBackup returns the team's slice with the secret of its key.
	SliceForBackup(ctx context.Context, teamID uuid.UUID) (instsvc.Slice, string, error)
	// SystemAccessKey returns the key of the system bucket the control-plane
	// backup writes to.
	SystemAccessKey(ctx context.Context) (accessKey, secret, bucket string, err error)
}

var errBuiltinUnavailable = apierr.Conflict("builtin_storage_unavailable", "Built-in backup storage is not available on this instance.")

// builtinStorageError turns a slice lookup failure into a plain message.
func builtinStorageError(err error) error {
	switch {
	case errors.Is(err, instsvc.ErrNoSlice):
		return apierr.Conflict("builtin_storage_unavailable", "This team has no built-in backup storage.")
	case errors.Is(err, instsvc.ErrNotReady):
		return apierr.Conflict("builtin_storage_not_ready", "Built-in backup storage is not ready yet. Try again in a minute.")
	}
	return fmt.Errorf("resolve built-in backup storage: %w", err)
}

// SetBuiltinStorage installs the resolver of built-in destinations (nil: they
// fail with a plain error).
func (s *Service) SetBuiltinStorage(b BuiltinStorage) { s.builtin.Store(&b) }

func (s *Service) builtinStorage() BuiltinStorage {
	if p := s.builtin.Load(); p != nil {
		return *p
	}
	return nil
}

// builtinSlice resolves the slice of a builtin destination's team.
func (s *Service) builtinSlice(ctx context.Context, d db.BackupDestination) (instsvc.Slice, string, error) {
	b := s.builtinStorage()
	if b == nil {
		return instsvc.Slice{}, "", errBuiltinUnavailable
	}
	sl, secret, err := b.SliceForBackup(ctx, d.WorkspaceID)
	if err != nil {
		return sl, "", builtinStorageError(err)
	}
	return sl, secret, nil
}

// destinationFor builds the agent-facing destination (no credentials). A
// builtin destination becomes an s3 destination of the team's bucket behind the
// agent's local endpoint.
func (s *Service) destinationFor(ctx context.Context, d db.BackupDestination) (agentprotocol.BackupDestination, error) {
	if d.Kind != DestBuiltin {
		return agentprotocol.BackupDestination{
			ID: d.ID.String(), Kind: d.Kind, Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, PathStyle: d.PathStyle,
			CompatMode: d.CompatMode, LocalDir: d.LocalDir,
		}, nil
	}
	sl, _, err := s.builtinSlice(ctx, d)
	if err != nil {
		return agentprotocol.BackupDestination{}, err
	}
	return agentprotocol.BackupDestination{
		ID: d.ID.String(), Kind: agentprotocol.DestS3, Endpoint: builtinEndpoint, Region: builtinRegion, Bucket: sl.Ref,
		PathStyle: true, CompatMode: true,
	}, nil
}

// builtinDestinationView hides everything of a builtin destination that is not
// the team's business: the endpoint, bucket and keys are the instance's.
func builtinDestinationView(v DestinationView) DestinationView {
	v.Endpoint, v.Bucket, v.Region = "", "", ""
	v.PathStyle, v.CompatMode = false, false
	v.AccessKeySet, v.SecretKeySet = false, false
	return v
}

// errBuiltinReadOnly refuses a change to the built-in destination.
func errBuiltinReadOnly(what string) error {
	return apierr.Conflict("destination_builtin", "The built-in backup storage is managed by the instance administrator and can not be "+what+".")
}

// quotaFullMessage is what a team reads when the built-in storage refused a write.
func quotaFullMessage(sl instsvc.Slice) string {
	return fmt.Sprintf("Built-in backup storage is full (%s of %s). Existing backups are kept.", humanBytes(sl.UsedBytes), humanBytes(sl.QuotaBytes))
}

// builtinFull rewrites the failure text of a write to a builtin destination
// when the slice is full, the quota gate answered, or the native quota flipped
// the bucket read-only. Anything else comes back unchanged.
func (s *Service) builtinFull(ctx context.Context, destID uuid.UUID, msg string) string {
	b := s.builtinStorage()
	if b == nil {
		return msg
	}
	d, err := s.q.GetBackupDestinationInternal(ctx, destID)
	if err != nil || d.Kind != DestBuiltin {
		return msg
	}
	sl, _, err := b.SliceForBackup(ctx, d.WorkspaceID)
	if err != nil && sl.ID == uuid.Nil {
		return msg
	}
	if sl.State == instsvc.StateFull || strings.Contains(msg, quotaMarker) || strings.Contains(msg, "AccessDenied") {
		return quotaFullMessage(sl)
	}
	return msg
}

// commandDestinationID is the destination a backup command writes to.
func commandDestinationID(kind string, payload []byte) (uuid.UUID, bool) {
	var raw string
	switch kind {
	case agentprotocol.KindDBBackup:
		var p agentprotocol.DBBackupPayload
		if json.Unmarshal(payload, &p) != nil {
			return uuid.Nil, false
		}
		raw = p.Destination.ID
	case agentprotocol.KindBackupTest:
		var p agentprotocol.BackupTestPayload
		if json.Unmarshal(payload, &p) != nil {
			return uuid.Nil, false
		}
		raw = p.Destination.ID
	case agentprotocol.KindVolumeBackup:
		var p agentprotocol.VolumeBackupPayload
		if json.Unmarshal(payload, &p) != nil {
			return uuid.Nil, false
		}
		raw = p.Repo.Destination.ID
	default:
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}

// builtinSkipReason is why a scheduled run to d can not start now, empty when it
// can. A team whose storage is not ready gets a skipped run with a plain reason
// instead of an error that rolls the schedule back and retries every minute.
func (s *Service) builtinSkipReason(ctx context.Context, d db.BackupDestination) string {
	if d.Kind != DestBuiltin {
		return ""
	}
	_, err := s.destinationFor(ctx, d)
	if ae, ok := apierr.As(err); ok {
		return ae.Message
	}
	return ""
}

// builtinStore opens the system bucket of the built-in S3 for the control-plane
// backup. The control plane is the S3's neighbour, so it uses the internal URL
// and the system key directly, not the agent tunnel.
func (s *InstanceService) builtinStore(ctx context.Context) (backup.Store, error) {
	b := s.builtin.Load()
	if b == nil {
		return nil, errors.New("the built-in storage is not available on this control plane")
	}
	ak, sk, bucket, err := b.storage.SystemAccessKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("open the built-in storage key: %w", err)
	}
	dest := agentprotocol.BackupDestination{
		Kind: agentprotocol.DestS3, Endpoint: b.s3URL, Region: builtinRegion, Bucket: bucket, PathStyle: true, CompatMode: true,
	}
	secrets := map[string]string{agentprotocol.SecretBackupAccessKey: ak, agentprotocol.SecretBackupSecretKey: sk}
	return backup.NewStoreFactory(s.backupDir)(dest, secrets)
}
