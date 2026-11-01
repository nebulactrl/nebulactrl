// Package buildplacement decides where a git deployment is built: on its target
// server (the default) or on a build server that pushes to an OCI registry.
//
// The location and the registry resolve service -> project -> team; the build
// server pin comes from the service, then the project. Without a pin, a build
// server is chosen by rendezvous hashing over the eligible servers with the
// service id as the key, so a service keeps building on the same server (and its
// per-project BuildKit cache) until that server stops being eligible. Anything
// that makes a build server unusable (none eligible, no registry, an unhealthy
// built-in registry) falls back to the target with a reason; a fallback never
// blocks a deploy.
package buildplacement

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Locations.
const (
	LocationTarget      = "target"
	LocationBuildServer = "build_server"

	inherit = "inherit"
)

// Warning codes.
const (
	// WarnFallback: the build moved to the target because no build server was usable.
	WarnFallback = "fallback"
	// WarnEmulation: the chosen build server builds the image under emulation (slow).
	WarnEmulation = "emulation"
	// WarnColdCache: the chosen build server has not built this project before.
	WarnColdCache = "cold_cache"
)

// Warning is a plan or event notice about the placement.
type Warning struct {
	Code    string `json:"code" enum:"fallback,emulation,cold_cache"`
	Message string `json:"message"`
}

// Placement is where one build runs.
type Placement struct {
	Location        string     `json:"location" enum:"target,build_server"`
	BuildServerID   *uuid.UUID `json:"buildServerId"`
	BuildServerName string     `json:"buildServerName,omitempty"`
	RegistryID      *uuid.UUID `json:"registryId"`
	RegistryHost    string     `json:"registryHost,omitempty"`
	// Platform is the platform of the image to build, e.g. linux/amd64 (the target's).
	Platform string `json:"platform"`
	// Reason is the human text for the plan and the events ("pinned", "sticky", ...).
	Reason string `json:"reason"`
	// Sticky is true when the build server came from rendezvous hashing (no pin).
	Sticky bool `json:"sticky"`
	// Emulated is true when the build server's architecture differs from the target's.
	Emulated bool `json:"emulated"`
	// Fallback is true when a build server was wanted but none was usable.
	Fallback bool `json:"fallback"`
	// MaxConcurrent is the chosen build server's concurrency cap (0 for target).
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// Scope is what Resolve needs to know about the deploying service.
type Scope struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	ServiceID   uuid.UUID
	// Location, BuildServerID and RegistryID are the service's own override
	// (services.Config); "" or "inherit" defers to the project, then the team.
	Location      string
	BuildServerID *uuid.UUID
	RegistryID    *uuid.UUID
	// Target is the server the service deploys to.
	Target db.Server
}

// FallbackMessage is the notice shown when a build falls back to the target.
func FallbackMessage(targetName, why string) string {
	return fmt.Sprintf("No build server available; building on %s (%s).", targetName, why)
}

// Platform returns "linux/<arch>" for the architecture a server reports ("" if unknown).
func Platform(facts json.RawMessage) string {
	return platformOf(decodeFacts(facts).Arch)
}

type facts struct {
	Arch         string   `json:"arch"`
	Capabilities []string `json:"capabilities"`
	Emulation    []string `json:"emulation"`
}

func decodeFacts(raw json.RawMessage) facts {
	var f facts
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

func platformOf(arch string) string {
	switch strings.ToLower(arch) {
	case "":
		return ""
	case "x86_64":
		return "linux/amd64"
	case "aarch64":
		return "linux/arm64"
	}
	return "linux/" + strings.ToLower(arch)
}

// Effective is the location, registry and pin after service -> project -> team
// resolution, before any server is chosen.
type Effective struct {
	Location      string
	RegistryID    *uuid.UUID
	BuildServerID *uuid.UUID
}

// EffectiveFor resolves the configured placement without looking at server liveness.
func EffectiveFor(ctx context.Context, q *db.Queries, sc Scope) (Effective, error) {
	proj, err := q.GetProject(ctx, db.GetProjectParams{ID: sc.ProjectID, WorkspaceID: sc.WorkspaceID})
	if err != nil {
		return Effective{}, fmt.Errorf("load project: %w", err)
	}
	team := db.TeamBuildSetting{BuildLocation: LocationTarget}
	switch t, err := q.GetTeamBuildSettings(ctx, sc.WorkspaceID); {
	case err == nil:
		team = t
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return Effective{}, fmt.Errorf("load team build settings: %w", err)
	}

	e := Effective{Location: team.BuildLocation}
	switch {
	case !isInherit(sc.Location):
		e.Location = sc.Location
	case !isInherit(proj.BuildLocation):
		e.Location = proj.BuildLocation
	}
	e.RegistryID = firstUUID(sc.RegistryID, proj.RegistryID, team.RegistryID)
	if e.Location == LocationBuildServer {
		e.BuildServerID = firstUUID(sc.BuildServerID, proj.BuildServerID)
	}
	return e, nil
}

func isInherit(loc string) bool { return loc == "" || loc == inherit }

func firstUUID(ids ...*uuid.UUID) *uuid.UUID {
	for _, id := range ids {
		if id != nil && *id != uuid.Nil {
			return id
		}
	}
	return nil
}

// Resolve decides where the build of the service runs. isPreview marks a pull
// request preview: previews never build on a build server unless the server
// opted in with build_allow_previews. It returns Location "target" (with a
// fallback warning when a build server was wanted) whenever no usable build
// server or registry exists.
func Resolve(ctx context.Context, q *db.Queries, sc Scope, isPreview bool) (Placement, []Warning, error) {
	target := Placement{Location: LocationTarget, Platform: Platform(sc.Target.Facts)}
	if target.Platform == "" {
		target.Platform = "linux/amd64"
	}
	eff, err := EffectiveFor(ctx, q, sc)
	if err != nil {
		return Placement{}, nil, err
	}
	if eff.Location != LocationBuildServer {
		target.Reason = "configured to build on the target server"
		return target, nil, nil
	}
	fallback := func(why string) (Placement, []Warning, error) {
		target.Fallback = true
		target.Reason = "fallback: " + why
		return target, []Warning{{Code: WarnFallback, Message: FallbackMessage(sc.Target.Name, why)}}, nil
	}

	reg, why, err := loadRegistry(ctx, q, sc.WorkspaceID, eff.RegistryID)
	if err != nil {
		return Placement{}, nil, err
	}
	if why != "" {
		return fallback(why)
	}
	// The built-in registry is reached through the agent's local tunnel endpoint:
	// the target pulls from it and the build server pushes to it, so both agents
	// must offer it.
	builtin := reg.Kind == "builtin"
	if builtin && !slices.Contains(decodeFacts(sc.Target.Facts).Capabilities, agentprotocol.CapInstanceTunnel) {
		return fallback(builtinTunnelWhy(sc.Target.Name))
	}

	servers, err := q.ListBuildServers(ctx, sc.WorkspaceID)
	if err != nil {
		return Placement{}, nil, fmt.Errorf("list build servers: %w", err)
	}
	targetPlat := Platform(sc.Target.Facts)

	type candidate struct {
		srv      db.Server
		platform string
		emulated bool
	}
	var eligible []candidate
	reasons := map[uuid.UUID]string{}
	for _, srv := range servers {
		plat, emu, why := eligibility(srv, targetPlat, isPreview)
		if why == "" && builtin && !slices.Contains(decodeFacts(srv.Facts).Capabilities, agentprotocol.CapInstanceTunnel) {
			why = builtinTunnelWhy(srv.Name)
		}
		if why != "" {
			reasons[srv.ID] = why
			continue
		}
		eligible = append(eligible, candidate{srv: srv, platform: plat, emulated: emu})
	}

	var pick *candidate
	sticky := false
	if eff.BuildServerID != nil {
		// An explicit pin wins; an unusable pin falls back to the target, not to another server.
		for i := range eligible {
			if eligible[i].srv.ID == *eff.BuildServerID {
				pick = &eligible[i]
			}
		}
		if pick == nil {
			name := eff.BuildServerID.String()
			why := "not a build server of this team"
			for _, srv := range servers {
				if srv.ID == *eff.BuildServerID {
					name, why = srv.Name, reasons[srv.ID]
				}
			}
			return fallback(fmt.Sprintf("pinned build server %s is unusable: %s", name, why))
		}
	} else {
		if len(eligible) == 0 {
			why := "no eligible build server"
			if len(servers) == 0 {
				why = "no build server is set up"
			} else {
				var parts []string
				for _, srv := range servers {
					parts = append(parts, srv.Name+": "+reasons[srv.ID])
				}
				why += " (" + strings.Join(parts, "; ") + ")"
			}
			return fallback(why)
		}
		best := 0
		for i := 1; i < len(eligible); i++ {
			if score(eligible[i].srv.ID, sc.ServiceID) > score(eligible[best].srv.ID, sc.ServiceID) {
				best = i
			}
		}
		pick, sticky = &eligible[best], true
	}

	p := Placement{
		Location: LocationBuildServer, BuildServerID: &pick.srv.ID, BuildServerName: pick.srv.Name,
		RegistryID: &reg.ID, RegistryHost: reg.Host, Platform: pick.platform, Sticky: sticky, Emulated: pick.emulated,
		MaxConcurrent: int(pick.srv.BuildMaxConcurrent),
	}
	if targetPlat != "" {
		p.Platform = targetPlat
	}
	if sticky {
		p.Reason = "sticky: chosen for this service among eligible build servers"
	} else {
		p.Reason = "pinned build server"
	}
	var warns []Warning
	if pick.emulated {
		warns = append(warns, Warning{Code: WarnEmulation, Message: fmt.Sprintf("%s builds %s under emulation; the build will be slow.", pick.srv.Name, p.Platform)})
	}
	if n, err := q.CountProjectBuildsOnServer(ctx, db.CountProjectBuildsOnServerParams{ProjectID: sc.ProjectID, BuildServerID: &pick.srv.ID}); err == nil && n == 0 {
		warns = append(warns, Warning{Code: WarnColdCache, Message: fmt.Sprintf("%s has not built this project yet; the first build runs without a warm BuildKit cache.", pick.srv.Name)})
	}
	return p, warns, nil
}

// builtinTunnelWhy is why a server cannot use the built-in registry.
func builtinTunnelWhy(server string) string {
	return "the agent on " + server + " cannot reach the built-in registry (update the agent)"
}

// eligibility reports whether a build-enabled server can build for the target
// platform. why is "" when it can. platform is the server's native platform;
// emulated says the target platform differs and emulation is allowed and reported.
func eligibility(srv db.Server, targetPlat string, isPreview bool) (platform string, emulated bool, why string) {
	f := decodeFacts(srv.Facts)
	platform = platformOf(f.Arch)
	switch {
	case !srv.BuildEnabled:
		return platform, false, "build role is off"
	case srv.Status == "revoked":
		return platform, false, "revoked"
	// The sweep job flips the status column every 10 s; a server silent for longer
	// than OfflineAfter is offline already (the API reports it so).
	case srv.Status != "online" || srv.LastSeenAt == nil || time.Since(*srv.LastSeenAt) > agentprotocol.OfflineAfter:
		return platform, false, "offline"
	case !slices.Contains(f.Capabilities, agentprotocol.CapBuildPush):
		return platform, false, "agent cannot build and push (update the agent)"
	case isPreview && !srv.BuildAllowPreviews:
		return platform, false, "previews do not build on this server"
	}
	if targetPlat != "" && platform != targetPlat {
		if !srv.BuildAllowEmulation {
			return platform, false, fmt.Sprintf("builds %s, the target is %s, and emulation is off", platform, targetPlat)
		}
		if !slices.Contains(f.Emulation, targetPlat) {
			return platform, false, fmt.Sprintf("does not report emulation for %s", targetPlat)
		}
		return platform, true, ""
	}
	return platform, false, ""
}

// score is the rendezvous (highest-random-weight) hash of a server for a service.
func score(serverID, serviceID uuid.UUID) uint64 {
	h := sha256.New()
	h.Write(serverID[:])
	h.Write(serviceID[:])
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}

// loadRegistry returns the registry a build pushes to. why is non-empty when it
// cannot be used (none configured, not of this team, an unhealthy built-in one).
func loadRegistry(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, id *uuid.UUID) (db.GetRegistryPlacementInfoRow, string, error) {
	if id == nil {
		return db.GetRegistryPlacementInfoRow{}, "no registry is configured", nil
	}
	reg, err := q.GetRegistryPlacementInfo(ctx, *id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && reg.WorkspaceID != workspaceID) {
		return db.GetRegistryPlacementInfoRow{}, "the configured registry no longer exists", nil
	}
	if err != nil {
		return db.GetRegistryPlacementInfoRow{}, "", fmt.Errorf("load registry: %w", err)
	}
	if reg.Kind == "builtin" && reg.PlatformStatus != "ready" {
		return reg, "the built-in registry is not healthy", nil
	}
	return reg, "", nil
}

// Slots accounts the build concurrency of one build server. The check and the
// insert of the build_image command must run in the same transaction: Acquire
// takes a per-server advisory lock that is held until the transaction ends.
type Slots struct{}

// Acquire reports whether the server has a free build slot. When it has not,
// the build queues: the caller keeps the deployment in preparing and retries.
// position is the number of builds ahead (running or queued) when no slot is free.
func (Slots) Acquire(ctx context.Context, tx pgx.Tx, serverID uuid.UUID, maxConcurrent int) (ok bool, running int, err error) {
	q := db.New(tx)
	if err := q.LockBuildServerSlots(ctx, serverID.String()); err != nil {
		return false, 0, fmt.Errorf("lock build slots: %w", err)
	}
	n, err := q.CountServerActiveBuilds(ctx, serverID)
	if err != nil {
		return false, 0, fmt.Errorf("count active builds: %w", err)
	}
	return int(n) < maxConcurrent, int(n), nil
}

// RepositoryName is the registry repository path of a service's images without
// the host: "<namespace>/<project>-<service>" (no leading slash when the
// namespace is empty).
func RepositoryName(namespace, projectSlug, serviceSlug string) string {
	name := projectSlug + "-" + serviceSlug
	if ns := strings.Trim(namespace, "/"); ns != "" {
		return ns + "/" + name
	}
	return name
}

// Tag is the registry tag of a release's image.
func Tag(releaseID uuid.UUID) string { return "r-" + agentprotocol.Short(releaseID.String()) }

// BuilderName is the per-project BuildKit builder on a build server.
func BuilderName(projectID uuid.UUID) string {
	return "nebulactrl-buildkit-" + agentprotocol.Short(projectID.String())
}

// ValidateTargets checks that a pinned build server and a registry exist in the
// team (a server of another team is as good as missing). A pin on a server that
// is not build-enabled is refused too: it could never be used.
func ValidateTargets(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, serverID, registryID *uuid.UUID) error {
	if serverID != nil {
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID, WorkspaceID: &workspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Unprocessable("build_server_not_found", "the selected build server does not exist")
		}
		if err != nil {
			return fmt.Errorf("load build server: %w", err)
		}
		if !srv.BuildEnabled {
			return apierr.Unprocessable("not_a_build_server", srv.Name+" is not a build server; turn on its build role first")
		}
	}
	if registryID != nil {
		reg, err := q.GetRegistryPlacementInfo(ctx, *registryID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && reg.WorkspaceID != workspaceID) {
			return apierr.Unprocessable("registry_not_found", "the selected registry does not exist")
		}
		if err != nil {
			return fmt.Errorf("load registry: %w", err)
		}
	}
	return nil
}

// ValidLocation reports whether loc is a stored placement value ("inherit"
// only where allowed).
func ValidLocation(loc string, allowInherit bool) bool {
	switch loc {
	case LocationTarget, LocationBuildServer:
		return true
	case inherit:
		return allowInherit
	}
	return false
}
