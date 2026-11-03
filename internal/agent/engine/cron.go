package engine

import (
	"context"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// noStart finishes a deploy of a cron service (DeployPayload.NoStart): the
// image is already pulled or built, and nothing is started. The service's
// containers from earlier releases are stopped and removed, and the service is
// forgotten so reconciliation neither restarts nor reports it. Its scheduled
// runs start the image themselves (runOneShot with ServiceRun).
func (d *deploy) noStart(ctx context.Context, st *deployState) agentprotocol.DeployResult {
	e := d.e
	all, err := e.containersOf(ctx, d.p.ServiceID, true)
	if err != nil {
		return d.fail("list containers: " + err.Error())
	}
	if len(all) > 0 {
		d.logf("Removing %d container(s) of the previous release: a cron service only runs on schedule", len(all))
		st.prev, st.recreate = all, true
		d.retire(ctx, st)
	}
	e.mu.Lock()
	delete(e.local, d.p.ServiceID)
	delete(e.desired, d.p.ServiceID)
	e.mu.Unlock()
	d.pruneImages(ctx)
	return d.result(agentprotocol.OutcomeSucceeded, agentprotocol.CronDeployMessage, "")
}
