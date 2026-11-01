package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func noPendingChange(field string) error {
	return apierr.NotFound("pending change").WithDetails(map[string]any{"field": field})
}

// DiscardPendingChange reverts one staged change to the state of the service's
// current release and advances the generation. field is an item of
// pending-changes: `config.<path>`, `server`, `variables.<KEY>`,
// `domains.<host>` or `volumes.<name>`. A field that is not pending is 404.
// It returns the updated service.
func (s *Service) DiscardPendingChange(ctx context.Context, serviceID uuid.UUID, field string) (services.ServiceView, error) {
	if f, err := url.PathUnescape(field); err == nil {
		field = f
	}
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return services.ServiceView{}, err
	}
	var unlinked *db.ServiceGit
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		svc, err := q.LockServiceRow(ctx, serviceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("service")
		}
		if err != nil {
			return fmt.Errorf("lock service: %w", err)
		}
		if svc.CurrentDeploymentID == nil {
			return noPendingChange(field)
		}
		deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*svc.CurrentDeploymentID})
		if err != nil {
			return fmt.Errorf("current deployment: %w", err)
		}
		if len(deps) != 1 || deps[0].ReleaseID == nil {
			return noPendingChange(field)
		}
		rel, err := q.GetReleaseByID(ctx, *deps[0].ReleaseID)
		if err != nil {
			return fmt.Errorf("get release: %w", err)
		}
		cur, err := services.LoadCurrentState(ctx, q, s.Box, svc)
		if err != nil {
			return err
		}
		changes, err := services.DiffRelease(ctx, q, s.Box, rel, cur)
		if err != nil {
			return err
		}
		var change *services.FieldChange
		for i := range changes {
			if changes[i].Field == field {
				change = &changes[i]
				break
			}
		}
		if change == nil {
			return noPendingChange(field)
		}
		if change.Origin == services.OriginInherited {
			return apierr.Conflict("inherited_change", "this variable changed because of "+change.Via+"; discard that change instead")
		}
		snap := services.DecodeSnapshot(rel.Config)
		switch {
		case strings.HasPrefix(field, "config."):
			err = s.discardConfig(ctx, q, svc, snap, strings.TrimPrefix(field, "config."))
			if err == nil && svc.Kind == services.KindRoute {
				err = resyncRoutePort(ctx, q, svc.ID)
			}
		case field == services.FieldSource:
			unlinked, err = s.discardSource(ctx, q, svc, snap, rel)
		case field == "server":
			_, err = q.SetServiceServerBumped(ctx, db.SetServiceServerBumpedParams{ID: svc.ID, ServerID: snap.ServerID})
		case strings.HasPrefix(field, "variables."):
			err = s.discardVariable(ctx, q, sc, rel, strings.TrimPrefix(field, "variables."))
		case strings.HasPrefix(field, "domains."):
			err = s.discardDomain(ctx, tx, q, svc, snap, strings.TrimPrefix(field, "domains."), change)
		case strings.HasPrefix(field, "volumes."):
			err = s.discardVolume(ctx, q, svc, snap, cur, strings.TrimPrefix(field, "volumes."))
		case strings.HasPrefix(field, "networks."):
			err = s.discardNetwork(ctx, q, svc, snap, cur, strings.TrimPrefix(field, "networks."))
		default:
			return noPendingChange(field)
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.discard_change", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": svc.Slug, "field": field}, WorkspaceID: sc.WorkspaceID,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return services.ServiceView{}, err
		}
		if services.IsUniqueViolation(err) {
			return services.ServiceView{}, apierr.Conflict("discard_conflict", "the previous value conflicts with another resource; remove that first")
		}
		return services.ServiceView{}, fmt.Errorf("discard change: %w", err)
	}
	s.Services.RepoUnlinked(ctx, unlinked)
	services.PublishService(s.Bus, services.EventServiceUpdated, sc)
	if strings.HasPrefix(field, "variables.") {
		services.Publish(s.Bus, services.EventVariableUpdated, sc.ProjectID, sc.EnvironmentID, serviceID)
	}
	if strings.HasPrefix(field, "domains.") {
		services.Publish(s.Bus, services.EventDomainUpdated, sc.ProjectID, sc.EnvironmentID, serviceID)
	}
	fresh, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return services.ServiceView{}, err
	}
	return s.Services.ViewFor(ctx, fresh)
}

// DiscardFields discards each of the given staged changes (items of
// pending-changes, as DiscardPendingChange takes them). A field that is not
// pending any more is skipped, so a caller that must undo exactly what it staged
// (the combine abort) can retry. Each field is its own transaction: the first
// failure other than "not pending" stops the run.
func (s *Service) DiscardFields(ctx context.Context, serviceID uuid.UUID, fields []string) error {
	for _, f := range fields {
		_, err := s.DiscardPendingChange(ctx, serviceID, f)
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Code == "not_found" {
			if _, notPending := ae.Details["field"]; notPending {
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("discard %s: %w", f, err)
		}
	}
	return nil
}

// discardConfig resets one config field (dotted JSON path) to the snapshot's value.
func (s *Service) discardConfig(ctx context.Context, q *db.Queries, svc db.Service, snap services.Snapshot, path string) error {
	oldCfg := services.DecodeConfig(svc.Config)
	newCfg, err := RevertConfigPath(oldCfg, snap.Config, path)
	if err != nil {
		return err
	}
	if newCfg.Stack != nil {
		// The components follow the compose file; a component that has domains must stay.
		newCfg = services.DeriveStack(newCfg)
		if err := services.CheckComponentsInUse(ctx, q, svc.ID, newCfg); err != nil {
			return err
		}
	}
	if newCfg.HasProcesses() || oldCfg.HasProcesses() {
		// A staged rename the restored config no longer has goes with it.
		if err := services.SyncProcessRenames(ctx, q, svc.ID, newCfg); err != nil {
			return err
		}
		// A process that domains still route to must stay, with a port; its domains follow the port.
		if err := services.CheckProcessesInUse(ctx, q, svc.ID, newCfg); err != nil {
			return err
		}
		if err := services.FollowProcessPorts(ctx, q, svc.ID, newCfg); err != nil {
			return err
		}
		// The scale rows of processes that neither the restored config nor the running release has
		// (the copies a staged rename made, the settings of a staged addition) are not needed.
		keep := []string{}
		for _, c := range []services.Config{newCfg, snap.Config} {
			for _, p := range c.Processes {
				keep = append(keep, p.Name)
			}
		}
		if err := q.PruneProcessScales(ctx, db.PruneProcessScalesParams{ServiceID: svc.ID, Keep: keep}); err != nil {
			return fmt.Errorf("prune process scales: %w", err)
		}
	}
	if newCfg.Port != oldCfg.Port {
		doms, err := q.ListServiceDomains(ctx, svc.ID)
		if err != nil {
			return err
		}
		// Only the default process' domains follow the service port; those of a
		// named process keep the port of their own process.
		if slices.ContainsFunc(doms, func(d db.Domain) bool { return d.Component == "" }) {
			if newCfg.Port == 0 {
				return apierr.Unprocessable("domains_need_port", "remove the service's domains before disabling its port")
			}
			if err := q.SetServiceCurrentPortDomains(ctx, db.SetServiceCurrentPortDomainsParams{ServiceID: svc.ID, TargetPort: int32(newCfg.Port)}); err != nil {
				return err
			}
		}
	}
	raw, err := json.Marshal(newCfg)
	if err != nil {
		return err
	}
	_, err = q.SetServiceConfigBumped(ctx, db.SetServiceConfigBumpedParams{ID: svc.ID, Config: raw})
	return err
}

// discardSource moves the service back to the source type of its current
// release and restores the image, repository and build settings the release
// ran. The credentials, the repository link and the deploy key the change
// cleared are not restored. It returns the repository link it removed (moving
// back to an image), for the webhook cleanup after the commit.
func (s *Service) discardSource(ctx context.Context, q *db.Queries, svc db.Service, snap services.Snapshot, rel db.Release) (*db.ServiceGit, error) {
	relType := services.DecodeSource(rel.Source).Type
	if !services.SourceSwitched(relType, svc.SourceType) {
		return nil, noPendingChange(services.FieldSource)
	}
	cfg := services.DecodeConfig(svc.Config).WithSourceOf(snap.Config).WithDefaults()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := q.SetServiceSourceType(ctx, db.SetServiceSourceTypeParams{ID: svc.ID, SourceType: relType}); err != nil {
		return nil, fmt.Errorf("set source type: %w", err)
	}
	var unlinked *db.ServiceGit
	if relType == services.SourceImage {
		if unlinked, err = services.ClearGitSource(ctx, q, svc.ID); err != nil {
			return nil, err
		}
	}
	_, err = q.SetServiceConfigBumped(ctx, db.SetServiceConfigBumpedParams{ID: svc.ID, Config: raw})
	return unlinked, err
}

// RevertConfigPath returns cur with the field at path (dotted JSON names, e.g.
// "healthcheck.path") taken from snap. A field the snapshot does not carry is
// cleared, so defaults apply again. The processes are keyed by name for the
// revert, so "processes.worker" restores or drops one whole process and
// "processes.worker.startCommand" one field of it.
func RevertConfigPath(cur, snap services.Config, path string) (services.Config, error) {
	cm, err := configMap(cur)
	if err != nil {
		return services.Config{}, err
	}
	sm, err := configMap(snap)
	if err != nil {
		return services.Config{}, err
	}
	keyProcesses(cm)
	keyProcesses(sm)
	parts := strings.Split(path, ".")
	val, have := lookupPath(sm, parts)
	setPath(cm, parts, val, have)
	unkeyProcesses(cm)
	raw, err := json.Marshal(cm)
	if err != nil {
		return services.Config{}, err
	}
	var out services.Config
	if err := json.Unmarshal(raw, &out); err != nil {
		return services.Config{}, fmt.Errorf("decode reverted config: %w", err)
	}
	return out.WithDefaults(), nil
}

// keyProcesses turns the `processes` array of a config map into an object keyed
// by process name, so a path can address one process (the shape DiffConfig uses
// for its `config.processes.<name>` fields).
func keyProcesses(m map[string]any) {
	list, ok := m["processes"].([]any)
	if !ok {
		return
	}
	keyed := make(map[string]any, len(list))
	for _, item := range list {
		if p, ok := item.(map[string]any); ok {
			if name, ok := p["name"].(string); ok {
				keyed[name] = p
			}
		}
	}
	m["processes"] = keyed
}

// unkeyProcesses is the inverse of keyProcesses: the name-keyed object becomes
// an array again, sorted by name (WithDefaults sorts it too); an empty one is
// dropped.
func unkeyProcesses(m map[string]any) {
	keyed, ok := m["processes"].(map[string]any)
	if !ok {
		return
	}
	if len(keyed) == 0 {
		delete(m, "processes")
		return
	}
	names := make([]string, 0, len(keyed))
	for name := range keyed {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]any, len(names))
	for i, name := range names {
		p, _ := keyed[name].(map[string]any)
		if p == nil {
			p = map[string]any{}
		}
		p["name"] = name
		list[i] = p
	}
	m["processes"] = list
}

func configMap(c services.Config) (map[string]any, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func lookupPath(m map[string]any, parts []string) (any, bool) {
	var cur any = m
	for _, p := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(m map[string]any, parts []string, val any, have bool) {
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			if !have {
				return
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	last := parts[len(parts)-1]
	if have {
		cur[last] = val
	} else {
		delete(cur, last)
	}
}

// discardVariable restores a variable to its value in the release, or removes
// it when the release did not have it. A `.scope` field restores only the scope.
func (s *Service) discardVariable(ctx context.Context, q *db.Queries, sc services.Scope, rel db.Release, field string) error {
	key, scopeOnly := strings.CutSuffix(field, ".scope")
	relRaw, err := services.ReleaseRaw(s.Box, rel)
	if err != nil {
		return err
	}
	if old, ok := relRaw[key]; ok {
		w := services.VarWrite{Key: key, Value: old.Value, Secret: old.Secret, Scope: old.Scope, Op: services.OpRestore}
		if w.Scope == "" {
			w.Scope = services.ScopeRuntime
		}
		if scopeOnly {
			cur, ok := q2(ctx, q, sc.Service.ID, key)
			if !ok {
				return noPendingChange("variables." + field)
			}
			v, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(sc.Service.ID.String(), key), cur.ValueEnc)
			if err != nil {
				return err
			}
			w.Value, w.Secret = v, cur.IsSecret
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.Box, sc, w); err != nil {
			return err
		}
	} else if _, _, err := services.DeleteServiceVariableVersioned(ctx, q, s.Box, sc, key); err != nil {
		return err
	}
	_, err = q.BumpServiceGeneration(ctx, sc.Service.ID)
	return err
}

func q2(ctx context.Context, q *db.Queries, id uuid.UUID, key string) (db.ServiceVariable, bool) {
	v, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: id, Key: key})
	return v, err == nil
}

// discardVolume detaches a volume added since the release, re-attaches one that
// was removed, or restores a changed mount path and Docker volume. A restore
// swap that is discarded moves the swapped-in Docker volume to the retired
// volumes (its data stays until the user deletes it) and un-retires the
// release's volume.
func (s *Service) discardVolume(ctx context.Context, q *db.Queries, svc db.Service, snap services.Snapshot, cur services.CurrentState, name string) error {
	var snapVol *services.SnapshotVolume
	for i := range snap.Volumes {
		if snap.Volumes[i].Name == name {
			snapVol = &snap.Volumes[i]
		}
	}
	var curVol *db.Volume
	for i := range cur.Volumes {
		if cur.Volumes[i].Name == name {
			curVol = &cur.Volumes[i]
		}
	}
	if _, err := q.DeleteServiceVolumeByName(ctx, db.DeleteServiceVolumeByNameParams{ServiceID: svc.ID, Name: name}); err != nil {
		return err
	}
	if curVol != nil && curVol.DockerName != "" && (snapVol == nil || snapVol.EffectiveDockerName(svc.ID) != curVol.DockerName) {
		// The swapped-in volume is no longer mounted: keep it as a retired volume.
		if err := q.InsertRetiredVolume(ctx, db.InsertRetiredVolumeParams{
			ServiceID: svc.ID, VolumeName: name, DockerName: curVol.DockerName, Reason: "Restore discarded before it was deployed",
		}); err != nil {
			return err
		}
	}
	if snapVol != nil {
		for _, v := range cur.Volumes {
			if v.Name != name && v.MountPath == snapVol.MountPath {
				return apierr.Conflict("mount_path_in_use", "another volume is already mounted at "+snapVol.MountPath+"; discard or remove it first")
			}
		}
		vol, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: svc.ID, Name: name, MountPath: snapVol.MountPath})
		if err != nil {
			return err
		}
		if snapVol.DockerName != "" {
			if err := q.SetVolumeDockerName(ctx, db.SetVolumeDockerNameParams{ID: vol.ID, DockerName: snapVol.DockerName, RestoreLabel: snapVol.RestoreLabel}); err != nil {
				return err
			}
		}
		// The release's Docker volume is mounted again, so it is no longer retired.
		if err := q.DeleteRetiredVolumeByDockerName(ctx, db.DeleteRetiredVolumeByDockerNameParams{
			ServiceID: svc.ID, DockerName: snapVol.EffectiveDockerName(svc.ID),
		}); err != nil {
			return err
		}
	}
	_, err := q.BumpServiceGeneration(ctx, svc.ID)
	return err
}
