// Package sysupdate is the control plane's own update: the daily check of the
// release feed, the notice it feeds (GET /system/update) and the instance
// admin's one-click update (POST /system/update).
//
// One click sends an update_control_plane command to the agent on the control
// plane's own host (the server the installer enrolled). The agent starts the
// installer detached from itself and from the control plane's container, so the
// installer survives both restarting. The installer takes a control-plane backup
// first, and puts the previous image back
// when the new version does not become healthy. The installer is signed with the
// release key (minisign): the control plane verifies it before it sends anything,
// and the agent verifies it again before it runs it as root.
//
// The outcome comes from the installer and from the next start:
//
//   - the installer records its own failure (including a rollback) in
//     instance_update before it exits non-zero;
//   - the control plane that starts with the target version marks the update
//     succeeded (Settle);
//   - an update that shows no result within StaleAfter turns failed.
//
// Every start and every outcome is audited.
package sysupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/internal/version"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Update states (instance_update.state).
const (
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
)

// Actions recorded in the audit log.
const (
	ActionStart     = "instance.update.start"     // the instance admin started an update
	ActionSucceeded = "instance.update.succeeded" // the control plane came up with the target version
	ActionFailed    = "instance.update.failed"    // the installer did not start, failed (and rolled back) or never reported
)

// Error codes (apierr).
const (
	CodeUpToDate      = "up_to_date"
	CodeInProgress    = "update_in_progress"
	CodeUnavailable   = "update_unavailable"
	CodeNotOffered    = "version_not_offered"
	CodeCheckDisabled = "update_check_disabled"
	CodeCheckFailed   = "update_check_failed"
	resourceType      = "instance"
	resourceID        = "update"
	defaultInstaller  = "https://nebulactrl.com/install.sh"
	feedPathSuffix    = "/releases/latest.json"
)

// Timings.
const (
	// StaleAfter is how long an update may run without a result: the installer's
	// backup, pull and health wait fit well inside it.
	StaleAfter = 30 * time.Minute
	// ResultVisible is how long a finished update stays in the view.
	ResultVisible = time.Hour
	// CheckInterval is how often the feed is fetched.
	CheckInterval = 24 * time.Hour
	checkTimeout  = 30 * time.Second
	// manualCheckTimeout bounds the instance admin's "check now", which waits for the feed.
	manualCheckTimeout = 15 * time.Second
	// manualCheckEvery is the least time between two manual checks.
	manualCheckEvery = 30 * time.Second
)

// AdminChecker tells whether a user is the instance admin (the first account).
// *auth.Service implements it.
type AdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Dispatcher agents.Dispatcher
	Admins     AdminChecker
	// Config is NEBULACTRL_UPDATE_CHECK and NEBULACTRL_UPDATE_FEED_URL. The zero
	// value (check off) is what a Config built without Load has.
	Config config.Update
	// Version returns the running version (default version.Current).
	Version func() string
	// HTTP fetches the feed and the installer's signature (default: a client with a timeout).
	HTTP *http.Client
	// PublicKey is the minisign public key the installer must be signed with
	// (default releasesign.PublicKey). Tests use a key of their own.
	PublicKey string
	Logger    *slog.Logger
}

// Service implements the update check and the one-click update.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	disp   agents.Dispatcher
	admins AdminChecker
	cfg    config.Update
	pubKey string
	ver    func() string
	hc     *http.Client
	log    *slog.Logger
	now    func() time.Time

	manualMu   sync.Mutex
	lastManual time.Time // the last manual check, for the rate limit
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Version == nil {
		d.Version = version.Current
	}
	if d.PublicKey == "" {
		d.PublicKey = releasesign.PublicKey
	}
	if d.Config.FeedURL == "" {
		d.Config.FeedURL = config.DefaultUpdateFeedURL
	}
	s := &Service{
		pool: d.Pool, disp: d.Dispatcher, admins: d.Admins, cfg: d.Config, pubKey: d.PublicKey,
		ver: d.Version, hc: d.HTTP, log: d.Logger.With("component", "sysupdate"), now: time.Now,
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// CheckEnabled reports whether the feed is read at all.
func (s *Service) CheckEnabled() bool { return s.cfg.Check }

// ---- views ---------------------------------------------------------------------

// UpdatingView is the update the instance admin started.
type UpdatingView struct {
	State         string     `json:"state" enum:"running,succeeded,failed" doc:"running while the installer works and until the new version answers; succeeded once the control plane runs the target version; failed when the installer did not start or failed (it rolled back to the previous version when the new one did not become healthy)."`
	FromVersion   string     `json:"fromVersion"`
	TargetVersion string     `json:"targetVersion"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Error         string     `json:"error,omitempty" doc:"Why it failed, including whether the previous version was put back."`
}

// UpdateView is GET /system/update.
type UpdateView struct {
	Current            string        `json:"current" doc:"The version this control plane runs (\"dev\" for a development build)."`
	Latest             string        `json:"latest,omitempty" doc:"The newest release the feed announced. Absent before the first successful check and while checks are off."`
	Available          bool          `json:"available" doc:"Latest is a release newer than current. Never true for a development build."`
	Notes              string        `json:"notes,omitempty" doc:"Release notes of latest, Markdown."`
	NotesURL           string        `json:"notesUrl,omitempty"`
	PublishedAt        *time.Time    `json:"publishedAt,omitempty"`
	CheckedAt          *time.Time    `json:"checkedAt,omitempty" doc:"When the feed was last read successfully."`
	CheckError         string        `json:"checkError,omitempty" doc:"Why the last check failed. The last known release stays."`
	CheckEnabled       bool          `json:"checkEnabled" doc:"False when NEBULACTRL_UPDATE_CHECK=false: the control plane never contacts the feed."`
	CanUpdate          bool          `json:"canUpdate" doc:"The caller may start the update now: the instance admin, an update is available and not running, and this control plane's own server has an online agent that can run the installer."`
	IsAdmin            bool          `json:"isAdmin" doc:"The caller is the instance admin: they can start an update and check for a new version now (POST /system/update/check)."`
	CannotUpdateReason string        `json:"cannotUpdateReason,omitempty" doc:"Why canUpdate is false while an update is available."`
	ManualCommand      string        `json:"manualCommand" doc:"The installer command that updates this control plane by hand. Empty unless the caller is the instance admin (the command names the mirror the update comes from)."`
	Updating           *UpdatingView `json:"updating,omitempty" doc:"The update in flight, and for an hour the one that just finished."`
}

// Get returns the update view for the caller.
func (s *Service) Get(ctx context.Context) (UpdateView, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return UpdateView{}, err
	}
	row, err := s.load(ctx)
	if err != nil {
		return UpdateView{}, err
	}
	v := s.view(row)
	admin := s.isAdmin(ctx, p)
	v.IsAdmin = admin
	if admin {
		v.ManualCommand = s.manualCommand(v.Latest, v.Available)
	}
	if !v.Available {
		return v, nil
	}
	switch {
	case row.State == StateRunning:
		v.CannotUpdateReason = "An update is already running."
	case !admin:
		v.CannotUpdateReason = "Only the instance admin can update."
	default:
		if _, reason, err := s.mechanics(ctx); err != nil {
			return UpdateView{}, err
		} else if reason != "" {
			v.CannotUpdateReason = reason
		} else {
			v.CanUpdate = true
		}
	}
	return v, nil
}

// view is the part of the view that depends on the stored state only.
func (s *Service) view(row db.InstanceUpdate) UpdateView {
	cur := s.ver()
	v := UpdateView{Current: cur, CheckEnabled: s.cfg.Check, CheckError: row.CheckError, CheckedAt: row.CheckedAt}
	if !s.cfg.Check {
		v.CheckError, v.CheckedAt = "", nil
	}
	if s.cfg.Check && row.LatestVersion != "" {
		v.Latest = row.LatestVersion
		v.Notes, v.NotesURL, v.PublishedAt = row.LatestNotes, row.LatestNotesUrl, row.LatestPublishedAt
		v.Available = Available(cur, row.LatestVersion)
	}
	if row.State != "" && row.StartedAt != nil && (row.State == StateRunning || (row.FinishedAt != nil && s.now().Sub(*row.FinishedAt) < ResultVisible)) {
		v.Updating = &UpdatingView{
			State: row.State, FromVersion: row.FromVersion, TargetVersion: row.TargetVersion,
			StartedAt: *row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
		}
	}
	return v
}

// installerURL is where the installer comes from: the site (or mirror) that
// serves the feed, else the official site. The feed URL may carry credentials
// (https://user:token@mirror/...): they are dropped here, so they reach neither
// the manual command every admin sees nor the agent's command line. A mirror's
// install.sh and install.sh.minisig therefore have to be readable without them.
// The control plane's own /install.sh is never the source of an update: it has
// no signature the agent could verify.
func (s *Service) installerURL() string {
	if u, err := url.Parse(s.cfg.FeedURL); err == nil && u.Host != "" && strings.HasSuffix(u.Path, feedPathSuffix) {
		u.Path = strings.TrimSuffix(u.Path, feedPathSuffix) + "/install.sh"
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	}
	return defaultInstaller
}

func (s *Service) manualCommand(latest string, available bool) string {
	cmd := "curl -fsSL " + s.installerURL() + " | sudo bash"
	if available && latest != "" {
		cmd += " -s -- --update-to " + latest
	}
	return cmd
}

// ---- authorization and the local server ---------------------------------------------

func (s *Service) isAdmin(ctx context.Context, p *auth.Principal) bool {
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return false
	}
	if s.admins == nil {
		return false
	}
	ok, err := s.admins.IsInstanceAdmin(ctx, p.UserID)
	return err == nil && ok
}

// authorize allows the instance admin (a token counts through its creator and
// needs the admin scope).
func (s *Service) authorize(ctx context.Context) (*auth.Principal, error) {
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

// mechanics finds the server that runs the control plane and says whether its
// agent can run the installer: reason is empty when it can.
func (s *Service) mechanics(ctx context.Context) (srv db.Server, reason string, err error) {
	srv, err = s.q.GetControlPlaneServer(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return srv, "This control plane has no server of its own (it was installed with --no-local-server), so it cannot update itself. Update it with the command below.", nil
	}
	if err != nil {
		return srv, "", fmt.Errorf("load the control-plane server: %w", err)
	}
	if srv.Status != "online" {
		return srv, fmt.Sprintf("The agent on this control plane's own server (%s) is %s. Bring it online, or update with the command below.", srv.Name, srv.Status), nil
	}
	var f agentprotocol.Facts
	_ = json.Unmarshal(srv.Facts, &f)
	if !slices.Contains(f.Capabilities, agentprotocol.CapUpdateControlPlane) {
		return srv, fmt.Sprintf("The agent on this control plane's own server (%s) is too old or the host does not run systemd. Upgrade the agent, or update with the command below.", srv.Name), nil
	}
	return srv, "", nil
}

// ---- the check ----------------------------------------------------------------------

// Check reads the release feed and stores the result. A failed fetch is stored
// as the check error and is not an error of the job: the last known release
// stays, and the next run is a day away. It does nothing when checks are off.
func (s *Service) Check(ctx context.Context) error {
	if !s.cfg.Check {
		return nil
	}
	err := s.refresh(ctx, checkTimeout)
	var fe *feedError
	if errors.As(err, &fe) {
		return nil
	}
	return err
}

// feedError is a failed read of the feed: recorded as the check error, the
// cause is free of secrets (see Fetcher).
type feedError struct{ cause error }

func (e *feedError) Error() string { return e.cause.Error() }
func (e *feedError) Unwrap() error { return e.cause }

// refresh reads the feed once and stores the outcome. checked_at moves on
// every successful read, also when the latest version is the one already known.
// A failed read is stored as the check error and returned as a *feedError.
func (s *Service) refresh(ctx context.Context, timeout time.Duration) error {
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	feed, err := Fetcher{HTTP: s.hc, URL: s.cfg.FeedURL, UserAgent: "nebulactrl/" + s.ver()}.Fetch(fctx)
	if err != nil {
		s.log.Warn("update check failed", "err", err)
		if qerr := s.q.RecordUpdateFeedError(ctx, err.Error()); qerr != nil {
			return fmt.Errorf("record update check error: %w", qerr)
		}
		return &feedError{cause: err}
	}
	if _, err := s.q.RecordUpdateFeed(ctx, db.RecordUpdateFeedParams{
		LatestVersion: feed.Version, PublishedAt: feed.PublishedAt, Notes: feed.Notes, NotesUrl: feed.NotesURL, MinAgent: feed.MinAgent,
	}); err != nil {
		return fmt.Errorf("record update feed: %w", err)
	}
	s.log.Info("update check", "current", s.ver(), "latest", feed.Version, "available", Available(s.ver(), feed.Version))
	return nil
}

// CheckNow is the instance admin's "check now": it reads the feed within
// manualCheckTimeout and returns the fresh view. 409 when checks are off, 429
// (with Retry-After) within manualCheckEvery of the previous manual check,
// 502 when the feed cannot be read.
func (s *Service) CheckNow(ctx context.Context) (UpdateView, error) {
	if _, err := s.authorize(ctx); err != nil {
		return UpdateView{}, err
	}
	if !s.cfg.Check {
		return UpdateView{}, apierr.Conflict(CodeCheckDisabled, "Update checks are turned off (NEBULACTRL_UPDATE_CHECK=false). Remove it or set NEBULACTRL_UPDATE_CHECK=true and restart the control plane to check for new versions.")
	}
	if wait := s.reserveManualCheck(); wait > 0 {
		secs := int((wait + time.Second - 1) / time.Second)
		h := http.Header{}
		h.Set("Retry-After", strconv.Itoa(secs))
		return UpdateView{}, huma.ErrorWithHeaders(apierr.TooManyRequests(fmt.Sprintf("the release feed was checked a moment ago; try again in %d seconds", secs)), h)
	}
	if err := s.refresh(ctx, manualCheckTimeout); err != nil {
		var fe *feedError
		if errors.As(err, &fe) {
			return UpdateView{}, apierr.New(http.StatusBadGateway, CodeCheckFailed, "The release feed could not be read: "+fe.Error())
		}
		return UpdateView{}, err
	}
	return s.Get(ctx)
}

// reserveManualCheck starts a manual check when none ran within
// manualCheckEvery, else returns how long to wait. A check that fails counts.
func (s *Service) reserveManualCheck() time.Duration {
	s.manualMu.Lock()
	defer s.manualMu.Unlock()
	now := s.now()
	if wait := s.lastManual.Add(manualCheckEvery).Sub(now); !s.lastManual.IsZero() && wait > 0 {
		return wait
	}
	s.lastManual = now
	return 0
}

// ---- starting an update -------------------------------------------------------------

// Start sends the installer to the local agent. The caller must be the instance
// admin. A refusal is an apierr.
func (s *Service) Start(ctx context.Context, want string) (UpdateView, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return UpdateView{}, err
	}
	if !s.cfg.Check {
		return UpdateView{}, apierr.Unprocessable(CodeUnavailable, "Update checks are turned off (NEBULACTRL_UPDATE_CHECK=false), so no release is offered. Update with the command shown in Admin → Updates.")
	}
	row, err := s.load(ctx)
	if err != nil {
		return UpdateView{}, err
	}
	cur := s.ver()
	if !Available(cur, row.LatestVersion) {
		return UpdateView{}, apierr.Conflict(CodeUpToDate, "Nothing newer than "+cur+" is available.")
	}
	if row.State == StateRunning {
		return UpdateView{}, apierr.Conflict(CodeInProgress, "An update to "+row.TargetVersion+" is already running.")
	}
	if strings.TrimPrefix(strings.TrimSpace(want), "v") != row.LatestVersion {
		return UpdateView{}, apierr.Unprocessable(CodeNotOffered, "Only "+row.LatestVersion+" is offered: it is the version the release feed announced.")
	}
	srv, reason, err := s.mechanics(ctx)
	if err != nil {
		return UpdateView{}, err
	}
	if reason != "" {
		return UpdateView{}, apierr.Unprocessable(CodeUnavailable, reason)
	}
	installer := s.installerURL()
	if err := s.verifyInstaller(ctx, installer); err != nil {
		return UpdateView{}, apierr.Unprocessable(CodeUnavailable, "The installer at "+installer+" could not be verified against the NebulaCtrl release signature, so nothing was started ("+err.Error()+"). A mirror has to serve install.sh and install.sh.minisig from the release, over https, without credentials. Update by hand with the command shown in Admin → Updates.")
	}
	payload, err := json.Marshal(agentprotocol.UpdateControlPlanePayload{Version: row.LatestVersion, InstallerURL: installer})
	if err != nil {
		return UpdateView{}, err
	}
	var updated db.InstanceUpdate
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		q := db.New(tx)
		// The conditional update is the one-update-at-a-time guard.
		updated, err = q.BeginInstanceUpdate(ctx, db.BeginInstanceUpdateParams{
			FromVersion: cur, TargetVersion: row.LatestVersion, RequestedBy: &p.UserID, StaleBefore: s.now().Add(-StaleAfter),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Conflict(CodeInProgress, "An update is already running.")
		}
		if err != nil {
			return fmt.Errorf("begin update: %w", err)
		}
		cid, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: srv.ID, Kind: agentprotocol.KindUpdateControlPlane, Payload: payload,
			IdempotencyKey: "update_control_plane:" + uuid.NewString(),
		})
		if err != nil {
			return fmt.Errorf("enqueue update_control_plane: %w", err)
		}
		if err := q.SetInstanceUpdateCommand(ctx, &cid); err != nil {
			return fmt.Errorf("record update command: %w", err)
		}
		updated.CommandID = &cid
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionStart, ResourceType: resourceType, ResourceID: resourceID, WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"from": cur, "to": row.LatestVersion, "server": srv.Name},
		})
	})
	if err != nil {
		return UpdateView{}, err
	}
	s.log.Info("control plane update started", "from", cur, "to", row.LatestVersion, "server", srv.Name)
	v := s.view(updated)
	v.IsAdmin = true
	v.ManualCommand = s.manualCommand(v.Latest, v.Available)
	v.CannotUpdateReason = "An update is already running."
	return v, nil
}

// maxInstallerBytes bounds the installer download (the assembled script is about 200 KiB).
const maxInstallerBytes = 4 << 20

// verifyInstaller checks, before anything is sent to the agent, that the file at
// installer is signed with the release key. The agent checks again before it runs it.
func (s *Service) verifyInstaller(ctx context.Context, installer string) error {
	vctx, cancel := context.WithTimeout(ctx, manualCheckTimeout)
	defer cancel()
	_, err := releasesign.FetchVerified(vctx, s.hc, installer, s.pubKey, maxInstallerBytes)
	return err
}

// ---- outcomes -----------------------------------------------------------------------

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}

// settleDecision says how a running update ends given the version that runs now:
// "" while it goes on.
//
//   - the target version runs: succeeded;
//   - the version that started it runs: the installer may still be working, or
//     it rolled back and records that itself, so it goes on until StaleAfter;
//   - any other version runs: failed.
func settleDecision(r db.InstanceUpdate, current string, now time.Time) (state, msg string) {
	if r.State != StateRunning {
		return "", ""
	}
	if sameVersion(current, r.TargetVersion) {
		return StateSucceeded, ""
	}
	if r.StartedAt != nil && now.Sub(*r.StartedAt) >= StaleAfter {
		return StateFailed, fmt.Sprintf("No result within %d minutes: the control plane still runs %s, not %s. The installer may not have started or may have stopped; its log is on the control plane's host (journalctl -u 'nebulactrl-update-*').", int(StaleAfter.Minutes()), current, r.TargetVersion)
	}
	if !sameVersion(current, r.FromVersion) {
		return StateFailed, fmt.Sprintf("The control plane came up as %s, not as the requested %s.", current, r.TargetVersion)
	}
	return "", ""
}

// load reads the state and settles an update that is decided already.
func (s *Service) load(ctx context.Context) (db.InstanceUpdate, error) {
	row, err := s.q.GetInstanceUpdate(ctx)
	if err != nil {
		return db.InstanceUpdate{}, fmt.Errorf("get instance update: %w", err)
	}
	if state, msg := settleDecision(row, s.ver(), s.now()); state != "" {
		// The outcome is the control plane's, not that of whoever happens to be looking.
		settled, err := s.finish(auth.WithPrincipal(ctx, auth.System(uuid.Nil, uuid.Nil, "control plane")), row, state, msg)
		if err != nil {
			return db.InstanceUpdate{}, err
		}
		return settled, nil
	}
	return row, nil
}

// Settle runs once at start, after the migrations: the control plane that comes
// up with the target version marks the update succeeded.
func (s *Service) Settle(ctx context.Context) error {
	_, err := s.load(ctx)
	return err
}

// finish moves a running update to a terminal state, once, and audits it. When
// somebody else finished it first, it returns the stored row.
func (s *Service) finish(ctx context.Context, row db.InstanceUpdate, state, msg string) (db.InstanceUpdate, error) {
	var done db.InstanceUpdate
	finished := false
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		done, err = db.New(tx).FinishInstanceUpdate(ctx, db.FinishInstanceUpdateParams{State: state, Error: msg, FromStates: []string{StateRunning}})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // somebody finished it first
		}
		if err != nil {
			return fmt.Errorf("finish update: %w", err)
		}
		action := ActionSucceeded
		if state == StateFailed {
			action = ActionFailed
		}
		md := map[string]any{"from": row.FromVersion, "to": row.TargetVersion}
		if msg != "" {
			md["error"] = msg
		}
		if err := audit.Record(ctx, tx, audit.Entry{Action: action, ResourceType: resourceType, ResourceID: resourceID, Metadata: md}); err != nil {
			return err
		}
		finished = true
		return nil
	})
	if err != nil {
		return db.InstanceUpdate{}, err
	}
	if finished {
		s.log.Info("control plane update finished", "state", state, "from", row.FromVersion, "to", row.TargetVersion, "error", msg)
		return done, nil
	}
	return s.q.GetInstanceUpdate(ctx)
}

// ---- agent hooks --------------------------------------------------------------------

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool { return kind == agentprotocol.KindUpdateControlPlane }

// ours returns the running update when commandID is its command.
func (s *Service) ours(ctx context.Context, commandID uuid.UUID) (db.InstanceUpdate, bool, error) {
	row, err := s.q.GetInstanceUpdate(ctx)
	if err != nil {
		return row, false, fmt.Errorf("get instance update: %w", err)
	}
	return row, row.State == StateRunning && row.CommandID != nil && *row.CommandID == commandID, nil
}

// HandleResult follows the result of an update_control_plane command. Started
// is not an outcome: the update stays running until the new version answers (or
// the installer records its failure). A failed result means the installer did not start.
func (s *Service) HandleResult(ctx context.Context, _, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	row, mine, err := s.ours(ctx, commandID)
	if err != nil || !mine {
		return err
	}
	if res.Outcome == agentprotocol.OutcomeSucceeded {
		var out agentprotocol.UpdateControlPlaneResult
		_ = json.Unmarshal(res.Data, &out)
		s.log.Info("control plane installer started", "to", row.TargetVersion, "unit", out.Unit)
		return nil
	}
	msg := strings.TrimSpace(res.Error)
	if msg == "" {
		msg = "The agent did not say why."
	}
	_, err = s.finish(ctx, row, StateFailed, "The installer did not start: "+msg+" Nothing was changed.")
	return err
}

// HandleUndelivered fails the update of a command that never reached its agent.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	row, mine, err := s.ours(ctx, commandID)
	if err != nil || !mine {
		return err
	}
	if msg == "" {
		msg = "Not delivered: the agent of the control plane's server was offline."
	}
	_, err = s.finish(ctx, row, StateFailed, msg+" Nothing was changed.")
	return err
}
