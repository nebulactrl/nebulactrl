package domains

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// stackTarget validates the component and container port of a stack domain:
// the component is required and must belong to the stack; the port is
// required unless the component declares exactly one; any port 1..65535 is
// accepted (a component may serve a port it does not declare).
func stackTarget(cfg services.Config, component string, port int) (int, error) {
	if component == "" {
		return 0, apierr.Unprocessable("component_required", "choose the component of the stack that the domain routes to")
	}
	comp, ok := cfg.Stack.Component(component)
	if !ok {
		return 0, apierr.Unprocessable("unknown_component", fmt.Sprintf("the stack has no component %q", component)).
			WithDetails(map[string]any{"components": cfg.Stack.ComponentNames()})
	}
	if port == 0 {
		if len(comp.Ports) != 1 {
			return 0, apierr.Unprocessable("target_port_required",
				fmt.Sprintf("component %q declares %d ports: set targetPort", component, len(comp.Ports)))
		}
		return comp.Ports[0], nil
	}
	if port < 1 || port > 65535 {
		return 0, apierr.Unprocessable("invalid_target_port", "targetPort must be between 1 and 65535")
	}
	return port, nil
}

// stackRoutes groups the http routes of stacks: one Route per (stack,
// component, container port), named by agentprotocol.StackRouteID, with the
// component's container as the only upstream. Maintenance and health paths do
// not apply to stacks.
type stackRoutes struct {
	idx    map[string]int
	routes []agentprotocol.Route
}

func newStackRoutes() *stackRoutes { return &stackRoutes{idx: map[string]int{}} }

// add puts one domain row into its (component, port) route.
func (b *stackRoutes) add(serviceID, environmentID uuid.UUID, blockEgress bool, d db.Domain) {
	id := serviceID.String()
	key := id + "/" + d.Component + "/" + strconv.Itoa(int(d.TargetPort))
	i, ok := b.idx[key]
	if !ok {
		i = len(b.routes)
		b.idx[key] = i
		b.routes = append(b.routes, agentprotocol.Route{
			ServiceID: agentprotocol.StackRouteID(id, d.Component, int(d.TargetPort)),
			Stack:     id,
			Upstreams: []string{agentprotocol.StackContainerName(id, d.Component) + ":" + strconv.Itoa(int(d.TargetPort))},
			Networks:  services.RouteNetworks(environmentID, blockEgress),
		})
	}
	b.routes[i].Domains = append(b.routes[i].Domains, RuleFromRow(d))
	if !slices.Contains(b.routes[i].Hosts, d.Hostname) {
		b.routes[i].Hosts = append(b.routes[i].Hosts, d.Hostname)
	}
}

// StackRoutes builds the http routes of one stack from its live domains
// (StackSpec.Routes of a deploy): one Route per (component, port).
func StackRoutes(serviceID, environmentID uuid.UUID, blockEgress bool, doms []db.Domain) []agentprotocol.Route {
	sorted := slices.Clone(doms)
	slices.SortStableFunc(sorted, func(a, b db.Domain) int { return int(a.TargetPort) - int(b.TargetPort) })
	b := newStackRoutes()
	for _, d := range sorted {
		b.add(serviceID, environmentID, blockEgress, d)
	}
	return b.routes
}
