package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func (f *txFixture) gitService(envID uuid.UUID, slug string, cfg services.Config) db.Service {
	f.t.Helper()
	cfg.GitURL, cfg.GitBranch = "https://git.example.com/acme/shop.git", "main"
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceGit, Config: raw,
	})
	require.NoError(f.t, err)
	return sv
}

// otherKind inserts a service of a kind that cannot change source.
func (f *txFixture) otherKind(envID uuid.UUID, slug, kind, source string) db.Service {
	f.t.Helper()
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: kind, SourceType: source, Config: json.RawMessage(`{}`),
	})
	require.NoError(f.t, err)
	return sv
}

func (f *txFixture) stageSource(id uuid.UUID, expected int64, in services.SourceChangeInput) (services.ServiceView, error) {
	f.t.Helper()
	return f.svcs.StageSourceChange(f.ctx, id, expected, in)
}

type recordingLinkHook struct{ repos []string }

func (h *recordingLinkHook) RepoUnlinked(_ context.Context, _ uuid.UUID, repo string) {
	h.repos = append(h.repos, repo)
}

func TestStageSourceChangeImageToGitKeepsTheServiceAndClearsImageCredentials(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.staging.ID, "web")
	cur, err := f.svcs.Get(f.ctx, svc.ID)
	require.NoError(t, err)
	// A private registry, a volume and a variable the switch must treat differently.
	cur, err = f.svcs.Update(f.ctx, svc.ID, cur.Generation, services.UpdateInput{RegistryAuth: &services.RegistryAuth{Username: "u", Password: "p"}})
	require.NoError(t, err)
	require.True(t, cur.RegistryAuthSet)
	_, err = f.svcs.AddVolume(f.ctx, svc.ID, "data", "/data")
	require.NoError(t, err)
	cur, err = f.svcs.Get(f.ctx, svc.ID)
	require.NoError(t, err)
	before := f.row(svc.ID)

	v, err := f.stageSource(svc.ID, cur.Generation, services.SourceChangeInput{
		SourceType: services.SourceGit, GitURL: " https://git.example.com/acme/shop.git ", GitBranch: "main", GitToken: "tok-secret",
		BuildType: "railpack",
	})
	require.NoError(t, err)
	require.Equal(t, services.SourceGit, v.SourceType)
	require.Equal(t, cur.Generation+1, v.Generation, "one staged change")
	require.True(t, v.HasStagedChanges)
	require.True(t, v.GitTokenSet)
	require.False(t, v.RegistryAuthSet, "the image credentials are cleared")

	row := f.row(svc.ID)
	require.Equal(t, services.SourceGit, row.SourceType)
	require.Empty(t, row.RegistryAuthEnc)
	require.NotEmpty(t, row.GitTokenEnc)
	cfg := services.DecodeConfig(row.Config)
	require.Equal(t, "https://git.example.com/acme/shop.git", cfg.GitURL)
	require.Equal(t, "main", cfg.GitBranch)
	require.Equal(t, "railpack", cfg.BuildType)
	require.Empty(t, cfg.Image, "the image is cleared")
	oldCfg := services.DecodeConfig(before.Config)
	require.Equal(t, oldCfg.Port, cfg.Port, "settings of the service stay")
	require.Equal(t, oldCfg.Healthcheck, cfg.Healthcheck)
	require.Equal(t, before.Name, row.Name)
	require.Equal(t, before.Slug, row.Slug)
	vols, err := f.svcs.ListVolumes(f.ctx, svc.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1, "volumes stay")

	require.Equal(t, 1, f.audits(services.ActionSourceChanged, svc.ID.String()))
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata::text FROM audit_events WHERE action = $1 AND resource_id = $2", services.ActionSourceChanged, svc.ID.String()).Scan(&meta))
	require.Contains(t, meta, `"from": "image"`)
	require.Contains(t, meta, `"to": "git"`)
	require.Contains(t, meta, "git.example.com/acme/shop")
	require.NotContains(t, meta, "tok-secret", "credentials never reach the audit log")
}

func TestStageSourceChangeGitToImageClearsTheRepositoryAndItsLink(t *testing.T) {
	f := newTxFixture(t)
	hook := &recordingLinkHook{}
	f.svcs.SetGitLinkHook(hook)
	sv := f.gitService(f.staging.ID, "api", services.Config{Port: 3000, BuildType: "dockerfile", DockerfilePath: "docker/Dockerfile", Submodules: "on"})
	conn, err := f.q.InsertGitConnection(context.Background(), db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: f.ws.ID, Provider: "gitea", Name: "acme", BaseUrl: "https://git.example.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	_, err = f.q.UpsertServiceGit(context.Background(), db.UpsertServiceGitParams{
		ServiceID: sv.ID, ConnectionID: conn.ID, RepoFullName: "acme/shop", AutoDeploy: true, WatchPaths: []string{},
	})
	require.NoError(t, err)

	v, err := f.stageSource(sv.ID, sv.Generation, services.SourceChangeInput{
		SourceType: services.SourceImage, Image: "ghcr.io/acme/shop:1.4", RegistryAuth: &services.RegistryAuth{Username: "u", Password: "p"},
	})
	require.NoError(t, err)
	require.Equal(t, services.SourceImage, v.SourceType)
	require.Equal(t, sv.Generation+1, v.Generation)
	require.True(t, v.RegistryAuthSet)
	require.False(t, v.GitTokenSet)
	require.Nil(t, v.Git, "the repository link is gone")

	cfg := services.DecodeConfig(f.row(sv.ID).Config)
	require.Equal(t, "ghcr.io/acme/shop:1.4", cfg.Image)
	require.Empty(t, cfg.GitURL)
	require.Empty(t, cfg.GitBranch)
	require.Equal(t, "auto", cfg.BuildType, "build settings are back to their defaults")
	require.Equal(t, "Dockerfile", cfg.DockerfilePath)
	require.Empty(t, cfg.Submodules)
	require.Equal(t, 3000, cfg.Port)
	_, err = f.q.GetServiceGit(context.Background(), sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, []string{"acme/shop"}, hook.repos, "the provider webhook can be dropped")
	require.Equal(t, 1, f.audits(services.ActionSourceChanged, sv.ID.String()))
}

func TestStageSourceChangeRequiresTheFieldsOfTheNewSource(t *testing.T) {
	f := newTxFixture(t)
	img := f.imageService(f.staging.ID, "web")
	gitSvc := f.gitService(f.staging.ID, "api", services.Config{Port: 80})

	for name, c := range map[string]struct {
		id  uuid.UUID
		gen int64
		in  services.SourceChangeInput
	}{
		"git without a url":       {img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceGit, GitBranch: "main"}},
		"git without a branch":    {img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceGit, GitURL: "https://git.example.com/a/b.git"}},
		"git with a bad url":      {img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceGit, GitURL: "ftp://x/y", GitBranch: "main"}},
		"git with a bad builder":  {img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceGit, GitURL: "https://git.example.com/a/b.git", GitBranch: "main", BuildType: "nix"}},
		"git with a path outside": {img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceGit, GitURL: "https://git.example.com/a/b.git", GitBranch: "main", BuildContext: "../x"}},
		"image without a ref":     {gitSvc.ID, gitSvc.Generation, services.SourceChangeInput{SourceType: services.SourceImage}},
		"image with a space":      {gitSvc.ID, gitSvc.Generation, services.SourceChangeInput{SourceType: services.SourceImage, Image: "nginx latest"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.stageSource(c.id, c.gen, c.in)
			requireCode(t, err, "invalid_config")
		})
	}
	// Nothing was written.
	require.Equal(t, services.SourceImage, f.row(img.ID).SourceType)
	require.Equal(t, img.Generation, f.row(img.ID).Generation)
	require.Equal(t, services.SourceGit, f.row(gitSvc.ID).SourceType)
	require.Zero(t, f.audits(services.ActionSourceChanged, img.ID.String())+f.audits(services.ActionSourceChanged, gitSvc.ID.String()))
}

func TestStageSourceChangeRefusesWhatIsNotAnImageOrGitApp(t *testing.T) {
	f := newTxFixture(t)
	stack := f.otherKind(f.staging.ID, "shop", services.KindStack, services.SourceCompose)
	database := f.otherKind(f.staging.ID, "db", services.KindDatabase, services.SourceImage)
	route := f.otherKind(f.staging.ID, "edge", services.KindRoute, services.SourceExternal)
	img := f.imageService(f.staging.ID, "web")

	git := services.SourceChangeInput{SourceType: services.SourceGit, GitURL: "https://git.example.com/a/b.git", GitBranch: "main"}
	for name, sv := range map[string]db.Service{"a compose stack": stack, "a database": database, "a route": route} {
		t.Run(name, func(t *testing.T) {
			_, err := f.stageSource(sv.ID, sv.Generation, git)
			requireCode(t, err, "not_applicable")
			_, err = f.stageSource(sv.ID, sv.Generation, services.SourceChangeInput{SourceType: services.SourceImage, Image: "nginx:1"})
			requireCode(t, err, "not_applicable")
			require.Equal(t, sv.SourceType, f.row(sv.ID).SourceType)
			require.Equal(t, sv.Generation, f.row(sv.ID).Generation)
		})
	}
	t.Run("an image service cannot become a stack", func(t *testing.T) {
		_, err := f.stageSource(img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceCompose})
		requireCode(t, err, "not_applicable")
		require.Equal(t, services.SourceImage, f.row(img.ID).SourceType)
	})
	t.Run("the current source type is not a change", func(t *testing.T) {
		_, err := f.stageSource(img.ID, img.Generation, services.SourceChangeInput{SourceType: services.SourceImage, Image: "nginx:2"})
		requireCode(t, err, "source_unchanged")
	})
}

func TestStageSourceChangeNeedsTheCurrentGeneration(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.staging.ID, "web")
	in := services.SourceChangeInput{SourceType: services.SourceGit, GitURL: "https://git.example.com/a/b.git", GitBranch: "main"}

	_, err := f.stageSource(svc.ID, svc.Generation-1, in)
	requireCode(t, err, "generation_conflict")
	require.Equal(t, services.SourceImage, f.row(svc.ID).SourceType)

	// A rolled back transaction leaves nothing behind either.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		_, _, err := f.svcs.StageSourceChangeTx(f.ctx, tx, f.scope(tx, svc.ID), svc.Generation, in)
		return err
	}))
	row := f.row(svc.ID)
	require.Equal(t, services.SourceImage, row.SourceType)
	require.Equal(t, svc.Generation, row.Generation)
	require.Equal(t, "traefik/whoami:latest", services.DecodeConfig(row.Config).Image)
	require.Zero(t, f.audits(services.ActionSourceChanged, svc.ID.String()))
}

func TestSourceSwitchShowsAsOneStagedChangeAndBackIsNoChange(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	box, err := secretbox.New(adapterKey)
	require.NoError(t, err)
	svc := f.imageService(f.staging.ID, "web")

	// The release the service runs now.
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(svc.Config)})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: "traefik/whoami:latest"})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: svc.ID, Source: src, Config: snap, ImageRef: "traefik/whoami:latest"})
	require.NoError(t, err)

	diff := func() []services.FieldChange {
		t.Helper()
		cur, err := services.LoadCurrentState(bg, f.q, box, f.row(svc.ID))
		require.NoError(t, err)
		changes, err := services.DiffRelease(bg, f.q, box, rel, cur)
		require.NoError(t, err)
		return changes
	}
	require.Empty(t, diff())

	_, err = f.stageSource(svc.ID, svc.Generation, services.SourceChangeInput{
		SourceType: services.SourceGit, GitURL: "https://github.com/acme/shop.git", GitBranch: "main", BuildType: "railpack",
	})
	require.NoError(t, err)
	changes := diff()
	require.Len(t, changes, 1, "the image and repository fields are part of the one change: %+v", changes)
	require.Equal(t, services.FieldSource, changes[0].Field)
	require.Equal(t, "image", changes[0].From)
	require.Equal(t, "Git (github.com/acme/shop, main, Railpack)", changes[0].To, "no scheme, no credentials")

	// Back to the image of the release: nothing is staged any more.
	cur, err := f.svcs.Get(f.ctx, svc.ID)
	require.NoError(t, err)
	_, err = f.stageSource(svc.ID, cur.Generation, services.SourceChangeInput{SourceType: services.SourceImage, Image: "traefik/whoami:latest"})
	require.NoError(t, err)
	require.Empty(t, diff())
	// A different image is a plain image change again.
	cur, err = f.svcs.Get(f.ctx, svc.ID)
	require.NoError(t, err)
	_, err = f.svcs.Update(f.ctx, svc.ID, cur.Generation, services.UpdateInput{Config: func() *services.Config {
		c := services.DecodeConfig(f.row(svc.ID).Config)
		c.Image = "traefik/whoami:v1.10"
		return &c
	}()})
	require.NoError(t, err)
	changes = diff()
	require.Len(t, changes, 1)
	require.Equal(t, "config.image", changes[0].Field)
}

func TestSourceLabels(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/shop.git":       "github.com/acme/shop",
		"https://user:tok@github.com/acme/shop":  "github.com/acme/shop",
		"git@github.com:acme/shop.git":           "github.com/acme/shop",
		"ssh://git@git.example.com:2222/a/b.git": "git.example.com:2222/a/b",
		"http://git.example.com/a/b/":            "git.example.com/a/b",
	} {
		require.Equal(t, want, services.RepoLabel(in), in)
	}
	require.Equal(t, "Auto", services.BuilderLabel(""))
	require.Equal(t, "Dockerfile", services.BuilderLabel("dockerfile"))
	require.Equal(t, "Railpack", services.BuilderLabel("railpack"))
	require.Equal(t, "Starter (node)", services.BuilderLabel("starter:node"))
	require.Equal(t, "image (nginx:1)", services.SourceText(services.SourceImage, services.Config{Image: "nginx:1"}, true))
	require.Equal(t, "image", services.SourceText(services.SourceImage, services.Config{Image: "nginx:1"}, false))
	require.True(t, services.SourceSwitched(services.SourceImage, services.SourceGit))
	require.True(t, services.SourceSwitched(services.SourceGit, services.SourceImage))
	require.False(t, services.SourceSwitched(services.SourceGit, services.SourceGit))
	require.False(t, services.SourceSwitched(services.SourceCompose, services.SourceGit))
	require.False(t, services.SourceSwitched("", services.SourceGit), "an old release without a source records no type")
}
