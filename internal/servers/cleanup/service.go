// Package cleanup is the control-plane side of the scheduled Docker cleanup of
// a server.
//
// Every server has a cleanup setting (on by default). A sweep job sends a
// docker_cleanup command to each online server once a day (04:00 UTC) and, when
// the server's disk alert is active, an emergency run (at most one per
// EmergencyCooldown). "Run cleanup now" sends the same command. The command
// carries the set of images the agent must never delete: the current release of
// every service on the server, the releases of its in-flight deployments and
// its newest imageRetention succeeded releases (the rollback candidates). The
// agent adds the pinned infrastructure images and refuses to prune without the
// set. Volumes are never part of a run; removing unused volumes is a separate,
// confirmed action.
package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Schedule and limits.
const (
	// DefaultSchedule is the cron expression (UTC) of a server that never
	// changed it: daily at 04:00.
	DefaultSchedule = "0 4 * * *"
	// MinScheduleGap is the least time between two firings of a schedule. The
	// sweep looks every SweepInterval, and a cleanup walks the whole image
	// list, so a schedule denser than this is refused.
	MinScheduleGap = 15 * time.Minute
	// Limits of the editable ages.
	MinUnusedImageHours  = 1
	MaxUnusedImageHours  = 8760 // one year
	DefaultBuildCacheDay = 7
	MinBuildCacheDays    = 1
	MaxBuildCacheDays    = 365
	// MinDiskTriggerPercent and MaxDiskTriggerPercent bound the custom disk
	// trigger, like the server's disk alert threshold.
	MinDiskTriggerPercent = 50
	MaxDiskTriggerPercent = 99
	// SweepInterval is how often the sweep looks for due and emergency runs.
	SweepInterval = 5 * time.Minute
	// EmergencyCooldown is the least time between two runs started by the disk
	// alert (or after any run): a cleanup that cannot free enough must not loop.
	EmergencyCooldown = 6 * time.Hour
	// HistoryKept is how many runs of a server are remembered.
	HistoryKept = 30
	// NotifyReclaimedBytes is the amount of reclaimed space above which a run
	// is worth a notification.
	NotifyReclaimedBytes = int64(1) << 30
	// RecentRuns is how many runs the server view lists.
	RecentRuns = 10
)

// Actions recorded in the audit log.
const (
	ActionRun        = "server.cleanup"        // a run finished (system)
	ActionStart      = "server.cleanup.start"  // a person started a run
	ActionSettings   = "server.cleanup.update" // settings changed
	ActionVolumesRun = "server.cleanup.volumes"
)

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Jobs       *jobs.Manager
	Notifier   notifications.Emitter
	Logger     *slog.Logger
}

// Service implements the cleanup use cases and the hooks of the agents hub.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	disp agents.Dispatcher
	jm   *jobs.Manager
	not  notifications.Emitter
	log  *slog.Logger
	now  func() time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, disp: d.Dispatcher, jm: d.Jobs, not: d.Notifier,
		log: d.Logger.With("component", "docker-cleanup"), now: time.Now,
	}
}

// SupportsCleanup reports whether the server's agent advertised
// agentprotocol.CapDockerCleanup. A server that has never connected has no
// facts yet and is assumed to enroll with a current agent.
func SupportsCleanup(facts json.RawMessage) bool {
	if len(facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapDockerCleanup)
}

// ---- views ----

// CleanupRun is one cleanup run.
type CleanupRun struct {
	ID             uuid.UUID     `json:"id"`
	Mode           string        `json:"mode" enum:"standard,volumes" doc:"standard is the scheduled cleanup; volumes the explicit removal of unused volumes"`
	Trigger        string        `json:"trigger" enum:"scheduled,emergency,manual"`
	Status         string        `json:"status" enum:"pending,succeeded,failed"`
	ReclaimedBytes int64         `json:"reclaimedBytes"`
	Items          int           `json:"items" doc:"Things removed: containers, images, networks or volumes"`
	Error          string        `json:"error,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
	FinishedAt     *time.Time    `json:"finishedAt"`
	Steps          []CleanupStep `json:"steps" nullable:"false" doc:"What each prune step did; empty until the agent reports"`
}

// CleanupStep is one prune step of a run.
type CleanupStep struct {
	Name           string   `json:"name" enum:"containers_unmanaged,images_dangling,images_unused,build_cache,networks,volumes"`
	Items          int      `json:"items"`
	ReclaimedBytes int64    `json:"reclaimedBytes"`
	Protected      int      `json:"protected" doc:"Images kept because a release or the infrastructure needs them"`
	Removed        []string `json:"removed" nullable:"false" doc:"Names of removed things (the first few)"`
	Error          string   `json:"error,omitempty"`
	Skipped        string   `json:"skipped,omitempty" doc:"Why the step did not run"`
}

// CleanupView is the cleanup state of a server.
type CleanupView struct {
	Enabled                   bool         `json:"enabled" doc:"Scheduled and emergency cleanups run (default true)"`
	RemoveStoppedUnmanaged    bool         `json:"removeStoppedUnmanaged" doc:"Also remove stopped containers NebulaCtrl does not manage (default false)"`
	Cron                      string       `json:"cron" doc:"The schedule as a 5-field cron expression, evaluated in UTC (default 0 4 * * *)"`
	Schedule                  string       `json:"schedule" doc:"The schedule in words"`
	UnusedImageMinAgeHours    int          `json:"unusedImageMinAgeHours" doc:"Unused images older than this are removed unless a release or the infrastructure needs them (default 72)"`
	BuildCacheMinAgeDays      int          `json:"buildCacheMinAgeDays" doc:"Build cache older than this many days is pruned (default 7)"`
	DiskTriggerPercent        *int         `json:"diskTriggerPercent" nullable:"true" doc:"Disk usage (50-99) that starts an emergency cleanup; null follows the server's disk alert threshold"`
	DiskAlertThresholdPercent int          `json:"diskAlertThresholdPercent" doc:"The server's disk alert threshold, which starts the emergency cleanup when diskTriggerPercent is null"`
	DiskAlert                 bool         `json:"diskAlert" doc:"The disk alert is active now"`
	AgentSupported            bool         `json:"agentSupported" doc:"The server's agent can run a cleanup"`
	Running                   bool         `json:"running" doc:"A cleanup is waiting for the agent or running"`
	NextRunAt                 *time.Time   `json:"nextRunAt" doc:"The next scheduled cleanup; null when cleanup is off"`
	Runs                      []CleanupRun `json:"runs" nullable:"false" doc:"The most recent runs, newest first"`
}

func runView(r db.ServerCleanupRun) CleanupRun {
	v := CleanupRun{
		ID: r.ID, Mode: r.Mode, Trigger: r.Trigger, Status: r.Status, ReclaimedBytes: r.ReclaimedBytes,
		Items: int(r.Items), Error: r.Error, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Steps: []CleanupStep{},
	}
	var rep agentprotocol.CleanupResult
	if len(r.Report) > 0 && json.Unmarshal(r.Report, &rep) == nil {
		for _, s := range rep.Steps {
			removed := s.Removed
			if removed == nil {
				removed = []string{}
			}
			v.Steps = append(v.Steps, CleanupStep{
				Name: s.Name, Items: s.Items, ReclaimedBytes: s.ReclaimedBytes, Protected: s.Protected,
				Removed: removed, Error: s.Error, Skipped: s.Skipped,
			})
		}
	}
	return v
}

var dailyRe = regexp.MustCompile(`^(\d{1,2}) (\d{1,2}) \* \* \*$`)

// ScheduleText describes a cron expression for the view.
func ScheduleText(cron string) string {
	cron = strings.Join(strings.Fields(cron), " ")
	if m := dailyRe.FindStringSubmatch(cron); m != nil {
		min, _ := strconv.Atoi(m[1])
		hour, _ := strconv.Atoi(m[2])
		return fmt.Sprintf("Daily at %02d:%02d UTC", hour, min)
	}
	return "Cron " + cron + " (UTC)"
}

// ParseSchedule parses a cleanup schedule and refuses one that fires more
// often than MinScheduleGap.
func ParseSchedule(cron string) (*cronexpr.Schedule, error) {
	sc, err := cronexpr.Parse(cron)
	if err != nil {
		return nil, err
	}
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 64 {
		next := sc.Next(t)
		if next.IsZero() {
			return nil, errors.New("cron: the schedule never fires")
		}
		after := sc.Next(next)
		if after.IsZero() {
			break
		}
		if after.Sub(next) < MinScheduleGap {
			return nil, fmt.Errorf("cron: runs must be at least %d minutes apart", int(MinScheduleGap/time.Minute))
		}
		t = next
	}
	return sc, nil
}

// Decide says what the sweep should start for a server: Scheduled once per
// firing of its schedule after ref (the later of the last scheduled run, the
// server's enrollment and the moment the schedule was last changed, so a
// server waits for the next firing instead of catching up on old ones),
// Emergency when the disk is over the trigger and nothing ran within
// EmergencyCooldown, or "" for nothing. lastAny is the start time of the newest
// run of any trigger (nil: none).
func Decide(now, ref time.Time, sched *cronexpr.Schedule, diskHot bool, lastAny *time.Time) string {
	if sched != nil {
		if next := sched.Next(ref.UTC()); !next.IsZero() && !next.After(now) {
			return agentprotocol.CleanupTriggerScheduled
		}
	}
	if diskHot && (lastAny == nil || now.Sub(*lastAny) >= EmergencyCooldown) {
		return agentprotocol.CleanupTriggerEmergency
	}
	return ""
}

// DiskHot says whether the server's disk is past the emergency trigger: the
// custom percentage against the newest heartbeat, or the disk alert flag when
// no custom trigger is set.
func DiskHot(alertActive bool, trigger *int32, used, total int64) bool {
	if trigger == nil {
		return alertActive
	}
	return total > 0 && used*100 >= total*int64(*trigger)
}

// ---- settings ----

// Settings are the editable cleanup settings. A zero Cron, UnusedImageMinAgeHours,
// BuildCacheMinAgeDays or DiskTriggerPercent means the default (the disk trigger
// then follows the server's disk alert threshold).
type Settings struct {
	Enabled                bool
	RemoveStoppedUnmanaged bool
	Cron                   string
	UnusedImageMinAgeHours int
	BuildCacheMinAgeDays   int
	DiskTriggerPercent     int
}

// Normalize fills the defaults and validates every field (422 on a bad one).
func (in Settings) Normalize() (Settings, error) {
	bad := func(format string, a ...any) (Settings, error) {
		return Settings{}, apierr.Unprocessable("validation_failed", fmt.Sprintf(format, a...))
	}
	in.Cron = strings.Join(strings.Fields(in.Cron), " ")
	if in.Cron == "" {
		in.Cron = DefaultSchedule
	}
	if _, err := ParseSchedule(in.Cron); err != nil {
		return bad("schedule: %s", strings.TrimPrefix(err.Error(), "cron: "))
	}
	if in.UnusedImageMinAgeHours == 0 {
		in.UnusedImageMinAgeHours = agentprotocol.DefaultUnusedImageMinAgeHours
	}
	if in.UnusedImageMinAgeHours < MinUnusedImageHours || in.UnusedImageMinAgeHours > MaxUnusedImageHours {
		return bad("unusedImageMinAgeHours must be %d-%d", MinUnusedImageHours, MaxUnusedImageHours)
	}
	if in.BuildCacheMinAgeDays == 0 {
		in.BuildCacheMinAgeDays = DefaultBuildCacheDay
	}
	if in.BuildCacheMinAgeDays < MinBuildCacheDays || in.BuildCacheMinAgeDays > MaxBuildCacheDays {
		return bad("buildCacheMinAgeDays must be %d-%d", MinBuildCacheDays, MaxBuildCacheDays)
	}
	if in.DiskTriggerPercent != 0 && (in.DiskTriggerPercent < MinDiskTriggerPercent || in.DiskTriggerPercent > MaxDiskTriggerPercent) {
		return bad("diskTriggerPercent must be %d-%d", MinDiskTriggerPercent, MaxDiskTriggerPercent)
	}
	return in, nil
}

func defaultSettings() Settings {
	return Settings{
		Enabled: true, Cron: DefaultSchedule, UnusedImageMinAgeHours: agentprotocol.DefaultUnusedImageMinAgeHours,
		BuildCacheMinAgeDays: DefaultBuildCacheDay,
	}
}

func (s *Service) publish(serverID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

func (s *Service) loadServer(ctx context.Context, scope string, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Server{}, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	return row, nil
}

func (s *Service) settings(ctx context.Context, q *db.Queries, serverID uuid.UUID) (Settings, error) {
	row, err := q.GetServerCleanupSettings(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get cleanup settings: %w", err)
	}
	set := Settings{
		Enabled: row.Enabled, RemoveStoppedUnmanaged: row.RemoveUnmanagedContainers, Cron: row.Schedule,
		UnusedImageMinAgeHours: int(row.UnusedImageMinAgeHours), BuildCacheMinAgeDays: int(row.BuildCacheMinAgeDays),
	}
	if row.DiskTriggerPercent != nil {
		set.DiskTriggerPercent = int(*row.DiskTriggerPercent)
	}
	return set, nil
}

// Get returns the cleanup state of a server.
func (s *Service) Get(ctx context.Context, serverID uuid.UUID) (CleanupView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersRead, serverID)
	if err != nil {
		return CleanupView{}, err
	}
	return s.view(ctx, srv)
}

func (s *Service) view(ctx context.Context, srv db.Server) (CleanupView, error) {
	set, err := s.settings(ctx, s.q, srv.ID)
	if err != nil {
		return CleanupView{}, err
	}
	runs, err := s.q.ListServerCleanupRuns(ctx, db.ListServerCleanupRunsParams{ServerID: srv.ID, Limit: RecentRuns})
	if err != nil {
		return CleanupView{}, fmt.Errorf("list cleanup runs: %w", err)
	}
	pending, err := s.q.HasPendingServerCleanupRun(ctx, srv.ID)
	if err != nil {
		return CleanupView{}, fmt.Errorf("check pending cleanup: %w", err)
	}
	v := CleanupView{
		Enabled: set.Enabled, RemoveStoppedUnmanaged: set.RemoveStoppedUnmanaged, Cron: set.Cron, Schedule: ScheduleText(set.Cron),
		UnusedImageMinAgeHours: set.UnusedImageMinAgeHours, BuildCacheMinAgeDays: set.BuildCacheMinAgeDays,
		DiskAlertThresholdPercent: int(srv.DiskAlertThresholdPercent), DiskAlert: srv.DiskAlertActive,
		AgentSupported: SupportsCleanup(srv.Facts), Running: pending, Runs: make([]CleanupRun, 0, len(runs)),
	}
	if set.DiskTriggerPercent != 0 {
		pct := set.DiskTriggerPercent
		v.DiskTriggerPercent = &pct
	}
	if sc, err := cronexpr.Parse(set.Cron); set.Enabled && err == nil {
		if next := sc.Next(s.now().UTC()); !next.IsZero() {
			v.NextRunAt = &next
		}
	}
	for _, r := range runs {
		v.Runs = append(v.Runs, runView(r))
	}
	return v, nil
}

// Update changes the cleanup settings of a server.
func (s *Service) Update(ctx context.Context, serverID uuid.UUID, in Settings) (CleanupView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return CleanupView{}, err
	}
	in, err = in.Normalize()
	if err != nil {
		return CleanupView{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var trigger *int32
		if in.DiskTriggerPercent != 0 {
			v := int32(in.DiskTriggerPercent)
			trigger = &v
		}
		if _, err := db.New(tx).UpsertServerCleanupSettings(ctx, db.UpsertServerCleanupSettingsParams{
			ServerID: serverID, WorkspaceID: srv.WorkspaceID, Enabled: in.Enabled, RemoveUnmanagedContainers: in.RemoveStoppedUnmanaged,
			Schedule: in.Cron, UnusedImageMinAgeHours: int32(in.UnusedImageMinAgeHours), BuildCacheMinAgeDays: int32(in.BuildCacheMinAgeDays),
			DiskTriggerPercent: trigger,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSettings, ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{
				"server": srv.Name, "enabled": in.Enabled, "removeStoppedUnmanaged": in.RemoveStoppedUnmanaged,
				"cron": in.Cron, "unusedImageMinAgeHours": in.UnusedImageMinAgeHours, "buildCacheMinAgeDays": in.BuildCacheMinAgeDays,
				"diskTriggerPercent": in.DiskTriggerPercent,
			},
		})
	})
	if err != nil {
		return CleanupView{}, fmt.Errorf("update cleanup settings: %w", err)
	}
	s.publish(serverID)
	return s.view(ctx, srv)
}

// ---- runs ----

// RunNow starts a cleanup of the server ("Run cleanup now"). It works whether
// or not the schedule is on.
func (s *Service) RunNow(ctx context.Context, serverID uuid.UUID) (CleanupRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return CleanupRun{}, err
	}
	if err := s.canRun(ctx, srv); err != nil {
		return CleanupRun{}, err
	}
	run, err := s.start(ctx, srv, agentprotocol.CleanupModeStandard, agentprotocol.CleanupTriggerManual, principalUser(ctx))
	if err != nil {
		return CleanupRun{}, err
	}
	return runView(run), nil
}

// PruneVolumes starts the explicit removal of unused volumes NebulaCtrl does
// not manage. confirm must be the server's name. The schedule never does this.
// It deletes data, so it needs the Owner or Admin role on top of the scope.
func (s *Service) PruneVolumes(ctx context.Context, serverID uuid.UUID, confirm string) (CleanupRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return CleanupRun{}, err
	}
	if err := policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin); err != nil {
		return CleanupRun{}, err
	}
	if strings.TrimSpace(confirm) != srv.Name {
		return CleanupRun{}, apierr.BadRequest("confirmation_mismatch", "type the server's name ("+srv.Name+") to confirm")
	}
	if err := s.canRun(ctx, srv); err != nil {
		return CleanupRun{}, err
	}
	run, err := s.start(ctx, srv, agentprotocol.CleanupModeVolumes, agentprotocol.CleanupTriggerManual, principalUser(ctx))
	if err != nil {
		return CleanupRun{}, err
	}
	return runView(run), nil
}

func principalUser(ctx context.Context) *uuid.UUID {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

func (s *Service) canRun(ctx context.Context, srv db.Server) error {
	switch {
	case srv.Status == "revoked":
		return apierr.Unprocessable("server_revoked", "the server was revoked")
	case srv.Status != "online":
		return apierr.Conflict("server_offline", srv.Name+" is offline. Run the cleanup when it is back online.")
	case !SupportsCleanup(srv.Facts):
		return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old to run a cleanup; update the agent first")
	}
	pending, err := s.q.HasPendingServerCleanupRun(ctx, srv.ID)
	if err != nil {
		return fmt.Errorf("check pending cleanup: %w", err)
	}
	if pending {
		return apierr.Conflict("cleanup_running", "a cleanup of "+srv.Name+" is already running")
	}
	return nil
}

// start records a run and queues its command in one transaction.
func (s *Service) start(ctx context.Context, srv db.Server, mode, trigger string, by *uuid.UUID) (db.ServerCleanupRun, error) {
	var run db.ServerCleanupRun
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		set, err := s.settings(ctx, q, srv.ID)
		if err != nil {
			return err
		}
		prot, err := s.Protected(ctx, q, srv.ID)
		if err != nil {
			return err
		}
		if run, err = q.InsertServerCleanupRun(ctx, db.InsertServerCleanupRunParams{
			ServerID: srv.ID, WorkspaceID: srv.WorkspaceID, Mode: mode, Trigger: trigger, RequestedBy: by,
		}); err != nil {
			return fmt.Errorf("insert cleanup run: %w", err)
		}
		raw, err := json.Marshal(agentprotocol.CleanupPayload{
			RunID: run.ID.String(), Mode: mode, Trigger: trigger,
			UnusedImageMinAgeHours: set.UnusedImageMinAgeHours, BuildCacheMinAgeHours: set.BuildCacheMinAgeDays * 24,
			RemoveStoppedUnmanaged: set.RemoveStoppedUnmanaged && mode == agentprotocol.CleanupModeStandard,
			Protected:              &prot,
		})
		if err != nil {
			return err
		}
		cid, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: srv.ID, Kind: agentprotocol.KindDockerCleanup, Payload: raw, IdempotencyKey: "docker_cleanup:" + run.ID.String(),
		})
		if err != nil {
			return fmt.Errorf("enqueue docker_cleanup: %w", err)
		}
		if err := q.SetServerCleanupRunCommand(ctx, db.SetServerCleanupRunCommandParams{ID: run.ID, CommandID: &cid}); err != nil {
			return err
		}
		run.CommandID = &cid
		if by == nil {
			return nil // scheduled and emergency runs are audited when they finish
		}
		action := ActionStart
		if mode == agentprotocol.CleanupModeVolumes {
			action = ActionVolumesRun
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "run": run.ID.String(), "mode": mode},
		})
	})
	if err != nil {
		return db.ServerCleanupRun{}, err
	}
	s.publish(srv.ID)
	return run, nil
}

// releaseSource is the part of a release's source the protected set needs.
type releaseSource struct {
	Type    string `json:"type"`
	Image   string `json:"image"`
	Digest  string `json:"digest"`
	ImageID string `json:"imageId"`
	// RegistryRef is repository@sha256:… of an image a build server pushed; the
	// target pulled exactly this digest.
	RegistryRef string `json:"registryRef"`
}

// Protected computes the images and volumes a cleanup of the server must keep
// (see the package comment).
func (s *Service) Protected(ctx context.Context, q *db.Queries, serverID uuid.UUID) (agentprotocol.CleanupProtected, error) {
	rels, err := q.ListCleanupProtectedReleases(ctx, &serverID)
	if err != nil {
		return agentprotocol.CleanupProtected{}, fmt.Errorf("list protected releases: %w", err)
	}
	refs, ids := map[string]struct{}{}, map[string]struct{}{}
	for _, r := range rels {
		var src releaseSource
		_ = json.Unmarshal(r.Source, &src)
		for _, ref := range []string{src.Image, src.Digest, src.RegistryRef, r.ImageRef} {
			if ref = strings.TrimSpace(ref); ref != "" {
				refs[ref] = struct{}{}
			}
		}
		if src.ImageID != "" {
			ids[src.ImageID] = struct{}{}
		}
	}
	vols, err := q.ListCleanupProtectedVolumes(ctx, &serverID)
	if err != nil {
		return agentprotocol.CleanupProtected{}, fmt.Errorf("list protected volumes: %w", err)
	}
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		if v.DockerName != "" {
			names = append(names, v.DockerName)
			continue
		}
		names = append(names, agentprotocol.VolumeName(v.ServiceID.String(), v.Name))
	}
	return agentprotocol.CleanupProtected{Images: sortedKeys(refs), ImageIDs: sortedKeys(ids), Volumes: names}, nil
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- sweep ----

// Sweep starts the scheduled and emergency cleanups that are due.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.q.ListServersDueForCleanup(ctx)
	if err != nil {
		return fmt.Errorf("list servers for cleanup: %w", err)
	}
	now := s.now()
	for _, r := range rows {
		if !SupportsCleanup(r.Facts) {
			continue
		}
		pending, err := s.q.HasPendingServerCleanupRun(ctx, r.ID)
		if err != nil || pending {
			if err != nil {
				s.log.WarnContext(ctx, "cleanup sweep: check pending", "server_id", r.ID, "err", err)
			}
			continue
		}
		lastSched, err := s.lastRun(ctx, r.ID, agentprotocol.CleanupTriggerScheduled)
		if err != nil {
			s.log.WarnContext(ctx, "cleanup sweep: last scheduled run", "server_id", r.ID, "err", err)
			continue
		}
		lastAny, err := s.lastRun(ctx, r.ID, agentprotocol.CleanupTriggerScheduled, agentprotocol.CleanupTriggerEmergency, agentprotocol.CleanupTriggerManual)
		if err != nil {
			s.log.WarnContext(ctx, "cleanup sweep: last run", "server_id", r.ID, "err", err)
			continue
		}
		sched, err := cronexpr.Parse(r.Schedule)
		if err != nil {
			s.log.WarnContext(ctx, "cleanup sweep: invalid schedule, using the default", "server_id", r.ID, "schedule", r.Schedule, "err", err)
			sched = cronexpr.MustParse(DefaultSchedule)
		}
		ref := r.ScheduleChangedAt
		if lastSched != nil && lastSched.After(ref) {
			ref = *lastSched
		}
		if r.CreatedAt.After(ref) {
			ref = r.CreatedAt
		}
		hot := DiskHot(r.DiskAlertActive, r.DiskTriggerPercent, r.DiskUsedBytes, r.DiskTotalBytes)
		trigger := Decide(now, ref, sched, hot, lastAny)
		if trigger == "" {
			continue
		}
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: r.ID})
		if err != nil {
			s.log.WarnContext(ctx, "cleanup sweep: load server", "server_id", r.ID, "err", err)
			continue
		}
		if _, err := s.start(ctx, srv, agentprotocol.CleanupModeStandard, trigger, nil); err != nil {
			s.log.WarnContext(ctx, "cleanup sweep: start", "server_id", r.ID, "trigger", trigger, "err", err)
			continue
		}
		s.log.InfoContext(ctx, "docker cleanup started", "server_id", r.ID, "trigger", trigger)
	}
	return nil
}

func (s *Service) lastRun(ctx context.Context, serverID uuid.UUID, triggers ...string) (*time.Time, error) {
	at, err := s.q.LastServerCleanupRunAt(ctx, db.LastServerCleanupRunAtParams{ServerID: serverID, Triggers: triggers})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// ---- agent hooks ----

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool { return kind == agentprotocol.KindDockerCleanup }

// HandleResult applies the result of a docker_cleanup command. Results of
// other commands, and duplicates, are ignored.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	run, err := s.q.GetServerCleanupRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get cleanup run: %w", err)
	}
	var rep agentprotocol.CleanupResult
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &rep)
	}
	status, msg := "succeeded", ""
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		status, msg = "failed", res.Error
		if msg == "" {
			msg = rep.Message
		}
		if msg == "" {
			msg = "The cleanup failed."
		}
	}
	var report json.RawMessage
	if len(rep.Steps) > 0 {
		report, _ = json.Marshal(rep)
	}
	return s.finish(ctx, run, status, rep.ReclaimedBytes, rep.Items, report, msg)
}

// HandleUndelivered closes the run of a docker_cleanup command that never
// reached its agent (expired or canceled before being acknowledged).
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	run, err := s.q.GetServerCleanupRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get cleanup run: %w", err)
	}
	return s.finish(ctx, run, "failed", 0, 0, nil, msg)
}

func (s *Service) finish(ctx context.Context, run db.ServerCleanupRun, status string, reclaimed int64, items int, report json.RawMessage, msg string) error {
	var done db.ServerCleanupRun
	var finished bool
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		done, err = q.FinishServerCleanupRun(ctx, db.FinishServerCleanupRunParams{
			ID: run.ID, Status: status, ReclaimedBytes: reclaimed, Items: int32(items), Report: report, Error: msg,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a duplicate result
		}
		if err != nil {
			return err
		}
		finished = true
		if err := q.PruneServerCleanupRuns(ctx, db.PruneServerCleanupRunsParams{ServerID: run.ServerID, Keep: HistoryKept}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRun, ResourceType: "server", ResourceID: run.ServerID.String(), WorkspaceID: run.WorkspaceID,
			Metadata: map[string]any{
				"run": run.ID.String(), "mode": run.Mode, "trigger": run.Trigger, "status": status,
				"items": items, "reclaimedBytes": reclaimed, "error": msg,
			},
		})
	})
	if err != nil {
		return fmt.Errorf("finish cleanup run: %w", err)
	}
	if !finished {
		return nil
	}
	s.publish(run.ServerID)
	s.notify(ctx, done)
	return nil
}

// notify tells the team about a run that failed, or freed more than 1 GB.
func (s *Service) notify(ctx context.Context, run db.ServerCleanupRun) {
	failed := run.Status != "succeeded"
	if s.not == nil || (!failed && run.ReclaimedBytes <= NotifyReclaimedBytes) {
		return
	}
	ev := notifications.Event{
		Type: notifications.TypeServerCleanup, ServerID: &run.ServerID, WorkspaceID: run.WorkspaceID,
		Fields: map[string]string{
			"Trigger": run.Trigger, "Removed": fmt.Sprintf("%d items", run.Items), "Reclaimed": HumanBytes(run.ReclaimedBytes),
		},
	}
	what := "Docker cleanup"
	if run.Mode == agentprotocol.CleanupModeVolumes {
		what = "Unused volume cleanup"
	}
	if failed {
		ev.Severity = notifications.SeverityError
		ev.Title = what + " failed"
		ev.Body = run.Error
	} else {
		ev.Severity = notifications.SeverityInfo
		ev.Title = fmt.Sprintf("%s freed %s", what, HumanBytes(run.ReclaimedBytes))
		ev.Body = fmt.Sprintf("Removed %d items from the server's Docker data.", run.Items)
	}
	notifications.Emit(ctx, s.not, ev)
}

// HumanBytes formats n with binary units ("1.5 GiB").
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
