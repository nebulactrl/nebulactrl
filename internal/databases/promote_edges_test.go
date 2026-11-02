package databases_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// promotion starts a switchover of a streaming replica and returns helpers to drive it.
type promotion struct {
	f       *replicaFixture
	id      uuid.UUID
	primary uuid.UUID
	replica uuid.UUID
}

func (f *replicaFixture) startSwitchover() *promotion {
	f.t.Helper()
	pg := f.primary("pg")
	target := f.streamingReplica(pg.ID, f.server.ID)
	_, err := f.pool.Exec(context.Background(), "UPDATE database_replicas SET replay_lsn = '0/1000000', observed_at = now() WHERE service_id = $1", target)
	require.NoError(f.t, err)
	res, err := f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: f.reload(target).Slug})
	require.NoError(f.t, err)
	return &promotion{f: f, id: *res.PromotionID, primary: pg.ID, replica: target}
}

func (p *promotion) key(step string) string { return "promo:" + p.id.String() + ":" + step }

func (p *promotion) advance() bool {
	p.f.t.Helper()
	done, err := p.f.dbs.AdvancePromotion(context.Background(), p.id)
	require.NoError(p.f.t, err)
	return done
}

func (p *promotion) row() (step, status, message string) {
	p.f.t.Helper()
	r := mustPromotion(p.f.t, p.f, p.id)
	return r.Step, r.Status, r.Message
}

// The switchover rolls back, with the reason, whenever a step toward the new primary fails.
func TestSwitchoverAbortsWhenAStepBeforeThePromotionFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(p *promotion)
		want string
	}{
		{"freezing fails", func(p *promotion) {
			p.advance()
			p.f.complete(p.key("freeze"), agentprotocol.OutcomeFailed, "database is busy", nil)
		}, "freezing the old primary failed: database is busy"},
		{"stopping fails", func(p *promotion) {
			p.advance()
			p.f.ok(p.key("freeze"), nil)
			p.advance()
			p.f.complete(p.key("stop"), agentprotocol.OutcomeFailed, "container will not stop", nil)
		}, "stopping the old primary failed: container will not stop"},
		{"the final position cannot be read", func(p *promotion) {
			p.advance()
			p.f.ok(p.key("freeze"), nil)
			p.advance()
			p.f.ok(p.key("stop"), nil)
			p.advance()
			p.f.complete(p.key("lsn"), agentprotocol.OutcomeFailed, "pg_controldata failed", nil)
		}, "reading the final position failed: pg_controldata failed"},
		{"the final position is garbage", func(p *promotion) {
			p.advance()
			p.f.ok(p.key("freeze"), nil)
			p.advance()
			p.f.ok(p.key("stop"), nil)
			p.advance()
			p.f.ok(p.key("lsn"), agentprotocol.DBAdminResult{FinalLSN: "not-an-lsn"})
		}, "no valid final position"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplicaFixture(t)
			p := f.startSwitchover()
			tc.fail(p)
			require.False(t, p.advance(), "the abort starts: unfence the old primary")
			step, status, msg := p.row()
			require.Equal(t, "abort_restart", step)
			require.Equal(t, "running", status)
			require.Contains(t, msg, tc.want)
			_, err := f.q.GetDatabaseFence(context.Background(), p.primary)
			require.Error(t, err, "the old primary is no longer fenced")
			require.Equal(t, services.DesiredRunning, f.reload(p.primary).DesiredState)
			require.NotNil(t, services.DecodeConfig(f.reload(p.replica).Config).Database.Replica, "the replica was never promoted")
		})
	}
}

func TestSwitchoverFailsWithRecoveryAdviceWhenThePromotionKeepsFailing(t *testing.T) {
	f := newReplicaFixture(t)
	p := f.startSwitchover()
	p.advance()
	p.f.ok(p.key("freeze"), nil)
	p.advance()
	p.f.ok(p.key("stop"), nil)
	p.advance()
	p.f.ok(p.key("lsn"), agentprotocol.DBAdminResult{FinalLSN: "0/1000000"})
	p.advance()
	_, err := f.pool.Exec(context.Background(), "UPDATE database_replicas SET replay_lsn = '0/1000000', observed_at = now() WHERE service_id = $1", p.replica)
	require.NoError(t, err)
	p.advance() // the replica replayed up to the final position: promote
	step, _, _ := p.row()
	require.Equal(t, "promote", step)

	// The agent is asked up to three times; the third failure ends the promotion.
	for n := 1; n <= 3; n++ {
		require.False(t, p.advance(), "attempt %d waits for the agent", n)
		f.complete(p.key("promote:"+string(rune('0'+n))), agentprotocol.OutcomeFailed, "pg_ctl promote timed out", nil)
	}
	require.True(t, p.advance())
	_, status, msg := p.row()
	require.Equal(t, "failed", status)
	require.Contains(t, msg, "promoting the replica failed: pg_ctl promote timed out")
	require.Contains(t, msg, "The old primary is fenced; promote the replica again (forced)")
	_, err = f.q.GetDatabaseFence(context.Background(), p.primary)
	require.NoError(t, err, "the old primary stays fenced: it must not run as a primary again")
}

func TestAPromotionOfAnUnknownIDIsSettled(t *testing.T) {
	f := newReplicaFixture(t)
	done, err := f.dbs.AdvancePromotion(context.Background(), uuid.New())
	require.NoError(t, err)
	require.True(t, done, "nothing to wait for")
}

func TestPromotePlanExplainsModeConsumersAndWhatBlocks(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)
	other := f.streamingReplica(pg.ID, f.server.ID)
	plan := func(id uuid.UUID) databases.PromotePlan {
		t.Helper()
		res, err := f.dbs.Promote(f.ctx, id, databases.PromoteInput{DryRun: true})
		require.NoError(t, err)
		require.True(t, res.DryRun)
		return res.Plan
	}
	blockers := func(p databases.PromotePlan) []string { return problemCodes(p.Blockers) }

	_, err := f.dbs.Promote(f.ctx, pg.ID, databases.PromoteInput{DryRun: true})
	require.Equal(t, "not_a_replica", apiCode(t, err))

	// A healthy group: a switchover; the other replica must follow the new primary; consumers are rewritten.
	api := f.app("api")
	f.setVar(api.ID, "DB", "${{pg.DATABASE_URL}}")
	p := plan(rep)
	require.Equal(t, databases.ModeSwitchover, p.Mode)
	require.Empty(t, p.Blockers)
	require.Equal(t, "pg", p.OldPrimary.Slug)
	require.Equal(t, "agent1", p.OldPrimary.ServerName)
	require.Len(t, p.RebuildReplicas, 1)
	require.Equal(t, other, p.RebuildReplicas[0].ID)
	require.Len(t, p.Consumers, 1)
	require.Equal(t, "api", p.Consumers[0].Slug)
	require.Equal(t, "agent1", p.Consumers[0].ServerName)
	codes := problemCodes(p.Warnings)
	require.Contains(t, codes, "manual")
	require.Contains(t, codes, "rebuild_others")
	require.Contains(t, codes, "old_primary_stopped")
	require.Contains(t, codes, "consumers_rewritten")
	require.NotContains(t, codes, "forced")

	// A replica that is not streaming (primary lost) can still be promoted by a switchover, with a warning.
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'broken' WHERE service_id = $1", rep)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(rep).Warnings), "replica_broken")
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'pending' WHERE service_id = $1", rep)
	require.NoError(t, err)
	require.Contains(t, blockers(plan(rep)), "replica_not_streaming")
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'streaming' WHERE service_id = $1", rep)
	require.NoError(t, err)

	// Its own server decides whether the replica can be promoted at all.
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", rep)
	require.NoError(t, err)
	require.Contains(t, blockers(plan(rep)), "replica_not_running")
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'running' WHERE id = $1", rep)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = '{}' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.Contains(t, blockers(plan(rep)), "agent_too_old")
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, replFacts)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = NULL WHERE id = $1", rep)
	require.NoError(t, err)
	require.Contains(t, blockers(plan(rep)), "replica_agent_offline")
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", rep, f.server.ID)
	require.NoError(t, err)

	// A running promotion blocks another one.
	_, err = f.pool.Exec(bg, `INSERT INTO database_promotions (workspace_id, environment_id, replica_service_id, old_primary_service_id, mode)
		VALUES ($1, $2, $3, $4, 'switchover')`, f.ws.ID, f.env.ID, other, pg.ID)
	require.NoError(t, err)
	require.Contains(t, blockers(plan(rep)), "promote_in_progress")
	_, err = f.pool.Exec(bg, "UPDATE database_promotions SET status = 'failed'")
	require.NoError(t, err)

	// The old primary's server is gone: a forced promote, with the data it may lose spelled out.
	lag := int64(2 << 20)
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET lag_bytes = $2, observed_at = now() WHERE service_id = $1", rep, lag)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	p = plan(rep)
	require.Equal(t, databases.ModeForced, p.Mode)
	require.Contains(t, blockers(p), "replica_agent_offline", "the replica runs on the same, offline server")
	require.Contains(t, p.DataLossWarning, "Forced promote")
	require.Contains(t, p.DataLossWarning, "2.0 MiB")
	require.Contains(t, problemCodes(p.Warnings), "forced")
}

func TestPromotePlanWarnsWhenAConsumerCannotReachTheNewPrimary(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server2.ID)
	api := f.app("api")
	f.setVar(api.ID, "DB", "${{pg.DATABASE_URL}}")
	plan := func() databases.PromotePlan {
		t.Helper()
		res, err := f.dbs.Promote(f.ctx, rep, databases.PromoteInput{DryRun: true})
		require.NoError(t, err)
		require.Len(t, res.Plan.Consumers, 1)
		return res.Plan
	}

	// The consumer is on server 1, the new primary on server 2: a link is needed, and the mesh says what is missing.
	f.mesh.plan = mesh.LinkPlan{DevicesAdded: 1, Blockers: []mesh.Blocker{{Code: "mesh_server_required", Message: "Enable mesh on agent2"}, {Message: "no code"}}}
	c := plan().Consumers[0]
	require.Equal(t, 1, c.DevicesAdded)
	require.Len(t, c.Warnings, 2)
	require.Equal(t, "mesh_server_required", c.Warnings[0].Code)
	require.Contains(t, c.Warnings[0].Message, "Enable mesh on agent2 (api cannot reach the new primary until this is fixed)")
	require.Equal(t, "mesh_server_required", c.Warnings[1].Code, "a blocker without a code is still named")

	f.dbs.SetReplicas(databases.ReplicaDeps{Links: f.links, Deployer: &fakeDeployer{f: f}})
	c = plan().Consumers[0]
	require.Len(t, c.Warnings, 1)
	require.Contains(t, c.Warnings[0].Message, "the server mesh is not available: api cannot reach the new primary")

	// A consumer on the new primary's own server needs no link.
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET server_id = $2 WHERE id = $1", api.ID, f.server2.ID)
	require.NoError(t, err)
	require.Empty(t, plan().Consumers[0].Warnings)
}
