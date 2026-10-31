package engine

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// inheritMaintenance returns table with every route's maintenance flag taken
// from the proxy's live table. Maintenance is toggled by sync_routes, which
// carries the whole table; tables built from a deploy payload are older than
// any toggle that happened since the payload was dispatched, so applying them
// verbatim would silently end a maintenance window (or start one).
func inheritMaintenance(table, live []agentprotocol.Route) []agentprotocol.Route {
	byID := make(map[string]*agentprotocol.Maintenance, len(live))
	for _, r := range live {
		if r.Maintenance != nil && !r.IsL4() {
			byID[r.ServiceID] = r.Maintenance
		}
	}
	out := make([]agentprotocol.Route, len(table))
	for i, r := range table {
		r.Maintenance = byID[r.ServiceID]
		out[i] = r
	}
	return out
}
