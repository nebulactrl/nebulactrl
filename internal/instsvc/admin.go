package instsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Audit actions.
const (
	ActionDefaultQuota = "instance_services.update"
	ActionSliceQuota   = "team_slice.quota"
	ActionSlicePurge   = "team_slice.purge"
)

// InstanceServiceView is one instance service as the instance administrator
// sees it.
type InstanceServiceView struct {
	Kind      string    `json:"kind" enum:"s3,registry,loki" doc:"s3 = backup storage, registry = image registry, loki = central logs"`
	Status    string    `json:"status" enum:"provisioning,ready,failed"`
	Message   string    `json:"message" doc:"Why the service is failed; empty when it is ready"`
	UpdatedAt time.Time `json:"updatedAt,omitzero" doc:"When the service was last checked; absent when it never was"`
	Teams     int       `json:"teams" doc:"Teams that have a slice of this service"`
	UsedBytes int64     `json:"usedBytes" doc:"s3: the bytes all teams' backups hold; 0 for the other kinds"`
}

// SliceView is one team's slice of a service. It carries no key, bucket or
// endpoint: the administrator manages quotas, not credentials.
type SliceView struct {
	ID                  uuid.UUID  `json:"id"`
	TeamID              uuid.UUID  `json:"teamId"`
	TeamName            string     `json:"teamName"`
	Kind                string     `json:"kind" enum:"s3,registry,loki"`
	UsedBytes           int64      `json:"usedBytes"`
	QuotaBytes          *int64     `json:"quotaBytes" doc:"The team's own quota; null = the instance default (s3 only)"`
	EffectiveQuotaBytes int64      `json:"effectiveQuotaBytes" doc:"The quota that applies (s3); 0 for kinds without a quota"`
	State               string     `json:"state" enum:"provisioning,ready,full,failed"`
	Message             string     `json:"message" doc:"Why the slice is failed; empty otherwise"`
	DeletedAt           *time.Time `json:"deletedAt" doc:"Set when the team was deleted; the slice is purged at purgeAt"`
	PurgeAt             *time.Time `json:"purgeAt"`
}

// InstanceServicesView is Admin → Built-in services.
type InstanceServicesView struct {
	Services          []InstanceServiceView `json:"services"`
	DefaultQuotaBytes int64                 `json:"defaultQuotaBytes" doc:"The S3 quota of every team without its own"`
	Slices            []SliceView           `json:"slices"`
}

// instanceAdmin authorizes the instance administrator (a token counts through
// its creator and needs the admin scope).
func (s *Service) instanceAdmin(ctx context.Context) (*auth.Principal, error) {
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
		return nil, apierr.New(http.StatusForbidden, "instance_admin_required", "Only the instance administrator can manage the built-in services.")
	}
	return p, nil
}

func sliceView(r db.TeamServiceSlice, def int64) SliceView {
	v := SliceView{
		ID: r.ID, TeamID: r.TeamID, TeamName: r.TeamName, Kind: r.Kind, UsedBytes: r.UsedBytes, QuotaBytes: r.QuotaBytes,
		State: r.State, Message: r.Message, DeletedAt: r.DeletedAt, PurgeAt: r.PurgeAt,
	}
	if r.Kind == KindS3 {
		v.EffectiveQuotaBytes = effectiveQuota(r.QuotaBytes, def)
	}
	return v
}

// Overview is the page of Admin → Built-in services.
func (s *Service) Overview(ctx context.Context) (InstanceServicesView, error) {
	if _, err := s.instanceAdmin(ctx); err != nil {
		return InstanceServicesView{}, err
	}
	return s.overview(ctx)
}

func (s *Service) overview(ctx context.Context) (InstanceServicesView, error) {
	out := InstanceServicesView{Services: []InstanceServiceView{}, Slices: []SliceView{}, DefaultQuotaBytes: DefaultQuotaBytes}
	rows, err := s.q.ListInstanceServices(ctx)
	if err != nil {
		return out, fmt.Errorf("list instance services: %w", err)
	}
	slices, err := s.q.ListSlicesForAdmin(ctx)
	if err != nil {
		return out, fmt.Errorf("list slices: %w", err)
	}
	byKind := map[string]db.InstanceService{}
	for _, r := range rows {
		byKind[r.Kind] = r
		if r.Kind == KindS3 {
			out.DefaultQuotaBytes = defaultQuotaOf(r.Options)
		}
	}
	teams, used := map[string]int{}, int64(0)
	for _, sl := range slices {
		out.Slices = append(out.Slices, sliceView(sl, out.DefaultQuotaBytes))
		if sl.DeletedAt == nil {
			teams[sl.Kind]++
			if sl.Kind == KindS3 {
				used += sl.UsedBytes
			}
		}
	}
	for _, kind := range kinds {
		v := InstanceServiceView{Kind: kind, Status: StateProvisioning, Teams: teams[kind]}
		if r, ok := byKind[kind]; ok {
			v.Status, v.Message, v.UpdatedAt = r.Status, r.Message, r.UpdatedAt
		} else if s.backends[kind] == nil {
			v.Status, v.Message = StateFailed, "Not set up on this installation."
		}
		if kind == KindS3 {
			v.UsedBytes = used
		}
		out.Services = append(out.Services, v)
	}
	return out, nil
}

// Check runs Ensure now (the "Check again" button) and returns the new state.
// What failed is on the services' rows, not an error of the request.
func (s *Service) Check(ctx context.Context) (InstanceServicesView, error) {
	if _, err := s.instanceAdmin(ctx); err != nil {
		return InstanceServicesView{}, err
	}
	if err := s.Ensure(ctx); err != nil {
		s.log.WarnContext(ctx, "check instance services", "err", err)
	}
	return s.overview(ctx)
}

// UpdateDefaultQuota sets the S3 quota of every team without its own. Lowering
// it below what a team uses only blocks that team's new writes.
func (s *Service) UpdateDefaultQuota(ctx context.Context, bytes int64) (InstanceServicesView, error) {
	pr, err := s.instanceAdmin(ctx)
	if err != nil {
		return InstanceServicesView{}, err
	}
	if bytes < MinQuotaBytes {
		return InstanceServicesView{}, apierr.Unprocessable("quota_too_small", "The default quota must be at least 1 GiB.")
	}
	if err := s.ensureRow(ctx, KindS3); err != nil {
		return InstanceServicesView{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.MergeInstanceServiceOptions(ctx, db.MergeInstanceServiceOptionsParams{
			Kind: KindS3, Patch: []byte(fmt.Sprintf(`{"defaultQuotaBytes":%d}`, bytes)),
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDefaultQuota, ResourceType: "instance_service", ResourceID: KindS3, WorkspaceID: pr.WorkspaceID,
			Metadata: map[string]any{"defaultQuotaBytes": bytes},
		})
	})
	if err != nil {
		return InstanceServicesView{}, fmt.Errorf("update default quota: %w", err)
	}
	rows, err := s.q.ListSlicesByKind(ctx, KindS3)
	if err != nil {
		return InstanceServicesView{}, fmt.Errorf("list s3 slices: %w", err)
	}
	for _, r := range rows {
		if r.QuotaBytes == nil {
			s.applyQuota(ctx, r, bytes)
		}
	}
	return s.overview(ctx)
}

// applyQuota pushes a changed effective quota: the native bucket quota (a second
// fence; the gate is authoritative) and the state and alerts the last usage
// reading now implies. A failed push is logged: the next Ensure of the team
// repeats it.
func (s *Service) applyQuota(ctx context.Context, r db.TeamServiceSlice, def int64) {
	if be := s.backends[r.Kind]; be != nil && (r.State == StateReady || r.State == StateFull) {
		sl := sliceOf(r, def)
		if err := be.EnsureSlice(ctx, sl, sl.QuotaBytes); err != nil {
			s.log.WarnContext(ctx, "push native quota", "team_id", r.TeamID, "err", err)
		}
	}
	if err := s.evaluate(ctx, r, r.UsedBytes, def); err != nil {
		s.log.WarnContext(ctx, "re-evaluate slice after quota change", "team_id", r.TeamID, "err", err)
	}
}

// UpdateSliceQuota sets a team's own S3 quota; nil resets it to the instance
// default. Lowering it below use only blocks new writes: nothing is deleted.
func (s *Service) UpdateSliceQuota(ctx context.Context, sliceID uuid.UUID, quota *int64) (SliceView, error) {
	pr, err := s.instanceAdmin(ctx)
	if err != nil {
		return SliceView{}, err
	}
	row, err := s.q.GetSlice(ctx, sliceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliceView{}, apierr.NotFound("slice")
	}
	if err != nil {
		return SliceView{}, fmt.Errorf("get slice: %w", err)
	}
	if row.Kind != KindS3 {
		return SliceView{}, apierr.Unprocessable("quota_not_supported", "Only backup storage has a quota; registry images and logs follow retention.")
	}
	if row.DeletedAt != nil {
		return SliceView{}, apierr.Conflict("slice_deleted", "The team was deleted; its slice only waits for its purge.")
	}
	if quota != nil && *quota < MinQuotaBytes {
		return SliceView{}, apierr.Unprocessable("quota_too_small", "A quota must be at least 1 GiB.")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if row, err = db.New(tx).SetSliceQuota(ctx, db.SetSliceQuotaParams{ID: sliceID, QuotaBytes: quota}); err != nil {
			return err
		}
		meta := map[string]any{"teamId": row.TeamID.String(), "teamName": row.TeamName, "reset": quota == nil}
		if quota != nil {
			meta["quotaBytes"] = *quota
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSliceQuota, ResourceType: "team_slice", ResourceID: sliceID.String(), WorkspaceID: pr.WorkspaceID, Metadata: meta,
		})
	})
	if err != nil {
		return SliceView{}, fmt.Errorf("update slice quota: %w", err)
	}
	def, err := defaultQuota(ctx, s.q)
	if err != nil {
		return SliceView{}, err
	}
	s.applyQuota(ctx, row, def)
	if row, err = s.q.GetSlice(ctx, sliceID); err != nil {
		return SliceView{}, fmt.Errorf("get slice: %w", err)
	}
	return sliceView(row, def), nil
}

// PurgeTeamSlice deletes a deleted team's slices now. confirm is the team's name
// as the table shows it. A team that still exists is never purged (409
// team_still_exists).
func (s *Service) PurgeTeamSlice(ctx context.Context, teamID uuid.UUID, confirm string) error {
	pr, err := s.instanceAdmin(ctx)
	if err != nil {
		return err
	}
	if _, err := s.q.GetWorkspace(ctx, teamID); err == nil {
		return apierr.Conflict("team_still_exists", "The team still exists. Only the slices of a deleted team can be purged.")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get team: %w", err)
	}
	rows, err := s.q.ListDeletedTeamSlices(ctx, teamID)
	if err != nil {
		return fmt.Errorf("list deleted slices: %w", err)
	}
	if len(rows) == 0 {
		return apierr.NotFound("team slice")
	}
	name := rows[0].TeamName
	if strings.TrimSpace(confirm) != name {
		return apierr.Unprocessable("confirmation_mismatch", "Type the deleted team's name to confirm.")
	}
	if err := s.PurgeTeam(ctx, teamID); err != nil {
		return apierr.New(http.StatusBadGateway, "purge_failed", "Some of the team's data could not be removed yet; it stays and the purge is tried again: "+truncate(err.Error()))
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSlicePurge, ResourceType: "team_slice", ResourceID: teamID.String(), WorkspaceID: pr.WorkspaceID,
			Metadata: map[string]any{"teamId": teamID.String(), "teamName": name, "slices": len(rows)},
		})
	})
}

// TeamSliceView is the team's own view of its slice of the built-in services. It
// never carries a key, a bucket, a repository prefix or an address: teams see
// space, not plumbing.
type TeamSliceView struct {
	Backup   BackupSliceView   `json:"backup"`
	Registry RegistrySliceView `json:"registry"`
	Logs     LogsSliceView     `json:"logs"`
}

// BackupSliceView is "Built-in backup storage".
type BackupSliceView struct {
	UsedBytes  int64  `json:"usedBytes"`
	QuotaBytes int64  `json:"quotaBytes"`
	Percent    int    `json:"percent" doc:"usedBytes as a percentage of quotaBytes; above 100 when the quota was lowered under the current use"`
	State      string `json:"state" enum:"provisioning,ready,full,failed,unavailable" doc:"full: new backups fail until space is freed; unavailable: this installation has no built-in backup storage"`
}

// RegistrySliceView is the team's share of the built-in image registry.
type RegistrySliceView struct {
	UsedBytes int64 `json:"usedBytes" doc:"Size of the team's live images; unused images are pruned by retention"`
}

// LogsSliceView is the team's central logs.
type LogsSliceView struct {
	RetentionDays int `json:"retentionDays"`
}

// TeamSlice returns the caller's team's slice for display.
func (s *Service) TeamSlice(ctx context.Context) (TeamSliceView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return TeamSliceView{}, err
	}
	team := policy.WorkspaceID(ctx)
	out := TeamSliceView{Backup: BackupSliceView{State: "unavailable"}, Logs: LogsSliceView{RetentionDays: LogRetentionDays}}
	ts, err := s.ForTeam(ctx, team)
	if err != nil {
		return out, err
	}
	if ts.S3 != nil {
		out.Backup = BackupSliceView{UsedBytes: ts.S3.UsedBytes, QuotaBytes: ts.S3.QuotaBytes, State: ts.S3.State}
		if ts.S3.QuotaBytes > 0 {
			out.Backup.Percent = int(ts.S3.UsedBytes * 100 / ts.S3.QuotaBytes)
		}
	}
	reg, err := s.q.GetBuiltinRegistry(ctx, team)
	if err == nil {
		st, err := s.q.RegistryImageStats(ctx, reg.ID)
		if err != nil {
			return out, fmt.Errorf("registry image stats: %w", err)
		}
		out.Registry.UsedBytes = st.SizeBytes
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("get builtin registry: %w", err)
	}
	return out, nil
}
