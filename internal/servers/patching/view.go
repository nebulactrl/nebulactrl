package patching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/hostpatch"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PatchPackage is one pending upgrade.
type PatchPackage struct {
	Name     string `json:"name"`
	From     string `json:"from,omitempty" doc:"Installed version, when the package manager says"`
	To       string `json:"to,omitempty" doc:"Version it would upgrade to"`
	Security bool   `json:"security" doc:"The update comes from a security repository"`
	Risk     string `json:"risk,omitempty" doc:"Why upgrading this package deserves a warning, empty for ordinary packages"`
}

// ProductionService is a service in a production environment that runs on a server.
type ProductionService struct {
	ServiceID     uuid.UUID `json:"serviceId"`
	Name          string    `json:"name"`
	EnvironmentID uuid.UUID `json:"environmentId"`
	Environment   string    `json:"environment"`
	ProjectID     uuid.UUID `json:"projectId"`
	Project       string    `json:"project"`
}

// PatchRun is one check, upgrade or reboot.
type PatchRun struct {
	ID             uuid.UUID  `json:"id"`
	Action         string     `json:"action" enum:"check,apply,reboot"`
	Trigger        string     `json:"trigger" enum:"scheduled,manual"`
	Status         string     `json:"status" enum:"pending,rebooting,succeeded,failed" doc:"rebooting: the host accepted the reboot and NebulaCtrl waits for the agent to reconnect on a new boot"`
	SecurityOnly   bool       `json:"securityOnly"`
	RequestedBy    *uuid.UUID `json:"requestedBy,omitempty" doc:"The person who started the run; absent for automatic checks"`
	UpdatesBefore  *int       `json:"updatesBefore"`
	UpdatesAfter   *int       `json:"updatesAfter"`
	ExitCode       *int       `json:"exitCode" doc:"The package manager's exit status (upgrades only)"`
	Message        string     `json:"message,omitempty"`
	Error          string     `json:"error,omitempty"`
	Log            string     `json:"log,omitempty" doc:"The tail of what the package manager printed (upgrades only; only in the single-run read)"`
	CreatedAt      time.Time  `json:"createdAt"`
	RebootingSince *time.Time `json:"rebootingSince"`
	FinishedAt     *time.Time `json:"finishedAt"`
}

// PatchView is the Updates panel of a server.
type PatchView struct {
	Online         bool           `json:"online"`
	AgentSupported bool           `json:"agentSupported" doc:"The agent advertised patching support; false means update the agent first"`
	Known          bool           `json:"known" doc:"A check has completed at least once"`
	Supported      bool           `json:"supported" doc:"The host has a supported package manager (apt or dnf)"`
	Manager        string         `json:"manager" enum:"apt,dnf," doc:"Empty until the first check"`
	Updates        int            `json:"updates"`
	Security       int            `json:"security" doc:"How many of the updates are security updates"`
	Packages       []PatchPackage `json:"packages" nullable:"false" doc:"Pending upgrades, security first, at most 200"`
	RebootRequired bool           `json:"rebootRequired"`
	RebootPackages []string       `json:"rebootPackages" nullable:"false" doc:"Packages that asked for the reboot, when the host says"`
	CheckedAt      *time.Time     `json:"checkedAt"`
	Stale          bool           `json:"stale" doc:"The last check is older than 12 hours"`
	Message        string         `json:"message,omitempty" doc:"A caution from the last check"`
	Busy           string         `json:"busy" enum:"check,apply,reboot," doc:"The run in flight, if any"`
	ControlPlane   bool           `json:"controlPlane" doc:"This server runs the control plane: upgrades and reboots take the dashboard down"`
	// ProductionServices lists the services in production environments on this server.
	ProductionServices []ProductionService `json:"productionServices" nullable:"false"`
	Runs               []PatchRun          `json:"runs" nullable:"false" doc:"The most recent runs, newest first, without logs"`
}

// PatchPlan is what an upgrade would do, shown before the person confirms.
type PatchPlan struct {
	Server       string         `json:"server"`
	Manager      string         `json:"manager" enum:"apt,dnf,"`
	SecurityOnly bool           `json:"securityOnly"`
	Updates      int            `json:"updates" doc:"Packages that would be upgraded"`
	Security     int            `json:"security"`
	Packages     []PatchPackage `json:"packages" nullable:"false"`
	// Risks are the packages in the plan that deserve a warning.
	Risks              []PatchRisk         `json:"risks" nullable:"false"`
	RebootLikely       bool                `json:"rebootLikely" doc:"The upgrade probably leaves the server wanting a reboot"`
	RestartsDocker     bool                `json:"restartsDocker" doc:"Docker is among the packages: its daemon restarts and containers may stop for a moment"`
	ServicesOnServer   int                 `json:"servicesOnServer"`
	ProductionServices []ProductionService `json:"productionServices" nullable:"false"`
	ControlPlane       bool                `json:"controlPlane"`
	// RequiresAcknowledgement is true when the apply request must set acknowledgeProduction.
	RequiresAcknowledgement bool       `json:"requiresAcknowledgement"`
	Confirm                 string     `json:"confirm" doc:"What the person types to confirm: the server's name"`
	Warnings                []string   `json:"warnings" nullable:"false"`
	CheckedAt               *time.Time `json:"checkedAt"`
	Stale                   bool       `json:"stale"`
	Ready                   bool       `json:"ready" doc:"The plan can be applied now"`
	NotReadyReason          string     `json:"notReadyReason,omitempty"`
}

// PatchRisk is a pending package with a warning.
type PatchRisk struct {
	Package string `json:"package"`
	Reason  string `json:"reason"`
	Reboot  bool   `json:"reboot" doc:"The upgrade only takes full effect after a reboot"`
}

func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}

func runView(r db.ServerPatchRun, withLog bool) PatchRun {
	out := PatchRun{
		ID: r.ID, Action: r.Action, Trigger: r.Trigger, Status: r.Status, SecurityOnly: r.SecurityOnly, RequestedBy: r.RequestedBy,
		UpdatesBefore: intPtr(r.UpdatesBefore), UpdatesAfter: intPtr(r.UpdatesAfter), ExitCode: intPtr(r.ExitCode),
		Message: r.Message, Error: r.Error, CreatedAt: r.CreatedAt, RebootingSince: r.RebootingSince, FinishedAt: r.FinishedAt,
	}
	if withLog {
		out.Log = r.Log
	}
	return out
}

// packagesOf decodes the stored pending packages.
func packagesOf(st db.ServerPatchState) []agentprotocol.PatchPackage {
	var pkgs []agentprotocol.PatchPackage
	if len(st.Packages) > 0 {
		_ = json.Unmarshal(st.Packages, &pkgs)
	}
	return pkgs
}

func packageViews(pkgs []agentprotocol.PatchPackage) []PatchPackage {
	risk := map[string]string{}
	for _, r := range hostpatch.Risks(pkgs) {
		risk[r.Package] = r.Reason
	}
	out := make([]PatchPackage, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, PatchPackage{Name: p.Name, From: p.From, To: p.To, Security: p.Security, Risk: risk[p.Name]})
	}
	return out
}

// Get returns the Updates panel of a server.
func (s *Service) Get(ctx context.Context, serverID uuid.UUID) (PatchView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersRead, serverID)
	if err != nil {
		return PatchView{}, err
	}
	return s.view(ctx, srv)
}

func (s *Service) view(ctx context.Context, srv db.Server) (PatchView, error) {
	v := PatchView{
		Online: srv.Status == "online", AgentSupported: SupportsPatching(srv.Facts), ControlPlane: srv.IsControlPlane,
		Packages: []PatchPackage{}, RebootPackages: []string{}, Runs: []PatchRun{}, ProductionServices: []ProductionService{},
	}
	st, err := s.q.GetServerPatchState(ctx, srv.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PatchView{}, fmt.Errorf("get patch state: %w", err)
	default:
		v.Known = st.CheckedAt != nil
		v.Supported, v.Manager, v.Updates, v.Security = st.Supported, st.Manager, int(st.Updates), int(st.SecurityUpdates)
		v.RebootRequired, v.CheckedAt, v.Message = st.RebootRequired, st.CheckedAt, st.Message
		v.Packages = packageViews(packagesOf(st))
		if st.RebootPackages != nil {
			v.RebootPackages = st.RebootPackages
		}
		v.Stale = st.CheckedAt != nil && s.now().Sub(*st.CheckedAt) > StaleAfter
	}
	runs, err := s.q.ListServerPatchRuns(ctx, db.ListServerPatchRunsParams{ServerID: srv.ID, Limit: RecentRuns})
	if err != nil {
		return PatchView{}, fmt.Errorf("list patch runs: %w", err)
	}
	for _, r := range runs {
		pr := runView(db.ServerPatchRun(r), false)
		v.Runs = append(v.Runs, pr)
		if v.Busy == "" && (pr.Status == StatusPending || pr.Status == StatusRebooting) && s.now().Sub(pr.CreatedAt) < 24*time.Hour {
			v.Busy = pr.Action
		}
	}
	if v.ProductionServices, err = s.productionServices(ctx, srv.ID); err != nil {
		return PatchView{}, err
	}
	return v, nil
}

// GetRun returns one run with its log.
func (s *Service) GetRun(ctx context.Context, serverID, runID uuid.UUID) (PatchRun, error) {
	if _, err := s.loadServer(ctx, policy.ScopeServersRead, serverID); err != nil {
		return PatchRun{}, err
	}
	r, err := s.q.GetServerPatchRun(ctx, db.GetServerPatchRunParams{ID: runID, ServerID: serverID})
	if errors.Is(err, pgx.ErrNoRows) {
		return PatchRun{}, apierr.NotFound("patch run")
	}
	if err != nil {
		return PatchRun{}, fmt.Errorf("get patch run: %w", err)
	}
	return runView(r, true), nil
}

// Plan says what an upgrade would do on the server, from the last check. It
// changes nothing; the apply request is the confirmation.
func (s *Service) Plan(ctx context.Context, serverID uuid.UUID, securityOnly bool) (PatchPlan, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersRead, serverID)
	if err != nil {
		return PatchPlan{}, err
	}
	plan := PatchPlan{
		Server: srv.Name, SecurityOnly: securityOnly, ControlPlane: srv.IsControlPlane, Confirm: srv.Name,
		Packages: []PatchPackage{}, Risks: []PatchRisk{}, Warnings: []string{}, ProductionServices: []ProductionService{},
	}
	if plan.ProductionServices, err = s.productionServices(ctx, serverID); err != nil {
		return PatchPlan{}, err
	}
	n, err := s.q.CountServerLiveServices(ctx, &serverID)
	if err != nil {
		return PatchPlan{}, fmt.Errorf("count services: %w", err)
	}
	plan.ServicesOnServer = int(n)
	plan.RequiresAcknowledgement = len(plan.ProductionServices) > 0 || srv.IsControlPlane

	st, err := s.q.GetServerPatchState(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && st.CheckedAt == nil) {
		plan.NotReadyReason = "No check has completed yet. Check for updates first."
		return plan, nil
	}
	if err != nil {
		return PatchPlan{}, fmt.Errorf("get patch state: %w", err)
	}
	plan.Manager, plan.CheckedAt = st.Manager, st.CheckedAt
	plan.Stale = s.now().Sub(*st.CheckedAt) > StaleAfter
	var pkgs []agentprotocol.PatchPackage
	for _, p := range packagesOf(st) {
		if !securityOnly || p.Security {
			pkgs = append(pkgs, p)
		}
	}
	plan.Packages = packageViews(pkgs)
	plan.Updates, plan.Security = int(st.Updates), int(st.SecurityUpdates)
	if securityOnly {
		plan.Updates = int(st.SecurityUpdates)
	}
	for _, r := range hostpatch.Risks(pkgs) {
		plan.Risks = append(plan.Risks, PatchRisk{Package: r.Package, Reason: r.Reason, Reboot: r.Reboot})
	}
	plan.RebootLikely = hostpatch.RebootLikely(pkgs) || st.RebootRequired
	plan.RestartsDocker = hostpatch.RestartsDocker(pkgs)
	if len(st.Packages) > 0 && int(st.Updates) > agentprotocol.MaxPatchPackages {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("Only the first %d of %d packages are listed; all of them are upgraded.", agentprotocol.MaxPatchPackages, st.Updates))
	}
	if plan.Stale {
		plan.Warnings = append(plan.Warnings, "The last check is old. Check again to see what is pending now.")
	}
	if st.Message != "" {
		plan.Warnings = append(plan.Warnings, st.Message)
	}
	if plan.RestartsDocker {
		plan.Warnings = append(plan.Warnings, "Upgrading Docker restarts its daemon. Containers stop and start again unless Docker's live-restore is on.")
	}
	if srv.IsControlPlane {
		plan.Warnings = append(plan.Warnings, "This server runs NebulaCtrl itself. The dashboard and API are unavailable while Docker or the server restarts.")
	}
	if plan.RebootLikely {
		plan.Warnings = append(plan.Warnings, "A reboot will probably be needed afterwards. Nothing reboots by itself.")
	}
	plan.Warnings = append(plan.Warnings, "Configuration files you changed are kept; packages are not removed. The upgrade keeps running on the host if the connection to NebulaCtrl is lost.")
	switch {
	case srv.Status != "online":
		plan.NotReadyReason = srv.Name + " is offline."
	case !SupportsPatching(srv.Facts):
		plan.NotReadyReason = "The agent on this server is too old to manage updates."
	case !st.Supported:
		plan.NotReadyReason = "This server has no supported package manager (apt or dnf)."
	case plan.Updates == 0:
		plan.NotReadyReason = "There is nothing to upgrade."
	default:
		plan.Ready = true
	}
	return plan, nil
}
