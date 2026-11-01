package agents

import (
	"reflect"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// recordGeo keeps the GeoIP database state the agent reports and notifies
// watchers when it changed. It lives with the connection: a server that is not
// connected has no status to show.
func (h *Hub) recordGeo(c *agentConn, hb agentprotocol.Heartbeat) {
	if prev := c.geo.Swap(hb.Geo); !reflect.DeepEqual(prev, hb.Geo) {
		h.publishServer(c.serverID)
	}
}

// GeoStatus is the GeoIP database the server's agent last reported, or nil
// when the agent is not connected or geolocation is off for it.
func (h *Hub) GeoStatus(serverID uuid.UUID) *agentprotocol.GeoStatus {
	if c := h.conn(serverID); c != nil {
		return c.geo.Load()
	}
	return nil
}
