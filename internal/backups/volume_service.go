package backups

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	codeVolumeUnavailable = "volume_backup_unavailable"
	maxExcludes           = 100
	maxExcludeLen         = 512
	maxHookArgLen         = 4096
)

// volumeScope authorizes scope on the service and returns it with its volumes.
func (s *Service) volumeScope(ctx context.Context, scope string, serviceID uuid.UUID) (services.Scope, []db.Volume, error) {
	sc, err := services.LoadScope(ctx, s.q, scope, serviceID)
	if err != nil {
		return services.Scope{}, nil, err
	}
	vols, err := s.q.ListServiceVolumes(ctx, serviceID)
	if err != nil {
		return services.Scope{}, nil, fmt.Errorf("list volumes: %w", err)
	}
	return sc, vols, nil
}

// volumeBackupScope authorizes scope on the backup's service and returns it.
func (s *Service) volumeBackupScope(ctx context.Context, scope string, id uuid.UUID) (db.VolumeBackup, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.VolumeBackup{}, services.Scope{}, err
	}
	b, err := s.q.GetVolumeBackup(ctx, db.GetVolumeBackupParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return b, services.Scope{}, apierr.NotFound("backup")
	}
	if err != nil {
		return b, services.Scope{}, fmt.Errorf("get volume backup: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, scope, b.ServiceID)
	if err != nil {
		return b, sc, err
	}
	return b, sc, nil
}

// volumeIneligibleReason says why a service can not use volume backups ("" =
// it can): managed databases use database backups, route services have no
// container, and a service needs a server and at least one volume.
func volumeIneligibleReason(sc services.Scope, vols []db.Volume) string {
	switch {
	case sc.Service.Kind == services.KindDatabase:
		return "Managed databases are backed up with database backups."
	case sc.Service.Kind == services.KindRoute:
		return "Route services have no files to back up."
	case sc.Service.Kind == services.KindStack:
		return "Not available for Compose stacks"
	case sc.Service.ServerID == nil:
		return "The service is not placed on a server yet."
	case len(vols) == 0:
		return "The service has no volumes. Add a volume to back up its files."
	}
	return ""
}

func requireVolumeEligible(sc services.Scope, vols []db.Volume) error {
	if why := volumeIneligibleReason(sc, vols); why != "" {
		return apierr.Unprocessable(codeVolumeUnavailable, why)
	}
	return nil
}

func volumeNames(vols []db.Volume) []string {
	out := make([]string, len(vols))
	for i, v := range vols {
		out[i] = v.Name
	}
	return out
}

// volumeRepoPath is the repository path of a service in a destination:
// <destination prefix>/volumes/<serviceId>.
func volumeRepoPath(d db.BackupDestination, serviceID uuid.UUID) string {
	p := "volumes/" + serviceID.String()
	if prefix := cleanPrefix(d.Prefix); prefix != "" {
		return prefix + "/" + p
	}
	return p
}

func decodeHook(raw []byte) *VolumeBackupHook {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var h VolumeBackupHook
	if json.Unmarshal(raw, &h) != nil || len(h.Command) == 0 {
		return nil
	}
	return &h
}

func encodeHook(h *VolumeBackupHook) json.RawMessage {
	if h == nil {
		return nil
	}
	raw, _ := json.Marshal(h)
	return raw
}

// validateHook checks a hook and fills the default timeout.
func validateHook(name string, h *VolumeBackupHook) (*VolumeBackupHook, error) {
	if h == nil {
		return nil, nil
	}
	bad := func(msg string) error { return apierr.Unprocessable("validation_failed", name+": "+msg) }
	if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
		return nil, bad("command needs at least the executable")
	}
	for _, a := range h.Command {
		if len(a) > maxHookArgLen || strings.ContainsRune(a, 0) {
			return nil, bad("a command argument is too long or contains a NUL byte")
		}
	}
	out := *h
	out.User = strings.TrimSpace(out.User)
	if len(out.User) > 128 || strings.ContainsAny(out.User, "\x00\n ") {
		return nil, bad("user is not valid")
	}
	if out.TimeoutSeconds == 0 {
		out.TimeoutSeconds = agentprotocol.DefaultHookTimeoutSeconds
	}
	if out.TimeoutSeconds < 1 || out.TimeoutSeconds > agentprotocol.MaxHookTimeoutSeconds {
		return nil, bad(fmt.Sprintf("timeoutSeconds must be 1-%d", agentprotocol.MaxHookTimeoutSeconds))
	}
	return &out, nil
}

func validateExcludes(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if len(e) > maxExcludeLen || strings.ContainsAny(e, "\x00\n\r") {
			return nil, apierr.Unprocessable("validation_failed", "an exclude pattern is too long or contains a control character")
		}
		out = append(out, e)
	}
	if len(out) > maxExcludes {
		return nil, apierr.Unprocessable("validation_failed", fmt.Sprintf("at most %d exclude patterns", maxExcludes))
	}
	return out, nil
}

func keepValue(p *int, def, max int, name string) (int32, error) {
	v := def
	if p != nil {
		v = *p
	}
	if v < 0 || v > max {
		return 0, apierr.Unprocessable("validation_failed", fmt.Sprintf("%s must be 0-%d (0 = no limit)", name, max))
	}
	return int32(v), nil
}

// volumeConfigView builds the API view of a service's configuration (the
// defaults when none is saved).
func (s *Service) volumeConfigView(ctx context.Context, sc services.Scope, vols []db.Volume, cfg *db.VolumeBackupConfig) (VolumeBackupConfigView, error) {
	v := VolumeBackupConfigView{
		ServiceID: sc.Service.ID, Volumes: make([]VolumeBackupVolumeRef, len(vols)), Eligible: true,
		Mode: agentprotocol.VolModeFreeze, FreezeBudgetSeconds: agentprotocol.DefaultFreezeBudgetSeconds, Excludes: []string{},
		KeepDaily: agentprotocol.DefaultKeepDaily, KeepWeekly: agentprotocol.DefaultKeepWeekly, KeepMonthly: agentprotocol.DefaultKeepMonthly,
		Timezone: "UTC",
	}
	for i, vol := range vols {
		v.Volumes[i] = VolumeBackupVolumeRef{ID: vol.ID, Name: vol.Name, MountPath: vol.MountPath}
	}
	if why := volumeIneligibleReason(sc, vols); why != "" {
		v.Eligible, v.IneligibleReason = false, why
	}
	if cfg == nil {
		return v, nil
	}
	v.Configured = true
	v.DestinationID = &cfg.DestinationID
	v.Mode, v.FreezeBudgetSeconds = cfg.Mode, int(cfg.FreezeBudgetSeconds)
	v.PreHook, v.PostHook = decodeHook(cfg.PreHook), decodeHook(cfg.PostHook)
	v.Excludes = nonNil(cfg.Excludes)
	v.KeepDaily, v.KeepWeekly, v.KeepMonthly = int(cfg.KeepDaily), int(cfg.KeepWeekly), int(cfg.KeepMonthly)
	v.Cron, v.Timezone, v.Enabled, v.NextRunAt, v.LastRunAt = cfg.Cron, cfg.Timezone, cfg.Enabled, cfg.NextRunAt, cfg.LastRunAt
	if d, err := s.q.GetBackupDestinationInternal(ctx, cfg.DestinationID); err == nil {
		v.DestinationName, v.DestinationKind = d.Name, d.Kind
	}
	if _, err := s.q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: sc.Service.ID, DestinationID: cfg.DestinationID}); err == nil {
		v.RepoReady = true
	}
	return v, nil
}

func (s *Service) loadVolumeConfig(ctx context.Context, q *db.Queries, serviceID uuid.UUID) (*db.VolumeBackupConfig, error) {
	cfg, err := q.GetVolumeBackupConfig(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get volume backup config: %w", err)
	}
	return &cfg, nil
}

// GetVolumeBackupConfig returns a service's volume backup configuration, with
// the defaults when none is saved.
func (s *Service) GetVolumeBackupConfig(ctx context.Context, serviceID uuid.UUID) (VolumeBackupConfigView, error) {
	sc, vols, err := s.volumeScope(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	cfg, err := s.loadVolumeConfig(ctx, s.q, serviceID)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	return s.volumeConfigView(ctx, sc, vols, cfg)
}

// PutVolumeBackupConfig saves (replaces) a service's volume backup
// configuration. With a schedule enabled next_run_at is set.
func (s *Service) PutVolumeBackupConfig(ctx context.Context, serviceID uuid.UUID, in VolumeBackupConfigInput) (VolumeBackupConfigView, error) {
	sc, vols, err := s.volumeScope(ctx, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	if err := requireVolumeEligible(sc, vols); err != nil {
		return VolumeBackupConfigView{}, err
	}
	dest, err := s.q.GetBackupDestination(ctx, db.GetBackupDestinationParams{ID: in.DestinationID, WorkspaceID: sc.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupConfigView{}, apierr.Unprocessable("validation_failed", "the backup destination does not exist")
	}
	if err != nil {
		return VolumeBackupConfigView{}, fmt.Errorf("get destination: %w", err)
	}
	mode := in.Mode
	if mode == "" {
		mode = agentprotocol.VolModeFreeze
	}
	switch mode {
	case agentprotocol.VolModeFreeze, agentprotocol.VolModeStop, agentprotocol.VolModeLive:
	default:
		return VolumeBackupConfigView{}, apierr.Unprocessable("validation_failed", "mode must be freeze, stop or live")
	}
	budget := in.FreezeBudgetSeconds
	if budget == 0 {
		budget = agentprotocol.DefaultFreezeBudgetSeconds
	}
	if budget < agentprotocol.MinFreezeBudgetSeconds || budget > agentprotocol.MaxFreezeBudgetSeconds {
		return VolumeBackupConfigView{}, apierr.Unprocessable("validation_failed",
			fmt.Sprintf("freezeBudgetSeconds must be %d-%d", agentprotocol.MinFreezeBudgetSeconds, agentprotocol.MaxFreezeBudgetSeconds))
	}
	pre, err := validateHook("preHook", in.PreHook)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	post, err := validateHook("postHook", in.PostHook)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	excludes, err := validateExcludes(in.Excludes)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	keepD, err := keepValue(in.KeepDaily, agentprotocol.DefaultKeepDaily, 3650, "keepDaily")
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	keepW, err := keepValue(in.KeepWeekly, agentprotocol.DefaultKeepWeekly, 520, "keepWeekly")
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	keepM, err := keepValue(in.KeepMonthly, agentprotocol.DefaultKeepMonthly, 120, "keepMonthly")
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	cron := strings.TrimSpace(in.Cron)
	tz := strings.TrimSpace(in.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := cronexpr.LoadLocation(tz); err != nil {
		return VolumeBackupConfigView{}, apierr.Unprocessable("invalid_timezone", err.Error())
	}
	if in.Enabled && cron == "" {
		return VolumeBackupConfigView{}, apierr.Unprocessable("validation_failed", "a schedule needs a cron expression")
	}
	cur, err := s.loadVolumeConfig(ctx, s.q, serviceID)
	if err != nil {
		return VolumeBackupConfigView{}, err
	}
	if err := sc.Guard(ctx, guardPutVolumeBackupConfig, fmt.Sprintf("Change the volume backup settings of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{
			"Volume backups and their retention follow the new settings at once.",
			fmt.Sprintf("Enabled: %t, schedule: %s (%s), mode: %s, keep daily/weekly/monthly: %d/%d/%d.", in.Enabled, cron, tz, mode, keepD, keepW, keepM),
		},
		dest.ID, mode, budget, cron, tz, in.Enabled, keepD, keepW, keepM, string(encodeHook(pre)), string(encodeHook(post)), strings.Join(excludes, "\n")); err != nil {
		return VolumeBackupConfigView{}, err
	}
	var next *time.Time
	if cron != "" {
		n, err := nextRun(cron, tz, s.now()) // validates the expression even when the schedule is off
		if err != nil {
			return VolumeBackupConfigView{}, err
		}
		if in.Enabled {
			next = &n
			// An unchanged running schedule keeps its next run (saving other settings does not skip or delay it).
			if cur != nil && cur.Enabled && cur.Cron == cron && cur.Timezone == tz && cur.NextRunAt != nil {
				next = cur.NextRunAt
			}
		}
	}
	var row db.VolumeBackupConfig
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpsertVolumeBackupConfig(ctx, db.UpsertVolumeBackupConfigParams{
			ServiceID: serviceID, WorkspaceID: sc.WorkspaceID, DestinationID: dest.ID, Mode: mode, FreezeBudgetSeconds: int32(budget),
			PreHook: encodeHook(pre), PostHook: encodeHook(post), Excludes: excludes,
			KeepDaily: keepD, KeepWeekly: keepW, KeepMonthly: keepM, Cron: cron, Timezone: tz, Enabled: in.Enabled, NextRunAt: next,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeConfigUpdate, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{
				"service": sc.Service.Slug, "destination": dest.Name, "mode": mode, "cron": cron, "timezone": tz, "enabled": in.Enabled,
				"preHook": pre != nil, "postHook": post != nil, "excludes": len(excludes),
				"keepDaily": keepD, "keepWeekly": keepW, "keepMonthly": keepM,
			},
		})
	})
	if err != nil {
		return VolumeBackupConfigView{}, fmt.Errorf("save volume backup config: %w", err)
	}
	s.publish(sc, EventBackupUpdated, serviceID)
	return s.volumeConfigView(ctx, sc, vols, &row)
}

// DeleteVolumeBackupConfig removes the configuration (and with it the
// schedule). Backups and the repository are kept.
func (s *Service) DeleteVolumeBackupConfig(ctx context.Context, serviceID uuid.UUID) error {
	sc, _, err := s.volumeScope(ctx, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return err
	}
	if err := sc.Guard(ctx, guardDeleteVolumeBackupConf, fmt.Sprintf("Delete the volume backup settings of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), true,
		[]string{"Scheduled volume backups stop."}); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := db.New(tx).DeleteVolumeBackupConfig(ctx, serviceID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apierr.NotFound("volume backup configuration")
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionConfigDelete, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete volume backup config: %w", err)
	}
	s.publish(sc, EventBackupUpdated, serviceID)
	return nil
}

// ---- repositories ----

func newRepoPassword() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ensureVolumeRepo returns the repository of a service in a destination and
// creates it (with a fresh random password) at the first backup.
func (s *Service) ensureVolumeRepo(ctx context.Context, q *db.Queries, serviceID uuid.UUID, d db.BackupDestination) (db.VolumeRepo, error) {
	key := db.GetVolumeRepoParams{ServiceID: serviceID, DestinationID: d.ID}
	repo, err := q.GetVolumeRepo(ctx, key)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return repo, fmt.Errorf("get volume repo: %w", err)
	}
	pw, err := newRepoPassword()
	if err != nil {
		return repo, fmt.Errorf("generate repository password: %w", err)
	}
	enc, err := s.box.SealString(secretbox.VolumeReposPassword, secretbox.RowID(serviceID.String(), d.ID.String()), pw)
	if err != nil {
		return repo, fmt.Errorf("seal repository password: %w", err)
	}
	if err := q.InsertVolumeRepo(ctx, db.InsertVolumeRepoParams{
		ServiceID: serviceID, DestinationID: d.ID, RepoPath: volumeRepoPath(d, serviceID), PasswordEnc: enc,
	}); err != nil {
		return repo, fmt.Errorf("insert volume repo: %w", err)
	}
	return q.GetVolumeRepo(ctx, key)
}

// repoSecrets returns the send-time secrets of a service's repository: the
// destination's keys and the repository password.
func (s *Service) repoSecrets(ctx context.Context, q *db.Queries, serviceID, destinationID uuid.UUID) (map[string]string, error) {
	d, err := q.GetBackupDestinationInternal(ctx, destinationID)
	if err != nil {
		return nil, fmt.Errorf("load destination: %w", err)
	}
	out, err := s.destinationSecrets(ctx, d)
	if err != nil {
		return nil, err
	}
	repo, err := q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: serviceID, DestinationID: destinationID})
	if err != nil {
		return nil, fmt.Errorf("load repository: %w", err)
	}
	pw, err := s.box.OpenString(secretbox.VolumeReposPassword, secretbox.RowID(serviceID.String(), destinationID.String()), repo.PasswordEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt repository password: %w", err)
	}
	out[agentprotocol.SecretResticPassword] = pw
	return out, nil
}

func (s *Service) volumeRepo(ctx context.Context, d db.BackupDestination, path string) (agentprotocol.VolumeRepo, error) {
	dest, err := s.destinationFor(ctx, d)
	if err != nil {
		return agentprotocol.VolumeRepo{}, err
	}
	return agentprotocol.VolumeRepo{Destination: dest, Path: path}, nil
}

// ---- starting a backup ----

type volumeStart struct {
	sc      services.Scope
	vols    []db.Volume
	cfg     db.VolumeBackupConfig
	dest    db.BackupDestination
	trigger string
	actor   string
}

// buildVolumeBackupPayload is the persisted payload of a volume_backup command.
// It names the repository but carries no secret.
func buildVolumeBackupPayload(in volumeStart, backupID uuid.UUID, repo agentprotocol.VolumeRepo) agentprotocol.VolumeBackupPayload {
	p := agentprotocol.VolumeBackupPayload{
		BackupID: backupID.String(), ServiceID: in.sc.Service.ID.String(), Repo: repo,
		Mode: in.cfg.Mode, FreezeBudgetSeconds: int(in.cfg.FreezeBudgetSeconds),
		PreHook: protoHook(decodeHook(in.cfg.PreHook)), PostHook: protoHook(decodeHook(in.cfg.PostHook)),
		Excludes: in.cfg.Excludes, Trigger: in.trigger,
		Meta: agentprotocol.ManifestMeta{
			Project: in.sc.ProjectSlug, Environment: in.sc.EnvSlug, Service: in.sc.Service.Slug,
			ServiceID: in.sc.Service.ID.String(), Trigger: in.trigger,
		},
	}
	for _, v := range in.vols {
		p.Volumes = append(p.Volumes, agentprotocol.VolumeRef{Name: v.Name, DockerName: services.DockerVolumeName(v), MountPath: v.MountPath})
	}
	return p
}

// startVolumeBackup records a backup run, creates the repository on first use
// and enqueues its volume_backup command inside tx.
func (s *Service) startVolumeBackup(ctx context.Context, tx pgx.Tx, in volumeStart) (db.VolumeBackup, error) {
	q := db.New(tx)
	if in.sc.Service.ServerID == nil {
		return db.VolumeBackup{}, apierr.Unprocessable("no_server", "the service is not placed on a server")
	}
	serverID := *in.sc.Service.ServerID
	repo, err := s.ensureVolumeRepo(ctx, q, in.sc.Service.ID, in.dest)
	if err != nil {
		return db.VolumeBackup{}, err
	}
	b, err := q.InsertVolumeBackup(ctx, db.InsertVolumeBackupParams{
		WorkspaceID: in.sc.WorkspaceID, ServiceID: in.sc.Service.ID, DestinationID: in.dest.ID, ServerID: &serverID,
		Trigger: in.trigger, Status: StatusQueued, Mode: in.cfg.Mode, Volumes: volumeNames(in.vols), ActorLabel: in.actor,
	})
	if err != nil {
		return b, fmt.Errorf("insert volume backup: %w", err)
	}
	agentRepo, err := s.volumeRepo(ctx, in.dest, repo.RepoPath)
	if err != nil {
		return b, err
	}
	payload, err := json.Marshal(buildVolumeBackupPayload(in, b.ID, agentRepo))
	if err != nil {
		return b, err
	}
	cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
		ServerID: serverID, Kind: agentprotocol.KindVolumeBackup, Payload: payload, IdempotencyKey: "volume_backup:" + b.ID.String(),
	})
	if err != nil {
		return b, fmt.Errorf("enqueue volume_backup: %w", err)
	}
	if err := q.SetVolumeBackupCommand(ctx, db.SetVolumeBackupCommandParams{ID: b.ID, CommandID: &cmdID}); err != nil {
		return b, err
	}
	b.CommandID = &cmdID
	return b, nil
}

// insertVolumeSkipped records a backup that was not attempted.
func (s *Service) insertVolumeSkipped(ctx context.Context, q *db.Queries, in volumeStart, reason string) (db.VolumeBackup, error) {
	now := s.now()
	return q.InsertVolumeBackup(ctx, db.InsertVolumeBackupParams{
		WorkspaceID: in.sc.WorkspaceID, ServiceID: in.sc.Service.ID, DestinationID: in.dest.ID, ServerID: in.sc.Service.ServerID,
		Trigger: in.trigger, Status: StatusSkipped, Mode: in.cfg.Mode, Volumes: volumeNames(in.vols), Error: reason, ActorLabel: in.actor,
		FinishedAt: &now,
	})
}

// CreateVolumeBackup starts an on-demand backup with the saved configuration.
func (s *Service) CreateVolumeBackup(ctx context.Context, serviceID uuid.UUID) (VolumeBackupView, error) {
	sc, vols, err := s.volumeScope(ctx, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return VolumeBackupView{}, err
	}
	if err := requireVolumeEligible(sc, vols); err != nil {
		return VolumeBackupView{}, err
	}
	cfg, err := s.loadVolumeConfig(ctx, s.q, serviceID)
	if err != nil {
		return VolumeBackupView{}, err
	}
	if cfg == nil {
		return VolumeBackupView{}, apierr.Unprocessable(codeVolumeUnavailable, "save a backup configuration (destination) first")
	}
	dest, err := s.destination(ctx, s.q, sc.WorkspaceID, cfg.DestinationID)
	if err != nil {
		return VolumeBackupView{}, err
	}
	name, online, err := s.serverState(ctx, s.q, sc.Service.ServerID)
	if err != nil {
		return VolumeBackupView{}, err
	}
	if !online {
		return VolumeBackupView{}, apierr.Conflict("server_offline", "the server "+name+" is offline; the backup was not started")
	}
	var b db.VolumeBackup
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		b, err = s.startVolumeBackup(ctx, tx, volumeStart{sc: sc, vols: vols, cfg: *cfg, dest: dest, trigger: TriggerManual, actor: actorLabel(ctx)})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeCreate, ResourceType: "volume_backup", ResourceID: b.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "destination": dest.Name, "trigger": TriggerManual, "mode": cfg.Mode},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return VolumeBackupView{}, err
		}
		return VolumeBackupView{}, fmt.Errorf("create volume backup: %w", err)
	}
	s.publish(sc, EventBackupUpdated, b.ID)
	return volumeBackupView(b, StatusQueued, dest.Name, dest.Kind), nil
}

// ListVolumeBackups returns a service's volume backups, newest first.
func (s *Service) ListVolumeBackups(ctx context.Context, serviceID uuid.UUID, limit int) ([]VolumeBackupView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.ListServiceVolumeBackups(ctx, db.ListServiceVolumeBackupsParams{ServiceID: sc.Service.ID, Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list volume backups: %w", err)
	}
	out := make([]VolumeBackupView, len(rows))
	for i, r := range rows {
		out[i] = volumeBackupView(r.VolumeBackup, r.EffectiveStatus, r.DestinationName, r.DestinationKind)
	}
	return out, nil
}

// GetVolumeBackup returns one volume backup.
func (s *Service) GetVolumeBackup(ctx context.Context, id uuid.UUID) (VolumeBackupView, error) {
	b, _, err := s.volumeBackupScope(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return VolumeBackupView{}, err
	}
	r, err := s.q.GetVolumeBackupView(ctx, db.GetVolumeBackupViewParams{ID: b.ID, WorkspaceID: b.WorkspaceID})
	if err != nil {
		return VolumeBackupView{}, fmt.Errorf("get volume backup: %w", err)
	}
	return volumeBackupView(r.VolumeBackup, r.EffectiveStatus, r.DestinationName, r.DestinationKind), nil
}

// SetVolumeBackupLocked protects a backup from retention (or lifts the protection).
func (s *Service) SetVolumeBackupLocked(ctx context.Context, id uuid.UUID, locked bool) (VolumeBackupView, error) {
	b, sc, err := s.volumeBackupScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return VolumeBackupView{}, err
	}
	if !locked {
		if err := sc.Guard(ctx, guardUnlockVolumeBackup, fmt.Sprintf("Unlock a volume backup of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
			[]string{"The snapshot can then be deleted by retention or by hand."}, id, locked); err != nil {
			return VolumeBackupView{}, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).SetVolumeBackupLocked(ctx, db.SetVolumeBackupLockedParams{ID: id, WorkspaceID: b.WorkspaceID, Locked: locked}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeLock, ResourceType: "volume_backup", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "locked": locked},
		})
	})
	if err != nil {
		return VolumeBackupView{}, fmt.Errorf("lock volume backup: %w", err)
	}
	s.publish(sc, EventBackupUpdated, id)
	return s.GetVolumeBackup(ctx, id)
}

// DeleteVolumeBackup removes a backup. Its snapshot is forgotten by the agent
// first; the row disappears once the agent confirmed (or immediately when
// there is no snapshot).
func (s *Service) DeleteVolumeBackup(ctx context.Context, id uuid.UUID) error {
	b, sc, err := s.volumeBackupScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return err
	}
	if b.Locked {
		return apierr.Conflict("backup_locked", "the backup is locked; unlock it before deleting")
	}
	if b.Status == StatusQueued {
		return apierr.Conflict("backup_in_progress", "the backup is still running")
	}
	if active, err := s.q.HasActiveVolumeRestore(ctx, b.ServiceID); err != nil {
		return fmt.Errorf("check restores: %w", err)
	} else if active {
		return apierr.Conflict("backup_in_progress", "a restore of this service is running; wait for it to finish")
	}
	if err := sc.Guard(ctx, guardDeleteVolumeBackup, fmt.Sprintf("Delete a volume backup of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), true,
		[]string{"The snapshot is removed from its destination."}, id); err != nil {
		return err
	}
	hasObject := b.Status == StatusSucceeded && b.ObjectDeletedAt == nil && b.SnapshotID != ""
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if hasObject {
			if err := s.enqueueVolumeDeletion(ctx, tx, b); err != nil {
				return err
			}
		} else if err := q.DeleteVolumeBackupsByIDs(ctx, []uuid.UUID{b.ID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeDelete, ResourceType: "volume_backup", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "snapshot": b.SnapshotID},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete volume backup: %w", err)
	}
	s.publish(sc, EventBackupUpdated, id)
	return nil
}

// enqueueVolumeDeletion marks a backup's snapshot as being deleted and
// enqueues the volume_forget command that removes it. The backup's server must
// be online.
func (s *Service) enqueueVolumeDeletion(ctx context.Context, tx pgx.Tx, b db.VolumeBackup) error {
	q := db.New(tx)
	if b.ServerID == nil {
		return apierr.Conflict("server_unavailable", "the server that holds this backup no longer exists")
	}
	name, online, err := s.serverState(ctx, q, b.ServerID)
	if err != nil {
		return err
	}
	if !online {
		return apierr.Conflict("server_offline", "the server "+name+" is offline; the stored backup can not be deleted now")
	}
	dest, err := q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return fmt.Errorf("load destination: %w", err)
	}
	repo, err := q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: b.ServiceID, DestinationID: b.DestinationID})
	if err != nil {
		return fmt.Errorf("load repository: %w", err)
	}
	agentRepo, err := s.volumeRepo(ctx, dest, repo.RepoPath)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(agentprotocol.VolumeForgetPayload{
		ServiceID: b.ServiceID.String(), Repo: agentRepo,
		SnapshotIDs: []string{b.SnapshotID}, BackupIDs: []string{b.ID.String()},
	})
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: *b.ServerID, Kind: agentprotocol.KindVolumeForget, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue volume_forget: %w", err)
	}
	return q.MarkVolumeBackupsObjectDeleted(ctx, []uuid.UUID{b.ID})
}
