package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// recDisp records agent commands and serves canned runtime log lines.
type recDisp struct {
	mu     sync.Mutex
	cmds   []agents.NewCommand
	subs   []agentprotocol.LogsSubscribe
	lines  map[string][]string // service id → runtime lines, oldest first
	subErr error
}

func (d *recDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	return uuid.New(), nil
}

func (d *recDisp) SubscribeLogs(_ context.Context, _ uuid.UUID, req agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs = append(d.subs, req)
	if d.subErr != nil {
		return nil, nil, d.subErr
	}
	lines := d.lines[req.ServiceID]
	if req.Tail > 0 && len(lines) > req.Tail {
		lines = lines[len(lines)-req.Tail:]
	}
	ch := make(chan agentprotocol.LogFrame, 1)
	fr := agentprotocol.LogFrame{StreamID: req.StreamID, ServiceID: req.ServiceID, Stream: agentprotocol.StreamRuntime}
	base := time.Now().Add(-time.Duration(len(lines)) * time.Second)
	for i, l := range lines {
		fr.Lines = append(fr.Lines, agentprotocol.LogLine{TS: base.Add(time.Duration(i) * time.Second), Text: l})
	}
	ch <- fr
	close(ch)
	return ch, func() {}, nil
}

func (d *recDisp) commands(kind string) []agents.NewCommand {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []agents.NewCommand
	for _, c := range d.cmds {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func (d *recDisp) subscriptions() []agentprotocol.LogsSubscribe {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]agentprotocol.LogsSubscribe(nil), d.subs...)
}

// opsFixture is a txFixture whose service talks to a recording dispatcher and
// has an online server to place services on.
type opsFixture struct {
	*txFixture
	disp   *recDisp
	server db.Server
	viewer context.Context
}

func newOpsFixture(t *testing.T) *opsFixture {
	t.Helper()
	f := newTxFixture(t)
	disp := &recDisp{lines: map[string][]string{}}
	f.svcs = services.NewService(f.pool, events.NewBus(), f.box(), disp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "edge"})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	p, _ := auth.PrincipalFrom(f.ctx)
	viewer := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsRead, policy.ScopeLogsRead},
	})
	return &opsFixture{txFixture: f, disp: disp, server: srv, viewer: viewer}
}

// deployed inserts an image service placed on the server with a succeeded current deployment.
func (f *opsFixture) deployed(envID uuid.UUID, slug string, cfg services.Config) db.Service {
	f.t.Helper()
	cfg = cfg.WithDefaults()
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	bg := context.Background()
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	return f.row(sv.ID)
}

// do runs one request against the service endpoints as ctx's principal.
func (f *opsFixture) do(ctx context.Context, method, path string, body any, header ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	p, ok := auth.PrincipalFrom(ctx)
	require.True(f.t, ok)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	services.Register(humachi.New(r, cfg), f.svcs)

	var rd *bytes.Reader
	if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (f *opsFixture) code(rec *httptest.ResponseRecorder) string {
	f.t.Helper()
	return errCode(f.t, rec)
}

func (f *opsFixture) desiredState(id uuid.UUID) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT desired_state FROM services WHERE id = $1", id).Scan(&s))
	return s
}

func (f *opsFixture) deletedAt(id uuid.UUID) *time.Time {
	f.t.Helper()
	var at *time.Time
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT deleted_at FROM services WHERE id = $1", id).Scan(&at))
	return at
}

// ---- restart and stop ----

func TestRestartAndStopQueueCommandsOnTheRunningServer(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80, StopGraceSeconds: 45})

	rec := f.do(f.ctx, http.MethodPost, "/services/"+sv.ID.String()+"/restart", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var res services.ActionResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEqual(t, uuid.Nil, res.CommandID)
	cmds := f.disp.commands(agentprotocol.KindRestartService)
	require.Len(t, cmds, 1)
	require.Equal(t, f.server.ID, cmds[0].ServerID)
	var payload agentprotocol.ServiceActionPayload
	require.NoError(t, json.Unmarshal(cmds[0].Payload, &payload))
	require.Equal(t, sv.ID.String(), payload.ServiceID)
	require.Equal(t, 45, payload.StopGraceSeconds, "the container gets the service's own stop grace period")
	require.Equal(t, "running", f.desiredState(sv.ID))
	require.Equal(t, 1, f.audits("service.restart", sv.ID.String()))

	rec = f.do(f.ctx, http.MethodPost, "/services/"+sv.ID.String()+"/stop", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Len(t, f.disp.commands(agentprotocol.KindStopService), 1)
	require.Equal(t, "stopped", f.desiredState(sv.ID), "a stopped service stays down for the reconciler")
	require.Equal(t, 1, f.audits("service.stop", sv.ID.String()))

	rec = f.do(f.ctx, http.MethodPost, "/services/"+sv.ID.String()+"/restart", nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, "running", f.desiredState(sv.ID), "restarting a stopped service wants it running again")
}

func TestRestartAndStopRefusals(t *testing.T) {
	f := newOpsFixture(t)
	notDeployed := f.imageService(f.prod.ID, "idle")
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})

	for _, op := range []string{"restart", "stop"} {
		rec := f.do(f.ctx, http.MethodPost, "/services/"+notDeployed.ID.String()+"/"+op, nil)
		require.Equal(t, http.StatusConflict, rec.Code, op)
		require.Equal(t, "not_deployed", f.code(rec))

		rec = f.do(f.viewer, http.MethodPost, "/services/"+sv.ID.String()+"/"+op, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, op+" needs deployments:write")

		rec = f.do(f.ctx, http.MethodPost, "/services/"+uuid.NewString()+"/"+op, nil)
		require.Equal(t, http.StatusNotFound, rec.Code, op)
	}
	require.Empty(t, f.disp.commands(agentprotocol.KindRestartService))
	require.Empty(t, f.disp.commands(agentprotocol.KindStopService))

	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	rec := f.do(f.ctx, http.MethodPost, "/services/"+sv.ID.String()+"/restart", nil)
	require.GreaterOrEqual(t, rec.Code, 409, "a revoked server cannot be told anything: %s", rec.Body.String())
	require.Empty(t, f.disp.commands(agentprotocol.KindRestartService))
	require.Equal(t, "running", f.desiredState(sv.ID), "the desired state does not change when nothing was queued")
}

// ---- delete ----

func TestDeleteServiceNeedsTheSlugAndRemovesTheContainer(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	_, err := f.pool.Exec(context.Background(), `INSERT INTO volumes (service_id, name, mount_path) VALUES ($1, 'data', '/data')`, sv.ID)
	require.NoError(t, err)
	_, err = f.q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: sv.ID, Hostname: "web.example.com", TargetPort: 80})
	require.NoError(t, err)
	path := "/services/" + sv.ID.String()

	rec := f.do(f.ctx, http.MethodDelete, path, nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "confirm_mismatch", f.code(rec))
	rec = f.do(f.ctx, http.MethodDelete, path+"?confirm=other", nil)
	require.Equal(t, "confirm_mismatch", f.code(rec))
	require.Nil(t, f.deletedAt(sv.ID))
	require.Equal(t, http.StatusForbidden, f.do(f.viewer, http.MethodDelete, path+"?confirm=web", nil).Code)

	rec = f.do(f.ctx, http.MethodDelete, path+"?confirm=web&deleteVolumes=true", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NotNil(t, f.deletedAt(sv.ID))
	cmds := f.disp.commands(agentprotocol.KindRemoveService)
	require.Len(t, cmds, 1, "the container is removed from its server")
	require.Equal(t, f.server.ID, cmds[0].ServerID)
	var payload agentprotocol.ServiceActionPayload
	require.NoError(t, json.Unmarshal(cmds[0].Payload, &payload))
	require.True(t, payload.DeleteVolumes)
	require.Equal(t, []string{agentprotocol.VolumeName(sv.ID.String(), "data")}, payload.VolumeNames, "the data volumes are named so the agent removes them")
	require.Zero(t, f.count("SELECT count(*) FROM domains WHERE service_id = $1", sv.ID), "its domains go with it")
	require.Equal(t, 1, f.audits("service.delete", sv.ID.String()))

	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodGet, path, nil).Code, "a deleted service is gone")
	rec = f.do(f.ctx, http.MethodGet, "/environments/"+f.prod.ID.String()+"/services", nil)
	require.NotContains(t, rec.Body.String(), `"web"`)
}

func TestDeleteKeepsVolumesByDefaultAndRefusesWhileDeploying(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	_, err := f.pool.Exec(context.Background(), `INSERT INTO volumes (service_id, name, mount_path) VALUES ($1, 'data', '/data')`, sv.ID)
	require.NoError(t, err)
	active, err := f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{ServiceID: sv.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)

	rec := f.do(f.ctx, http.MethodDelete, "/services/"+sv.ID.String()+"?confirm=web", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "deployment_in_progress", f.code(rec))
	require.Nil(t, f.deletedAt(sv.ID), "nothing was removed")
	require.Empty(t, f.disp.commands(agentprotocol.KindRemoveService))

	_, err = f.pool.Exec(context.Background(), "UPDATE deployments SET status = 'failed' WHERE id = $1", active.ID)
	require.NoError(t, err)
	rec = f.do(f.ctx, http.MethodDelete, "/services/"+sv.ID.String()+"?confirm=web", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	var payload agentprotocol.ServiceActionPayload
	require.NoError(t, json.Unmarshal(f.disp.commands(agentprotocol.KindRemoveService)[0].Payload, &payload))
	require.False(t, payload.DeleteVolumes)
	require.Empty(t, payload.VolumeNames, "volumes stay unless asked for")
}

func TestDeleteOfAServiceThatNeverRanSendsNothingToAnAgent(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.imageService(f.prod.ID, "idle")
	rec := f.do(f.ctx, http.MethodDelete, "/services/"+sv.ID.String()+"?confirm=idle", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NotNil(t, f.deletedAt(sv.ID))
	require.Empty(t, f.disp.commands(agentprotocol.KindRemoveService))
}

func TestDeleteInternalTxCascadesLikeDeleteAndSkipsTheConfirmation(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	combine := uuid.New()

	var after func(context.Context)
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		var err error
		after, err = f.svcs.DeleteInternalTx(f.ctx, tx, sv.ID, services.DeleteOptions{Reason: "combined", ByCombine: combine})
		return err
	}))
	require.NotNil(t, after)
	after(f.ctx)
	require.NotNil(t, f.deletedAt(sv.ID))
	require.Len(t, f.disp.commands(agentprotocol.KindRemoveService), 1)
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata::text FROM audit_events WHERE action = 'service.delete' AND resource_id = $1", sv.ID.String()).Scan(&meta))
	require.Contains(t, meta, `"reason": "combined"`)
	require.Contains(t, meta, combine.String())

	err := f.inTx(true, func(tx pgx.Tx) error {
		_, err := f.svcs.DeleteInternalTx(f.ctx, tx, uuid.New(), services.DeleteOptions{})
		return err
	})
	require.Error(t, err, "an unknown service is an error, not a silent success")
}

// ---- create, update ----

func TestCreateGetListAndUpdateServiceOverHTTP(t *testing.T) {
	f := newOpsFixture(t)
	envPath := "/environments/" + f.prod.ID.String() + "/services"

	rec := f.do(f.ctx, http.MethodPost, envPath, map[string]any{
		"name": "Web App", "sourceType": "image", "serverId": f.server.ID, "config": map[string]any{"image": "traefik/whoami:latest", "port": 8080},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeView(t, rec)
	require.Equal(t, "web-app", created.Slug)
	require.Equal(t, services.KindApp, created.Kind)
	require.Equal(t, 8080, created.Config.Port)
	require.Equal(t, "\"1\"", rec.Header().Get("ETag"), "the ETag is the generation")
	require.Equal(t, 1, f.audits("service.create", created.ID.String()))

	rec = f.do(f.ctx, http.MethodGet, envPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	page := decodeJSONBody[api.Page[services.ServiceView]](t, rec)
	require.Len(t, page.Items, 1)
	require.Equal(t, created.ID, page.Items[0].ID)

	rec = f.do(f.ctx, http.MethodGet, "/services/"+created.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, created.ID, decodeView(t, rec).ID)

	// A bad payload is a 422 that creates nothing.
	rec = f.do(f.ctx, http.MethodPost, envPath, map[string]any{"name": "No Image", "sourceType": "image", "config": map[string]any{"port": 80}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = f.do(f.viewer, http.MethodPost, envPath, map[string]any{"name": "x", "sourceType": "image", "config": map[string]any{"image": "a:1"}})
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = f.do(f.ctx, http.MethodPost, "/environments/"+uuid.NewString()+"/services", map[string]any{"name": "x", "sourceType": "image", "config": map[string]any{"image": "a:1"}})
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Update needs the generation: missing → 428, stale → 412, right → 200 and a new ETag.
	path := "/services/" + created.ID.String()
	body := map[string]any{"name": "Renamed"}
	rec = f.do(f.ctx, http.MethodPatch, path, body)
	require.Equal(t, http.StatusPreconditionRequired, rec.Code)
	require.Equal(t, "if_match_required", f.code(rec))
	rec = f.do(f.ctx, http.MethodPatch, path, body, "If-Match", `"99"`)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code)
	require.Equal(t, "generation_conflict", f.code(rec))
	rec = f.do(f.ctx, http.MethodPatch, path, body, "If-Match", "not-a-number")
	require.GreaterOrEqual(t, rec.Code, 400)
	rec = f.do(f.ctx, http.MethodPatch, path, body, "If-Match", `"1"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "Renamed", decodeView(t, rec).Name)
	require.Equal(t, "\"2\"", rec.Header().Get("ETag"))

	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"config": map[string]any{"image": "traefik/whoami:v2", "port": 9090, "blockEgress": true}}, "If-Match", `"2"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	require.Equal(t, "traefik/whoami:v2", v.Config.Image)
	require.Equal(t, 9090, v.Config.Port)
	require.True(t, v.Config.BlockEgress)
}

func TestSourceChangeEndpointRequiresTheGeneration(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	path := "/services/" + sv.ID.String() + "/source"
	body := map[string]any{"sourceType": "git", "gitUrl": "https://git.example.com/acme/shop.git", "gitBranch": "main"}

	rec := f.do(f.ctx, http.MethodPut, path, body)
	require.Equal(t, http.StatusPreconditionRequired, rec.Code)
	require.Equal(t, "if_match_required", f.code(rec))
	rec = f.do(f.ctx, http.MethodPut, path, body, "If-Match", `"42"`)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code)

	rec = f.do(f.ctx, http.MethodPut, path, body, "If-Match", `"1"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	require.Equal(t, services.SourceGit, v.SourceType)
	require.Equal(t, "https://git.example.com/acme/shop.git", v.Config.GitURL)

	rec = f.do(f.ctx, http.MethodPut, path, map[string]any{"sourceType": "git", "gitUrl": "https://git.example.com/acme/shop.git"}, "If-Match", `"`+strings.TrimPrefix(rec.Header().Get("ETag"), `"`))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "source_unchanged", f.code(rec))
}

func decodeJSONBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

// ---- volumes ----

func TestVolumesOverHTTP(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	path := "/services/" + sv.ID.String() + "/volumes"

	rec := f.do(f.ctx, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"items":[]}`, rec.Body.String())

	rec = f.do(f.ctx, http.MethodPost, path, map[string]any{"name": "data", "mountPath": "/var/lib/data"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	vol := decodeJSONBody[services.VolumeView](t, rec)
	require.Equal(t, "data", vol.Name)
	require.Equal(t, "/var/lib/data", vol.MountPath)
	require.Equal(t, agentprotocol.VolumeName(sv.ID.String(), "data"), vol.DockerName)
	require.Greater(t, f.row(sv.ID).Generation, sv.Generation, "attaching a volume is a staged change")

	rec = f.do(f.ctx, http.MethodPost, path, map[string]any{"name": "data", "mountPath": "/other"})
	require.Equal(t, http.StatusConflict, rec.Code, "the name is taken")
	rec = f.do(f.ctx, http.MethodPost, path, map[string]any{"name": "Bad Name!", "mountPath": "/x"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = f.do(f.ctx, http.MethodPost, path, map[string]any{"name": "other", "mountPath": "relative/path"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "mount paths are absolute")
	require.Equal(t, http.StatusForbidden, f.do(f.viewer, http.MethodPost, path, map[string]any{"name": "other", "mountPath": "/x"}).Code)

	rec = f.do(f.ctx, http.MethodGet, path, nil)
	require.Len(t, decodeJSONBody[struct {
		Items []services.VolumeView `json:"items"`
	}](t, rec).Items, 1)

	rec = f.do(f.ctx, http.MethodDelete, "/volumes/"+vol.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Zero(t, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", sv.ID))
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodDelete, "/volumes/"+vol.ID.String(), nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodGet, "/services/"+uuid.NewString()+"/volumes", nil).Code)
}

// ---- logs ----

func (f *opsFixture) logLines(serviceID uuid.UUID, lines ...string) {
	f.disp.mu.Lock()
	defer f.disp.mu.Unlock()
	f.disp.lines[serviceID.String()] = lines
}

func TestServiceLogsAreTailedOrderedAndRedacted(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	_, err := services.WriteServiceVariable(f.ctx, f.q, f.box(), f.varScope(sv.ID), services.VarWrite{Key: "API_KEY", Value: "super-secret-value-1234", Secret: true})
	require.NoError(t, err)
	f.logLines(sv.ID, "booting", "connecting with super-secret-value-1234", "ready", "listening on :80")
	path := "/services/" + sv.ID.String() + "/logs"

	rec := f.do(f.viewer, http.MethodGet, path+"?tail=3", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeJSONBody[struct {
		Items []services.LogEntry `json:"items"`
	}](t, rec)
	require.Len(t, out.Items, 3)
	require.Equal(t, "connecting with "+services.Mask, out.Items[0].Line, "secrets never leave in log lines")
	require.Equal(t, "ready", out.Items[1].Line)
	require.Equal(t, "listening on :80", out.Items[2].Line)
	require.True(t, out.Items[0].TS.Before(out.Items[2].TS), "oldest first")
	require.NotContains(t, rec.Body.String(), "super-secret-value-1234")
	subs := f.disp.subscriptions()
	require.Len(t, subs, 1)
	require.Equal(t, 3, subs[0].Tail)
	require.False(t, subs[0].Follow)
	require.Equal(t, sv.ID.String(), subs[0].ServiceID)

	// An absurd tail is clamped for the agent.
	rec = f.do(f.viewer, http.MethodGet, path+"?tail=5000", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, services.MaxTail, f.disp.subscriptions()[1].Tail)
	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.viewer, http.MethodGet, path+"?tail=5001", nil).Code, "huma refuses more than the maximum")
}

func TestServiceLogsFollowStreamsEventsAndEnds(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	f.logLines(sv.ID, "first line", "second line")

	rec := f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String()+"/logs?follow=true", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, ": connected\n\n"), body)
	require.Contains(t, body, `"line":"first line"`)
	require.Contains(t, body, `"line":"second line"`)
	require.Less(t, strings.Index(body, "first line"), strings.Index(body, "second line"))
	require.Contains(t, body, "event: end\ndata: {}\n\n", "the stream says when the agent has nothing more")
	require.True(t, f.disp.subscriptions()[0].Follow)
}

func TestServiceLogsRefusals(t *testing.T) {
	f := newOpsFixture(t)
	idle := f.imageService(f.prod.ID, "idle")
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})

	rec := f.do(f.viewer, http.MethodGet, "/services/"+idle.ID.String()+"/logs", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "not_deployed", f.code(rec))

	rec = f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String()+"/logs?component=db&process=web", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", f.code(rec), "component and process select by different things")
	rec = f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String()+"/logs?component=db", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "components belong to Compose stacks")
	rec = f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String()+"/logs?process=worker", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an app without processes has no process to pick")

	auth0, _ := auth.PrincipalFrom(f.ctx)
	noLogs := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: auth0.UserID, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsRead}})
	require.Equal(t, http.StatusForbidden, f.do(noLogs, http.MethodGet, "/services/"+sv.ID.String()+"/logs", nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.viewer, http.MethodGet, "/services/"+uuid.NewString()+"/logs", nil).Code)

	f.disp.subErr = apierrOffline
	rec = f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String()+"/logs", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "an agent that is not connected is a 503: %s", rec.Body.String())
}

// apierrOffline is what the agent hub answers for a server that is not connected.
var apierrOffline = apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is not connected")

func TestEnvironmentLogsMergeServicesAndSkipThoseThatCannotBeRead(t *testing.T) {
	f := newOpsFixture(t)
	a := f.deployed(f.prod.ID, "api", services.Config{Image: "traefik/whoami:latest", Port: 80})
	b := f.deployed(f.prod.ID, "worker", services.Config{Image: "traefik/whoami:latest", Port: 80})
	f.imageService(f.prod.ID, "idle") // never deployed: skipped
	f.logLines(a.ID, "api up")
	f.logLines(b.ID, "worker up")
	path := "/environments/" + f.prod.ID.String() + "/logs"

	rec := f.do(f.viewer, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeJSONBody[struct {
		Items []services.LogEntry `json:"items"`
	}](t, rec)
	require.Len(t, out.Items, 2)
	names := map[string]string{}
	for _, e := range out.Items {
		names[e.ServiceName] = e.Line
	}
	require.Equal(t, map[string]string{"api": "api up", "worker": "worker up"}, names, "every entry says which service it came from")
	require.Len(t, f.disp.subscriptions(), 2, "the idle service was not subscribed")

	rec = f.do(f.viewer, http.MethodGet, path+"?follow=true", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"serviceName":"api"`)
	require.Contains(t, rec.Body.String(), "event: end")

	// Nothing deployed in the environment: a conflict that says so.
	rec = f.do(f.viewer, http.MethodGet, "/environments/"+f.staging.ID.String()+"/logs", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "not_deployed", f.code(rec))

	// Every agent unreachable: the last reason is reported.
	f.disp.subErr = apierrOffline
	rec = f.do(f.viewer, http.MethodGet, path, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, http.StatusNotFound, f.do(f.viewer, http.MethodGet, "/environments/"+uuid.NewString()+"/logs", nil).Code)
}

func TestTailRuntimeLogsInternalReturnsTheLastLinesRedacted(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	_, err := services.WriteServiceVariable(f.ctx, f.q, f.box(), f.varScope(sv.ID), services.VarWrite{Key: "TOKEN", Value: "tok-0123456789abcdef", Secret: true})
	require.NoError(t, err)
	f.logLines(sv.ID, "one", "two with tok-0123456789abcdef", "three", "four")

	lines, err := f.svcs.TailRuntimeLogsInternal(context.Background(), sv.ID, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"two with " + services.Mask, "three", "four"}, lines, "no principal is needed, but secrets are still masked")

	idle := f.imageService(f.prod.ID, "idle")
	_, err = f.svcs.TailRuntimeLogsInternal(context.Background(), idle.ID, 3)
	requireCode(t, err, "not_deployed")
	_, err = f.svcs.TailRuntimeLogsInternal(context.Background(), uuid.New(), 3)
	require.Error(t, err)
}

// ---- links ----

func TestLinksArePreviewedCreatedListedMergedAndDeleted(t *testing.T) {
	f := newOpsFixture(t)
	api := f.imageService(f.prod.ID, "api")
	pg := f.imageService(f.prod.ID, "db")
	worker := f.imageService(f.prod.ID, "worker")
	envPath := "/environments/" + f.prod.ID.String()

	// The preview proposes what the consumer would hold, writes nothing, and flags what exists.
	rec := f.do(f.ctx, http.MethodGet, envPath+"/link-proposals?from="+api.ID.String()+"&to="+pg.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	prev := decodeJSONBody[services.LinkPreview](t, rec)
	require.False(t, prev.Linked)
	require.NotEmpty(t, prev.ProposedVariables)
	keys := map[string]services.ProposedVariable{}
	for _, p := range prev.ProposedVariables {
		keys[p.Key] = p
		require.Equal(t, "new", p.Status, p.Key)
		require.Contains(t, p.Value, "${{db.", "every proposal is a reference to the provider")
	}
	require.Contains(t, keys, "DB_HOST")
	require.True(t, keys["DB_HOST"].Recommended)
	require.Empty(t, prev.ExistingKeys)
	require.Zero(t, f.count("SELECT count(*) FROM service_links"), "a preview writes nothing")

	// With DB_HOST already on the consumer the preview says so: same value = unchanged, other = replaces.
	sc := f.varScope(api.ID)
	_, err := services.WriteServiceVariable(f.ctx, f.q, f.box(), sc, services.VarWrite{Key: "DB_HOST", Value: keys["DB_HOST"].Value})
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(f.ctx, f.q, f.box(), sc, services.VarWrite{Key: "DB_PORT", Value: "5432"})
	require.NoError(t, err)
	rec = f.do(f.ctx, http.MethodGet, envPath+"/link-proposals?from="+api.ID.String()+"&to="+pg.ID.String(), nil)
	prev = decodeJSONBody[services.LinkPreview](t, rec)
	require.Equal(t, []string{"DB_HOST", "DB_PORT"}, prev.ExistingKeys)
	for _, p := range prev.ProposedVariables {
		switch p.Key {
		case "DB_HOST":
			require.Equal(t, "unchanged", p.Status)
		case "DB_PORT":
			require.Equal(t, "replaces", p.Status)
		}
	}

	// Invalid previews.
	rec = f.do(f.ctx, http.MethodGet, envPath+"/link-proposals?from="+api.ID.String()+"&to="+api.ID.String(), nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "invalid_link", f.code(rec))
	other := f.imageService(f.staging.ID, "elsewhere")
	rec = f.do(f.ctx, http.MethodGet, envPath+"/link-proposals?from="+api.ID.String()+"&to="+other.ID.String(), nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "services of another environment cannot be linked")
	require.Equal(t, http.StatusForbidden, f.do(f.viewer, http.MethodGet, envPath+"/link-proposals?from="+api.ID.String()+"&to="+pg.ID.String(), nil).Code)

	// Creating the link records the edge and proposes (but does not add) variables.
	rec = f.do(f.ctx, http.MethodPost, envPath+"/links", map[string]any{"fromServiceId": api.ID, "toServiceId": pg.ID})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	res := decodeJSONBody[services.LinkResult](t, rec)
	require.Equal(t, api.ID, res.Link.FromServiceID)
	require.Equal(t, pg.ID, res.Link.ToServiceID)
	require.Equal(t, services.LinkManual, res.Link.Source)
	require.NotEmpty(t, res.ProposedVariables)
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links"))
	require.Equal(t, 1, f.audits("link.create", res.Link.ID))
	_, err = f.q.GetServiceVariable(f.ctx, dbParams(api.ID, "DB_URL"))
	require.Error(t, err, "proposed variables are not written")

	rec = f.do(f.ctx, http.MethodPost, envPath+"/links", map[string]any{"fromServiceId": api.ID, "toServiceId": pg.ID})
	require.Equal(t, http.StatusConflict, rec.Code, "the same link twice")
	rec = f.do(f.ctx, http.MethodPost, envPath+"/links", map[string]any{"fromServiceId": api.ID, "toServiceId": api.ID})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, http.StatusForbidden, f.do(f.viewer, http.MethodPost, envPath+"/links", map[string]any{"fromServiceId": worker.ID, "toServiceId": pg.ID}).Code)

	// A variable that references another service is a derived link; with a manual one on the same pair they merge.
	_, err = services.WriteServiceVariable(f.ctx, f.q, f.box(), f.varScope(worker.ID), services.VarWrite{Key: "DB", Value: "${{db.NEBULA_PRIVATE_HOST}}"})
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(f.ctx, f.q, f.box(), f.varScope(worker.ID), services.VarWrite{Key: "SELF", Value: "${{worker.PORT}}"})
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(f.ctx, f.q, f.box(), sc, services.VarWrite{Key: "DB_NAME", Value: "${{db.NEBULA_SERVICE_NAME}}"})
	require.NoError(t, err)

	rec = f.do(f.ctx, http.MethodGet, envPath+"/links", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	links := decodeJSONBody[struct {
		Items []services.LinkView `json:"items"`
	}](t, rec).Items
	require.Len(t, links, 2, "api→db (manual and referenced) and worker→db (referenced only)")
	byFrom := map[uuid.UUID]services.LinkView{}
	for _, l := range links {
		byFrom[l.FromServiceID] = l
	}
	manual := byFrom[api.ID]
	require.Equal(t, services.LinkManual, manual.Source, "a link that is also referenced reports manual")
	require.Equal(t, res.Link.ID, manual.ID)
	require.Equal(t, []services.LinkReferenceKey{{Key: "DB_HOST", Staged: true}, {Key: "DB_NAME", Staged: true}}, manual.References, "the consumer never deployed, so its references are staged")
	require.True(t, manual.Staged)
	derived := byFrom[worker.ID]
	require.Equal(t, services.LinkReference, derived.Source)
	require.Equal(t, "ref:"+worker.ID.String()+":"+pg.ID.String(), derived.ID)
	require.Equal(t, []services.LinkReferenceKey{{Key: "DB", Staged: true}}, derived.References, "a self reference is no edge")

	// Derived links cannot be deleted, unknown ids are a 404, manual ones go.
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodDelete, "/links/"+derived.ID, nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodDelete, "/links/not-a-uuid", nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodDelete, "/links/"+uuid.NewString(), nil).Code)
	require.Equal(t, http.StatusForbidden, f.do(f.viewer, http.MethodDelete, "/links/"+manual.ID, nil).Code)
	rec = f.do(f.ctx, http.MethodDelete, "/links/"+manual.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Zero(t, f.count("SELECT count(*) FROM service_links"))
	require.Equal(t, 1, f.audits("link.delete", manual.ID))

	rec = f.do(f.ctx, http.MethodGet, envPath+"/links", nil)
	after := decodeJSONBody[struct {
		Items []services.LinkView `json:"items"`
	}](t, rec).Items
	require.Len(t, after, 2, "the references still draw their edges after the manual link is gone")
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodGet, "/environments/"+uuid.NewString()+"/links", nil).Code)
}

func dbParams(serviceID uuid.UUID, key string) db.GetServiceVariableParams {
	return db.GetServiceVariableParams{ServiceID: serviceID, Key: key}
}
