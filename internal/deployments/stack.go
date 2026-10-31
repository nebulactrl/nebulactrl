package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

// ReasonStack explains the strategy of a Compose stack.
const ReasonStack = "Compose stacks are always recreated: `docker compose up` replaces the containers"

// stackProblems lists what blocks a deploy of the stack config cfg onto srv:
// a compose file the allow-list refuses (the server's Unrestricted Compose
// switch is read now, so rollbacks and reconcile redeploys are checked too),
// a server whose agent has no Docker Compose plugin, and component names that
// collide with other services of the environment.
func stackProblems(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, srv *db.Server) ([]PlanBlocking, error) {
	if cfg.Stack == nil {
		return nil, nil
	}
	var out []PlanBlocking
	if srv != nil {
		if has, _ := services.ServerHasCapability(*srv, agentprotocol.CapCompose); !has {
			out = append(out, PlanBlocking{
				Code:    "compose_unavailable",
				Message: fmt.Sprintf("Server %s cannot run Compose stacks: install the Docker Compose plugin (version 2.24 or newer) on it and restart the agent.", srv.Name),
			})
		}
	}
	unrestricted := srv != nil && srv.Unrestricted
	if _, err := services.CheckStackCompose(ctx, q, cfg.Stack.Compose, unrestricted); err != nil {
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Code != "invalid_compose" {
			return nil, err
		}
		b := PlanBlocking{Code: ae.Code, Message: ae.Message}
		if vs, ok := ae.Details["violations"].([]composestack.Violation); ok {
			b.Violations = vs
		}
		out = append(out, b)
	}
	conflicts, err := stackAliasConflicts(ctx, q, sc, cfg)
	if err != nil {
		return nil, err
	}
	return append(out, conflicts...), nil
}

// stackAliasConflicts reports components whose name equals the slug of another
// service of the environment or a component of another stack in it: both are
// DNS aliases on the environment network.
func stackAliasConflicts(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config) ([]PlanBlocking, error) {
	rows, err := q.ListAllEnvironmentServices(ctx, sc.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("list environment services: %w", err)
	}
	taken := map[string]string{} // alias -> what owns it
	for _, r := range rows {
		if r.ID == sc.Service.ID {
			continue
		}
		taken[r.Slug] = "the service " + r.Name
		if r.Kind == services.KindStack {
			other := services.DecodeConfig(r.Config).Stack
			for _, n := range other.ComponentNames() {
				if _, dup := taken[n]; !dup {
					taken[n] = "the component " + n + " of the stack " + r.Name
				}
			}
		}
	}
	var out []PlanBlocking
	for _, n := range cfg.Stack.ComponentNames() {
		if owner, clash := taken[n]; clash {
			out = append(out, PlanBlocking{
				Code:    "stack_alias_conflict",
				Message: fmt.Sprintf("The component %q has the same name as %s in this environment; both would answer to that name on the private network. Rename the component in the compose file.", n, owner),
			})
		}
	}
	return out, nil
}

// stackWarnings are the static non-blocking concerns about deploying a stack
// (the compose notes need the agent, which runs the real override).
func stackWarnings(cfg services.Config) []string {
	out := []string{"Components restart on every deploy: `docker compose up` replaces the containers."}
	var ported []string
	for _, c := range cfg.Stack.Components {
		if len(c.Ports) > 0 {
			ported = append(ported, c.Name)
		}
	}
	if len(ported) > 0 {
		out = append(out, fmt.Sprintf("Published ports (ports: in the compose file of %s) are not published on the host; point a domain at a container port to route HTTP traffic.", strings.Join(ported, ", ")))
	}
	return out
}

// stackSpec builds DeployPayload.Stack: the compose file as written, the
// components the control plane derived, the server's Unrestricted Compose
// switch read now, the per-component limits, the pinned images of the release
// (rollbacks and redeploys; a fresh deploy pulls what the file says) and the
// stack's own http routes from its live domains. It carries no secret value.
func stackSpec(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, srv db.Server, source services.ReleaseSource, doms []db.Domain) (*agentprotocol.StackSpec, error) {
	st := cfg.Stack
	spec := &agentprotocol.StackSpec{
		Compose:      st.Compose,
		Components:   slices.Clone(st.Components),
		Unrestricted: srv.Unrestricted,
		Resources:    maps.Clone(st.Resources),
		Images:       maps.Clone(source.Components),
		WaitSeconds:  st.WaitSeconds,
	}
	// A rollback to a release with other components must not route to a
	// component it does not run.
	var routed []db.Domain
	for _, d := range doms {
		if _, ok := st.Component(d.Component); ok {
			routed = append(routed, d)
		}
	}
	spec.Routes = domains.StackRoutes(sc.Service.ID, sc.EnvironmentID, cfg.BlockEgress, routed)
	if err := dns01.ApplyAuto(ctx, q, srv.ID, spec.Routes); err != nil {
		return nil, err
	}
	if err := cloudflare.ApplyOriginLock(ctx, q, srv.ID, spec.Routes); err != nil {
		return nil, err
	}
	return spec, nil
}

// stackImages returns the component images of a deploy result, nil for other services.
func stackImages(dr agentprotocol.DeployResult) map[string]string {
	if dr.Stack == nil || len(dr.Stack.Images) == 0 {
		return nil
	}
	return dr.Stack.Images
}

// stackObserved seeds the service's observed state from a stack deploy result,
// so the components show up before the agent's next report. It is nil for
// results that carry no components (every other service).
func stackObserved(deploymentID uuid.UUID, dr agentprotocol.DeployResult) json.RawMessage {
	if dr.Stack == nil || len(dr.Stack.Components) == 0 {
		return nil
	}
	view := services.ObservedView{
		State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, DeploymentID: deploymentID.String(),
		ReplicasDesired: len(dr.Stack.Components), Replicas: make([]services.ObservedReplicaView, 0, len(dr.Stack.Components)),
	}
	running := false
	for _, c := range dr.Stack.Components {
		rv := services.ObservedReplicaView{
			Index: c.Index, ContainerID: c.ContainerID, State: c.State, Health: c.Health, RestartCount: c.RestartCount, Component: c.Component,
		}
		if !c.StartedAt.IsZero() {
			t := c.StartedAt
			rv.StartedAt = &t
		}
		if c.State == agentprotocol.StateRunning {
			running = true
		}
		if (c.State == agentprotocol.StateRunning || c.State == agentprotocol.StateExited) && c.Health == agentprotocol.HealthHealthy {
			view.ReplicasReady++
		}
		view.Replicas = append(view.Replicas, rv)
	}
	if !running {
		view.State = agentprotocol.StateExited
	}
	if view.ReplicasReady < view.ReplicasDesired {
		view.Health = agentprotocol.HealthDegraded
		if view.ReplicasReady == 0 {
			view.Health = agentprotocol.HealthUnhealthy
		}
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return nil
	}
	return raw
}
