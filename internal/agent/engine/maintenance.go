package engine

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// inheritOperational returns table with every route's maintenance flag and
// firewall taken from the proxy's live table. Both are toggled by
// sync_routes, which carries the whole table; tables built from a deploy
// payload are older than any toggle that happened since the payload was
// dispatched, so applying them verbatim would silently end a maintenance
// window (or start one) or revert a firewall change. A route that is not live
// yet keeps the firewall its payload carried (a first deploy is protected at
// once); its maintenance flag stays off.
func inheritOperational(table, live []agentprotocol.Route) []agentprotocol.Route {
	type state struct {
		maint *agentprotocol.Maintenance
		fw    *agentprotocol.Firewall
	}
	byID := make(map[string]state, len(live))
	for _, r := range live {
		if !r.IsL4() {
			byID[r.ServiceID] = state{r.Maintenance, r.Firewall}
		}
	}
	out := make([]agentprotocol.Route, len(table))
	for i, r := range table {
		s, ok := byID[r.ServiceID]
		r.Maintenance = s.maint
		if ok {
			r.Firewall = s.fw
		}
		out[i] = r
	}
	return out
}
