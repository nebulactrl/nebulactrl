package agents

import (
	"slices"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// wantsAdoptionHold reports whether a hello asks to hold its server for
// adoption if this is the server's first hello: the agent already runs
// workloads of an earlier NebulaCtrl. While a server is held the desired state
// tells the agent to leave every managed container and live route it does not
// know alone (Reconcile.KeepUnknown), until a person adopts or leaves them.
//
// Only the first hello after enrollment holds ("when the server is added"):
// UpdateServerHello sets the hold in the same statement that flips a pending
// server online. A reconnect of a known server never holds, so its orphan GC
// is unchanged. An agent that does not advertise CapAdoption cannot honour the
// hold, so it is not held.
func wantsAdoptionHold(f agentprotocol.Facts) bool {
	return slices.Contains(f.Capabilities, agentprotocol.CapAdoption) && len(f.ManagedServices) > 0
}
