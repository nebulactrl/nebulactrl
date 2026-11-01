package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

// StackConfig is the Docker Compose project of a kind=stack service
// (Config.Stack). One stack is one service: one deployment, one release, one
// rollback. Its compose services are components; they have no rows of their own.
type StackConfig struct {
	// Compose is the compose file as written. `${VAR}` placeholders are
	// interpolated by the agent from the service's variables at deploy time.
	Compose string `json:"compose" maxLength:"262144" doc:"The compose file (YAML) as written; ${VAR} placeholders read the service's variables"`
	// Components are the compose services, derived by the server on every write
	// from Compose (sorted by name; Index is the position). Clients never set them.
	Components []agentprotocol.StackComponent `json:"components" doc:"Derived by the server from the compose file; never set by clients"`
	// Resources are per-component limits applied when the compose file sets none.
	Resources map[string]agentprotocol.StackResources `json:"resources,omitempty" doc:"Per-component memory/CPU limits, keyed by component name; the compose file's own limits win"`
	// WaitSeconds bounds `docker compose up --wait` (default 300, 1..3600).
	WaitSeconds int `json:"waitSeconds,omitempty" doc:"How long a deploy waits for every component to become healthy (default 300, max 3600)"`
}

// StackConfigInput is StackConfig as clients write it: the components are
// derived, so they are not an input.
type StackConfigInput struct {
	Compose     string                                  `json:"compose" minLength:"1" maxLength:"262144" doc:"The compose file (YAML) as written; ${VAR} placeholders read the service's variables"`
	Resources   map[string]agentprotocol.StackResources `json:"resources,omitempty" doc:"Per-component memory/CPU limits, keyed by component name; the compose file's own limits win"`
	WaitSeconds int                                     `json:"waitSeconds,omitempty" doc:"How long a deploy waits for every component to become healthy (default 300, max 3600)"`
}

func (c *StackConfigInput) toConfig() *StackConfig {
	if c == nil {
		return nil
	}
	return &StackConfig{Compose: c.Compose, Resources: c.Resources, WaitSeconds: c.WaitSeconds}
}

// maxStackWaitSeconds caps StackConfig.WaitSeconds.
const maxStackWaitSeconds = 3600

// withDefaults returns a copy with the wait timeout defaulted, the components
// derived when they are missing (rows written before the compose file parsed
// have none) and the nil collections replaced by empty ones.
func (c *StackConfig) withDefaults() *StackConfig {
	if c == nil {
		return nil
	}
	out := *c
	if out.WaitSeconds == 0 {
		out.WaitSeconds = agentprotocol.DefaultStackWaitSeconds
	}
	if len(out.Components) == 0 {
		if p, err := composestack.Parse([]byte(out.Compose)); err == nil {
			out.Components = p.Components()
		}
	}
	if out.Components == nil {
		out.Components = []agentprotocol.StackComponent{}
	}
	return &out
}

// ComponentNames lists the component names, sorted.
func (c *StackConfig) ComponentNames() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.Components))
	for i, comp := range c.Components {
		out[i] = comp.Name
	}
	return out
}

// Component returns the named component.
func (c *StackConfig) Component(name string) (agentprotocol.StackComponent, bool) {
	if c == nil {
		return agentprotocol.StackComponent{}, false
	}
	for _, comp := range c.Components {
		if comp.Name == name {
			return comp, true
		}
	}
	return agentprotocol.StackComponent{}, false
}

// validateStack checks the structure of a stack config. The allow-list needs
// the service's server (the "Unrestricted" switch) and is enforced by
// CheckStackCompose where the server is known.
func (c Config) validateStack() error {
	if c.Stack == nil {
		return invalid("stack.compose is required for compose sources")
	}
	switch {
	case c.Image != "" || c.GitURL != "":
		return invalid("image and git sources are not available for Compose stacks")
	case c.Port != 0 || len(c.StartCommand) > 0:
		return invalid("port and startCommand are not available for Compose stacks: declare them in the compose file")
	case len(c.Files) > 0 || c.OneShot || c.Route != nil || c.Database != nil:
		return invalid("files, oneshot, route and database settings are not available for Compose stacks")
	case len(c.PreDeployCommand) > 0 || len(c.PostDeployCommand) > 0:
		return invalid("pre/post-deploy commands are not available for Compose stacks")
	case c.Tailnet != nil:
		return invalid("tailnet is not available for Compose stacks")
	case c.Docker != nil:
		return invalid("docker options are not available for Compose stacks: set them in the compose file")
	case c.MemoryMB != 0 || c.CPUs != 0:
		return invalid("memoryMb and cpus are not available for Compose stacks: use stack.resources per component")
	case c.Healthcheck.Type != HealthNone:
		return invalid("health checks are not available for Compose stacks: use healthcheck in the compose file")
	}
	if strings.TrimSpace(c.Stack.Compose) == "" {
		return invalid("stack.compose is required for compose sources")
	}
	p, err := composestack.Parse([]byte(c.Stack.Compose))
	if err != nil {
		return &composeParseError{err: err}
	}
	if c.Stack.WaitSeconds < 1 || c.Stack.WaitSeconds > maxStackWaitSeconds {
		return invalid("stack.waitSeconds must be between 1 and %d", maxStackWaitSeconds)
	}
	names := map[string]bool{}
	for _, n := range p.Names() {
		names[n] = true
	}
	for name, r := range c.Stack.Resources {
		switch {
		case !names[name]:
			return invalid("stack.resources: %q is not a service of the compose file", name)
		case r.MemoryMB < 0 || r.CPUs < 0:
			return invalid("stack.resources.%s: limits must not be negative", name)
		case r.MemoryMB > 0 && r.MemoryMB < 6:
			return invalid("stack.resources.%s: memoryMb must be at least 6", name)
		}
	}
	return nil
}

// ErrNotForStacks is the shared 422 for features a Compose stack does not
// have (scaling, volumes API, files, tasks, maintenance, tailnet, ...).
func ErrNotForStacks() error {
	return apierr.Unprocessable("not_applicable", "Not available for Compose stacks")
}

// RequireNotStack returns the shared 422 `not_applicable` for stacks.
func RequireNotStack(sc Scope) error {
	if sc.Service.Kind == KindStack {
		return ErrNotForStacks()
	}
	return nil
}

// StackPolicy is the compose policy for a server: the per-server Unrestricted
// switch. The compose is pre-checked as written, so no project name is
// acceptable (Project = "").
func StackPolicy(unrestricted bool) composestack.Policy {
	return composestack.Policy{Unrestricted: unrestricted}
}

// InvalidComposeError is the 422 `invalid_compose` with the violations as details.
func InvalidComposeError(parseErr error, vs []composestack.Violation) *apierr.Error {
	if vs == nil {
		vs = []composestack.Violation{}
	}
	var msg string
	switch {
	case parseErr != nil:
		msg = "the compose file is not valid: " + parseErr.Error()
	default:
		msg = "the compose file breaks the stack rules:\n" + composestack.Summary(vs)
		if len(composestack.Restricted(vs)) > 0 {
			msg += "\nAn instance admin can allow the restricted ones by switching the server to Unrestricted."
		}
	}
	return apierr.Unprocessable("invalid_compose", msg).WithDetails(map[string]any{"violations": vs})
}

// CheckStackCompose parses the compose file and applies the allow-list for a
// server that is (or is not) unrestricted. A nil error means the stack may be
// staged and deployed.
func CheckStackCompose(ctx context.Context, q *db.Queries, compose string, unrestricted bool) (*composestack.Project, error) {
	p, err := composestack.Parse([]byte(compose))
	if err != nil {
		return nil, InvalidComposeError(err, nil)
	}
	if vs := composestack.Check(p, StackPolicy(unrestricted)); len(vs) > 0 {
		return nil, InvalidComposeError(nil, vs)
	}
	return p, nil
}

// checkStackWrite applies the allow-list to a stack config for the server it
// will run on (none yet: restricted) and overwrites the derived components.
func (s *Service) checkStackWrite(ctx context.Context, q *db.Queries, cfg Config, serverID *uuid.UUID) (Config, error) {
	unrestricted := false
	if serverID != nil {
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID})
		if err != nil {
			return cfg, fmt.Errorf("load server: %w", err)
		}
		unrestricted = srv.Unrestricted
	}
	p, err := CheckStackCompose(ctx, q, cfg.Stack.Compose, unrestricted)
	if err != nil {
		return cfg, err
	}
	st := *cfg.Stack
	st.Components = p.Components()
	cfg.Stack = &st
	return cfg, nil
}

// ServerFacts decodes the facts the server's agent reported; known is false
// until it reported any.
func ServerFacts(srv db.Server) (f agentprotocol.Facts, known bool) {
	if len(srv.Facts) == 0 || json.Unmarshal(srv.Facts, &f) != nil {
		return agentprotocol.Facts{}, false
	}
	return f, f.OS != ""
}

// ServerHasCapability reports whether the server's agent advertised the
// capability; known is false before the agent reported its facts.
func ServerHasCapability(srv db.Server, capability string) (has, known bool) {
	f, known := ServerFacts(srv)
	return slices.Contains(f.Capabilities, capability), known
}

// CheckComponentsInUse refuses a stack config that drops a component which
// still has domains (422 `component_in_use`).
func CheckComponentsInUse(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg Config) error {
	doms, err := q.ListServiceDomains(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	for _, d := range doms {
		if _, ok := cfg.Stack.Component(d.Component); !ok {
			return apierr.Unprocessable("component_in_use",
				fmt.Sprintf("component %q still has the domain %s: remove the domain first", d.Component, d.Hostname)).
				WithDetails(map[string]any{"component": d.Component, "hostname": d.Hostname})
		}
	}
	return nil
}

// DeriveStack returns cfg with Stack.Components re-derived from the compose
// file (cfg is unchanged when it has no stack or the file does not parse; the
// write paths reject such a file anyway).
func DeriveStack(cfg Config) Config {
	if cfg.Stack == nil {
		return cfg
	}
	p, err := composestack.Parse([]byte(cfg.Stack.Compose))
	if err != nil {
		return cfg
	}
	st := *cfg.Stack
	st.Components = p.Components()
	cfg.Stack = &st
	return cfg
}

// StackCheck is the result of POST /environments/{id}/stacks/check.
type StackCheck struct {
	Components []agentprotocol.StackComponent `json:"components"`
	Violations []composestack.Violation       `json:"violations"`
	// Notes are warnings about the compose file (ports that are not published,
	// profiles that do not start by default, networks without a route).
	Notes []string `json:"notes"`
	// Unrestricted is whether the selected server has Unrestricted on
	// (false without a server).
	Unrestricted bool `json:"unrestricted"`
	// ComposeAvailable is whether the selected server's agent has the Docker
	// Compose plugin (>= 2.24); null without a server or before it reported facts.
	ComposeAvailable *bool `json:"composeAvailable"`
}

// CheckStack parses a compose file and applies the allow-list for the given
// server (none: the file is checked as restricted). It has no side effects.
// An unparsable file is a 422 `invalid_compose`; everything else is reported
// in the result.
func (s *Service) CheckStack(ctx context.Context, envID uuid.UUID, compose string, serverID *uuid.UUID) (StackCheck, error) {
	if _, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID); err != nil {
		return StackCheck{}, err
	}
	out := StackCheck{Components: []agentprotocol.StackComponent{}, Violations: []composestack.Violation{}, Notes: []string{}}
	if serverID != nil {
		ws := policy.WorkspaceID(ctx)
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID, WorkspaceID: &ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return StackCheck{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
		}
		if err != nil {
			return StackCheck{}, fmt.Errorf("load server: %w", err)
		}
		out.Unrestricted = srv.Unrestricted
		if has, known := ServerHasCapability(srv, agentprotocol.CapCompose); known {
			out.ComposeAvailable = &has
		}
	}
	p, err := composestack.Parse([]byte(compose))
	if err != nil {
		return StackCheck{}, InvalidComposeError(err, nil)
	}
	out.Components = p.Components()
	if vs := composestack.Check(p, StackPolicy(out.Unrestricted)); len(vs) > 0 {
		out.Violations = vs
	}
	// The override generator knows the notes; its IDs do not matter for them.
	if ov, err := composestack.BuildOverride(p, composestack.OverrideInput{ServiceID: uuid.Nil.String(), DeploymentID: uuid.Nil.String()}); err == nil && len(ov.Notes) > 0 {
		out.Notes = ov.Notes
	}
	return out, nil
}

type checkStackInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          struct {
		Compose  string     `json:"compose" minLength:"1" maxLength:"262144" doc:"The compose file (YAML) to check"`
		ServerID *uuid.UUID `json:"serverId,omitempty" doc:"The server the stack will run on; decides whether Unrestricted applies"`
	}
}

type checkStackOutput struct{ Body StackCheck }

// RegisterStacks attaches the stack endpoints.
func RegisterStacks(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkStack", Method: http.MethodPost, Path: "/environments/{environmentId}/stacks/check", Tags: []string{"stacks"},
		Summary: "Check a compose file for running as a stack",
		Description: "Parses the compose file, lists its components and applies the Compose allow-list for the given server. " +
			"Nothing is stored. Violations of kind `unsupported` always block a stack; `restricted` ones only on a server without Unrestricted. " +
			"A file that is not valid YAML or not a compose file is a 422 `invalid_compose`.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *checkStackInput) (*checkStackOutput, error) {
		out, err := svc.CheckStack(ctx, in.EnvironmentID, in.Body.Compose, in.Body.ServerID)
		if err != nil {
			return nil, err
		}
		return &checkStackOutput{Body: out}, nil
	})

}

// resolveKindSource defaults and cross-checks the kind and source type of a
// new service: a stack is kind stack with source compose, nothing else is.
func resolveKindSource(kind, sourceType string) (string, string, error) {
	switch {
	case kind == "" && sourceType == SourceCompose:
		kind = KindStack
	case kind == "":
		kind = KindApp
	}
	if kind == KindStack && sourceType == "" {
		sourceType = SourceCompose
	}
	if kind != KindApp && kind != KindWorker && kind != KindDatabase && kind != KindStack {
		return "", "", apierr.Unprocessable("validation_failed", "kind must be app, worker, database or stack")
	}
	if sourceType != "" && (kind == KindStack) != (sourceType == SourceCompose) {
		return "", "", apierr.Unprocessable("validation_failed", "a Compose stack is kind stack with sourceType compose")
	}
	return kind, sourceType, nil
}

// CheckLogComponent validates the `component` filter of the log endpoints: ""
// is every component; a name must belong to a stack and to its components.
func CheckLogComponent(sc Scope, component string) error {
	if component == "" {
		return nil
	}
	if sc.Service.Kind != KindStack {
		return apierr.Unprocessable("validation_failed", "component only applies to Compose stacks")
	}
	if _, ok := sc.Config().Stack.Component(component); !ok {
		return apierr.Unprocessable("unknown_component", fmt.Sprintf("the stack has no component %q", component))
	}
	return nil
}

// composeParseError is a compose file that does not parse: an invalid config
// whose API form is 422 `invalid_compose`.
type composeParseError struct{ err error }

func (e *composeParseError) Error() string {
	return ErrInvalidConfig.Error() + ": stack.compose: " + e.err.Error()
}

func (e *composeParseError) Unwrap() error { return ErrInvalidConfig }
