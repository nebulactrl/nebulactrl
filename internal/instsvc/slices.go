package instsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Slice is a team's share of one instance service. The forwarders and the
// Backends see only this: the sealed secret stays behind Slices.Secret.
type Slice struct {
	ID, TeamID uuid.UUID
	Kind       string
	// Ref is the bucket (s3), the repository prefix "<team-id>/" (registry) or
	// the Loki tenant (loki).
	Ref string
	// AccessKey is the bucket-scoped key (s3 only).
	AccessKey string
	// QuotaBytes is the effective quota: the team's own or the instance default
	// (s3 only; 0 for the other kinds).
	QuotaBytes int64
	UsedBytes  int64
	State      string
}

// TeamSlices is what a team holds; a kind that is not configured (or not
// provisioned yet) is nil.
type TeamSlices struct{ S3, Registry, Loki *Slice }

// Slices resolves who a connection is and what it may touch. The forwarders
// depend on this and nothing else of the database.
type Slices interface {
	// TeamOfServer is the team a server belongs to (the agent's identity decides
	// the team, never anything the agent sends).
	TeamOfServer(ctx context.Context, serverID uuid.UUID) (uuid.UUID, error)
	// ForTeam returns the team's live slices (a deleted team's are not served).
	ForTeam(ctx context.Context, teamID uuid.UUID) (TeamSlices, error)
	// Secret opens the secret of an s3 slice's key. For the system slice
	// (TeamID == uuid.Nil) it is the system key's secret.
	Secret(ctx context.Context, s Slice) (string, error)
	// AccessKeyOwner is the team that owns an s3 access key.
	AccessKeyOwner(ctx context.Context, accessKey string) (uuid.UUID, error)
}

var _ Slices = (*Service)(nil)

func sliceOf(r db.TeamServiceSlice, defaultQuota int64) Slice {
	s := Slice{ID: r.ID, TeamID: r.TeamID, Kind: r.Kind, Ref: r.Ref, AccessKey: r.AccessKey, UsedBytes: r.UsedBytes, State: r.State}
	if r.Kind == KindS3 {
		s.QuotaBytes = effectiveQuota(r.QuotaBytes, defaultQuota)
	}
	return s
}

// effectiveQuota is the team's own quota, else the instance default.
func effectiveQuota(own *int64, def int64) int64 {
	if own != nil && *own > 0 {
		return *own
	}
	if def > 0 {
		return def
	}
	return DefaultQuotaBytes
}

// defaultQuota reads the instance default quota (options.defaultQuotaBytes).
func defaultQuota(ctx context.Context, q *db.Queries) (int64, error) {
	row, err := q.GetInstanceService(ctx, KindS3)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultQuotaBytes, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get s3 service: %w", err)
	}
	return defaultQuotaOf(row.Options), nil
}

func defaultQuotaOf(options []byte) int64 {
	var o struct {
		DefaultQuotaBytes int64 `json:"defaultQuotaBytes"`
	}
	if json.Unmarshal(options, &o) != nil || o.DefaultQuotaBytes <= 0 {
		return DefaultQuotaBytes
	}
	return o.DefaultQuotaBytes
}

// TeamOfServer implements Slices.
func (s *Service) TeamOfServer(ctx context.Context, serverID uuid.UUID) (uuid.UUID, error) {
	team, err := s.q.GetServerTeam(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNoSlice
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get server team: %w", err)
	}
	return team, nil
}

// ForTeam implements Slices.
func (s *Service) ForTeam(ctx context.Context, teamID uuid.UUID) (TeamSlices, error) {
	var out TeamSlices
	rows, err := s.q.ListTeamSlices(ctx, teamID)
	if err != nil {
		return out, fmt.Errorf("list slices: %w", err)
	}
	def, err := defaultQuota(ctx, s.q)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		sl := sliceOf(r, def)
		switch r.Kind {
		case KindS3:
			out.S3 = &sl
		case KindRegistry:
			out.Registry = &sl
		case KindLoki:
			out.Loki = &sl
		}
	}
	return out, nil
}

// systemSecret is the sealed content of instance_services.secrets_enc of kind s3.
type systemSecret struct {
	AccessKey string `json:"accessKey"`
	Secret    string `json:"secret"`
}

// Secret implements Slices.
func (s *Service) Secret(ctx context.Context, sl Slice) (string, error) {
	if sl.TeamID == uuid.Nil {
		sys, err := s.systemKey(ctx)
		if err != nil {
			return "", err
		}
		return sys.Secret, nil
	}
	row, err := s.q.GetSlice(ctx, sl.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSlice
	}
	if err != nil {
		return "", fmt.Errorf("get slice: %w", err)
	}
	if row.Kind != KindS3 || len(row.SecretEnc) == 0 {
		return "", ErrNoSlice
	}
	plain, err := s.box.OpenString(secretbox.TeamSliceSecrets, row.ID.String(), row.SecretEnc)
	if err != nil {
		return "", fmt.Errorf("open slice secret: %w", err)
	}
	return plain, nil
}

// systemKey opens the system bucket's key.
func (s *Service) systemKey(ctx context.Context) (systemSecret, error) {
	var sys systemSecret
	row, err := s.q.GetInstanceService(ctx, KindS3)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(row.SecretsEnc) == 0) {
		return sys, ErrNoSlice
	}
	if err != nil {
		return sys, fmt.Errorf("get s3 service: %w", err)
	}
	raw, err := s.box.Open(secretbox.InstanceServicesSecrets, KindS3, row.SecretsEnc)
	if err != nil {
		return sys, fmt.Errorf("open system key: %w", err)
	}
	defer secretbox.Wipe(raw)
	if err := json.Unmarshal(raw, &sys); err != nil {
		return sys, fmt.Errorf("decode system key: %w", err)
	}
	return sys, nil
}

// SystemAccessKey returns the access key and secret of the system bucket (the
// control-plane backup writes there directly). The caller must not log them.
func (s *Service) SystemAccessKey(ctx context.Context) (accessKey, secret, bucket string, err error) {
	sys, err := s.systemKey(ctx)
	if err != nil {
		return "", "", "", err
	}
	return sys.AccessKey, sys.Secret, SystemBucket, nil
}

// AccessKeyOwner implements Slices.
func (s *Service) AccessKeyOwner(ctx context.Context, accessKey string) (uuid.UUID, error) {
	if accessKey == "" {
		return uuid.Nil, ErrNoSlice
	}
	row, err := s.q.GetSliceByAccessKey(ctx, accessKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNoSlice
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get slice by key: %w", err)
	}
	return row.TeamID, nil
}

// SliceForBackup resolves the team's built-in S3 slice with its secret, for the
// control plane to build a backup command (Command.Secrets). It returns
// ErrNoSlice when the team has none and ErrNotReady while it is being set up or
// failed. A full slice is returned: reads and restores still work.
func (s *Service) SliceForBackup(ctx context.Context, teamID uuid.UUID) (sl Slice, secret string, err error) {
	ts, err := s.ForTeam(ctx, teamID)
	if err != nil {
		return Slice{}, "", err
	}
	if ts.S3 == nil {
		return Slice{}, "", ErrNoSlice
	}
	if ts.S3.State != StateReady && ts.S3.State != StateFull {
		return *ts.S3, "", ErrNotReady
	}
	secret, err = s.Secret(ctx, *ts.S3)
	if err != nil {
		return Slice{}, "", err
	}
	return *ts.S3, secret, nil
}
