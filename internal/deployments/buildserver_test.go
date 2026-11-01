package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// dbDisp is a dispatcher that records commands and writes real agent_commands
// rows, which the slot accounting and the frame ownership checks read.
type dbDisp struct {
	cmds []agents.NewCommand
	ids  []uuid.UUID
}

func (d *dbDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	key := c.IdempotencyKey
	if key == "" {
		key = id.String()
	}
	if _, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: key, ExpiresAt: time.Now().Add(15 * time.Minute),
	}); err != nil {
		return uuid.Nil, err
	}
	d.cmds = append(d.cmds, c)
	d.ids = append(d.ids, id)
	return id, nil
}

func (d *dbDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, errors.New("not supported")
}

// fakeRegistries is the registry provider of the tests.
type fakeRegistries struct {
	info  RegistryInfo
	creds []string
}

func (r *fakeRegistries) RegistryInfo(_ context.Context, id uuid.UUID) (RegistryInfo, error) {
	i := r.info
	i.ID = id
	return i, nil
}

func (r *fakeRegistries) RegistryCredentials(_ context.Context, _ uuid.UUID, repository string, actions []string, _ time.Duration) (string, error) {
	c := "cred:" + repository + ":" + strings.Join(actions, "+")
	r.creds = append(r.creds, c)
	return c, nil
}

type buildFixture struct {
	*routeFixture
	dd      *dbDisp
	reg     *fakeRegistries
	regID   uuid.UUID
	builder db.Server
}

var (
	sha1 = strings.Repeat("a", 40)
	sha2 = strings.Repeat("b", 40)
	dig1 = "sha256:" + strings.Repeat("1", 64)
	dig2 = "sha256:" + strings.Repeat("2", 64)
)

// newBuildFixture sets up a team whose default is to build on a build server: an
// online amd64 builder with the build capability and an external registry.
func newBuildFixture(t *testing.T) *buildFixture {
	t.Helper()
	rf := newRouteFixture(t)
	bg := context.Background()
	dd := &dbDisp{}
	rf.deps.Dispatcher = dd
	reg := &fakeRegistries{info: RegistryInfo{Host: "registry.example.com", Healthy: true}}
	rf.deps.SetRegistries(reg)

	// The target accepts cancel_deploy; the facts name an architecture.
	tfacts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapCancelDeploy}})
	_, err := rf.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", rf.server.ID, tfacts)
	require.NoError(t, err)

	f := &buildFixture{routeFixture: rf, dd: dd, reg: reg}
	f.builder = f.addBuilder("builder")
	require.NoError(t, rf.pool.QueryRow(bg,
		`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'main', 'external', 'registry.example.com') RETURNING id`, rf.server.WorkspaceID).Scan(&f.regID))
	_, err = rf.q.UpsertTeamBuildSettings(bg, db.UpsertTeamBuildSettingsParams{WorkspaceID: rf.server.WorkspaceID, BuildLocation: "build_server", RegistryID: &f.regID})
	require.NoError(t, err)
	return f
}

func (f *buildFixture) addBuilder(name string) db.Server {
	f.t.Helper()
	bg := context.Background()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.server.WorkspaceID, Name: name})
	require.NoError(f.t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: []string{agentprotocol.CapBuildPush, agentprotocol.CapCancelDeploy}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', last_seen_at = now(), facts = $2, build_enabled = true, accept_workloads = false WHERE id = $1", srv.ID, facts)
	require.NoError(f.t, err)
	srv, err = f.q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(f.t, err)
	return srv
}

func (f *buildFixture) gitService(slug string) db.Service {
	f.t.Helper()
	cfg := services.Config{GitURL: "git://127.0.0.1:1/app.git", GitBranch: "main", Port: 8080}.WithDefaults()
	raw, _ := json.Marshal(cfg)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceGit, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

// deployAt creates a deployment pinned to the commit and runs its prepare job.
func (f *buildFixture) deployAt(sv db.Service, sha string) (uuid.UUID, error) {
	f.t.Helper()
	v, err := f.deps.Deploy(f.ctx, sv.ID, sha, "")
	require.NoError(f.t, err)
	return v.ID, f.deps.runDeployRef(context.Background(), v.ID, sha, "")
}

func (f *buildFixture) commands(kind string) []agents.NewCommand {
	var out []agents.NewCommand
	for _, c := range f.dd.cmds {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func (f *buildFixture) last(kind string) agents.NewCommand {
	f.t.Helper()
	cs := f.commands(kind)
	require.NotEmpty(f.t, cs, "no %s command was enqueued", kind)
	return cs[len(cs)-1]
}

func (f *buildFixture) buildCmd(depID uuid.UUID) db.AgentCommand {
	f.t.Helper()
	c, err := f.q.GetBuildCommandByDeployment(context.Background(), &depID)
	require.NoError(f.t, err)
	return c
}

func (f *buildFixture) deployCmd(depID uuid.UUID) db.AgentCommand {
	f.t.Helper()
	c, err := f.q.GetDeployCommandByDeployment(context.Background(), &depID)
	require.NoError(f.t, err)
	return c
}

func (f *buildFixture) setCmdStatus(id uuid.UUID, status string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE agent_commands SET status = $2 WHERE id = $1", id, status)
	require.NoError(f.t, err)
}

func (f *buildFixture) status(depID uuid.UUID) string {
	f.t.Helper()
	var st string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT status FROM deployments WHERE id = $1", depID).Scan(&st))
	return st
}

func (f *buildFixture) events(depID uuid.UUID, step string) []string {
	f.t.Helper()
	rows, err := f.q.ListDeploymentEvents(context.Background(), depID)
	require.NoError(f.t, err)
	var out []string
	for _, r := range rows {
		if r.Step == step {
			out = append(out, r.Message)
		}
	}
	return out
}

func (f *buildFixture) source(depID uuid.UUID) services.ReleaseSource {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT r.source FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1", depID).Scan(&raw))
	return services.DecodeSource(raw)
}

func (f *buildFixture) afterBuildJobs(depID uuid.UUID) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'deploy_service' AND args->>'afterBuild' = 'true' AND args->>'deploymentId' = $1`, depID.String()).Scan(&n))
	return n
}

// finishBuild delivers the build server's result for the deployment's build.
func (f *buildFixture) finishBuild(depID uuid.UUID, outcome, digest string) {
	f.t.Helper()
	cmd := f.buildCmd(depID)
	var bp agentprotocol.BuildImagePayload
	require.NoError(f.t, json.Unmarshal(cmd.Payload, &bp))
	status := "failed"
	if outcome == agentprotocol.OutcomeSucceeded {
		status = "succeeded"
	}
	f.setCmdStatus(cmd.ID, status)
	data, _ := json.Marshal(agentprotocol.BuildImageResult{Outcome: outcome, Digest: digest, Repository: bp.Repository, Tag: bp.Tag, Platform: bp.Platform, SizeBytes: 1234, Message: "boom"})
	require.NoError(f.t, f.deps.HandleResult(context.Background(), cmd.ServerID, cmd.ID, &depID, agentprotocol.ResultFrame{CommandID: cmd.ID.String(), Outcome: outcome, Data: data}))
}

// finishDeploy delivers the target's success result of the deployment's deploy command.
func (f *buildFixture) finishDeploy(depID uuid.UUID, digest string) {
	f.t.Helper()
	cmd := f.deployCmd(depID)
	f.setCmdStatus(cmd.ID, "succeeded")
	data, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeSucceeded, ImageDigest: digest})
	require.NoError(f.t, f.deps.HandleResult(context.Background(), f.server.ID, cmd.ID, &depID, agentprotocol.ResultFrame{CommandID: cmd.ID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
}

// buildAndDeploy runs a whole deployment on the build server and returns its id.
func (f *buildFixture) buildAndDeploy(sv db.Service, sha, digest string) uuid.UUID {
	f.t.Helper()
	depID, err := f.deployAt(sv, sha)
	require.NoError(f.t, err)
	f.finishBuild(depID, agentprotocol.OutcomeSucceeded, digest)
	require.NoError(f.t, f.deps.runDeployOpts(context.Background(), depID, "", "", true))
	f.finishDeploy(depID, "registry.example.com/shop-"+sv.Slug+"@"+digest)
	require.Equal(f.t, "succeeded", f.status(depID))
	return depID
}

func TestBuildOnBuildServerThenDeployFromRegistry(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()

	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)

	// The prepare phase dispatches the build to the build server and nothing to the target.
	require.Empty(t, f.commands(agentprotocol.KindDeploy), "the target gets its deploy after the build")
	bc := f.last(agentprotocol.KindBuildImage)
	require.Equal(t, f.builder.ID, bc.ServerID)
	require.Equal(t, "build:"+depID.String(), bc.IdempotencyKey)
	var bp agentprotocol.BuildImagePayload
	require.NoError(t, json.Unmarshal(bc.Payload, &bp))
	require.Equal(t, "registry.example.com/shop-api", bp.Repository)
	require.Equal(t, "linux/amd64", bp.Platform)
	require.Equal(t, sha1, bp.Git.CommitSHA)
	require.Equal(t, "git://127.0.0.1:1/app.git", bp.Git.URL)
	require.Equal(t, buildplacement.BuilderName(f.projectID()), bp.Builder)
	require.Equal(t, depID.String(), bp.DeploymentID)
	require.False(t, bp.Insecure)
	require.NotContains(t, string(bc.Payload), "cred:", "no credential in the persisted payload")

	var buildServer *uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT build_server_id FROM deployments WHERE id = $1", depID).Scan(&buildServer))
	require.Equal(t, f.builder.ID, *buildServer)
	require.Equal(t, "preparing", f.status(depID))
	src := f.source(depID)
	require.Equal(t, f.regID, *src.RegistryID)
	require.Empty(t, src.RegistryRef, "the digest is known after the build")
	require.NotEmpty(t, src.BuildHash)
	require.Equal(t, "r-", bp.Tag[:2])
	var imageRef string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT r.image_ref FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1", depID).Scan(&imageRef))
	require.Equal(t, bp.Repository+":"+bp.Tag, imageRef, "NEBULA_IMAGE names the registry tag")

	// Send-time secrets of the build: the registry push credential (push and pull).
	secrets, err := f.deps.SecretsForCommand(bg, f.dd.ids[len(f.dd.ids)-1], agentprotocol.KindBuildImage, bc.Payload)
	require.NoError(t, err)
	require.Equal(t, "cred:shop-api:push+pull", secrets[agentprotocol.SecretRegistryPushAuth])

	// The build server's events and results count for the deployment; a stranger's do not.
	require.NoError(t, f.deps.HandleEvent(bg, f.builder.ID, agentprotocol.EventFrame{DeploymentID: depID.String(), Step: agentprotocol.StepBuild, Status: agentprotocol.StepStarted}))
	require.Equal(t, "building", f.status(depID))
	stranger := f.addBuilder("stranger")
	require.NoError(t, f.deps.HandleEvent(bg, stranger.ID, agentprotocol.EventFrame{DeploymentID: depID.String(), Step: "custom", Status: agentprotocol.StepStarted}))
	require.Empty(t, f.events(depID, "custom"), "an event from a server without a command of the deployment is dropped")
	require.NoError(t, f.deps.HandleEvent(bg, f.builder.ID, agentprotocol.EventFrame{DeploymentID: depID.String(), Step: agentprotocol.StepPush, Status: agentprotocol.StepStarted}))
	require.Equal(t, "building", f.status(depID))

	// The build result: the image is recorded and the deploy job is enqueued once.
	f.finishBuild(depID, agentprotocol.OutcomeSucceeded, dig1)
	f.finishBuild(depID, agentprotocol.OutcomeSucceeded, dig1) // redelivery
	require.Equal(t, 1, f.afterBuildJobs(depID))
	src = f.source(depID)
	require.Equal(t, "registry.example.com/shop-api@"+dig1, src.RegistryRef)
	require.Equal(t, "linux/amd64", src.Platform)
	require.Equal(t, f.builder.ID, *src.BuiltOn)
	var state, repo, digest string
	var size int64
	require.NoError(t, f.pool.QueryRow(bg, "SELECT state, repository, digest, size_bytes FROM registry_images WHERE registry_id = $1", f.regID).Scan(&state, &repo, &digest, &size))
	require.Equal(t, []any{"live", "registry.example.com/shop-api", dig1, int64(1234)}, []any{state, repo, digest, size})
	require.Contains(t, f.events(depID, PlacementStep)[len(f.events(depID, PlacementStep))-1], "Built on builder")

	// The target deploys the image by digest; it pulls with the registry's pull credential.
	require.NoError(t, f.deps.runDeployOpts(bg, depID, "", "", true))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.Equal(t, f.server.ID, f.last(agentprotocol.KindDeploy).ServerID)
	require.Nil(t, dp.Git)
	require.NotNil(t, dp.Image)
	require.Equal(t, "registry.example.com/shop-api@"+dig1, dp.Image.Ref)
	require.Equal(t, dp.Image.Ref, dp.Image.Digest)
	require.True(t, dp.Image.HasRegistryAuth)
	require.Equal(t, depID.String(), dp.DeploymentID)
	require.NotNil(t, dp.Image.BuiltFrom)
	require.Equal(t, f.builder.ID.String(), dp.Image.BuiltFrom.BuildServerID)
	require.Equal(t, sha1, dp.Image.BuiltFrom.CommitSHA)
	require.Equal(t, dp.ReleaseID, dp.Image.BuiltFrom.ReleaseID)
	dsecrets, err := f.deps.SecretsForCommand(bg, f.dd.ids[len(f.dd.ids)-1], agentprotocol.KindDeploy, f.last(agentprotocol.KindDeploy).Payload)
	require.NoError(t, err)
	require.Equal(t, "cred:shop-api:pull", dsecrets[agentprotocol.SecretRegistryAuth])
	require.Equal(t, "building", f.status(depID), "still building until the target reports")

	// A retried after-build job does not dispatch a second deploy.
	n := len(f.commands(agentprotocol.KindDeploy))
	require.NoError(t, f.deps.runDeployOpts(bg, depID, "", "", true))
	require.Len(t, f.commands(agentprotocol.KindDeploy), n)

	f.finishDeploy(depID, "registry.example.com/shop-api@"+dig1)
	require.Equal(t, "succeeded", f.status(depID))
	src = f.source(depID)
	require.Empty(t, src.ImageID, "a registry release has no local image id")
	require.Equal(t, "registry.example.com/shop-api@"+dig1, src.RegistryRef)
	var current *uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT current_deployment_id FROM services WHERE id = $1", sv.ID).Scan(&current))
	require.Equal(t, depID, *current)
}

func (f *buildFixture) projectID() uuid.UUID {
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT project_id FROM environments WHERE id = $1", f.env.ID).Scan(&id))
	return id
}

func TestReusesTheImageOfSameCommitAndSettings(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	f.buildAndDeploy(sv, sha1, dig1)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 1)

	// The same commit again: no build, the deploy goes to the target at once.
	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 1, "no second build")
	var dp agentprotocol.DeployPayload
	dc := f.deployCmd(depID)
	require.NoError(t, json.Unmarshal(dc.Payload, &dp))
	require.Equal(t, "registry.example.com/shop-api@"+dig1, dp.Image.Ref)
	require.Equal(t, f.server.ID, dc.ServerID)
	require.Contains(t, f.events(depID, PlacementStep)[0], "Reusing the image already built for commit aaaaaaa")
	require.Equal(t, "registry.example.com/shop-api@"+dig1, f.source(depID).RegistryRef)
	var bs *uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT build_server_id FROM deployments WHERE id = $1", depID).Scan(&bs))
	require.Nil(t, bs)
	f.finishDeploy(depID, "registry.example.com/shop-api@"+dig1)
	// The image now belongs to the newest release (retention keeps it alive with it).
	var releaseID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT release_id FROM registry_images WHERE digest = $1", dig1).Scan(&releaseID))
	var depRelease uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT release_id FROM deployments WHERE id = $1", depID).Scan(&depRelease))
	require.Equal(t, depRelease, releaseID)

	// Another commit builds again; so do changed build inputs (a build variable).
	d3, err := f.deployAt(sv, sha2)
	require.NoError(t, err)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 2)
	f.finishBuild(d3, agentprotocol.OutcomeSucceeded, dig2)
	require.NoError(t, f.deps.runDeployOpts(bg, d3, "", "", true))
	f.finishDeploy(d3, "registry.example.com/shop-api@"+dig2)

	_, err = f.pool.Exec(bg, "UPDATE services SET config = jsonb_set(config, '{dockerfilePath}', '\"deploy/Dockerfile\"') WHERE id = $1", sv.ID)
	require.NoError(t, err)
	_, err = f.deployAt(sv, sha1)
	require.NoError(t, err)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 3, "a different Dockerfile path is a different build")
}

func TestFallbackBuildsOnTargetWithAnEvent(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.builder.ID)
	require.NoError(t, err)

	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	require.Empty(t, f.commands(agentprotocol.KindBuildImage))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.Nil(t, dp.Image)
	require.NotNil(t, dp.Git, "the target builds the source itself")
	require.Equal(t, sha1, dp.Git.CommitSHA)
	evs := f.events(depID, PlacementStep)
	require.Len(t, evs, 1)
	require.Contains(t, evs[0], "No build server available; building on agent1")
	require.Contains(t, evs[0], "builder: offline")
	var st string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT e.status FROM deployment_events e WHERE e.deployment_id = $1 AND e.step = $2", depID, PlacementStep).Scan(&st))
	require.Equal(t, PlacementInfo, st)
	src := f.source(depID)
	require.Empty(t, src.RegistryRef)
	require.Nil(t, src.RegistryID)
	var imageRef string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT r.image_ref FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1", depID).Scan(&imageRef))
	require.True(t, strings.HasPrefix(imageRef, agentprotocol.BuildImagePrefix), "the local tag: %s", imageRef)

	// The same without registries wired: a fallback too, never a failed deploy.
	f.deps.registries = nil
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', last_seen_at = now() WHERE id = $1", f.builder.ID)
	require.NoError(t, err)
	f.finishDeploy(depID, "sha256:"+strings.Repeat("9", 64))
	d2, err := f.deployAt(sv, sha2)
	require.NoError(t, err)
	require.Contains(t, f.events(d2, PlacementStep)[0], "registries are not available")
	require.Len(t, f.commands(agentprotocol.KindDeploy), 2)
}

func TestPreviewDeploymentsBuildOnTheirTarget(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	v, err := f.deps.DeployWith(f.ctx, sv.ID, TriggerPreview, InternalOptions{Commit: &CommitInfo{SHA: sha1}})
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(context.Background(), v.ID, sha1, ""))
	require.Empty(t, f.commands(agentprotocol.KindBuildImage), "previews never build on a shared build server")
	require.NotNil(t, f.last(agentprotocol.KindDeploy).Payload)
	require.Contains(t, f.events(v.ID, PlacementStep)[0], "previews do not build on this server")
}

func TestBuildQueuesWhenTheBuildServerIsFull(t *testing.T) {
	f := newBuildFixture(t)
	bg := context.Background()
	_, err := f.pool.Exec(bg, "UPDATE servers SET build_max_concurrent = 1 WHERE id = $1", f.builder.ID)
	require.NoError(t, err)
	a, b := f.gitService("a"), f.gitService("b")

	da, err := f.deployAt(a, sha1)
	require.NoError(t, err)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 1)

	db1, err := f.deployAt(b, sha1)
	var wait *buildWaitError
	require.ErrorAs(t, err, &wait)
	require.Equal(t, "Waiting for a build slot on builder (1 build running).", wait.message)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 1, "the second build is not dispatched")
	require.Equal(t, "preparing", f.status(db1))
	evs := f.events(db1, PlacementStep)
	require.Equal(t, []string{"Waiting for a build slot on builder (1 build running)."}, evs)
	// The retry loop does not repeat the notice and leaves nothing behind.
	require.ErrorAs(t, f.deps.runDeployRef(bg, db1, sha1, ""), &wait)
	require.Len(t, f.events(db1, PlacementStep), 1)
	var releases int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM releases WHERE service_id = $1", b.ID).Scan(&releases))
	require.Zero(t, releases, "nothing is written while waiting")

	// The first build fails: its slot is free and the second build starts.
	f.finishBuild(da, agentprotocol.OutcomeFailed, "")
	require.Equal(t, "failed", f.status(da))
	require.NoError(t, f.deps.runDeployRef(bg, db1, sha1, ""))
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 2)
	require.Equal(t, f.builder.ID, f.last(agentprotocol.KindBuildImage).ServerID)
}

func TestFailedAndCanceledBuildsEndTheDeployment(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()

	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	f.finishBuild(depID, agentprotocol.OutcomeFailed, "")
	var st, msg string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT status, status_message FROM deployments WHERE id = $1", depID).Scan(&st, &msg))
	require.Equal(t, "failed", st)
	require.Equal(t, "boom", msg)
	require.Empty(t, f.commands(agentprotocol.KindDeploy))
	require.Zero(t, f.afterBuildJobs(depID))

	d2, err := f.deployAt(sv, sha2)
	require.NoError(t, err)
	cmd := f.buildCmd(d2)
	f.setCmdStatus(cmd.ID, "failed")
	data, _ := json.Marshal(agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeCanceled, CanceledAt: agentprotocol.StepBuild})
	require.NoError(t, f.deps.HandleResult(bg, f.builder.ID, cmd.ID, &d2, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeCanceled, Data: data}))
	var step string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT status, canceled_at_step FROM deployments WHERE id = $1", d2).Scan(&st, &step))
	require.Equal(t, "canceled", st)
	require.Equal(t, agentprotocol.StepBuild, step)
}

func TestCancelDuringBuildTargetsTheBuilderAndDuringDeployTheTarget(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()

	// Before the build server took the build: the command is canceled, the deployment ends.
	d0, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	_, err = f.deps.Cancel(f.ctx, d0)
	require.NoError(t, err)
	require.Equal(t, "canceled", f.status(d0))
	require.Equal(t, "canceled", f.buildCmd(d0).Status)
	require.Empty(t, f.commands(agentprotocol.KindCancelDeploy))

	// While the build server runs it: a cancel_deploy goes to the build server.
	d1, err := f.deployAt(sv, sha2)
	require.NoError(t, err)
	f.setCmdStatus(f.buildCmd(d1).ID, "acked")
	require.NoError(t, f.deps.HandleEvent(bg, f.builder.ID, agentprotocol.EventFrame{DeploymentID: d1.String(), Step: agentprotocol.StepBuild, Status: agentprotocol.StepStarted}))
	_, err = f.deps.Cancel(f.ctx, d1)
	require.NoError(t, err)
	cc := f.last(agentprotocol.KindCancelDeploy)
	require.Equal(t, f.builder.ID, cc.ServerID, "the build runs on the build server")
	var cp agentprotocol.CancelDeployPayload
	require.NoError(t, json.Unmarshal(cc.Payload, &cp))
	require.Equal(t, d1.String(), cp.DeploymentID)
	require.Equal(t, "building", f.status(d1), "the agent reports the cancel")
	// A second cancel is idempotent (same key) and still goes to the builder.
	_, err = f.deps.Cancel(f.ctx, d1)
	require.NoError(t, err)
	require.Equal(t, f.builder.ID, f.last(agentprotocol.KindCancelDeploy).ServerID)
	// The agent aborts the build.
	cmd := f.buildCmd(d1)
	data, _ := json.Marshal(agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeCanceled, CanceledAt: agentprotocol.StepPush})
	f.setCmdStatus(cmd.ID, "failed")
	require.NoError(t, f.deps.HandleResult(bg, f.builder.ID, cmd.ID, &d1, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeCanceled, Data: data}))
	require.Equal(t, "canceled", f.status(d1))

	// After the build, while the target deploys: the cancel goes to the target.
	d2, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	f.finishBuild(d2, agentprotocol.OutcomeSucceeded, dig1)
	require.NoError(t, f.deps.runDeployOpts(bg, d2, "", "", true))
	f.setCmdStatus(f.deployCmd(d2).ID, "acked")
	_, err = f.deps.Cancel(f.ctx, d2)
	require.NoError(t, err)
	require.Equal(t, f.server.ID, f.last(agentprotocol.KindCancelDeploy).ServerID, "the deploy runs on the target")

	// Between the build result and the deploy command there is nothing to ask the agent: it just ends.
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'failed' WHERE id = $1", d2)
	require.NoError(t, err)
	d3, err := f.deployAt(sv, sha2)
	require.NoError(t, err)
	f.finishBuild(d3, agentprotocol.OutcomeSucceeded, dig2)
	n := len(f.commands(agentprotocol.KindCancelDeploy))
	_, err = f.deps.Cancel(f.ctx, d3)
	require.NoError(t, err)
	require.Equal(t, "canceled", f.status(d3))
	require.Len(t, f.commands(agentprotocol.KindCancelDeploy), n)
	require.NoError(t, f.deps.runDeployOpts(bg, d3, "", "", true))
	require.Empty(t, f.deployCmdsOf(d3), "the queued after-build job finds the deployment canceled")
}

func (f *buildFixture) deployCmdsOf(depID uuid.UUID) []db.AgentCommand {
	rows, err := f.q.ListDeploymentCommands(context.Background(), &depID)
	require.NoError(f.t, err)
	var out []db.AgentCommand
	for _, r := range rows {
		if r.Kind == agentprotocol.KindDeploy {
			out = append(out, r)
		}
	}
	return out
}

func TestRollbackOfARegistryReleaseNeedsNoBuildServer(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	d1 := f.buildAndDeploy(sv, sha1, dig1)
	f.buildAndDeploy(sv, sha2, dig2)

	// The build server is gone and registry retention keeps the image: rollback pulls by digest.
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline', build_enabled = false WHERE id = $1", f.builder.ID)
	require.NoError(t, err)
	builds := len(f.commands(agentprotocol.KindBuildImage))
	rv, err := f.deps.Rollback(f.ctx, d1)
	require.NoError(t, err)
	require.Equal(t, TriggerRollback, rv.Trigger)
	require.NoError(t, f.deps.runDeployRef(bg, rv.ID, "", ""))
	require.Len(t, f.commands(agentprotocol.KindBuildImage), builds)
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.Nil(t, dp.Git)
	require.Equal(t, "registry.example.com/shop-api@"+dig1, dp.Image.Ref)
	require.True(t, dp.Image.HasRegistryAuth)
	require.Empty(t, dp.PreDeploy, "a rollback runs no pre-deploy command")
	secrets, err := f.deps.SecretsForCommand(bg, uuid.New(), agentprotocol.KindDeploy, f.last(agentprotocol.KindDeploy).Payload)
	require.NoError(t, err)
	require.Equal(t, "cred:shop-api:pull", secrets[agentprotocol.SecretRegistryAuth])
	f.finishDeploy(rv.ID, "registry.example.com/shop-api@"+dig1)

	// The image left the registry (retention expired it): 409 image_pruned and a rebuild is offered.
	d2 := f.deploymentOfDigest(dig2)
	_, err = f.pool.Exec(bg, "UPDATE registry_images SET state = 'expired' WHERE digest = $1", dig2)
	require.NoError(t, err)
	_, err = f.deps.Rollback(f.ctx, d2)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, "image_pruned", ae.Code)

	// A live image rolls back again.
	_, err = f.pool.Exec(bg, "UPDATE registry_images SET state = 'live'")
	require.NoError(t, err)
	_, err = f.deps.Rollback(f.ctx, d2)
	require.NoError(t, err, "a live image rolls back")
}

func (f *buildFixture) deploymentOfDigest(digest string) uuid.UUID {
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`SELECT d.id FROM deployments d JOIN registry_images ri ON ri.release_id = d.release_id WHERE ri.digest = $1 AND d.status = 'succeeded' ORDER BY d.created_at DESC LIMIT 1`, digest).Scan(&id))
	return id
}

func TestRollbackOfARegistryReleaseWhoseRegistryWasDeleted(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	d1 := f.buildAndDeploy(sv, sha1, dig1)
	f.buildAndDeploy(sv, sha2, dig2)
	_, err := f.pool.Exec(context.Background(), "DELETE FROM registries WHERE id = $1", f.regID)
	require.NoError(t, err)
	_, err = f.deps.Rollback(f.ctx, d1)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "image_pruned", ae.Code)
	require.Equal(t, sha1, ae.Details["commitSha"])
}

// Releases built before build servers existed keep their old semantics: a local
// image id, redeployed with ExistingImageID and gated by the newest imageRetention.
func TestOldImageIDReleasesStillRollBack(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	oldID := "sha256:" + strings.Repeat("c", 64)

	cfg := services.DecodeConfig(sv.Config)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg, ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit, GitURL: cfg.GitURL, Branch: "main", CommitSHA: sha1, ImageID: oldID})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: agentprotocol.BuildImagePrefix + "api:old"})
	require.NoError(t, err)
	old, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded', finished_at = now() - interval '1 day' WHERE id = $1", old.ID)
	require.NoError(t, err)

	f.buildAndDeploy(sv, sha2, dig2)
	rv, err := f.deps.Rollback(f.ctx, old.ID)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(bg, rv.ID, "", ""))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.Nil(t, dp.Image)
	require.NotNil(t, dp.Git)
	require.Equal(t, oldID, dp.Git.ExistingImageID)
	require.Empty(t, f.commands(agentprotocol.KindBuildImage)[1:], "no build for an old release")

	// The old rule still applies to them: an image id outside the retention is pruned.
	f.finishDeploy(rv.ID, oldID)
	_, err = f.pool.Exec(bg, "UPDATE services SET config = jsonb_set(config, '{imageRetention}', '1') WHERE id = $1", sv.ID)
	require.NoError(t, err)
	f.buildAndDeploy(sv, strings.Repeat("d", 40), dig1)
	_, err = f.deps.Rollback(f.ctx, old.ID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "image_pruned", ae.Code)
}

func TestRetentionExpiresOldImagesOnlyInBuiltinRegistries(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	_, err := f.pool.Exec(bg, "UPDATE services SET config = jsonb_set(config, '{imageRetention}', '1') WHERE id = $1", sv.ID)
	require.NoError(t, err)
	var builtinID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg,
		`INSERT INTO registries (workspace_id, name, kind, host, manage_retention) VALUES ($1, 'built-in', 'builtin', '127.0.0.1:18500', true) RETURNING id`, f.server.WorkspaceID).Scan(&builtinID))

	// Three succeeded releases, each with an image, in the external and in the built-in registry.
	var rels []uuid.UUID
	for i := range 3 {
		src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit, RegistryRef: "x@" + dig1})
		rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: []byte(`{}`), ImageRef: "x"})
		require.NoError(t, err)
		dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
		require.NoError(t, err)
		_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded', finished_at = now() - make_interval(hours => $2) WHERE id = $1", dep.ID, 10-i)
		require.NoError(t, err)
		for _, reg := range []uuid.UUID{f.regID, builtinID} {
			_, err = f.pool.Exec(bg, `INSERT INTO registry_images (registry_id, service_id, release_id, repository, digest, platform, build_hash, pushed_at)
				VALUES ($1, $2, $3, 'r', $4, 'linux/amd64', 'h', now() - interval '2 hours')`, reg, sv.ID, rel.ID, "sha256:"+strings.Repeat(string(rune('1'+i)), 64))
			require.NoError(t, err)
		}
		rels = append(rels, rel.ID)
	}
	sc, err := services.LoadScopeInternal(bg, f.q, sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.deps.retainRegistryImages(bg, f.q, sc, rels[2]))

	states := func(reg uuid.UUID) []string {
		rows, err := f.pool.Query(bg, "SELECT state FROM registry_images WHERE registry_id = $1 ORDER BY digest", reg)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			out = append(out, s)
		}
		return out
	}
	require.Equal(t, []string{"expired", "expired", "live"}, states(builtinID), "only the newest imageRetention release stays live")
	require.Equal(t, []string{"live", "live", "live"}, states(f.regID), "an external registry keeps everything: NebulaCtrl never deletes from it")
}

func TestUndeliveredBuildFallsBackToTheTarget(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	cmd := f.buildCmd(depID)
	f.setCmdStatus(cmd.ID, "expired")

	require.NoError(t, f.deps.HandleUndelivered(bg, f.builder.ID, cmd.ID, &depID, "Not delivered: server offline. No changes were made."))
	require.NotEqual(t, "failed", f.status(depID), "an undelivered build does not fail the deployment")
	require.Equal(t, 1, f.afterBuildJobs(depID))
	require.Contains(t, f.events(depID, PlacementStep)[1], "Build server builder did not take the build; building on agent1.")
	var bs *uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT build_server_id FROM deployments WHERE id = $1", depID).Scan(&bs))
	require.Nil(t, bs)

	require.NoError(t, f.deps.runDeployOpts(bg, depID, "", "", true))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.NotNil(t, dp.Git, "the release has no registry image: the target builds it")
	require.Equal(t, sha1, dp.Git.CommitSHA)
	require.Equal(t, f.server.ID, f.last(agentprotocol.KindDeploy).ServerID)
}

func TestFramesOfAnyServerThatOwnsACommandAreAccepted(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	bg := context.Background()
	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	stranger := f.addBuilder("stranger")

	owns := func(server uuid.UUID) bool {
		_, err := f.q.GetDeploymentServiceForReporter(bg, db.GetDeploymentServiceForReporterParams{ID: depID, ServerID: &server})
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		require.NoError(t, err)
		return true
	}
	require.True(t, owns(f.server.ID), "the target")
	require.True(t, owns(f.builder.ID), "the build server owns the build_image command")
	require.False(t, owns(stranger.ID))

	// The same ownership gates the build server's source scan of the checkout.
	built, err := f.q.ServerBuiltForService(bg, db.ServerBuiltForServiceParams{ServerID: f.builder.ID, ServiceID: sv.ID})
	require.NoError(t, err)
	require.True(t, built)
	built, err = f.q.ServerBuiltForService(bg, db.ServerBuiltForServiceParams{ServerID: stranger.ID, ServiceID: sv.ID})
	require.NoError(t, err)
	require.False(t, built)

	// A result for the build from the wrong server is ignored.
	cmd := f.buildCmd(depID)
	data, _ := json.Marshal(agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeSucceeded, Digest: dig1})
	require.NoError(t, f.deps.HandleResult(bg, stranger.ID, cmd.ID, &depID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	require.Zero(t, f.afterBuildJobs(depID))
	// An invalid digest fails the deployment instead of deploying garbage.
	bad, _ := json.Marshal(agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeSucceeded, Digest: "latest"})
	require.NoError(t, f.deps.HandleResult(bg, f.builder.ID, cmd.ID, &depID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: bad}))
	require.Equal(t, "failed", f.status(depID))
}

func TestPlanShowsPlacement(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	plan, err := f.deps.Plan(f.ctx, sv.ID)
	require.NoError(t, err)
	require.NotNil(t, plan.Placement)
	require.Equal(t, "build_server", plan.Placement.Location)
	require.Equal(t, "builder", plan.Placement.BuildServerName)
	require.Equal(t, f.builder.ID, *plan.Placement.BuildServerID)
	require.Equal(t, "registry.example.com", plan.Placement.RegistryHost)
	require.Equal(t, "linux/amd64", plan.Placement.Platform)
	require.True(t, plan.Placement.Sticky)
	require.Equal(t, 2, plan.Placement.MaxConcurrent)
	var codes []string
	for _, w := range plan.Placement.Warnings {
		codes = append(codes, w.Code)
	}
	require.Equal(t, []string{buildplacement.WarnColdCache}, codes)

	// Without a usable build server the plan says so, with the same text the event will carry.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.builder.ID)
	require.NoError(t, err)
	plan, err = f.deps.Plan(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Equal(t, "target", plan.Placement.Location)
	require.True(t, plan.Placement.Fallback)
	require.Equal(t, "fallback", plan.Placement.Warnings[0].Code)
	require.Contains(t, plan.Warnings, plan.Placement.Warnings[0].Message)

	// Image services have no placement.
	web := f.web("web")
	plan, err = f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	require.Nil(t, plan.Placement)
}

func TestDeploymentViewNamesTheBuildServer(t *testing.T) {
	f := newBuildFixture(t)
	sv := f.gitService("api")
	depID, err := f.deployAt(sv, sha1)
	require.NoError(t, err)
	v, err := f.deps.Get(f.ctx, depID)
	require.NoError(t, err)
	require.Equal(t, f.builder.ID, *v.BuildServerID)
	require.Equal(t, "builder", v.BuildServerName)
	require.Nil(t, v.WaitingFor)

	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.builder.ID)
	require.NoError(t, err)
	v, err = f.deps.Get(f.ctx, depID)
	require.NoError(t, err)
	require.NotNil(t, v.WaitingFor, "an offline build server leaves the deployment waiting for it")
	require.Equal(t, "builder", v.WaitingFor.ServerName)
}
