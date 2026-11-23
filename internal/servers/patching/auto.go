package patching

// Automatic updates: every night, inside a maintenance window the person chose,
// the control plane starts an `apply` run (trigger "scheduled") on each server
// that opted in. The agent re-checks first and leaves Docker and containerd
// alone (hostpatch.BuiltinHolds), so what it upgrades is the safe stream; the
// disruptive packages stay in the manual Apply flow.
//
// The decisions are plain functions of the clock and the settings (dueSlot,
// nextSlotStart, quietestStart) so that the window, the jitter and daylight
// saving time can be tested without a database; the Service methods add the
// state of the instance (a run in flight, a deployment or backup on the server,
// another scheduled upgrade anywhere).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/hostpatch"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ActionAuto is recorded in the audit log when a person (or, after too many
// failures, the system) changes the automatic updates of a server.
const ActionAuto = "server.patches.auto"

// Scopes of automatic updates.
const (
	ScopeSecurity = "security"
	ScopeAll      = "all"
)

// Reboot policies of automatic updates.
const (
	RebootNever        = "never"
	RebootWhenRequired = "whenRequired"
)

// Defaults and limits of the automatic updates.
const (
	DefaultWindowStart   = "03:00"
	DefaultWindowMinutes = 120
	MinWindowMinutes     = 60
	MaxWindowMinutes     = 240
	DefaultTimezone      = "UTC"
	// MaxHoldPackages bounds the extra held packages of a server.
	MaxHoldPackages = agentprotocol.MaxPatchHolds
	// AutoSweepInterval is how often the sweep looks for servers due their upgrade.
	AutoSweepInterval = 5 * time.Minute
	// MaxAutoFailures is how many scheduled runs in a row may fail before the
	// schedule is switched off.
	MaxAutoFailures = 3

	// maxJitter bounds the stable per-server offset into the window.
	maxJitter = 30 * time.Minute
	// lastStartMargin is the time that must remain in the window to start an
	// upgrade: nothing starts in the last minutes, and nothing running is killed.
	lastStartMargin = 15 * time.Minute
	// rebootMargin is the time that must remain in the window to reboot after an upgrade.
	rebootMargin = 10 * time.Minute
	// freshCheck is how old the last check may be for "nothing to upgrade" to be
	// believed without asking the host.
	freshCheck = 6 * time.Hour
	// nativeRetry is the least time between two attempts to hand the host's own
	// automatic updates over (or back).
	nativeRetry = 30 * time.Minute
	// failureReasonLen clips the error kept in a disabled reason and a notification.
	failureReasonLen = 300
)

// ---- settings ----

// AutoSettings are the automatic update settings of a server.
type AutoSettings struct {
	Enabled       bool
	Scope         string
	WindowStart   string // "HH:MM"
	WindowMinutes int
	Timezone      string // IANA name
	Reboot        string
	HoldPackages  []string
}

// DefaultAutoSettings are the settings of a server without a row: off, nightly
// security updates at 03:00 UTC for two hours, no reboot.
func DefaultAutoSettings() AutoSettings {
	return AutoSettings{
		Scope: ScopeSecurity, WindowStart: DefaultWindowStart, WindowMinutes: DefaultWindowMinutes,
		Timezone: DefaultTimezone, Reboot: RebootNever, HoldPackages: []string{},
	}
}

func settingsOf(r db.ServerPatchAutoSetting) AutoSettings {
	hold := r.HoldPackages
	if hold == nil {
		hold = []string{}
	}
	return AutoSettings{
		Enabled: r.Enabled, Scope: r.Scope, WindowStart: r.WindowStart, WindowMinutes: int(r.WindowMinutes),
		Timezone: r.Timezone, Reboot: r.Reboot, HoldPackages: hold,
	}
}

// Normalize validates the settings a person submitted and returns them in
// canonical form (422 with a specific code on a bad field). It does not look at
// the server, only at the values.
func (in AutoSettings) Normalize() (AutoSettings, error) {
	in.Scope = strings.TrimSpace(in.Scope)
	if in.Scope == "" {
		in.Scope = ScopeSecurity
	}
	if in.Scope != ScopeSecurity && in.Scope != ScopeAll {
		return AutoSettings{}, apierr.Unprocessable("validation_failed", `scope must be "security" or "all"`)
	}
	in.Reboot = strings.TrimSpace(in.Reboot)
	if in.Reboot == "" {
		in.Reboot = RebootNever
	}
	if in.Reboot != RebootNever && in.Reboot != RebootWhenRequired {
		return AutoSettings{}, apierr.Unprocessable("validation_failed", `reboot must be "never" or "whenRequired"`)
	}
	in.WindowStart = strings.TrimSpace(in.WindowStart)
	if in.WindowStart == "" {
		in.WindowStart = DefaultWindowStart
	}
	if _, _, ok := parseClock(in.WindowStart); !ok {
		return AutoSettings{}, apierr.Unprocessable("invalid_window", "windowStart must be a time of day as HH:MM (24-hour), for example 03:00")
	}
	if in.WindowMinutes < MinWindowMinutes || in.WindowMinutes > MaxWindowMinutes {
		return AutoSettings{}, apierr.Unprocessable("invalid_window",
			fmt.Sprintf("windowMinutes must be between %d and %d", MinWindowMinutes, MaxWindowMinutes))
	}
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.Timezone == "" {
		in.Timezone = DefaultTimezone
	}
	if _, err := cronexpr.LoadLocation(in.Timezone); err != nil {
		return AutoSettings{}, apierr.Unprocessable("invalid_timezone", fmt.Sprintf("%q is not an IANA time zone (for example Europe/Berlin)", in.Timezone))
	}
	hold := make([]string, 0, len(in.HoldPackages))
	for _, h := range in.HoldPackages {
		h = strings.TrimSpace(h)
		if !hostpatch.ValidPackageName(h) {
			return AutoSettings{}, apierr.Unprocessable("invalid_package", fmt.Sprintf("%q is not a valid package name", h))
		}
		if !slices.Contains(hold, h) {
			hold = append(hold, h)
		}
	}
	if len(hold) > MaxHoldPackages {
		return AutoSettings{}, apierr.Unprocessable("invalid_package", fmt.Sprintf("hold at most %d packages", MaxHoldPackages))
	}
	in.HoldPackages = hold
	return in, nil
}

// parseClock reads "HH:MM".
func parseClock(s string) (hour, minute int, ok bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, 0, false
		}
	}
	hour, minute = int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	return hour, minute, hour < 24 && minute < 60
}

// ---- the window ----

// jitter is the stable offset of a server into its window: a hash of its ID
// modulo min(30 min, window/4), so servers of one team do not all start at the
// window's first minute and a server's night is predictable.
func jitter(id uuid.UUID, windowMinutes int) time.Duration {
	n := min(int(maxJitter/time.Minute), windowMinutes/4)
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write(id[:])
	return time.Duration(h.Sum32()%uint32(n)) * time.Minute
}

// slotOn is the window of the local calendar day of t: it starts at the
// configured clock time on that date. One window per date, whatever daylight
// saving time does to the clock time (time.Date picks one instant for a time
// that does not exist or exists twice), so a window is a fire-once slot.
func slotOn(t time.Time, loc *time.Location, hour, minute, windowMinutes int) (start, end time.Time) {
	y, m, d := t.In(loc).Date()
	start = time.Date(y, m, d, hour, minute, 0, 0, loc)
	return start, start.Add(time.Duration(windowMinutes) * time.Minute)
}

// dueSlot returns the start of the window an upgrade of the server may start in
// right now: now lies between the window's start plus the server's jitter and
// the window's end minus lastStartMargin. A window that began before the
// settings were last saved is skipped (the new settings take effect from the
// next night), and nothing catches up after a window has passed.
func dueSlot(now time.Time, id uuid.UUID, c AutoSettings, loc *time.Location, changedAt time.Time) (time.Time, bool) {
	if !c.Enabled {
		return time.Time{}, false
	}
	hour, minute, ok := parseClock(c.WindowStart)
	if !ok {
		return time.Time{}, false
	}
	// A window can span midnight, so today's and yesterday's windows are both candidates.
	for _, back := range []int{0, 1} {
		start, end := slotOn(now.In(loc).AddDate(0, 0, -back), loc, hour, minute, c.WindowMinutes)
		if now.Before(start.Add(jitter(id, c.WindowMinutes))) || now.After(end.Add(-lastStartMargin)) {
			continue
		}
		if changedAt.After(start) {
			return time.Time{}, false
		}
		return start, true
	}
	return time.Time{}, false
}

// nextSlotStart is the start of the first window after now.
func nextSlotStart(now time.Time, loc *time.Location, hour, minute int) time.Time {
	for off := range 3 {
		start, _ := slotOn(now.In(loc).AddDate(0, 0, off), loc, hour, minute, 0)
		if start.After(now) {
			return start
		}
	}
	return time.Time{}
}

// ---- the quiet-hour suggestion ----

// hourlyRequests is one hour of a server's traffic.
type hourlyRequests struct {
	Bucket   time.Time
	Requests int64
}

// minSuggestDays is how many distinct days of traffic the suggestion needs.
const minSuggestDays = 3

// quietestStart is the start of the quietest contiguous window (windowMinutes
// rounded up to whole hours) over the traffic of the last days, in the server's
// time zone, as "HH:00". It never suggests 01:00-02:59 (the hours daylight
// saving time moves) and prefers the hour closest to 03:00 on a tie. Nil when
// there are fewer than minSuggestDays days of data.
func quietestStart(rows []hourlyRequests, loc *time.Location, windowMinutes int) *string {
	var byHour [24]int64
	days := map[string]bool{}
	for _, r := range rows {
		lt := r.Bucket.In(loc)
		days[lt.Format("2006-01-02")] = true
		byHour[lt.Hour()] += r.Requests
	}
	if len(days) < minSuggestDays {
		return nil
	}
	span := (windowMinutes + 59) / 60
	best, bestCost, bestDist := -1, int64(0), 0
	for s := range 24 {
		if s == 1 || s == 2 {
			continue
		}
		var cost int64
		for k := range span {
			cost += byHour[(s+k)%24]
		}
		dist := min((s-3+24)%24, (3-s+24)%24)
		if best < 0 || cost < bestCost || (cost == bestCost && dist < bestDist) {
			best, bestCost, bestDist = s, cost, dist
		}
	}
	out := fmt.Sprintf("%02d:00", best)
	return &out
}

// ---- view ----

// PatchAutoRun is the newest scheduled upgrade of a server.
type PatchAutoRun struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status" enum:"pending,rebooting,succeeded,failed"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// PatchAuto is the automatic updates of a server: its settings and where the
// schedule stands.
type PatchAuto struct {
	Enabled       bool     `json:"enabled"`
	Scope         string   `json:"scope" enum:"security,all" doc:"security: only the packages with a security update (apt and dnf); all: every package"`
	WindowStart   string   `json:"windowStart" doc:"Start of the nightly maintenance window as HH:MM in timezone"`
	WindowMinutes int      `json:"windowMinutes" doc:"Length of the window, 60-240. Nothing starts in its last 15 minutes"`
	Timezone      string   `json:"timezone" doc:"IANA time zone of windowStart"`
	Reboot        string   `json:"reboot" enum:"never,whenRequired" doc:"whenRequired: reboot after an upgrade that asks for it, inside the window, with guards (see the server patching docs)"`
	HoldPackages  []string `json:"holdPackages" nullable:"false" doc:"Packages a scheduled run never upgrades, extra to builtinHolds"`

	BuiltinHolds          []string `json:"builtinHolds" nullable:"false" doc:"Packages always excluded from scheduled runs (Docker and containerd); upgrade them with Apply updates"`
	SecurityOnlySupported bool     `json:"securityOnlySupported" doc:"The host's package manager can limit an upgrade to security updates"`
	NativeAutoUpdates     string   `json:"nativeAutoUpdates" enum:"unattended-upgrades,dnf-automatic," doc:"The host's own automatic update mechanism the last check found active; empty when none is (including after NebulaCtrl switched it off)"`

	NextWindowStart         *time.Time    `json:"nextWindowStart" doc:"Start of the next window; null while the schedule is off"`
	LastScheduledRun        *PatchAutoRun `json:"lastScheduledRun" doc:"The newest scheduled upgrade, if any"`
	ConsecutiveFailures     int           `json:"consecutiveFailures" doc:"Scheduled runs that failed in a row; three switch the schedule off"`
	DisabledReason          *string       `json:"disabledReason" doc:"Why NebulaCtrl switched the schedule off after repeated failures"`
	SuggestedStart          *string       `json:"suggestedStart" doc:"Start of the quietest window in the last 14 days of traffic, in timezone (HH:MM); null with less than 3 days of data"`
	RequiresAcknowledgement bool          `json:"requiresAcknowledgement" doc:"Enabling needs acknowledgeProduction: production services or the control plane run on this server"`
}

// AutoInput is the body of the settings update.
type AutoInput struct {
	AutoSettings
	// AcknowledgeProduction accepts that production services run on the server.
	AcknowledgeProduction bool
}

// SupportsAutoPatching reports whether the server's agent advertised
// agentprotocol.CapHostPatchingAuto: it applies holds and the disk preflight. A
// scheduled upgrade is only sent to such an agent. A server that has never
// connected has no facts yet and is assumed to enroll with a current agent.
func SupportsAutoPatching(facts json.RawMessage) bool {
	return supportsCapability(facts, agentprotocol.CapHostPatchingAuto)
}

func (s *Service) autoView(ctx context.Context, srv db.Server, known, supported bool, native string, requiresAck bool) (PatchAuto, error) {
	cfg := DefaultAutoSettings()
	a := PatchAuto{}
	row, err := s.q.GetServerPatchAutoSettings(ctx, srv.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PatchAuto{}, fmt.Errorf("get automatic update settings: %w", err)
	default:
		cfg = settingsOf(row)
		a.ConsecutiveFailures = int(row.ConsecutiveFailures)
		if row.DisabledReason != "" {
			reason := row.DisabledReason
			a.DisabledReason = &reason
		}
	}
	a.Enabled, a.Scope, a.WindowStart, a.WindowMinutes, a.Timezone, a.Reboot, a.HoldPackages =
		cfg.Enabled, cfg.Scope, cfg.WindowStart, cfg.WindowMinutes, cfg.Timezone, cfg.Reboot, cfg.HoldPackages
	a.BuiltinHolds = slices.Clone(hostpatch.BuiltinHolds)
	a.SecurityOnlySupported = !known || supported
	a.NativeAutoUpdates = native
	a.RequiresAcknowledgement = requiresAck

	loc, err := cronexpr.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	if cfg.Enabled {
		if h, m, ok := parseClock(cfg.WindowStart); ok {
			if next := nextSlotStart(s.now(), loc, h, m); !next.IsZero() {
				next = next.UTC()
				a.NextWindowStart = &next
			}
		}
	}
	last, err := s.q.LastScheduledPatchApply(ctx, srv.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PatchAuto{}, fmt.Errorf("get last scheduled run: %w", err)
	default:
		a.LastScheduledRun = &PatchAutoRun{ID: last.ID, Status: last.Status, StartedAt: last.CreatedAt, FinishedAt: last.FinishedAt}
	}
	traffic, err := s.q.ListServerHourlyRequests(ctx, srv.ID)
	if err != nil {
		return PatchAuto{}, fmt.Errorf("list hourly traffic: %w", err)
	}
	rows := make([]hourlyRequests, 0, len(traffic))
	for _, t := range traffic {
		rows = append(rows, hourlyRequests{Bucket: t.Bucket, Requests: t.Requests})
	}
	a.SuggestedStart = quietestStart(rows, loc, cfg.WindowMinutes)
	return a, nil
}

// ---- changing the settings ----

// UpdateAuto saves the automatic update settings of a server. Instance admin
// only. Turning them on needs, on a server that runs production services or the
// control plane, the same acknowledgement as an upgrade by hand.
func (s *Service) UpdateAuto(ctx context.Context, serverID uuid.UUID, in AutoInput) (PatchAuto, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return PatchAuto{}, err
	}
	if err := s.requireInstanceAdmin(ctx); err != nil {
		return PatchAuto{}, err
	}
	cfg, err := in.AutoSettings.Normalize()
	if err != nil {
		return PatchAuto{}, err
	}
	known, supported := false, false
	native := ""
	st, err := s.q.GetServerPatchState(ctx, serverID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PatchAuto{}, fmt.Errorf("get patch state: %w", err)
	default:
		known, supported, native = st.CheckedAt != nil, st.Supported, st.NativeAutoUpdates
	}
	if known && !supported {
		if cfg.Enabled {
			return PatchAuto{}, apierr.Unprocessable("patching_unsupported", srv.Name+" has no supported package manager (apt or dnf)")
		}
		if cfg.Scope == ScopeSecurity {
			return PatchAuto{}, apierr.Unprocessable("scope_unsupported", "this host's package manager cannot limit upgrades to security updates")
		}
	}
	prod, err := s.productionServices(ctx, serverID)
	if err != nil {
		return PatchAuto{}, err
	}
	if cfg.Enabled {
		if !SupportsAutoPatching(srv.Facts) {
			return PatchAuto{}, apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old to apply updates automatically; update the agent first")
		}
		effects := []string{
			"Upgrades the operating system packages of " + srv.Name + " as root every night from " + cfg.WindowStart + " (" + cfg.Timezone + "), unattended. Docker and containerd packages are never included.",
		}
		if cfg.Reboot == RebootWhenRequired {
			effects = append(effects, "Reboots the server when an upgrade needs it: every service on it goes down until Docker starts them again.")
		}
		if err := s.guard(ctx, srv, prod, in.AcknowledgeProduction, "patch_server_auto", "Automatic updates of "+srv.Name, effects, false,
			cfg.Scope, cfg.WindowStart, cfg.WindowMinutes, cfg.Timezone, cfg.Reboot); err != nil {
			return PatchAuto{}, err
		}
	}

	var row db.ServerPatchAutoSetting
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpsertServerPatchAutoSettings(ctx, db.UpsertServerPatchAutoSettingsParams{
			ServerID: srv.ID, WorkspaceID: srv.WorkspaceID, Enabled: cfg.Enabled, Scope: cfg.Scope, WindowStart: cfg.WindowStart,
			WindowMinutes: int32(cfg.WindowMinutes), Timezone: cfg.Timezone, Reboot: cfg.Reboot, HoldPackages: cfg.HoldPackages,
			UpdatedBy: principalUser(ctx),
		})
		if err != nil {
			return fmt.Errorf("save automatic update settings: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionAuto, ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{
				"server": srv.Name, "enabled": cfg.Enabled, "scope": cfg.Scope, "windowStart": cfg.WindowStart, "windowMinutes": cfg.WindowMinutes,
				"timezone": cfg.Timezone, "reboot": cfg.Reboot, "holdPackages": cfg.HoldPackages, "controlPlane": srv.IsControlPlane,
				"productionServices": len(prod),
			},
		})
	})
	if err != nil {
		return PatchAuto{}, err
	}
	s.publish(srv.ID)
	s.syncNative(ctx, srv, row)
	return s.autoView(ctx, srv, known, supported, native, len(prod) > 0 || srv.IsControlPlane)
}

// nativeDirective is what a check or upgrade of the server should do about the
// host's own automatic updates: switch them off while the schedule is on, back on
// once it is off and they are still switched off by us, otherwise nothing.
func nativeDirective(row db.ServerPatchAutoSetting) string {
	switch {
	case row.Enabled:
		return agentprotocol.PatchNativeDisable
	case row.NativeHandoff:
		return agentprotocol.PatchNativeRestore
	}
	return ""
}

// syncNative asks the agent to bring the host's own automatic updates in line
// with the schedule right away (a check that carries the directive). The sweep
// does it later if this one cannot (the server is offline).
func (s *Service) syncNative(ctx context.Context, srv db.Server, row db.ServerPatchAutoSetting) {
	if row.Enabled == row.NativeHandoff || !SupportsAutoPatching(srv.Facts) {
		return
	}
	if _, err := s.start(ctx, srv, startSpec{action: agentprotocol.PatchingActionCheck, trigger: agentprotocol.PatchingTriggerScheduled}); err != nil {
		if _, expected := apierr.As(err); !expected {
			s.log.WarnContext(ctx, "automatic updates: sync host updates", "server_id", srv.ID, "err", err)
		}
	}
}

// recordNative stores that the agent switched the host's own automatic updates.
func (s *Service) recordNative(ctx context.Context, serverID uuid.UUID, applied string) {
	if applied == "" {
		return
	}
	if err := s.q.SetServerPatchAutoNativeHandoff(ctx, db.SetServerPatchAutoNativeHandoffParams{
		ServerID: serverID, NativeHandoff: applied == agentprotocol.PatchNativeDisable,
	}); err != nil {
		s.log.WarnContext(ctx, "automatic updates: record host updates", "server_id", serverID, "err", err)
	}
}

// ---- the sweep ----

// AutoSweep starts the scheduled upgrades that are due and brings the host's own
// automatic updates in line with the schedules. One scheduled upgrade runs at a
// time on the instance: the sweep starts at most the servers that are due one
// after the other, and a server that is due while another runs waits for the next
// sweep (and the end of its window).
func (s *Service) AutoSweep(ctx context.Context) error {
	rows, err := s.q.ListOnlineServerPatchAuto(ctx)
	if err != nil {
		return fmt.Errorf("list automatic update schedules: %w", err)
	}
	now := s.now()
	for _, r := range rows {
		if err := s.autoSweepOne(ctx, now, r); err != nil {
			s.log.WarnContext(ctx, "automatic updates: sweep", "server_id", r.ServerPatchAutoSetting.ServerID, "err", err)
		}
	}
	pending, err := s.q.ListServersNeedingNativeHandoff(ctx, nativeRetry.Seconds())
	if err != nil {
		return fmt.Errorf("list servers needing a host updates hand-over: %w", err)
	}
	for _, p := range pending {
		if !SupportsAutoPatching(p.Facts) {
			continue
		}
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: p.ID})
		if err != nil {
			s.log.WarnContext(ctx, "automatic updates: load server", "server_id", p.ID, "err", err)
			continue
		}
		if _, err := s.start(ctx, srv, startSpec{action: agentprotocol.PatchingActionCheck, trigger: agentprotocol.PatchingTriggerScheduled}); err != nil {
			if _, expected := apierr.As(err); !expected {
				s.log.WarnContext(ctx, "automatic updates: sync host updates", "server_id", p.ID, "err", err)
			}
		}
	}
	return nil
}

func (s *Service) autoSweepOne(ctx context.Context, now time.Time, r db.ListOnlineServerPatchAutoRow) error {
	row := r.ServerPatchAutoSetting
	if !SupportsAutoPatching(r.ServerFacts) {
		return nil
	}
	cfg := settingsOf(row)
	loc, err := cronexpr.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("time zone %q: %w", cfg.Timezone, err)
	}
	slot, ok := dueSlot(now, row.ServerID, cfg, loc, row.SettingsChangedAt)
	if !ok {
		return nil
	}
	securityOnly := cfg.Scope == ScopeSecurity
	pending := int(r.StateUpdates)
	if securityOnly {
		pending = int(r.StateSecurityUpdates)
	}
	if r.StateCheckedAt != nil && now.Sub(*r.StateCheckedAt) < freshCheck && pending == 0 {
		return nil // nothing to do, and the check is fresh enough to believe it
	}
	if done, err := s.q.HasScheduledPatchApplyForSlot(ctx, db.HasScheduledPatchApplyForSlotParams{ServerID: row.ServerID, SlotStart: &slot}); err != nil || done {
		return err
	}
	if busy, err := s.q.HasServerBusyWork(ctx, &row.ServerID); err != nil || busy {
		return err
	}
	if running, err := s.q.HasScheduledPatchRunInFlight(ctx); err != nil || running {
		return err
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: row.ServerID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	spec := startSpec{
		action: agentprotocol.PatchingActionApply, trigger: agentprotocol.PatchingTriggerScheduled,
		securityOnly: securityOnly, holds: cfg.HoldPackages, slotStart: &slot,
		checkContainers: cfg.Reboot == RebootWhenRequired && !srv.IsControlPlane,
	}
	if r.StateCheckedAt != nil {
		spec.updatesBefore = &pending
	}
	if _, err := s.start(ctx, srv, spec); err != nil {
		var pgErr *pgconn.PgError
		if _, expected := apierr.As(err); expected || (errors.As(err, &pgErr) && pgErr.Code == "23505") {
			return nil // a run is in flight, or another sweep started this window's upgrade
		}
		return err
	}
	return nil
}

// ---- after a scheduled run ----

// autoRebootPlan decides whether the scheduled upgrade that just succeeded may be
// followed by a reboot, and returns a note for the run when it may not although
// the settings ask for one. Every guard must hold: the server asks for a reboot,
// at least rebootMargin is left in the window, no automatic reboot happened in the
// last 24 hours, the server is not the control plane host, and the agent saw that
// every running container restarts by itself.
func (s *Service) autoRebootPlan(ctx context.Context, run db.ServerPatchRun, out agentprotocol.PatchingResult) (bool, string) {
	if run.Status != StatusPending || run.SlotStart == nil || !out.State.RebootRequired {
		return false, ""
	}
	row, err := s.q.GetServerPatchAutoSettings(ctx, run.ServerID)
	if err != nil || !row.Enabled || row.Reboot != RebootWhenRequired {
		return false, ""
	}
	skip := func(why string) (bool, string) { return false, "Automatic reboot skipped: " + why }
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: run.ServerID})
	if err != nil {
		return skip("the server could not be read.")
	}
	switch {
	case srv.IsControlPlane:
		return skip("this server runs the NebulaCtrl control plane.")
	case run.SlotStart.Add(time.Duration(row.WindowMinutes)*time.Minute).Sub(s.now()) < rebootMargin:
		return skip("less than 10 minutes are left in the maintenance window.")
	}
	recent, err := s.q.HasRecentScheduledPatchReboot(ctx, run.ServerID)
	if err != nil {
		return skip("the reboot history could not be read.")
	}
	if recent {
		return skip("the server was already rebooted automatically in the last 24 hours.")
	}
	c := out.Containers
	switch {
	case c == nil || !c.Known:
		why := "the containers' restart policies could not be read"
		if c != nil && c.Reason != "" {
			why += " (" + c.Reason + ")"
		}
		return skip(why + ".")
	case len(c.Unsafe) > 0:
		return skip("containers that do not restart by themselves (restart policy must be always or unless-stopped): " + strings.Join(c.Unsafe, ", ") + ".")
	}
	return true, ""
}

// startAutoReboot reboots the server after its scheduled upgrade, through the
// same flow as a reboot by hand.
func (s *Service) startAutoReboot(ctx context.Context, run db.ServerPatchRun) {
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: run.ServerID})
	if err == nil {
		_, err = s.start(ctx, srv, startSpec{
			action: agentprotocol.PatchingActionReboot, trigger: agentprotocol.PatchingTriggerScheduled, slotStart: run.SlotStart,
		})
	}
	if err != nil {
		s.log.WarnContext(ctx, "automatic updates: reboot", "server_id", run.ServerID, "err", err)
	}
}

// afterScheduledRun updates the failure count of the schedule when a scheduled
// upgrade or reboot has finished: a success starts the count over, a failure
// counts, tells the team and, at MaxAutoFailures in a row, switches the schedule off.
func (s *Service) afterScheduledRun(ctx context.Context, run db.ServerPatchRun, status, errMsg string) {
	switch {
	case status == StatusFailed:
		s.autoFailed(ctx, run, errMsg)
	case status == StatusSucceeded && run.Action == agentprotocol.PatchingActionApply:
		if err := s.q.ResetServerPatchAutoFailures(ctx, run.ServerID); err != nil {
			s.log.WarnContext(ctx, "automatic updates: reset failures", "server_id", run.ServerID, "err", err)
		}
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "..."
}

func (s *Service) autoFailed(ctx context.Context, run db.ServerPatchRun, errMsg string) {
	if errMsg == "" {
		errMsg = "the run failed"
	}
	reason := fmt.Sprintf("Switched off after %d failed scheduled runs in a row. The last one: %s", MaxAutoFailures, clip(errMsg, failureReasonLen))
	var row db.ServerPatchAutoSetting
	var disabled bool
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).RecordServerPatchAutoFailure(ctx, db.RecordServerPatchAutoFailureParams{
			ServerID: run.ServerID, MaxFailures: MaxAutoFailures, Reason: reason,
		})
		if err != nil {
			return err
		}
		disabled = !row.Enabled && row.DisabledReason == reason
		if !disabled {
			return nil
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionAuto, ResourceType: "server", ResourceID: run.ServerID.String(), WorkspaceID: run.WorkspaceID,
			Metadata: map[string]any{"enabled": false, "disabledReason": reason, "consecutiveFailures": row.ConsecutiveFailures, "run": run.ID.String()},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return // the settings were removed
	}
	if err != nil {
		s.log.WarnContext(ctx, "automatic updates: record failure", "server_id", run.ServerID, "err", err)
		return
	}
	s.publish(run.ServerID)
	s.notifyAutoFailed(ctx, run, errMsg, int(row.ConsecutiveFailures), disabled)
}

func (s *Service) notifyAutoFailed(ctx context.Context, run db.ServerPatchRun, errMsg string, failures int, disabled bool) {
	if s.not == nil {
		return
	}
	name := run.ServerID.String()
	if srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: run.ServerID}); err == nil {
		name = srv.Name
	}
	what := "The automatic update"
	if run.Action == agentprotocol.PatchingActionReboot {
		what = "The automatic reboot"
	}
	ev := notifications.Event{
		Type: notifications.TypeServerAutoUpdateFailed, ServerID: &run.ServerID, Severity: notifications.SeverityWarning,
		Title:  "Automatic updates failed on " + name,
		Body:   what + " of " + name + " failed: " + clip(errMsg, failureReasonLen),
		Fields: map[string]string{"Failed runs in a row": fmt.Sprintf("%d of %d", failures, MaxAutoFailures)},
	}
	if disabled {
		ev.Title = "Automatic updates switched off on " + name
		ev.Body += " Automatic updates were switched off after " + fmt.Sprint(MaxAutoFailures) + " failures in a row. Fix the cause, then switch them on again in the server's Updates tab."
	}
	notifications.Emit(ctx, s.not, ev)
}
