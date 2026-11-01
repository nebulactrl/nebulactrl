package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// CreateInput describes a new service.
type CreateInput struct {
	Name string
	// Slug is the exact slug to use; empty derives it from Name. Only
	// CreateInTx honours it (Create rejects it).
	Slug         string
	Kind         string
	SourceType   string
	Config       Config
	ServerID     *uuid.UUID
	GitToken     string
	RegistryAuth *RegistryAuth
}

// checkServer verifies the server exists in the caller's workspace and has not been revoked.
func (s *Service) checkServer(ctx context.Context, q *db.Queries, id uuid.UUID) (db.Server, error) {
	ws := policy.WorkspaceID(ctx)
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return db.Server{}, serverNotUsable(srv.Name, "revoked")
	}
	if srv.DrainState == DrainStateDraining || srv.DrainState == DrainStateDrained {
		return db.Server{}, ServerDrainingError(srv.Name)
	}
	if !srv.AcceptWorkloads {
		return db.Server{}, ServerNotAcceptingError(srv.Name)
	}
	return srv, nil
}

func (s *Service) sealRegistryAuth(id uuid.UUID, a *RegistryAuth) ([]byte, error) {
	if a == nil || (a.Username == "" && a.Password == "") {
		return nil, nil
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(raw)
	return s.box.Seal(secretbox.ServicesRegistryAuth, id.String(), raw)
}

// Create adds a service to an environment.
func (s *Service) Create(ctx context.Context, envID uuid.UUID, in CreateInput) (ServiceView, error) {
	env, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return ServiceView{}, err
	}
	name := strings.TrimSpace(in.Name)
	if in.Slug != "" {
		return ServiceView{}, apierr.Unprocessable("validation_failed", "slug cannot be chosen when creating a service here; it derives from the name")
	}
	if name == "" {
		return ServiceView{}, apierr.Unprocessable("validation_failed", "name is required")
	}
	kind, sourceType, cfg := in.Kind, in.SourceType, in.Config
	kind, sourceType, err = resolveKindSource(kind, sourceType)
	if err != nil {
		return ServiceView{}, err
	}
	if sourceType != SourceImage && sourceType != SourceGit && sourceType != SourceCompose {
		return ServiceView{}, apierr.Unprocessable("validation_failed", "sourceType must be image, git or compose")
	}
	if sourceType == SourceGit && cfg.GitBranch == "" {
		cfg.GitBranch = "main"
	}
	if sourceType == SourceImage && in.GitToken != "" {
		return ServiceView{}, apierr.Unprocessable("validation_failed", "gitToken only applies to git sources")
	}
	if sourceType == SourceGit && in.RegistryAuth != nil {
		return ServiceView{}, apierr.Unprocessable("validation_failed", "registryAuth only applies to image sources")
	}
	if sourceType == SourceCompose && (in.GitToken != "" || in.RegistryAuth != nil) {
		return ServiceView{}, apierr.Unprocessable("validation_failed", "gitToken and registryAuth do not apply to Compose stacks")
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(sourceType); err != nil {
		return ServiceView{}, ConfigError(err)
	}
	if err := checkProcessKind(kind, cfg); err != nil {
		return ServiceView{}, err
	}
	if cfg.BuildServerID != nil || cfg.RegistryID != nil {
		if err := buildplacement.ValidateTargets(ctx, s.q, env.WorkspaceID, cfg.BuildServerID, cfg.RegistryID); err != nil {
			return ServiceView{}, err
		}
	}
	if in.ServerID != nil {
		srv, err := s.checkServer(ctx, s.q, *in.ServerID)
		if err != nil {
			return ServiceView{}, err
		}
		if err := CheckDockerPlacement(cfg, srv.Unrestricted); err != nil {
			return ServiceView{}, err
		}
	}
	if kind == KindStack {
		if cfg, err = s.checkStackWrite(ctx, s.q, cfg, in.ServerID); err != nil {
			return ServiceView{}, err
		}
	}
	svcID := uuid.New()
	var gitTok []byte
	if in.GitToken != "" {
		if gitTok, err = s.box.SealString(secretbox.ServicesGitToken, svcID.String(), in.GitToken); err != nil {
			return ServiceView{}, fmt.Errorf("seal git token: %w", err)
		}
	}
	regAuth, err := s.sealRegistryAuth(svcID, in.RegistryAuth)
	if err != nil {
		return ServiceView{}, fmt.Errorf("seal registry auth: %w", err)
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return ServiceView{}, err
	}

	var row db.Service
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		base := projects.Slugify(name, "service")
		taken, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: envID, Base: base})
		if err != nil {
			return err
		}
		taken = append(taken, ReservedSlugs...) // `shared` and `vault` are reference namespaces
		row, err = q.InsertService(ctx, db.InsertServiceParams{
			ID: svcID, EnvironmentID: envID, Name: name, Slug: projects.UniqueSlug(base, taken), Kind: kind, SourceType: sourceType,
			Config: cfgJSON, GitTokenEnc: gitTok, RegistryAuthEnc: regAuth, ServerID: in.ServerID,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.create", ResourceType: "service", ResourceID: row.ID.String(), ProjectID: &env.Env.ProjectID,
			Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "kind": kind, "sourceType": sourceType},
		})
	})
	if IsUniqueViolation(err) {
		return ServiceView{}, apierr.Conflict("slug_conflict", "a service with that name was just created; retry")
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ServiceView{}, err
		}
		return ServiceView{}, fmt.Errorf("create service: %w", err)
	}
	sc := Scope{
		Service: row, WorkspaceID: env.WorkspaceID, ProjectID: env.Env.ProjectID, EnvironmentID: envID,
		ProjectSlug: env.ProjectSlug, EnvSlug: env.Env.Slug, EnvName: env.Env.Name, IsProduction: env.Env.IsProduction,
	}
	PublishService(s.bus, EventServiceUpdated, sc)
	s.gitSourceChanged(ctx, sc, nil, nil, false)
	return s.ViewFor(ctx, sc)
}

// UpdateInput holds the optional fields of a service PATCH.
type UpdateInput struct {
	Name         *string
	Config       *Config
	ServerID     *uuid.UUID
	GitToken     *string // "" clears
	RegistryAuth *RegistryAuth

	// BlockEgress, when set, replaces Config.BlockEgress; nil keeps the
	// service's current value (a config PATCH that omits it must not unblock a database).
	BlockEgress *bool

	// RenameProcesses maps an old process name to its new one: with Config, the
	// renamed process keeps its replicas setting and its domains. Names that are
	// not a process of the new config (or whose old name still is one) are not renames
	// and are ignored. Only meaningful together with Config.
	RenameProcesses map[string]string
}

// Update applies a partial update guarded by the expected generation. It
// returns the updated view; the generation only advances when something changed.
func (s *Service) Update(ctx context.Context, id uuid.UUID, expected int64, in UpdateInput) (ServiceView, error) {
	var (
		sc    Scope
		row   db.Service
		after func(context.Context)
	)
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if sc, err = LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id); err != nil {
			return err
		}
		row, after, err = s.UpdateTx(ctx, tx, sc, expected, in)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ServiceView{}, err
		}
		return ServiceView{}, fmt.Errorf("update service: %w", err)
	}
	if row.Generation == expected {
		return s.ViewFor(ctx, sc) // nothing changed
	}
	sc.Service = row
	PublishService(s.bus, EventServiceUpdated, sc)
	after(ctx)
	return s.ViewFor(ctx, sc)
}

// UpdateTx is Update inside the caller's transaction. sc is the service's
// scope, loaded by the caller through the same transaction
// (LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id)). It returns the
// updated row (the current one when nothing changed: the generation then does
// not move) and a function the caller runs once the transaction committed: it
// repairs the mesh after a server change and tells the git source hook. The
// function is never nil. UpdateTx publishes no event.
func (s *Service) UpdateTx(ctx context.Context, tx pgx.Tx, sc Scope, expected int64, in UpdateInput) (db.Service, func(context.Context), error) {
	noop := func(context.Context) {}
	q := db.New(tx)
	id := sc.Service.ID
	cur := sc.Service
	if cur.Kind == KindRoute && (in.Config != nil || in.ServerID != nil || in.GitToken != nil || in.RegistryAuth != nil) {
		return db.Service{}, noop, apierr.Unprocessable("not_applicable", "edit a route service's upstreams and ports with PATCH /routes/{id}; only its name can change here")
	}
	if cur.Generation != expected {
		return db.Service{}, noop, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it").
			WithDetails(map[string]any{"currentGeneration": cur.Generation})
	}
	var err error
	oldCfg := sc.Config()
	newCfg, newName, newServer := oldCfg, cur.Name, cur.ServerID
	var changed []string

	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return db.Service{}, noop, apierr.Unprocessable("validation_failed", "name must not be empty")
		}
		if n != cur.Name {
			newName = n
			changed = append(changed, "name")
		}
	}
	if in.Config != nil {
		c := in.Config.WithDefaults()
		c.Database = oldCfg.Database // the engine and version change only through the databases API
		c = c.WithDefaults()
		c.BlockEgress = oldCfg.BlockEgress
		if in.BlockEgress != nil {
			c.BlockEgress = *in.BlockEgress
		}
		if err := c.Validate(cur.SourceType); err != nil {
			return db.Service{}, noop, ConfigError(err)
		}
		if err := checkProcessKind(cur.Kind, c); err != nil {
			return db.Service{}, noop, err
		}
		vols, err := q.ListServiceVolumes(ctx, id)
		if err != nil {
			return db.Service{}, noop, fmt.Errorf("list volumes: %w", err)
		}
		if err := CheckDockerFiles(c, vols); err != nil {
			return db.Service{}, noop, ConfigError(err)
		}
		if !sameUUID(c.BuildServerID, oldCfg.BuildServerID) || !sameUUID(c.RegistryID, oldCfg.RegistryID) {
			// Only a changed pin or registry is checked: a deleted registry must not block other edits.
			if err := buildplacement.ValidateTargets(ctx, q, sc.WorkspaceID, c.BuildServerID, c.RegistryID); err != nil {
				return db.Service{}, noop, err
			}
		}
		newCfg = c
		if s.tailnetHook != nil && c.Tailnet != nil && tailnetChanged(oldCfg.Tailnet, c.Tailnet) {
			if err := s.tailnetHook.ValidateConfig(ctx, sc, *c.Tailnet); err != nil {
				return db.Service{}, noop, err
			}
		}
		for _, d := range DiffConfig("config", oldCfg, newCfg) {
			changed = append(changed, d.Field)
		}
	}
	serverChanged := in.ServerID != nil && (cur.ServerID == nil || *cur.ServerID != *in.ServerID)
	if serverChanged {
		if _, err := s.checkServer(ctx, q, *in.ServerID); err != nil {
			return db.Service{}, noop, err
		}
		if s.dbGuard != nil {
			if err := s.dbGuard.BeforeMove(ctx, q, sc, in.ServerID); err != nil {
				return db.Service{}, noop, err
			}
		}
		newServer = in.ServerID
		changed = append(changed, "serverId")
	}
	if newCfg.Docker.NeedsUnrestricted() && newServer != nil && (in.Config != nil || newServer != cur.ServerID) {
		// New options on a server, or a move of options to another server: the
		// target must be unrestricted (instance admin's switch).
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *newServer})
		if err != nil {
			return db.Service{}, noop, fmt.Errorf("load server: %w", err)
		}
		if err := CheckDockerPlacement(newCfg, srv.Unrestricted); err != nil {
			return db.Service{}, noop, err
		}
	}
	if cur.Kind == KindStack && (in.Config != nil || newServer != cur.ServerID) {
		// The allow-list depends on the server's Unrestricted Compose switch, so a
		// server change re-checks the compose file too.
		if newCfg, err = s.checkStackWrite(ctx, q, newCfg, newServer); err != nil {
			return db.Service{}, noop, err
		}
		if err := CheckComponentsInUse(ctx, q, id, newCfg); err != nil {
			return db.Service{}, noop, err
		}
	}
	gitTok, regAuth := cur.GitTokenEnc, cur.RegistryAuthEnc
	if in.GitToken != nil {
		if cur.SourceType != SourceGit {
			return db.Service{}, noop, apierr.Unprocessable("validation_failed", "gitToken only applies to git sources")
		}
		if *in.GitToken == "" {
			gitTok = nil
		} else if gitTok, err = s.box.SealString(secretbox.ServicesGitToken, id.String(), *in.GitToken); err != nil {
			return db.Service{}, noop, fmt.Errorf("seal git token: %w", err)
		}
		changed = append(changed, "gitToken")
	}
	if in.RegistryAuth != nil {
		if cur.SourceType != SourceImage {
			return db.Service{}, noop, apierr.Unprocessable("validation_failed", "registryAuth only applies to image sources")
		}
		if regAuth, err = s.sealRegistryAuth(id, in.RegistryAuth); err != nil {
			return db.Service{}, noop, fmt.Errorf("seal registry auth: %w", err)
		}
		changed = append(changed, "registryAuth")
	}
	if len(changed) == 0 {
		return cur, noop, nil
	}

	cfgJSON, err := json.Marshal(newCfg)
	if err != nil {
		return db.Service{}, noop, err
	}
	row, err := q.UpdateServiceCAS(ctx, db.UpdateServiceCASParams{
		ID: id, ExpectedGeneration: expected, Name: newName, Config: cfgJSON, ServerID: newServer,
		GitTokenEnc: gitTok, RegistryAuthEnc: regAuth,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, noop, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it")
	}
	if err != nil {
		return db.Service{}, noop, err
	}
	// The git connection's credentials belong to the repository it was linked
	// to: pointing the service at another repository or host ends the link.
	var unlinked *db.ServiceGit
	if in.Config != nil && cur.SourceType == SourceGit && oldCfg.GitURL != newCfg.GitURL && !SameGitRepo(oldCfg.GitURL, newCfg.GitURL) {
		if sg, err := q.GetServiceGit(ctx, id); err == nil {
			if err := q.DeleteServiceGit(ctx, id); err != nil {
				return db.Service{}, noop, fmt.Errorf("unlink repository: %w", err)
			}
			unlinked = &sg
			changed = append(changed, "gitLink")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return db.Service{}, noop, fmt.Errorf("load git link: %w", err)
		}
	}
	if s.tailnetHook != nil && tailnetChanged(oldCfg.Tailnet, newCfg.Tailnet) {
		if err := s.tailnetHook.ConfigChanged(ctx, tx, sc, oldCfg.Tailnet, newCfg.Tailnet); err != nil {
			return db.Service{}, noop, err
		}
	}
	detached, err := syncNetworks(ctx, q, id, newCfg, newServer, serverChanged)
	if err != nil {
		return db.Service{}, noop, err
	}
	if in.Config != nil {
		if err := renameProcesses(ctx, q, id, newCfg, in.RenameProcesses); err != nil {
			return db.Service{}, noop, err
		}
		if err := CheckProcessesInUse(ctx, q, id, newCfg); err != nil {
			return db.Service{}, noop, err
		}
	}
	if newCfg.Port != oldCfg.Port {
		// Only the default process' domains (empty component) follow the service port;
		// the domains of named processes route to their own ports.
		doms, err := q.ListServiceDomains(ctx, id)
		if err != nil {
			return db.Service{}, noop, err
		}
		own := 0
		for _, d := range doms {
			if d.Component == "" {
				own++
			}
		}
		if own > 0 {
			if newCfg.Port == 0 {
				return db.Service{}, noop, apierr.Unprocessable("domains_need_port", "remove the service's domains before disabling its port")
			}
			if err := q.SetServiceCurrentPortDomains(ctx, db.SetServiceCurrentPortDomainsParams{ServiceID: id, TargetPort: int32(newCfg.Port)}); err != nil {
				return db.Service{}, noop, err
			}
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "service.update", ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"fields": changed, "generation": row.Generation, "detachedNetworks": detached},
	}); err != nil {
		return db.Service{}, noop, err
	}
	newSc := sc
	newSc.Service = row
	oldServer, gitTokenSet := cur.ServerID, in.GitToken != nil
	after := func(ctx context.Context) {
		if s.mesh != nil && !sameUUID(oldServer, row.ServerID) {
			// A moved consumer or provider changes which links it needs: the removal-only
			// reconciliation drops what no longer applies (it never adds a device).
			s.mesh.Repair(context.WithoutCancel(ctx), newSc.EnvironmentID)
		}
		s.RepoUnlinked(ctx, unlinked)
		s.gitSourceChanged(ctx, newSc, &oldCfg, oldServer, gitTokenSet)
	}
	return row, after, nil
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// DeleteInput selects what a service delete removes.
type DeleteInput struct {
	Confirm       string
	DeleteVolumes bool
	// RemoveKey is the idempotency key of the remove_service command the delete
	// queues. A caller that must know when the agent finished (a saga that
	// deletes its own row afterwards) sets it, so it can find the command and
	// read its result; concurrent deletes then share one command. Empty: the
	// command gets a generated key and nobody waits for it.
	RemoveKey string
}

// DeleteOptions selects what DeleteInternalTx removes and records.
type DeleteOptions struct {
	DeleteVolumes bool
	// Reason is stored in the audit entry.
	Reason string
	// ByCombine marks a delete that is part of a combine: its id is recorded in
	// the audit entry. uuid.Nil: not part of one.
	ByCombine uuid.UUID
}

// Delete soft-deletes a service. If it is deployed, a remove_service command
// is queued for the server that runs it.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, in DeleteInput) error {
	sc, err := LoadScope(ctx, s.q, policy.ScopeProjectsWrite, id)
	if err != nil {
		return err
	}
	if in.Confirm != sc.Service.Slug {
		return apierr.Unprocessable("confirm_mismatch", "confirm must equal the service slug").
			WithDetails(map[string]any{"slug": sc.Service.Slug})
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		eff := []string{"Removes the service, its running container and its domains."}
		if in.DeleteVolumes {
			eff = append(eff, "Deletes its data volumes. This cannot be undone.")
		} else {
			eff = append(eff, "Its data volumes are kept.")
		}
		if err := prodguard.Check(ctx,
			sc.ProdScope(fmt.Sprintf("Delete %s from production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), true, eff...),
			"delete_service", prodguard.Digest("delete_service", id, in.DeleteVolumes)); err != nil {
			return err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.deleteTx(ctx, tx, sc, in.DeleteVolumes, in.RemoveKey, map[string]any{})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete service: %w", err)
	}
	PublishService(s.bus, EventServiceDeleted, sc)
	return nil
}

// DeleteInternalTx is Delete inside the caller's transaction, for sagas that
// remove services as one step among others (a combine). It skips the typed
// confirmation and the production guard: the caller's request carried them. It
// runs the same cascade as Delete (the remove_service command, the soft delete,
// the domains, the config source, the mesh and tailnet hooks, the audit entry)
// and returns the function to run once the transaction committed: it publishes
// the deletion. The service is loaded without authorization.
func (s *Service) DeleteInternalTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, opts DeleteOptions) (afterCommit func(context.Context), err error) {
	sc, err := LoadScopeInternal(ctx, db.New(tx), id)
	if err != nil {
		return nil, err
	}
	extra := map[string]any{}
	if opts.Reason != "" {
		extra["reason"] = opts.Reason
	}
	if opts.ByCombine != uuid.Nil {
		extra["combine"] = opts.ByCombine.String()
	}
	if err := s.deleteTx(ctx, tx, sc, opts.DeleteVolumes, "", extra); err != nil {
		return nil, err
	}
	return func(context.Context) { PublishService(s.bus, EventServiceDeleted, sc) }, nil
}

// deleteTx is the cascade of a service delete inside tx; extra is added to the
// audit entry's metadata.
func (s *Service) deleteTx(ctx context.Context, tx pgx.Tx, sc Scope, deleteVolumes bool, removeKey string, extra map[string]any) error {
	id := sc.Service.ID
	q := db.New(tx)
	if s.dbGuard != nil {
		if err := s.dbGuard.BeforeDelete(ctx, tx, sc); err != nil {
			return err
		}
	}
	active, err := q.CountActiveDeployments(ctx, id)
	if err != nil {
		return err
	}
	if active > 0 {
		return apierr.Conflict("deployment_in_progress", "a deployment is in progress; wait for it to finish or cancel it")
	}
	vols, err := q.ListServiceVolumes(ctx, id)
	if err != nil {
		return err
	}
	if sc.Service.CurrentDeploymentID != nil && sc.Service.Kind != KindRoute {
		deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sc.Service.CurrentDeploymentID})
		if err != nil {
			return err
		}
		if len(deps) == 1 && deps[0].ServerID != nil {
			payload := agentprotocol.ServiceActionPayload{
				ServiceID: id.String(), StopGraceSeconds: sc.Config().StopGraceSeconds, DeleteVolumes: deleteVolumes,
				Stack: sc.Service.Kind == KindStack,
			}
			if deleteVolumes {
				for _, v := range vols {
					payload.VolumeNames = append(payload.VolumeNames, DockerVolumeName(v))
				}
				// Docker volumes a backup restore swapped out are removed with the service.
				retired, err := q.ListRetiredVolumes(ctx, id)
				if err != nil {
					return err
				}
				for _, r := range retired {
					payload.VolumeNames = append(payload.VolumeNames, r.DockerName)
				}
			}
			raw, _ := json.Marshal(payload)
			if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
				ServerID: *deps[0].ServerID, Kind: agentprotocol.KindRemoveService, Payload: raw, IdempotencyKey: removeKey,
			}); err != nil {
				return fmt.Errorf("enqueue remove_service: %w", err)
			}
		}
	}
	if err := q.SoftDeleteService(ctx, id); err != nil {
		return err
	}
	// The file no longer owns anything of a deleted service.
	if err := q.DeleteConfigSource(ctx, id); err != nil {
		return err
	}
	if s.mesh != nil {
		if err := s.mesh.ServiceDeleted(ctx, tx, sc.EnvironmentID, id); err != nil {
			return err
		}
	}
	if err := q.DeleteServiceDomains(ctx, id); err != nil {
		return err
	}
	if s.dnsHook != nil {
		if err := s.dnsHook.DomainsRemoved(ctx, tx, sc.WorkspaceID); err != nil {
			return err
		}
	}
	if s.tailnetHook != nil {
		if err := s.tailnetHook.ServiceDeleted(ctx, tx, sc); err != nil {
			return err
		}
	}
	// The audit entry first: the route hook numbers the server's route table,
	// and the counter's lock must be the last this transaction takes.
	meta := map[string]any{"name": sc.Service.Name, "slug": sc.Service.Slug, "deleteVolumes": deleteVolumes}
	for k, v := range extra {
		meta[k] = v
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "service.delete", ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID,
		Metadata: meta,
	}); err != nil {
		return err
	}
	if sc.Service.Kind == KindRoute {
		// A route service has no container: free its listen port and let the
		// proxy owner push the remaining route table to the server.
		if err := q.DeleteRoutePort(ctx, id); err != nil {
			return err
		}
		if s.routeHook != nil {
			return s.routeHook.RouteDeleted(ctx, tx, sc)
		}
	}
	return nil
}

// ActionResult is returned by restart and stop.
type ActionResult struct {
	CommandID uuid.UUID `json:"commandId"`
}

// Restart queues a restart of the running container.
func (s *Service) Restart(ctx context.Context, id uuid.UUID) (ActionResult, error) {
	return s.action(ctx, id, agentprotocol.KindRestartService, "service.restart")
}

// Stop queues a stop of the running container. The container is kept so the
// service can be restarted without a new deployment.
func (s *Service) Stop(ctx context.Context, id uuid.UUID) (ActionResult, error) {
	return s.action(ctx, id, agentprotocol.KindStopService, "service.stop")
}

func (s *Service) action(ctx context.Context, id uuid.UUID, kind, auditAction string) (ActionResult, error) {
	sc, err := LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return ActionResult{}, err
	}
	if err := RequireContainer(sc, "restart/stop"); err != nil {
		return ActionResult{}, err
	}
	if sc.Service.CurrentDeploymentID == nil {
		return ActionResult{}, apierr.Conflict("not_deployed", "the service has no running deployment")
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		verb, effect := "Restart", "Restarts the running container; expect a brief interruption."
		if kind == agentprotocol.KindStopService {
			verb, effect = "Stop", "Stops the running container. The service stays down until it is started or deployed again."
		}
		op := strings.ToLower(verb) + "_service"
		if err := prodguard.Check(ctx,
			sc.ProdScope(fmt.Sprintf("%s %s in production (%s / %s)", verb, sc.Service.Name, sc.ProjectName, sc.EnvName), false, effect),
			op, prodguard.Digest(op, id)); err != nil {
			return ActionResult{}, err
		}
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sc.Service.CurrentDeploymentID})
	if err != nil || len(deps) != 1 || deps[0].ServerID == nil {
		return ActionResult{}, apierr.Conflict("not_deployed", "the service has no running deployment")
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *deps[0].ServerID})
	if err != nil {
		return ActionResult{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return ActionResult{}, serverNotUsable(srv.Name, "revoked")
	}
	raw, _ := json.Marshal(agentprotocol.ServiceActionPayload{
		ServiceID: id.String(), StopGraceSeconds: sc.Config().StopGraceSeconds, Stack: sc.Service.Kind == KindStack,
	})
	var cmdID uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		cmdID, err = s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: srv.ID, Kind: kind, Payload: raw})
		if err != nil {
			return err
		}
		desired := DesiredRunning
		if kind == agentprotocol.KindStopService {
			desired = DesiredStopped
		}
		if err := db.New(tx).SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: id, DesiredState: desired}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: auditAction, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"slug": sc.Service.Slug, "server": srv.Name},
		})
	})
	if err != nil {
		return ActionResult{}, fmt.Errorf("%s: %w", auditAction, err)
	}
	PublishService(s.bus, EventServiceUpdated, sc)
	return ActionResult{CommandID: cmdID}, nil
}

// checkProcessKind refuses processes on a service that is not an app or worker:
// databases, stacks and routes are not runtime services of one image.
func checkProcessKind(kind string, cfg Config) error {
	if cfg.HasProcesses() && kind != KindApp && kind != KindWorker {
		return apierr.Unprocessable("validation_failed", "processes only apply to app and worker services")
	}
	return nil
}

// renameProcesses carries what belongs to a process over to its new name inside
// the config write: the replicas setting and the domains that route to it. A
// pair counts only when the new name is a named process of cfg and the old one
// is none (so no name is taken from a live process). Renaming the default
// process moves nothing: its settings live on the service and its domains have
// no process name.
func renameProcesses(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg Config, renames map[string]string) error {
	if len(renames) == 0 {
		return nil
	}
	var doms []db.Domain
	for _, from := range slices.Sorted(maps.Keys(renames)) {
		to := renames[from]
		if from == to || from == "" || to == cfg.DefaultProcessName() {
			continue
		}
		if _, ok := cfg.Process(to); !ok {
			continue
		}
		if _, ok := cfg.Process(from); ok {
			continue
		}
		if err := q.RenameProcessScale(ctx, db.RenameProcessScaleParams{ServiceID: serviceID, FromProcess: from, ToProcess: to}); err != nil {
			return fmt.Errorf("rename process scale: %w", err)
		}
		if doms == nil {
			var err error
			if doms, err = q.ListServiceDomains(ctx, serviceID); err != nil {
				return fmt.Errorf("list domains: %w", err)
			}
		}
		var ids []uuid.UUID
		for _, d := range doms {
			if d.Component == from {
				ids = append(ids, d.ID)
			}
		}
		if len(ids) > 0 {
			if _, err := q.MoveDomainsToService(ctx, db.MoveDomainsToServiceParams{ServiceID: serviceID, Component: to, DomainIds: ids}); err != nil {
				return fmt.Errorf("rename process domains: %w", err)
			}
		}
	}
	return nil
}
