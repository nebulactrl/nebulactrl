package deployments

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
)

func TestDiscardingARenameRestoresTheProcessAndDropsTheStagedName(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	bg := context.Background()
	released := keepProcesses(processesConfig(), "api", "worker")
	setConfig(t, f, web.ID, configJSON(t, released))
	f.setScale(web.ID, "api", 3)
	_, err := f.doms.CreateForComponent(f.ctx, web.ID, "api.example.com", 0, "api", domains.RuleInput{})
	require.NoError(t, err)
	f.deployWeb(web.ID)

	// api is renamed to gateway: the staged config has the new name, the rename is recorded and the
	// new name got a copy of the scale row.
	staged := released
	staged.Processes = slices.Clone(released.Processes)
	for i := range staged.Processes {
		if staged.Processes[i].Name == "api" {
			staged.Processes[i].Name = "gateway"
		}
	}
	setConfig(t, f, web.ID, configJSON(t, staged))
	require.NoError(t, f.q.CopyProcessScale(bg, db.CopyProcessScaleParams{ServiceID: web.ID, FromProcess: "api", ToProcess: "gateway"}))
	require.NoError(t, f.q.UpsertProcessRename(bg, db.UpsertProcessRenameParams{ServiceID: web.ID, FromProcess: "api", ToProcess: "gateway"}))
	require.Equal(t, map[string]int{"api": 3, "gateway": 3}, f.scales(web.ID))

	// The old name comes back first (its domain needs it), then the new one goes.
	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "config.processes.api")
	require.NoError(t, err)
	renames, err := f.q.ListProcessRenames(bg, web.ID)
	require.NoError(t, err)
	require.Empty(t, renames, "the rename is not staged any more once the old name is back")
	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "config.processes.gateway")
	require.NoError(t, err)

	require.Equal(t, map[string]int{"api": 3}, f.scales(web.ID), "the copy made for the staged name is gone, api keeps its count")
	doms, err := f.q.ListServiceDomains(bg, web.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)
	require.Equal(t, "api", doms[0].Component, "the domain never left the running name")
}

func TestDiscardingAPortChangeRetargetsTheProcessDomains(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	bg := context.Background()
	released := keepProcesses(processesConfig(), "api")
	setConfig(t, f, web.ID, configJSON(t, released))
	_, err := f.doms.CreateForComponent(f.ctx, web.ID, "api.example.com", 0, "api", domains.RuleInput{})
	require.NoError(t, err)
	f.deployWeb(web.ID)

	// The port change was staged together with the domain following it.
	staged := released
	staged.Processes = slices.Clone(released.Processes)
	staged.Processes[0].Port = 4001
	setConfig(t, f, web.ID, configJSON(t, staged))
	_, err = f.pool.Exec(bg, "UPDATE domains SET target_port = 4001 WHERE service_id = $1", web.ID)
	require.NoError(t, err)

	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "config.processes.api.port")
	require.NoError(t, err)
	doms, err := f.q.ListServiceDomains(bg, web.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)
	require.EqualValues(t, 4000, doms[0].TargetPort, "the domain targets the restored port again")
}
