package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// VolumeView is a named volume attached to a service.
type VolumeView struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	MountPath  string    `json:"mountPath"`
	DockerName string    `json:"dockerName"`
	// RestoreLabel says why the row points at a restored Docker volume
	// ("restored from Jan 12, 03:30"); empty for a regular volume.
	RestoreLabel string `json:"restoreLabel,omitempty"`
}

// DockerVolumeName is the Docker volume behind a volume row: the restored
// volume a backup restore swapped in, else the one derived from the row's name.
func DockerVolumeName(v db.Volume) string {
	if v.DockerName != "" {
		return v.DockerName
	}
	return agentprotocol.VolumeName(v.ServiceID.String(), v.Name)
}

func toVolumeView(v db.Volume) VolumeView {
	return VolumeView{ID: v.ID, Name: v.Name, MountPath: v.MountPath, DockerName: DockerVolumeName(v), RestoreLabel: v.RestoreLabel}
}

var volumeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// ListVolumes returns a service's volumes.
func (s *Service) ListVolumes(ctx context.Context, serviceID uuid.UUID) ([]VolumeView, error) {
	if _, err := LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceVolumes(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	out := make([]VolumeView, len(rows))
	for i, r := range rows {
		out[i] = toVolumeView(r)
	}
	return out, nil
}

// AddVolume attaches a named volume. The change is staged until the next deploy.
func (s *Service) AddVolume(ctx context.Context, serviceID uuid.UUID, name, mountPath string) (VolumeView, error) {
	sc, err := LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return VolumeView{}, err
	}
	var view VolumeView
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		view, err = s.AddVolumeTx(ctx, tx, sc, name, mountPath)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return VolumeView{}, err
		}
		return VolumeView{}, fmt.Errorf("add volume: %w", err)
	}
	PublishService(s.bus, EventServiceUpdated, sc)
	return view, nil
}

// AddVolumeTx is AddVolume inside the caller's transaction: same validation,
// the service's generation moves and volume.create is audited. sc is the
// service's scope, loaded through the same transaction with projects:write.
// It publishes no event.
func (s *Service) AddVolumeTx(ctx context.Context, tx pgx.Tx, sc Scope, name, mountPath string) (VolumeView, error) {
	if err := RequireContainer(sc, "volumes"); err != nil {
		return VolumeView{}, err
	}
	if err := RequireNotStack(sc); err != nil {
		return VolumeView{}, err
	}
	name = strings.TrimSpace(name)
	if !volumeNameRe.MatchString(name) {
		return VolumeView{}, apierr.Unprocessable("invalid_volume_name", "volume names are lowercase letters, digits, '.', '_' and '-' (max 63 characters)")
	}
	mountPath = path.Clean(strings.TrimSpace(mountPath))
	if !strings.HasPrefix(mountPath, "/") || mountPath == "/" || strings.Contains(mountPath, ":") {
		return VolumeView{}, apierr.Unprocessable("invalid_mount_path", "mountPath must be an absolute path other than /")
	}
	serviceID := sc.Service.ID
	q := db.New(tx)
	existing, err := q.ListServiceVolumes(ctx, serviceID)
	if err != nil {
		return VolumeView{}, fmt.Errorf("list volumes: %w", err)
	}
	for _, e := range existing {
		if e.Name == name {
			return VolumeView{}, apierr.Conflict("volume_exists", "a volume with that name already exists")
		}
		if e.MountPath == mountPath {
			return VolumeView{}, apierr.Conflict("mount_path_in_use", "another volume is already mounted at that path")
		}
	}
	vol, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: serviceID, Name: name, MountPath: mountPath})
	if IsUniqueViolation(err) {
		return VolumeView{}, apierr.Conflict("volume_exists", "a volume with that name already exists")
	}
	if err != nil {
		return VolumeView{}, fmt.Errorf("insert volume: %w", err)
	}
	if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
		return VolumeView{}, fmt.Errorf("bump generation: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "volume.create", ResourceType: "volume", ResourceID: vol.ID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": sc.Service.Slug, "name": name, "mountPath": mountPath},
	}); err != nil {
		return VolumeView{}, err
	}
	return toVolumeView(vol), nil
}

// DeleteVolume detaches a volume (staged). The Docker volume and its data are
// kept on the host.
func (s *Service) DeleteVolume(ctx context.Context, volumeID uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return err
	}
	vol, err := s.q.GetVolumeByID(ctx, volumeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("volume")
	}
	if err != nil {
		return fmt.Errorf("get volume: %w", err)
	}
	sc, err := LoadScope(ctx, s.q, policy.ScopeProjectsWrite, vol.ServiceID)
	if err != nil {
		return err
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		if err := prodguard.Check(ctx,
			sc.ProdScope(fmt.Sprintf("Detach the volume %s from %s in production (%s / %s)", vol.Name, sc.Service.Name, sc.ProjectName, sc.EnvName), false,
				"Stages the removal of the volume mounted at "+vol.MountPath+"; it takes effect with the next deployment.",
				"The Docker volume and its data stay on the host, but the service no longer mounts them."),
			"delete_volume", prodguard.Digest("delete_volume", volumeID)); err != nil {
			return err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return detachVolume(ctx, tx, sc, vol)
	})
	if err != nil {
		return fmt.Errorf("delete volume: %w", err)
	}
	PublishService(s.bus, EventServiceUpdated, sc)
	return nil
}

// DeleteVolumeTx detaches the volume called name inside the caller's
// transaction (staged: it takes effect with the next deploy; the Docker
// volume and its data are never deleted). An unknown name is a 404. sc is the
// service's scope, loaded through the same transaction with projects:write.
// The production guard is the caller's business. It publishes no event.
func (s *Service) DeleteVolumeTx(ctx context.Context, tx pgx.Tx, sc Scope, name string) error {
	vols, err := db.New(tx).ListServiceVolumes(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range vols {
		if v.Name == name {
			return detachVolume(ctx, tx, sc, v)
		}
	}
	return apierr.NotFound("volume")
}

// detachVolume removes a volume row, moves the service's generation and
// audits volume.delete.
func detachVolume(ctx context.Context, tx pgx.Tx, sc Scope, vol db.Volume) error {
	q := db.New(tx)
	if _, err := q.DeleteVolume(ctx, vol.ID); err != nil {
		return err
	}
	if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{
		Action: "volume.delete", ResourceType: "volume", ResourceID: vol.ID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": sc.Service.Slug, "name": vol.Name},
	})
}

// ---- links ----

// Link sources.
const (
	LinkManual    = "manual"
	LinkReference = "reference"
)

// LinkReferenceKey is a variable of the consumer that references the provider.
type LinkReferenceKey struct {
	Key    string `json:"key"`
	Staged bool   `json:"staged" doc:"The source service's running release does not hold this reference yet; it applies on its next deploy"`
}

// LinkView is a link between two services: a manual edge drawn by the user or
// one derived from `${{slug.KEY}}` variable references (consumer → provider).
type LinkView struct {
	// ID is a UUID for manual links and "ref:<fromId>:<toId>" for derived ones.
	ID            string             `json:"id"`
	EnvironmentID uuid.UUID          `json:"environmentId"`
	FromServiceID uuid.UUID          `json:"fromServiceId"`
	ToServiceID   uuid.UUID          `json:"toServiceId"`
	Source        string             `json:"source" enum:"manual,reference" doc:"A manual link that is also referenced by variables reports manual"`
	References    []LinkReferenceKey `json:"references" doc:"Variables of the source service that reference the target"`
	Staged        bool               `json:"staged" doc:"Every reference that makes this link is staged: the edge is drawn but not live until the source deploys"`
	CreatedAt     time.Time          `json:"createdAt"`
}

func toLinkView(l db.ServiceLink) LinkView {
	return LinkView{
		ID: l.ID.String(), EnvironmentID: l.EnvironmentID, FromServiceID: l.FromServiceID, ToServiceID: l.ToServiceID,
		Source: LinkManual, References: []LinkReferenceKey{}, CreatedAt: l.CreatedAt,
	}
}

// ProposedVariable is a variable the web proposes adding to the source service.
type ProposedVariable struct {
	Key   string `json:"key"`
	Value string `json:"value" doc:"A reference such as ${{postgres.DATABASE_URL}}"`
	// Recommended marks the set add_database and the connect popover select by default.
	Recommended bool   `json:"recommended" doc:"Part of the one-click recommended set"`
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret" doc:"The reference resolves to a secret value"`
	Status      string `json:"status,omitempty" enum:"new,unchanged,replaces" doc:"Only on a link preview: new = the source has no such variable, unchanged = it already holds this reference, replaces = it holds another value"`
}

// LinkResult is the response of creating a link. Proposed variables are NOT
// added: the client adds them after the user confirms.
type LinkResult struct {
	Link              LinkView           `json:"link"`
	ProposedVariables []ProposedVariable `json:"proposedVariables"`
	Warnings          []string           `json:"warnings"`
	// Mesh is what the edge needs from the mesh when the two services run on
	// different servers (the links and devices it opened, or what blocks it).
	Mesh MeshPlan `json:"mesh"`
}

// CrossServerWarning is shown by flows that cannot open the mesh links
// themselves (the MCP composites) when linked services run on different servers.
const CrossServerWarning = "These services run on different servers: they reach each other over the mesh. Enable the mesh on both servers (Team settings → Server mesh), then deploy: the deployment plan lists the links and devices it adds."

// LinkOption tunes CreateLink.
type LinkOption func(*linkOpts)

type linkOpts struct{ acceptMesh bool }

// AcceptMeshChanges confirms the tailnet devices the edge adds when its two
// services run on different servers (the review dialog states how many).
func AcceptMeshChanges() LinkOption { return func(o *linkOpts) { o.acceptMesh = true } }

// ListLinks returns the links of an environment: the manual ones plus the
// edges derived from variable references.
func (s *Service) ListLinks(ctx context.Context, envID uuid.UUID) ([]LinkView, error) {
	if _, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnvironmentLinks(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	edges, err := ReferenceEdges(ctx, s.q, s.box, envID)
	if err != nil {
		return nil, err
	}
	links := MergeLinks(envID, rows, edges)
	if err := s.markStaged(ctx, envID, links); err != nil {
		return nil, err
	}
	return links, nil
}

// CreateLink records an edge from one service to another and proposes the
// variables the source would need to reach the target.
func (s *Service) CreateLink(ctx context.Context, envID, fromID, toID uuid.UUID, opts ...LinkOption) (LinkResult, error) {
	var lo linkOpts
	for _, o := range opts {
		o(&lo)
	}
	env, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return LinkResult{}, err
	}
	if err := env.Guard(ctx, "createLink", fmt.Sprintf("Link two services of the production environment %s", env.Env.Name), false,
		[]string{
			"When they run on different servers the link opens a mesh grant between them at once.",
			"From service " + fromID.String() + " to service " + toID.String() + ".",
		}, fromID, toID, lo.acceptMesh); err != nil {
		return LinkResult{}, err
	}
	var (
		res   LinkResult
		after func(context.Context)
	)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, after, err = s.CreateLinkTx(ctx, tx, env, fromID, toID, opts...)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return LinkResult{}, err
		}
		return LinkResult{}, fmt.Errorf("create link: %w", err)
	}
	after(ctx)
	return res, nil
}

// CreateLinkTx is CreateLink inside the caller's transaction. env is the
// environment of both services, loaded with projects:write (LoadEnvironment);
// the two services are loaded here through tx. Same checks (no self link, both
// services in env, no duplicate, the mesh confirmation of AcceptMeshChanges),
// same mesh grants and audit entry. The returned function, which the caller
// runs once the transaction committed, publishes the link event; it is never
// nil.
//
// The mesh planner reads committed rows only. When it cannot see one of the
// services (it was created in this transaction) an edge between services on
// two different servers needs AcceptMeshChanges, because the devices it adds
// cannot be counted.
func (s *Service) CreateLinkTx(ctx context.Context, tx pgx.Tx, env EnvScope, fromID, toID uuid.UUID, opts ...LinkOption) (LinkResult, func(context.Context), error) {
	noop := func(context.Context) {}
	var lo linkOpts
	for _, o := range opts {
		o(&lo)
	}
	envID := env.Env.ID
	if fromID == toID {
		return LinkResult{}, noop, apierr.Unprocessable("invalid_link", "a service cannot link to itself")
	}
	q := db.New(tx)
	from, err := LoadScope(ctx, q, policy.ScopeProjectsWrite, fromID)
	if err != nil {
		return LinkResult{}, noop, err
	}
	to, err := LoadScope(ctx, q, policy.ScopeProjectsWrite, toID)
	if err != nil {
		return LinkResult{}, noop, err
	}
	if from.EnvironmentID != envID || to.EnvironmentID != envID {
		return LinkResult{}, noop, apierr.Unprocessable("invalid_link", "both services must belong to this environment")
	}
	plan := EmptyMeshPlan()
	if s.mesh != nil {
		p, err := s.mesh.PlanEdge(ctx, envID, fromID, toID)
		switch ae, isAPI := apierr.As(err); {
		case err == nil:
			plan = p
			if plan.CrossServer && plan.DevicesAdded > 0 && len(plan.Blockers) == 0 && !lo.acceptMesh {
				return LinkResult{}, noop, apierr.Conflict(CodeMeshConfirmationRequired,
					fmt.Sprintf("this link adds %d tailnet %s (tagged devices may be billed by Tailscale): confirm with acceptMeshChanges", plan.DevicesAdded, plural(plan.DevicesAdded, "device", "devices"))).
					WithDetails(map[string]any{"devicesAdded": plan.DevicesAdded, "mesh": plan})
			}
		case isAPI && ae.Status == http.StatusNotFound:
			if from.Service.ServerID != nil && to.Service.ServerID != nil && !sameUUID(from.Service.ServerID, to.Service.ServerID) && !lo.acceptMesh {
				return LinkResult{}, noop, apierr.Conflict(CodeMeshConfirmationRequired,
					"this link joins services on different servers and may add tailnet devices (tagged devices may be billed by Tailscale): confirm with acceptMeshChanges")
			}
		default:
			return LinkResult{}, noop, err
		}
	}
	existing, err := q.ListEnvironmentLinks(ctx, envID)
	if err != nil {
		return LinkResult{}, noop, fmt.Errorf("list links: %w", err)
	}
	for _, l := range existing {
		if l.FromServiceID == fromID && l.ToServiceID == toID {
			return LinkResult{}, noop, apierr.Conflict("link_exists", "these services are already linked")
		}
	}
	link, err := q.InsertLink(ctx, db.InsertLinkParams{EnvironmentID: envID, FromServiceID: fromID, ToServiceID: toID})
	if IsUniqueViolation(err) {
		return LinkResult{}, noop, apierr.Conflict("link_exists", "these services are already linked")
	}
	if err != nil {
		return LinkResult{}, noop, err
	}
	if s.mesh != nil {
		if err := s.mesh.EdgeCreated(ctx, tx, envID, fromID, toID); err != nil {
			return LinkResult{}, noop, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "link.create", ResourceType: "link", ResourceID: link.ID.String(), ProjectID: &env.Env.ProjectID,
		Metadata: map[string]any{"from": from.Service.Slug, "to": to.Service.Slug},
	}); err != nil {
		return LinkResult{}, noop, err
	}

	res := LinkResult{Link: toLinkView(link), ProposedVariables: []ProposedVariable{}, Warnings: []string{}, Mesh: plan}
	vars, err := q.ListServiceVariables(ctx, fromID)
	if err != nil {
		return LinkResult{}, noop, fmt.Errorf("list variables: %w", err)
	}
	have := map[string]bool{}
	for _, v := range vars {
		have[v.Key] = true
	}
	for _, p := range ProposeLinkVariables(to.Service.Slug, to.Service.Kind, to.Config()) {
		if !have[p.Key] {
			res.ProposedVariables = append(res.ProposedVariables, p)
		}
	}
	projectID := env.Env.ProjectID
	after := func(context.Context) { Publish(s.bus, EventLinkUpdated, projectID, envID, link.ID) }
	return res, after, nil
}

// DeleteLink removes a manual link. Links derived from variable references
// ("ref:…" ids) cannot be deleted: they disappear when the references do.
func (s *Service) DeleteLink(ctx context.Context, id string) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return err
	}
	linkID, err := parseLinkID(id)
	if err != nil {
		return err
	}
	link, err := s.q.GetLinkByID(ctx, linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("link")
	}
	if err != nil {
		return fmt.Errorf("get link: %w", err)
	}
	env, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, link.EnvironmentID)
	if err != nil {
		return err
	}
	if err := env.Guard(ctx, "deleteLink", fmt.Sprintf("Remove a link between services of the production environment %s", env.Env.Name), false,
		[]string{
			"The link and its mesh grant are removed at once.",
			"From service " + link.FromServiceID.String() + " to service " + link.ToServiceID.String() + ".",
		}, linkID); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.removeLink(ctx, tx, env, link)
	})
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	Publish(s.bus, EventLinkUpdated, env.Env.ProjectID, link.EnvironmentID, linkID)
	return nil
}

// DeleteLinkTx removes the manual link from fromID to toID inside the
// caller's transaction (env: LoadEnvironment with projects:write). It reports
// whether a link existed; deleting a missing link is not an error. Mesh
// grants the edge opened are removed and link.delete is audited. The returned
// function, run by the caller after the commit, publishes the link event
// (a no-op when nothing was deleted); it is never nil.
func (s *Service) DeleteLinkTx(ctx context.Context, tx pgx.Tx, env EnvScope, fromID, toID uuid.UUID) (bool, func(context.Context), error) {
	noop := func(context.Context) {}
	links, err := db.New(tx).ListEnvironmentLinks(ctx, env.Env.ID)
	if err != nil {
		return false, noop, fmt.Errorf("list links: %w", err)
	}
	for _, l := range links {
		if l.FromServiceID != fromID || l.ToServiceID != toID {
			continue
		}
		if err := s.removeLink(ctx, tx, env, l); err != nil {
			return false, noop, err
		}
		projectID, envID, linkID := env.Env.ProjectID, env.Env.ID, l.ID
		return true, func(context.Context) { Publish(s.bus, EventLinkUpdated, projectID, envID, linkID) }, nil
	}
	return false, noop, nil
}

// removeLink drops the mesh grants of a manual link, deletes it and audits
// link.delete.
func (s *Service) removeLink(ctx context.Context, tx pgx.Tx, env EnvScope, link db.ServiceLink) error {
	if s.mesh != nil {
		if err := s.mesh.EdgeDeleted(ctx, tx, link.EnvironmentID, link.FromServiceID, link.ToServiceID); err != nil {
			return err
		}
	}
	if _, err := db.New(tx).DeleteLink(ctx, link.ID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{
		Action: "link.delete", ResourceType: "link", ResourceID: link.ID.String(), ProjectID: &env.Env.ProjectID,
	})
}
