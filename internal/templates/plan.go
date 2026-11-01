package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Check statuses.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// TemplatePlanCheck is one preflight result.
type TemplatePlanCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status" enum:"ok,warn,fail"`
	Message string `json:"message"`
}

// TemplatePlanPort is a non-HTTP port of a planned service.
type TemplatePlanPort struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Publish  bool   `json:"publish"`
}

// TemplatePlanService is a service the install would create.
type TemplatePlanService struct {
	Key       string             `json:"key"`
	Name      string             `json:"name"`
	Slug      string             `json:"slug"`
	Kind      string             `json:"kind"`
	Image     string             `json:"image,omitempty"`
	Database  string             `json:"database,omitempty"`
	Port      int                `json:"port,omitempty"`
	Volumes   []string           `json:"volumes"`
	Files     int                `json:"files"`
	Ports     []TemplatePlanPort `json:"ports"`
	OneShot   bool               `json:"oneshot"`
	Domain    string             `json:"domain,omitempty"`
	DependsOn []string           `json:"dependsOn"`
	MemoryMB  int                `json:"memoryMb,omitempty"`
}

// TemplatePlanInput is one install input as the form shows it.
type TemplatePlanInput struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
	Secret      bool     `json:"secret"`
	Options     []string `json:"options,omitempty"`
	// Format is text | email | url | hostname | number (empty: text).
	Format string `json:"format,omitempty" enum:"text,email,url,hostname,number"`
	// Generated is true when the value will be generated at install.
	Generated bool `json:"generated"`
	// Value is the effective non-secret value; empty for secret or generated inputs.
	Value string `json:"value,omitempty"`
}

// TemplatePlan is the preflight of an install. Secret values never appear.
type TemplatePlan struct {
	Template TemplateSummary       `json:"template"`
	Services []TemplatePlanService `json:"services"`
	Inputs   []TemplatePlanInput   `json:"inputs"`
	// Secrets lists "<service>.<KEY>" of every secret variable that will be generated or stored.
	Secrets  []string            `json:"secrets"`
	Checks   []TemplatePlanCheck `json:"checks"`
	Warnings []string            `json:"warnings"`
	// Installable is false when any check failed.
	Installable bool `json:"installable"`
	// ProxyRestart is true when published ports restart the proxy briefly.
	ProxyRestart bool `json:"proxyRestart"`
}

// TemplatePlanRequest is the body of plan and install.
type TemplatePlanRequest struct {
	ServerID *uuid.UUID        `json:"serverId,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
}

// evaluated is the shared result of planning before an install.
type evaluated struct {
	view   TemplatePlan
	inputs ResolvedInputs
	server *db.Server
	env    services.EnvScope
	tpl    *CatalogTemplate
}

func (s *Service) template(id string) (*CatalogTemplate, error) {
	t, ok := s.Catalog.Get(id)
	if !ok {
		return nil, apierr.NotFound("template")
	}
	return t, nil
}

// Plan reports what installing a template into an environment would do.
func (s *Service) Plan(ctx context.Context, envID uuid.UUID, templateID string, req TemplatePlanRequest) (TemplatePlan, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID)
	if err != nil {
		return TemplatePlan{}, err
	}
	t, err := s.template(templateID)
	if err != nil {
		return TemplatePlan{}, err
	}
	ev, err := s.evaluate(ctx, s.q, env, t, req, false)
	if err != nil {
		return TemplatePlan{}, err
	}
	return ev.view, nil
}

// evaluate builds the plan. forInstall materialises generated inputs and turns
// a missing server into an error.
func (s *Service) evaluate(ctx context.Context, q *db.Queries, env services.EnvScope, t *CatalogTemplate, req TemplatePlanRequest, forInstall bool) (*evaluated, error) {
	var mat Materializer
	if forInstall {
		mat = s.Materialize
	}
	in, err := ResolveInputs(t, req.Inputs, mat)
	if err != nil {
		return nil, err
	}
	ev := &evaluated{inputs: in, env: env, tpl: t}
	v := TemplatePlan{Template: summaryOf(t), Services: []TemplatePlanService{}, Inputs: []TemplatePlanInput{}, Secrets: []string{}, Checks: []TemplatePlanCheck{}, Warnings: []string{}}

	for _, ti := range t.Inputs {
		pi := TemplatePlanInput{Key: ti.Key, Label: ti.Label, Description: ti.Description, Required: ti.Required, Secret: ti.Secret, Options: ti.Options, Format: ti.Format, Generated: in.Generated[ti.Key]}
		if !ti.Secret && !in.Secret[ti.Key] && !pi.Generated {
			pi.Value = in.Values[ti.Key]
		}
		v.Inputs = append(v.Inputs, pi)
	}

	var srv *db.Server
	if req.ServerID != nil {
		row, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *req.ServerID, WorkspaceID: &env.WorkspaceID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, apierr.Unprocessable("server_not_found", "the selected server does not exist")
		case err != nil:
			return nil, fmt.Errorf("load server: %w", err)
		}
		srv = &row
		ev.server = srv
	} else if forInstall {
		return nil, apierr.Unprocessable("server_required", "choose the server the stack runs on")
	}

	// Predicted slugs (the install allocates the real ones in its transaction).
	taken := map[string]bool{}
	slugs := map[string]string{}
	for i := range t.Services {
		sv := &t.Services[i]
		base := projects.Slugify(sv.Name, "service")
		existing, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: env.Env.ID, Base: base})
		if err != nil {
			return nil, fmt.Errorf("list slugs: %w", err)
		}
		for k := range taken {
			existing = append(existing, k)
		}
		existing = append(existing, services.ReservedSlugs...)
		slug := projects.UniqueSlug(base, existing)
		taken[slug] = true
		slugs[sv.Key] = slug
	}

	totalMem, hasPorts := 0, false
	for i := range t.Services {
		sv := &t.Services[i]
		ps := TemplatePlanService{
			Key: sv.Key, Name: sv.Name, Slug: slugs[sv.Key], Kind: kindOf(sv), Port: sv.Port, Volumes: []string{}, Files: len(sv.Files),
			Ports: []TemplatePlanPort{}, OneShot: sv.OneShot, DependsOn: nonNil(sv.DependsOn),
		}
		if sv.Image != "" {
			ps.Image = sv.Image
		}
		if sv.Database != nil {
			ps.Database = sv.Database.Engine + " " + sv.Database.Version
		}
		if sv.Resources != nil {
			ps.MemoryMB = sv.Resources.MemoryMB
			totalMem += sv.Resources.MemoryMB
		}
		for _, vol := range sv.Volumes {
			ps.Volumes = append(ps.Volumes, vol.Path)
		}
		for _, p := range sv.Ports {
			ps.Ports = append(ps.Ports, TemplatePlanPort{Name: p.Name, Port: p.Port, Protocol: p.Protocol, Publish: p.Publish})
			hasPorts = hasPorts || p.Publish
		}
		for _, d := range sv.Domains {
			switch {
			case d.Generate:
				ps.Domain = "generated"
			default:
				ps.Domain = expandInputs(d.Host, in.Values, false)
			}
		}
		v.Services = append(v.Services, ps)
		for k, val := range sv.Env {
			if envIsSecret(sv, k, val, in.Secret) {
				v.Secrets = append(v.Secrets, sv.Key+"."+k)
			}
		}
	}
	slices.Sort(v.Secrets)
	v.ProxyRestart = hasPorts

	add := func(id, status, msg string) {
		v.Checks = append(v.Checks, TemplatePlanCheck{ID: id, Status: status, Message: msg})
	}

	switch t.Upstream.Class {
	case LicenseSourceAvailable:
		add("license", CheckWarn, fmt.Sprintf("%s is source-available (%s), not open source. Review the licence before using it commercially.", t.Name, t.Upstream.License))
	case LicenseProprietary:
		add("license", CheckWarn, fmt.Sprintf("%s is proprietary software (%s); only its container image is run here.", t.Name, t.Upstream.License))
	case LicenseCopyleft:
		add("license", CheckOK, fmt.Sprintf("%s is licensed %s.", t.Name, t.Upstream.License))
	default:
		add("license", CheckOK, fmt.Sprintf("%s is licensed %s.", t.Name, t.Upstream.License))
	}
	if t.Heavy {
		add("heavy", CheckWarn, fmt.Sprintf("%s is a heavy install: several GB to download, a lot of RAM and a slow first start. Check the server has the room before deploying.", t.Name))
	}
	if t.RequiresUnrestricted && srv == nil {
		add("unrestricted", CheckWarn, fmt.Sprintf("%s uses devices, GPUs or other host-level Docker options: it installs only on a server an instance admin made unrestricted.", t.Name))
	}
	if len(v.Secrets) > 0 {
		add("secrets", CheckOK, fmt.Sprintf("%d secret variable(s) will be generated or stored encrypted; they are never shown again.", len(v.Secrets)))
	}

	if srv == nil {
		add("server", CheckWarn, "Choose a server to check capacity, architecture and port conflicts.")
	} else {
		s.serverChecks(ctx, q, t, srv, in, &v, totalMem, add)
	}
	if hasPorts {
		add("proxy-restart", CheckWarn, "Publishing TCP/UDP ports restarts the server's proxy briefly (about a second) the first time a new port is added.")
	}
	v.Installable = true
	for _, c := range v.Checks {
		if c.Status == CheckFail {
			v.Installable = false
		}
	}
	ev.view = v
	return ev, nil
}

func kindOf(s *TemplateService) string {
	if s.Database != nil {
		return services.KindDatabase
	}
	if s.Kind == "" {
		return services.KindApp
	}
	return s.Kind
}

// serverChecks adds the checks that need the chosen server.
func (s *Service) serverChecks(ctx context.Context, q *db.Queries, t *CatalogTemplate, srv *db.Server, in ResolvedInputs, v *TemplatePlan, totalMem int, add func(id, status, msg string)) {
	switch {
	case srv.Status == "revoked":
		add("server", CheckFail, fmt.Sprintf("%s has been revoked.", srv.Name))
		return
	case srv.DrainState == services.DrainStateDraining || srv.DrainState == services.DrainStateDrained:
		add("server", CheckFail, fmt.Sprintf("%s is being drained and cannot take new services.", srv.Name))
		return
	case !srv.AcceptWorkloads:
		add("server", CheckFail, fmt.Sprintf("%s does not accept workloads (Accept workloads is off in its settings).", srv.Name))
		return
	case srv.Status != "online":
		add("server", CheckWarn, fmt.Sprintf("%s is %s: the stack is staged, but deploying waits until the agent connects.", srv.Name, srv.Status))
	default:
		add("server", CheckOK, fmt.Sprintf("%s is online.", srv.Name))
	}
	if t.RequiresUnrestricted {
		if srv.Unrestricted {
			add("unrestricted", CheckOK, fmt.Sprintf("%s is unrestricted, so %s may use devices, GPUs or other host-level options.", srv.Name, t.Name))
		} else {
			add("unrestricted", CheckFail, fmt.Sprintf("%s needs a server where an instance admin allowed unrestricted containers (it uses devices, GPUs or other host-level Docker options). %s is a restricted server: choose another server or ask an instance admin.", t.Name, srv.Name))
		}
	}

	var facts agentprotocol.Facts
	if len(srv.Facts) > 0 {
		_ = json.Unmarshal(srv.Facts, &facts)
	}
	if facts.Arch != "" {
		arch := t.Requires.Arch
		if len(arch) == 0 {
			arch = []string{"amd64", "arm64"}
		}
		if slices.Contains(arch, facts.Arch) {
			add("arch", CheckOK, fmt.Sprintf("Images are available for %s.", facts.Arch))
		} else {
			add("arch", CheckFail, fmt.Sprintf("%s runs %s, but this template only supports %s.", srv.Name, facts.Arch, strings.Join(arch, ", ")))
		}
	}
	need := max(t.Requires.MemoryMB, totalMem)
	if facts.MemTotal > 0 && need > 0 {
		totalMB := int(facts.MemTotal >> 20)
		switch {
		case need > totalMB:
			add("memory", CheckFail, fmt.Sprintf("Needs about %d MB of RAM; %s has %d MB in total.", need, srv.Name, totalMB))
		case need*2 > totalMB:
			add("memory", CheckWarn, fmt.Sprintf("Needs about %d MB of RAM; %s has %d MB in total, so it will be tight.", need, srv.Name, totalMB))
		default:
			add("memory", CheckOK, fmt.Sprintf("Needs about %d MB of RAM; %s has %d MB.", need, srv.Name, totalMB))
		}
	}
	if facts.DiskTotal > 0 && t.Requires.DiskGB > 0 && int(facts.DiskTotal>>30) < t.Requires.DiskGB {
		add("disk", CheckWarn, fmt.Sprintf("Needs about %d GB of disk; %s has %d GB in total.", t.Requires.DiskGB, srv.Name, int(facts.DiskTotal>>30)))
	}

	wantsDomain := false
	for _, sv := range t.Services {
		wantsDomain = wantsDomain || len(sv.Domains) > 0
	}
	if wantsDomain {
		if srv.ProxyStatus != nil && *srv.ProxyStatus == agentprotocol.ProxyError {
			add("proxy", CheckFail, fmt.Sprintf("The proxy on %s is not running; domains cannot be attached.", srv.Name))
		}
		hasGen := false
		for i, sv := range t.Services {
			for _, d := range sv.Domains {
				if d.Generate {
					hasGen = true
					continue
				}
				host := expandInputs(d.Host, in.Values, false)
				if host == "" {
					continue
				}
				h, err := domains.ValidateHostname(host)
				if err != nil {
					add("domain", CheckFail, fmt.Sprintf("%s: %v", v.Services[i].Name, err))
					continue
				}
				if n, err := q.CountDomainsByHostname(ctx, h); err == nil && n > 0 {
					add("domain", CheckFail, fmt.Sprintf("The hostname %s is already in use.", h))
				}
			}
		}
		if hasGen {
			if base, ok := domains.BaseDomain(*srv); ok {
				add("domain", CheckOK, "A hostname under "+base+" will be generated.")
			} else {
				add("domain", CheckWarn, "The server has neither a base domain nor a public IP, so no hostname can be generated. Add a domain after the install.")
			}
		}
	}

	if s.Ports != nil {
		publishes := false
		for _, sv := range t.Services {
			for _, p := range sv.Ports {
				publishes = publishes || p.Publish
			}
		}
		if publishes && !domains.SupportsProxyV2(*srv) {
			add("agent", CheckFail, fmt.Sprintf("The agent on %s is too old to publish TCP/UDP ports; update the agent first.", srv.Name))
			return
		}
		for _, sv := range t.Services {
			for _, p := range sv.Ports {
				if !p.Publish {
					continue
				}
				busy, err := s.Ports.PortInUse(ctx, q, srv.ID, p.Protocol, p.Port)
				switch {
				case err != nil:
					add("port-"+strconv.Itoa(p.Port), CheckWarn, fmt.Sprintf("Could not check whether %s/%d is free: %v", p.Protocol, p.Port, err))
				case busy:
					add("port-"+strconv.Itoa(p.Port), CheckFail, fmt.Sprintf("%s/%d is already published by another route on %s.", p.Protocol, p.Port, srv.Name))
				default:
					add("port-"+strconv.Itoa(p.Port), CheckOK, fmt.Sprintf("%s/%d (%s) will be published on %s.", p.Protocol, p.Port, p.Name, srv.Name))
				}
			}
		}
	}
}
