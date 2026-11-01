package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// TemplateInstallRequest is the body of an install.
type TemplateInstallRequest struct {
	ServerID uuid.UUID         `json:"serverId"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	// Deploy deploys the new services right away (needs deployments:write);
	// otherwise they stay staged on the canvas.
	Deploy bool `json:"deploy,omitempty"`
	// Name overrides the canvas group title.
	Name string `json:"name,omitempty" maxLength:"120"`
}

// TemplateInstallResult is the outcome of an install.
type TemplateInstallResult struct {
	Instance TemplateInstance       `json:"instance"`
	Services []services.ServiceView `json:"services"`
	// Deployments are the deployments started when Deploy was set, in order.
	Deployments []deployments.DeploymentView `json:"deployments"`
	Warnings    []string                     `json:"warnings"`
}

// Install turns a template into staged services in an environment: services,
// volumes, files, variables, domain, published ports, links, a canvas group
// and the instance record, all in one transaction.
func (s *Service) Install(ctx context.Context, envID uuid.UUID, templateID string, req TemplateInstallRequest) (TemplateInstallResult, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return TemplateInstallResult{}, err
	}
	if req.Deploy {
		if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &env.Env.ProjectID); err != nil {
			return TemplateInstallResult{}, err
		}
	}
	t, err := s.template(templateID)
	if err != nil {
		return TemplateInstallResult{}, err
	}
	sid := req.ServerID
	ev, err := s.evaluate(ctx, s.q, env, t, TemplatePlanRequest{ServerID: &sid, Inputs: req.Inputs}, true)
	if err != nil {
		return TemplateInstallResult{}, err
	}
	for _, c := range ev.view.Checks {
		if c.Status != CheckFail {
			continue
		}
		if c.ID == "unrestricted" {
			return TemplateInstallResult{}, apierr.Unprocessable("requires_unrestricted_server", c.Message).
				WithDetails(map[string]any{"checks": ev.view.Checks})
		}
		return TemplateInstallResult{}, apierr.Unprocessable("preflight_failed", c.Message).
			WithDetails(map[string]any{"checks": ev.view.Checks})
	}
	title := strings.TrimSpace(req.Name)
	if title == "" {
		title = t.Group
	}
	if title == "" {
		title = t.Name
	}

	b := &build{s: s, env: env, t: t, ev: ev, title: title, warnings: []string{}}
	err = db.InTx(ctx, s.Pool, b.run(ctx))
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return TemplateInstallResult{}, err
		}
		if services.IsUniqueViolation(err) {
			return TemplateInstallResult{}, apierr.Conflict("install_conflict", "a hostname or port of this template was just taken; review the plan and retry")
		}
		return TemplateInstallResult{}, fmt.Errorf("install template: %w", err)
	}

	for _, row := range b.created {
		services.PublishService(s.Bus, services.EventServiceUpdated, services.ScopeOf(env, row))
	}
	services.Publish(s.Bus, services.EventCanvasUpdated, env.Env.ProjectID, envID, envID)

	res := TemplateInstallResult{Instance: b.instanceView(), Warnings: b.warnings, Deployments: []deployments.DeploymentView{}}
	res.Services = make([]services.ServiceView, 0, len(b.created))
	for _, row := range b.created {
		sc, err := services.LoadScopeInternal(ctx, s.q, row.ID)
		if err != nil {
			return TemplateInstallResult{}, err
		}
		v, err := s.Services.ViewFor(ctx, sc)
		if err != nil {
			return TemplateInstallResult{}, err
		}
		res.Services = append(res.Services, v)
	}
	if req.Deploy {
		ids := make([]uuid.UUID, len(b.created))
		for i, r := range b.created {
			ids[i] = r.ID
		}
		staged, err := s.Deploy.DeployStaged(ctx, envID, ids, "Installed from template "+t.Name)
		if err != nil {
			// The stack exists and is staged; report the deploy problem without losing the install.
			res.Warnings = append(res.Warnings, "The stack was added but deploying failed: "+err.Error())
		} else {
			res.Deployments = staged.Items
		}
	}
	return res, nil
}

// build carries one install transaction.
type build struct {
	s        *Service
	env      services.EnvScope
	t        *CatalogTemplate
	ev       *evaluated
	title    string
	warnings []string

	instance db.TemplateInstance
	created  []db.Service // every service created, route services last
	byKey    map[string]db.Service
	slugs    map[string]string // template key → slug
	instSvcs []TemplateInstanceService
}

func (b *build) instanceView() TemplateInstance {
	inputs := map[string]string{}
	for _, in := range b.t.Inputs {
		if !in.Secret && !b.ev.inputs.Secret[in.Key] && !b.ev.inputs.Generated[in.Key] {
			inputs[in.Key] = b.ev.inputs.Values[in.Key]
		}
	}
	return TemplateInstance{
		ID: b.instance.ID, EnvironmentID: b.env.Env.ID, TemplateID: b.t.ID, TemplateRevision: b.t.Revision, TemplateVersion: AppVersion(b.t),
		Name: b.instance.Name, GroupID: b.instance.GroupID, Inputs: inputs, Services: b.instSvcs, CreatedAt: b.instance.CreatedAt,
		CurrentRevision: b.t.Revision, CurrentVersion: AppVersion(b.t),
	}
}

func (b *build) run(ctx context.Context) func(pgx.Tx) error {
	return func(tx pgx.Tx) error {
		s, q := b.s, db.New(tx)
		b.created, b.instSvcs = nil, nil
		b.byKey = map[string]db.Service{}
		b.slugs = map[string]string{}
		b.warnings = []string{}
		srv := *b.ev.server

		actorType, actorID := audit.ActorSystem, ""
		if p, ok := auth.PrincipalFrom(ctx); ok {
			actorType, actorID = p.Type, p.ID().String()
		}
		inputsJSON, err := json.Marshal(b.instanceView().Inputs)
		if err != nil {
			return err
		}
		snapshot, err := encodeSnapshot(b.t)
		if err != nil {
			return err
		}
		groupID := "tpl-" + uuid.NewString()[:8]
		b.instance, err = q.InsertTemplateInstance(ctx, db.InsertTemplateInstanceParams{
			WorkspaceID: b.env.WorkspaceID, EnvironmentID: b.env.Env.ID, TemplateID: b.t.ID, TemplateRevision: int32(b.t.Revision),
			TemplateVersion: AppVersion(b.t), TemplateSnapshot: snapshot, Name: b.title, GroupID: groupID, Inputs: inputsJSON, CreatedByType: actorType, CreatedBy: actorID,
		})
		if err != nil {
			return fmt.Errorf("insert instance: %w", err)
		}

		// 1. Services (every key gets its slug before variables are written).
		for i := range b.t.Services {
			sv := &b.t.Services[i]
			row, err := b.createService(ctx, tx, sv, srv.ID)
			if err != nil {
				return err
			}
			if err := q.SetServiceTemplate(ctx, db.SetServiceTemplateParams{ID: row.ID, TemplateInstanceID: &b.instance.ID, TemplateKey: &sv.Key}); err != nil {
				return err
			}
			b.byKey[sv.Key], b.slugs[sv.Key] = row, row.Slug
			b.created = append(b.created, row)
			b.instSvcs = append(b.instSvcs, TemplateInstanceService{ID: row.ID, Key: sv.Key, Slug: row.Slug, Name: row.Name})
		}

		// 2. Volumes, variables, domain, ports, links.
		var hosts []string
		for i := range b.t.Services {
			sv := &b.t.Services[i]
			row := b.byKey[sv.Key]
			for _, v := range sv.Volumes {
				if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: v.Name, MountPath: v.Path}); err != nil {
					return fmt.Errorf("insert volume: %w", err)
				}
			}
			if err := b.writeEnv(ctx, tx, sv, row); err != nil {
				return err
			}
			for _, d := range sv.Domains {
				host, warn, err := b.addDomain(ctx, tx, row, srv, d)
				if err != nil {
					return err
				}
				if warn != "" {
					b.warnings = append(b.warnings, warn)
				}
				if host != "" {
					hosts = append(hosts, host)
				}
			}
			for _, dep := range sv.DependsOn {
				if _, err := q.InsertLink(ctx, db.InsertLinkParams{EnvironmentID: b.env.Env.ID, FromServiceID: row.ID, ToServiceID: b.byKey[dep].ID}); err != nil {
					return fmt.Errorf("insert link: %w", err)
				}
			}
			for _, p := range sv.Ports {
				if !p.Publish {
					continue
				}
				if s.Ports == nil {
					return apierr.Unprocessable("ports_unavailable", "this server build cannot publish TCP/UDP ports")
				}
				rid, err := s.Ports.AddPortRouteTx(ctx, tx, PortRoute{
					EnvironmentID: b.env.Env.ID, TargetServiceID: row.ID, Name: sv.Key + "-" + strconv.Itoa(p.Port) + "-" + p.Protocol,
					Protocol: p.Protocol, ListenPort: p.Port, TargetPort: p.Port,
				})
				if err != nil {
					return err
				}
				if err := q.SetServiceTemplate(ctx, db.SetServiceTemplateParams{ID: rid, TemplateInstanceID: &b.instance.ID, TemplateKey: new("port:" + sv.Key + ":" + p.Name)}); err != nil {
					return err
				}
				routeScope, err := services.LoadScopeInternal(ctx, q, rid)
				if err != nil {
					return err
				}
				routeRow := routeScope.Service
				b.created = append(b.created, routeRow)
				b.instSvcs = append(b.instSvcs, TemplateInstanceService{ID: routeRow.ID, Key: "port:" + sv.Key + ":" + p.Name, Slug: routeRow.Slug, Name: routeRow.Name})
			}
		}

		// 3. Canvas group of the template services (route services stay outside).
		groupIDs := make([]uuid.UUID, 0, len(b.t.Services))
		for i := range b.t.Services {
			groupIDs = append(groupIDs, b.byKey[b.t.Services[i].Key].ID)
		}
		if err := canvas.AddGroupTx(ctx, tx, b.env.Env.ID, groupID, b.title, groupIDs); err != nil {
			return err
		}

		return audit.Record(ctx, tx, audit.Entry{
			Action: "template.install", ResourceType: "template_instance", ResourceID: b.instance.ID.String(), ProjectID: &b.env.Env.ProjectID,
			Metadata: map[string]any{"template": b.t.ID, "revision": b.t.Revision, "version": AppVersion(b.t), "server": srv.Name, "services": len(b.created), "hostnames": hosts},
		})
	}
}

// createService creates the service row (image service or managed database).
func (b *build) createService(ctx context.Context, tx pgx.Tx, sv *TemplateService, serverID uuid.UUID) (db.Service, error) {
	s := b.s
	if sv.Database != nil {
		if s.Databases == nil {
			return db.Service{}, apierr.Unprocessable("databases_unavailable", "this server build cannot create managed databases")
		}
		row, err := s.Databases.CreateTx(ctx, tx, DatabaseSpec{
			EnvironmentID: b.env.Env.ID, Name: sv.Name, Engine: sv.Database.Engine, Version: sv.Database.Version,
			DatabaseName: sv.Database.DatabaseName, ServerID: &serverID,
		})
		if err != nil {
			return db.Service{}, fmt.Errorf("create database %s: %w", sv.Key, err)
		}
		return row, nil
	}
	return s.Services.CreateInTx(ctx, tx, b.env, services.CreateInput{
		Name: sv.Name, Kind: kindOf(sv), SourceType: services.SourceImage, Config: b.config(sv), ServerID: &serverID,
	})
}

// config builds the service configuration of an image service.
func (b *build) config(sv *TemplateService) services.Config {
	return serviceConfig(sv, b.ev.inputs.Values)
}

// serviceConfig builds the configuration of an image service from its template
// text; vals are the install input values. Installing and updating share it, so
// an update compares exactly what an install would have written.
func serviceConfig(sv *TemplateService, vals map[string]string) services.Config {
	cfg := services.Config{
		Image: sv.Image + "@" + sv.Digest, Port: sv.Port, StopGraceSeconds: sv.StopGraceSeconds, OneShot: sv.OneShot,
	}
	for _, a := range sv.Command {
		cfg.StartCommand = append(cfg.StartCommand, expandInputs(a, vals, false))
	}
	if sv.Docker != nil {
		cfg.Docker = expandDockerInputs(&sv.Docker.DockerOptions, vals).Normalized()
	}
	if r := sv.Resources; r != nil {
		cfg.MemoryMB, cfg.CPUs = r.MemoryMB, r.CPUs
	}
	for _, f := range sv.Files {
		cfg.Files = append(cfg.Files, services.FileMount{Path: f.Path, Mode: f.Mode, Content: expandInputs(f.Content, vals, false)})
	}
	switch h := sv.Healthcheck; {
	case sv.OneShot:
		cfg.Healthcheck = services.Healthcheck{Type: services.HealthNone, DeadlineSeconds: 900}
	case h != nil:
		cfg.Healthcheck = services.Healthcheck{
			Type: h.Type, Path: h.Path, Port: h.Port, Command: h.Command, IntervalSeconds: h.IntervalSeconds,
			TimeoutSeconds: h.TimeoutSeconds, StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds,
			SuccessThreshold: h.SuccessThreshold,
		}
	}
	return cfg
}

// writeEnv writes the template service's variables.
func (b *build) writeEnv(ctx context.Context, tx pgx.Tx, sv *TemplateService, row db.Service) error {
	if len(sv.Env) == 0 {
		return nil
	}
	if b.s.Variables == nil {
		return apierr.Unprocessable("variables_unavailable", "this server build cannot write variables")
	}
	keys := make([]string, 0, len(sv.Env))
	for k := range sv.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw := sv.Env[k]
		secret := envIsSecret(sv, k, raw, b.ev.inputs.Secret)
		val := rewriteRefs(expandInputs(raw, b.ev.inputs.Values, true), b.slugs)
		if err := b.s.Variables.UpsertTx(ctx, tx, row.ID, k, val, secret); err != nil {
			return fmt.Errorf("write variable %s.%s: %w", sv.Key, k, err)
		}
	}
	return nil
}

// addDomain attaches the template's hostname to the service (staged).
func (b *build) addDomain(ctx context.Context, tx pgx.Tx, row db.Service, srv db.Server, d TemplateDomain) (host, warn string, err error) {
	q := db.New(tx)
	cfg := services.DecodeConfig(row.Config)
	if cfg.Port <= 0 {
		return "", "", nil
	}
	if srv.ProxyStatus != nil && *srv.ProxyStatus == "error" {
		return "", "", apierr.Unprocessable("proxy_unavailable", "the proxy on "+srv.Name+" is not running")
	}
	generated := d.Generate
	if generated {
		base, ok := domains.BaseDomain(srv)
		if !ok {
			return "", "No hostname was generated for " + row.Name + ": the server has neither a base domain nor a public IP. Add a domain in its settings.", nil
		}
		first := domains.GenerateHostname(row.Slug, b.env.Env.Slug, b.env.ProjectSlug, base)
		host = first
		for i := 2; ; i++ {
			n, err := q.CountDomainsByHostname(ctx, host)
			if err != nil {
				return "", "", err
			}
			if n == 0 {
				break
			}
			if i > 9 {
				return "", "", apierr.Conflict("domain_conflict", "no free generated hostname for "+row.Name)
			}
			label, rest, _ := strings.Cut(first, ".")
			suffix := "-" + strconv.Itoa(i)
			if len(label)+len(suffix) > 63 {
				label = strings.Trim(label[:63-len(suffix)], "-")
			}
			host = label + suffix + "." + rest
		}
	} else {
		host, err = domains.ValidateHostname(expandInputs(d.Host, b.ev.inputs.Values, false))
		if err != nil {
			return "", "", err
		}
	}
	dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
		ServiceID: row.ID, Hostname: host, TargetPort: int32(cfg.Port), Generated: generated, StatusMessage: domains.DNSGuidance(host, srv.PublicIp),
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return "", "", apierr.Conflict("domain_conflict", "the hostname "+host+" is already in use")
		}
		return "", "", err
	}
	if _, err := q.BumpServiceGeneration(ctx, row.ID); err != nil {
		return "", "", err
	}
	if err := b.s.Jobs.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
		return "", "", fmt.Errorf("enqueue dns check: %w", err)
	}
	return host, "", nil
}

// ---- instances ----

// ListInstances returns the template stacks installed in an environment.
func (s *Service) ListInstances(ctx context.Context, envID uuid.UUID) ([]TemplateInstance, error) {
	if _, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnvironmentTemplateInstances(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	out := make([]TemplateInstance, 0, len(rows))
	for _, r := range rows {
		v, err := s.instanceView(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) instanceView(ctx context.Context, r db.TemplateInstance) (TemplateInstance, error) {
	svcs, err := s.q.ListTemplateInstanceServices(ctx, &r.ID)
	if err != nil {
		return TemplateInstance{}, fmt.Errorf("list instance services: %w", err)
	}
	inputs := map[string]string{}
	_ = json.Unmarshal(r.Inputs, &inputs)
	v := TemplateInstance{
		ID: r.ID, EnvironmentID: r.EnvironmentID, TemplateID: r.TemplateID, TemplateRevision: int(r.TemplateRevision), TemplateVersion: r.TemplateVersion,
		Name: r.Name, GroupID: r.GroupID, Inputs: inputs, Services: []TemplateInstanceService{}, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	s.markUpdate(&v, r)
	for _, sv := range svcs {
		key := ""
		if sv.TemplateKey != nil {
			key = *sv.TemplateKey
		}
		v.Services = append(v.Services, TemplateInstanceService{ID: sv.ID, Key: key, Slug: sv.Slug, Name: sv.Name})
	}
	return v, nil
}

// DeleteInstance removes every service of a stack (with typed confirmation:
// the template id) and its canvas group. Volumes are kept unless deleteVolumes.
func (s *Service) DeleteInstance(ctx context.Context, instanceID uuid.UUID, confirm string, deleteVolumes bool) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return err
	}
	inst, err := s.q.GetTemplateInstance(ctx, instanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("template instance")
	}
	if err != nil {
		return fmt.Errorf("get instance: %w", err)
	}
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, inst.EnvironmentID)
	if err != nil {
		return apierr.NotFound("template instance")
	}
	if confirm != inst.TemplateID {
		return apierr.Unprocessable("confirm_mismatch", "confirm must equal the template id").
			WithDetails(map[string]any{"templateId": inst.TemplateID})
	}
	svcs, err := s.q.ListTemplateInstanceServices(ctx, &inst.ID)
	if err != nil {
		return fmt.Errorf("list instance services: %w", err)
	}
	if prodguard.Applies(ctx, env.Env.IsProduction) {
		targets := make([]prodguard.ApprovalTarget, len(svcs))
		names := make([]string, len(svcs))
		for i, sv := range svcs {
			targets[i] = prodguard.ApprovalTarget{Kind: "service", ID: sv.ID, Name: sv.Name, Generation: sv.Generation}
			names[i] = sv.Name
		}
		eff := []string{"Removes every service of the template and its canvas group: " + strings.Join(names, ", ") + "."}
		if deleteVolumes {
			eff = append(eff, "Deletes their data volumes. This cannot be undone.")
		} else {
			eff = append(eff, "Their data volumes are kept.")
		}
		if err := prodguard.Check(ctx,
			env.ProdScope(fmt.Sprintf("Delete the template installation %s from production (%s)", inst.TemplateID, env.Env.Name), true, targets, eff...),
			"delete_template_instance", prodguard.DigestIDs("delete_template_instance", []uuid.UUID{inst.ID}, deleteVolumes)); err != nil {
			return err
		}
		ctx = prodguard.Covered(ctx) // the per-service deletes below are part of this approval
	}
	// Route services first, then consumers before providers (reverse creation).
	for i := len(svcs) - 1; i >= 0; i-- {
		sv := svcs[i]
		if err := s.Services.Delete(ctx, sv.ID, services.DeleteInput{Confirm: sv.Slug, DeleteVolumes: deleteVolumes}); err != nil {
			return err
		}
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := canvas.RemoveGroupTx(ctx, tx, inst.EnvironmentID, inst.GroupID); err != nil {
			return err
		}
		if err := q.SoftDeleteTemplateInstance(ctx, inst.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "template.delete", ResourceType: "template_instance", ResourceID: inst.ID.String(), ProjectID: &env.Env.ProjectID,
			Metadata: map[string]any{"template": inst.TemplateID, "services": len(svcs), "deleteVolumes": deleteVolumes},
		})
	})
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	services.Publish(s.Bus, services.EventCanvasUpdated, env.Env.ProjectID, inst.EnvironmentID, inst.EnvironmentID)
	return nil
}
