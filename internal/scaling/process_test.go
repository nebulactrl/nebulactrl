package scaling_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// multi inserts an app with the default process "web" (port 3000) and the
// processes worker (no port) and clock (a singleton).
func (f *fixture) multi(slug string) db.Service {
	f.t.Helper()
	return f.service(slug, services.KindApp, services.Config{
		Image: "traefik/whoami:latest", Port: 3000,
		Processes: []services.ProcessConfig{
			{Name: "worker", StartCommand: []string{"bin/worker"}},
			{Name: "clock", StartCommand: []string{"bin/clock"}, Singleton: true},
		},
	})
}

// deployMulti is deploy for a service that runs several processes: the current
// deployment's command carries one spec per process.
func (f *fixture) deployMulti(sv db.Service) uuid.UUID {
	f.t.Helper()
	bg := context.Background()
	envJSON, err := json.Marshal(services.EnvSnapshot{Vars: map[string]string{"API_KEY": canary}, Secret: []string{"API_KEY"}})
	require.NoError(f.t, err)
	relID := uuid.New()
	enc, err := f.box.Seal(secretbox.ReleasesEnv, relID.String(), envJSON)
	require.NoError(f.t, err)
	cfg := services.DecodeConfig(sv.Config)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: relID, ServiceID: sv.ID, Source: src, Config: snap, EnvEnc: enc, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	var specs []agentprotocol.ProcessSpec
	for _, p := range services.RolloutOrder(cfg.EffectiveProcesses()) {
		strategy := services.ProcessStrategy(cfg, 0, p)
		specs = append(specs, agentprotocol.ProcessSpec{
			Name: p.Name, Default: p.Default, Replicas: 1, Strategy: strategy, Port: p.Port, StartCommand: p.Command,
			Healthcheck: p.Healthcheck.Spec(), StopGraceSeconds: p.StopGraceSeconds, Singleton: p.Singleton,
		})
	}
	payload, err := json.Marshal(agentprotocol.DeployPayload{
		DeploymentID: dep.ID.String(), ReleaseID: rel.ID.String(), ServiceID: sv.ID.String(), ServiceSlug: sv.Slug,
		Strategy: "rolling", Image: &agentprotocol.ImageSource{Ref: cfg.Image}, Port: cfg.Port, Replicas: 1, Processes: specs,
		EnvKeys: []string{"API_KEY"}, SecretEnvKeys: []string{"API_KEY"},
	})
	require.NoError(f.t, err)
	_, err = f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, DeploymentID: &dep.ID, Kind: agentprotocol.KindDeploy, Payload: payload,
		IdempotencyKey: "deploy:" + dep.ID.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	return dep.ID
}

func (f *fixture) processRow(id uuid.UUID, process string) (db.ServiceProcessScale, bool) {
	f.t.Helper()
	rows, err := f.q.ListProcessScales(context.Background(), id)
	require.NoError(f.t, err)
	for _, r := range rows {
		if r.Process == process {
			return r, true
		}
	}
	return db.ServiceProcessScale{}, false
}

func (f *fixture) enableProcessAutoscaling(id uuid.UUID, process string, a services.Autoscaling) {
	f.t.Helper()
	raw, _ := json.Marshal(a)
	_, err := f.q.SetProcessAutoscaling(context.Background(), db.SetProcessAutoscalingParams{ServiceID: id, Process: process, Autoscaling: raw})
	require.NoError(f.t, err)
}

// loadProcess fills the last 3 minutes with samples of one replica of one process every 10 s.
func (f *fixture) loadProcess(id uuid.UUID, process string, cpu float64) {
	f.t.Helper()
	now := time.Now()
	for ts := now.Add(-3 * time.Minute); ts.Before(now); ts = ts.Add(10 * time.Second) {
		require.NoError(f.t, f.q.InsertServiceMetricSample(context.Background(), db.InsertServiceMetricSampleParams{
			ServerID: &f.server.ID, ServiceID: id, Ts: ts, Replica: 0, Process: process, CpuPercent: cpu, MemBytes: 200 << 20, MemLimitBytes: 1 << 30,
		}))
	}
}

func (f *fixture) setFacts(caps ...string) {
	f.t.Helper()
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(f.t, err)
}

func decodeScale(t *testing.T, raw json.RawMessage) agentprotocol.ScaleServicePayload {
	t.Helper()
	var p agentprotocol.ScaleServicePayload
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

func processSpec(t *testing.T, p agentprotocol.DeployPayload, name string) agentprotocol.ProcessSpec {
	t.Helper()
	for _, s := range p.Processes {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no process %q in the payload", name)
	return agentprotocol.ProcessSpec{}
}

func TestSetProcessScalesOneNamedProcessThroughTheAgent(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	depID := f.deployMulti(sv)
	f.setFacts(agentprotocol.CapProcesses)

	v, err := f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(3)})
	require.NoError(t, err)
	require.Equal(t, sv.Generation, v.Generation, "a process' replicas are never a staged change")
	require.Equal(t, 1, v.Scaling.Replicas, "the service's own scaling describes the default process")

	row, ok := f.processRow(sv.ID, "worker")
	require.True(t, ok)
	require.EqualValues(t, 3, row.Replicas)
	require.NotNil(t, row.LastScaleAt)
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas, "the default process is untouched")
	ev := services.DecodeScaleEvent(row.LastScaleEvent)
	require.NotNil(t, ev)
	require.Equal(t, 1, ev.From)
	require.Equal(t, 3, ev.To)
	require.Equal(t, "dev@example.com", ev.Actor)

	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	require.Nil(t, cmds[0].DeploymentID)
	require.NotContains(t, string(cmds[0].Payload), canary)
	p := decodeScale(t, cmds[0].Payload)
	require.Equal(t, "worker", p.Process)
	require.Equal(t, 3, p.Replicas)
	require.Equal(t, depID.String(), p.DeploymentID)
	require.True(t, row.LastScaleAt.Equal(p.ScaledAt))
	require.Len(t, p.Deploy.Processes, 3, "the current deployment's payload, processes included")
	require.Equal(t, 3, processSpec(t, p.Deploy, "worker").Replicas)
	require.Equal(t, 1, processSpec(t, p.Deploy, "web").Replicas)
	require.Equal(t, 1, p.Deploy.Replicas, "the top-level fields stay the default process'")

	var audited []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = 'service.scale' AND resource_id = $1", sv.ID.String()).Scan(&audited))
	var md map[string]any
	require.NoError(t, json.Unmarshal(audited, &md))
	require.Equal(t, "worker", md["process"])
	require.EqualValues(t, 3, md["to"])

	// Scaling back stores the new count, again through the agent.
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	require.Len(t, f.scaleCommands(), 2)
	row, _ = f.processRow(sv.ID, "worker")
	require.EqualValues(t, 2, row.Replicas)

	// The send-time secrets of a process' scale command match a deploy's.
	dep := deployments.NewService(deployments.Deps{Pool: f.pool, Box: f.box})
	sec, err := dep.SecretsForCommand(f.ctx, uuid.Nil, agentprotocol.KindScaleService, f.scaleCommands()[0].Payload)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"env:API_KEY": canary}, sec)
}

func TestSetProcessOfTheDefaultProcessIsTheLegacySet(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)

	v, err := f.scal.SetProcess(f.ctx, sv.ID, "web", scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	require.Equal(t, 2, v.Scaling.Replicas)
	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)
	_, ok := f.processRow(sv.ID, "web")
	require.False(t, ok, "the default process has no scale row")

	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	p := decodeScale(t, cmds[0].Payload)
	require.Empty(t, p.Process, "the default process is the empty name")
	require.Equal(t, 2, p.Deploy.Replicas)
	require.Equal(t, 2, processSpec(t, p.Deploy, "web").Replicas, "the default process' spec follows")
	require.Equal(t, 1, processSpec(t, p.Deploy, "worker").Replicas)

	// A service without processes has just its default process.
	plain := f.web("blog")
	_, err = f.scal.SetProcess(f.ctx, plain.ID, "web", scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	require.EqualValues(t, 2, f.reload(plain.ID).Replicas)
}

func TestSetProcessUnknownNameIs404(t *testing.T) {
	f := newFixture(t)
	multi := f.multi("shop")
	_, err := f.scal.SetProcess(f.ctx, multi.ID, "nope", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 404, "unknown_process")

	plain := f.web("blog")
	_, err = f.scal.SetProcess(f.ctx, plain.ID, "worker", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 404, "unknown_process")
	require.Empty(t, f.disp.cmds)
}

func TestSetProcessEligibilityAndValidation(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")

	// A singleton never scales; a port is not needed for the others to.
	_, err := f.scal.SetProcess(f.ctx, sv.ID, "clock", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 422, "replicas_require_stateless")
	require.Contains(t, err.Error(), "Singleton")
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "clock", scaling.SetInput{Autoscaling: &scaling.AutoscalingPatch{Enabled: new(true)}})
	requireAPIError(t, err, 422, "replicas_require_stateless")
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "clock", scaling.SetInput{Replicas: new(1)})
	require.NoError(t, err, "one replica is always allowed")
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(4)})
	require.NoError(t, err, "a process without a port scales")

	// Volumes make every process single-instance.
	vol := f.multi("data")
	_, err = f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: vol.ID, Name: "d", MountPath: "/data"})
	require.NoError(t, err)
	_, err = f.scal.SetProcess(f.ctx, vol.ID, "worker", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 422, "replicas_require_stateless")

	// The same rules as the service's scaling.
	for name, in := range map[string]scaling.SetInput{
		"empty":       {},
		"replicas 0":  {Replicas: new(0)},
		"replicas 11": {Replicas: new(11)},
		"min > max":   {Autoscaling: &scaling.AutoscalingPatch{Min: new(4), Max: new(3)}},
		"target 10":   {Autoscaling: &scaling.AutoscalingPatch{TargetCPUPercent: new(10)}},
	} {
		_, err := f.scal.SetProcess(f.ctx, sv.ID, "worker", in)
		requireAPIError(t, err, 422, "validation_failed")
		_ = name
	}
	v, err := f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Autoscaling: &scaling.AutoscalingPatch{Enabled: new(true), Max: new(6)}})
	require.NoError(t, err)
	_ = v
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 409, "autoscaling_enabled")
	row, _ := f.processRow(sv.ID, "worker")
	as := services.DecodeAutoscaling(row.Autoscaling)
	require.True(t, as.Enabled)
	require.Equal(t, 6, as.Max)
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2), Autoscaling: &scaling.AutoscalingPatch{Enabled: new(false)}})
	require.NoError(t, err)
	row, _ = f.processRow(sv.ID, "worker")
	require.EqualValues(t, 2, row.Replicas)
}

func TestSetProcessOfAStagedProcessOnlyStoresTheSetting(t *testing.T) {
	f := newFixture(t)
	// The running release has no processes; the staged config adds one.
	sv := f.web("shop")
	f.deploy(sv)
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, Processes: []services.ProcessConfig{{Name: "worker", StartCommand: []string{"bin/worker"}}}}.WithDefaults()
	raw, _ := json.Marshal(cfg)
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2::jsonb, generation = generation + 1 WHERE id = $1", sv.ID, raw)
	require.NoError(t, err)

	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(3)})
	require.NoError(t, err)
	row, ok := f.processRow(sv.ID, "worker")
	require.True(t, ok)
	require.EqualValues(t, 3, row.Replicas)
	require.Empty(t, f.scaleCommands(), "nothing runs that process yet")
}

func TestSetProcessNeedsACapableAgentOnceDeployed(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.setFacts() // the agent reported its facts, none of them the processes capability

	_, err := f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 422, "agent_too_old")
	require.Contains(t, err.Error(), "too old to run a service with several processes")
	_, ok := f.processRow(sv.ID, "worker")
	require.False(t, ok, "nothing was stored")
	require.Empty(t, f.scaleCommands())

	f.setFacts(agentprotocol.CapProcesses)
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
}

func TestSetProcessRejectedDuringDeployment(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)
	_, err := f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{
		ServiceID: sv.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(t, err)
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 409, "deployment_in_progress")
}

func TestAutoscalerScalesANamedProcessFromItsOwnSamples(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.enableProcessAutoscaling(sv.ID, "worker", services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})

	// The worker is hot, the web process idle: only the worker's own series counts.
	f.loadProcess(sv.ID, "worker", 90)
	f.loadProcess(sv.ID, "web", 5)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))

	row, _ := f.processRow(sv.ID, "worker")
	require.EqualValues(t, 2, row.Replicas)
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas, "the default process is not the worker")
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	p := decodeScale(t, cmds[0].Payload)
	require.Equal(t, "worker", p.Process)
	require.Equal(t, 2, p.Replicas)
	require.Equal(t, 2, processSpec(t, p.Deploy, "worker").Replicas)

	var md []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = 'service.autoscale' AND resource_id = $1", sv.ID.String()).Scan(&md))
	var meta map[string]any
	require.NoError(t, json.Unmarshal(md, &meta))
	require.Equal(t, "worker", meta["process"])
	require.Equal(t, "CPU 90% > 70% for 2m", meta["reason"])

	// The cooldown is the worker row's own.
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	row, _ = f.processRow(sv.ID, "worker")
	require.EqualValues(t, 2, row.Replicas)
	require.Len(t, f.scaleCommands(), 1)
	_, err := f.pool.Exec(context.Background(), "UPDATE service_process_scale SET last_scale_at = now() - interval '4 minutes' WHERE service_id = $1 AND process = 'worker'", sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	row, _ = f.processRow(sv.ID, "worker")
	require.EqualValues(t, 3, row.Replicas)
}

func TestAutoscalerDefaultProcessOfAMultiProcessServiceUsesItsOwnSamples(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})

	f.loadProcess(sv.ID, "web", 90)
	f.loadProcess(sv.ID, "worker", 5)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))

	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	p := decodeScale(t, cmds[0].Payload)
	require.Empty(t, p.Process)
	require.Equal(t, 2, processSpec(t, p.Deploy, "web").Replicas)
	_, ok := f.processRow(sv.ID, "worker")
	require.False(t, ok, "no worker row was created")
}

func TestAutoscalerNeverScalesASingletonProcessOrOneThatIsNotDeployed(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.enableProcessAutoscaling(sv.ID, "clock", services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.loadProcess(sv.ID, "clock", 95)

	// A process the running release does not have yet.
	staged := f.service("blog", services.KindApp, services.Config{Image: "x", Port: 80})
	f.deploy(staged)
	cfg := services.Config{Image: "x", Port: 80, Processes: []services.ProcessConfig{{Name: "worker", StartCommand: []string{"bin/worker"}}}}.WithDefaults()
	raw, _ := json.Marshal(cfg)
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2::jsonb WHERE id = $1", staged.ID, raw)
	require.NoError(t, err)
	f.enableProcessAutoscaling(staged.ID, "worker", services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.loadProcess(staged.ID, "worker", 95)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	row, _ := f.processRow(sv.ID, "clock")
	require.EqualValues(t, 1, row.Replicas)
	row, _ = f.processRow(staged.ID, "worker")
	require.EqualValues(t, 1, row.Replicas)
	require.Empty(t, f.scaleCommands())
}

// stageRename renames the named process api of a deployed service to web2 in the
// staged config only, as a pending rename.
func (f *fixture) stageRename(sv db.Service) {
	f.t.Helper()
	cfg := services.DecodeConfig(sv.Config)
	cfg.Processes[0].Name = "web2"
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET config = $2::jsonb WHERE id = $1", sv.ID, raw)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.UpsertProcessRename(context.Background(), db.UpsertProcessRenameParams{ServiceID: sv.ID, FromProcess: "api", ToProcess: "web2"}))
}

func TestAutoscalerScalesTheRunningProcessWhileARenameIsPending(t *testing.T) {
	f := newFixture(t)
	sv := f.service("shop", services.KindApp, services.Config{
		Image: "traefik/whoami:latest", Port: 3000,
		Processes: []services.ProcessConfig{{Name: "api", StartCommand: []string{"bin/api"}}},
	})
	f.deployMulti(sv)
	f.stageRename(sv)
	// The scale row was copied to the new name at rename time.
	f.enableProcessAutoscaling(sv.ID, "web2", services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.loadProcess(sv.ID, "api", 90)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))

	row, _ := f.processRow(sv.ID, "web2")
	require.EqualValues(t, 2, row.Replicas, "the decision is stored on the staged name's row")
	_, ok := f.processRow(sv.ID, "api")
	require.False(t, ok, "the old name has no row of its own")
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	p := decodeScale(t, cmds[0].Payload)
	require.Equal(t, "api", p.Process, "the command addresses the running process")
	require.Equal(t, 2, p.Replicas)
	require.Equal(t, 2, processSpec(t, p.Deploy, "api").Replicas)

	var md []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = 'service.autoscale' AND resource_id = $1", sv.ID.String()).Scan(&md))
	var meta map[string]any
	require.NoError(t, json.Unmarshal(md, &meta))
	require.Equal(t, "web2", meta["process"])

	// The cooldown of the staged row applies to the next pass.
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	row, _ = f.processRow(sv.ID, "web2")
	require.EqualValues(t, 2, row.Replicas)
	require.Len(t, f.scaleCommands(), 1)
}

func TestAutoscalerFollowsTheStagedRowOfAPendingRename(t *testing.T) {
	f := newFixture(t)
	sv := f.service("shop", services.KindApp, services.Config{
		Image: "traefik/whoami:latest", Port: 3000,
		Processes: []services.ProcessConfig{{Name: "api", StartCommand: []string{"bin/api"}}},
	})
	f.deployMulti(sv)
	f.stageRename(sv)
	// Autoscaling is on for the old name only: the staged row (off) wins.
	f.enableProcessAutoscaling(sv.ID, "api", services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.loadProcess(sv.ID, "api", 90)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))

	row, _ := f.processRow(sv.ID, "api")
	require.EqualValues(t, 1, row.Replicas)
	_, ok := f.processRow(sv.ID, "web2")
	require.False(t, ok)
	require.Empty(t, f.scaleCommands())
}

func TestBlockedProcessScaleOutNamesTheProcess(t *testing.T) {
	f := newFixture(t)
	rec := &notificationstest.Recorder{}
	f.scal.SetNotifier(rec)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.enableProcessAutoscaling(sv.ID, "worker", services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.loadProcess(sv.ID, "worker", 95)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET disk_alert_active = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	row, _ := f.processRow(sv.ID, "worker")
	require.EqualValues(t, 1, row.Replicas)
	require.NotNil(t, row.ScaleBlockedReason)
	require.Equal(t, "Scale-out blocked: agent1 has an active disk alert", *row.ScaleBlockedReason)
	require.Nil(t, f.reload(sv.ID).ScaleBlockedReason, "the default process is not blocked")

	got := rec.OfType(notifications.TypeScaleBlocked)
	require.Len(t, got, 1)
	require.Equal(t, "shop (worker) can not scale out", got[0].Title)
	require.Equal(t, "Process worker: Scale-out blocked: agent1 has an active disk alert", got[0].Body)
	require.Equal(t, sv.ID, *got[0].ServiceID)

	// Announced once per block.
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.Len(t, rec.OfType(notifications.TypeScaleBlocked), 1)
}

func TestFailedProcessScaleCommandBlocksOnlyThatProcess(t *testing.T) {
	f := newFixture(t)
	rec := &notificationstest.Recorder{}
	f.scal.SetNotifier(rec)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.setFacts(agentprotocol.CapProcesses)
	_, err := f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)

	// The agent could not scale it: the command row exists with that payload.
	id := uuid.New()
	_, err = f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: id, ServerID: f.server.ID, Kind: agentprotocol.KindScaleService, Payload: cmds[0].Payload,
		IdempotencyKey: "scale:" + id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.scal.HandleResult(context.Background(), id, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "no free port"}))

	row, _ := f.processRow(sv.ID, "worker")
	require.NotNil(t, row.ScaleBlockedReason)
	require.Equal(t, "Scale failed: no free port", *row.ScaleBlockedReason)
	require.Nil(t, f.reload(sv.ID).ScaleBlockedReason)
	got := rec.OfType(notifications.TypeScaleBlocked)
	require.Len(t, got, 1)
	require.Equal(t, "shop (worker) can not scale out", got[0].Title)

	// The next manual change clears it.
	_, err = f.scal.SetProcess(f.ctx, sv.ID, "worker", scaling.SetInput{Replicas: new(3)})
	require.NoError(t, err)
	row, _ = f.processRow(sv.ID, "worker")
	require.Nil(t, row.ScaleBlockedReason)
}
