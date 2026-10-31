package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// httpDo runs one request against the service endpoints as the fixture's caller.
func (f *txFixture) httpDo(method, path string, body any, header ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	p, err := policy.Authenticated(f.ctx)
	require.NoError(f.t, err)
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

func decodeView(t *testing.T, rec *httptest.ResponseRecorder) services.ServiceView {
	t.Helper()
	var v services.ServiceView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

// errCode is the machine code of an error response.
func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e.Code
}

// processesBody is a create request for an app with the default process web on
// 3000 and the processes worker (queue, no port) and api (its own port).
func processesBody() map[string]any {
	return map[string]any{
		"name": "shop", "sourceType": "image",
		"config": map[string]any{
			"image": "traefik/whoami:latest", "port": 3000, "startCommand": []string{"bin/web"}, "processName": "web",
			"processes": []map[string]any{
				{"name": "worker", "startCommand": []string{"bin/queue", "--fast"}, "stopGraceSeconds": 120, "memoryMb": 256},
				{"name": "api", "startCommand": []string{"bin/api"}, "port": 4000, "healthcheck": map[string]any{"type": "http", "path": "/up"}},
			},
		},
	}
}

func TestCreateServiceAcceptsProcessesAndViewsThem(t *testing.T) {
	f := newTxFixture(t)
	rec := f.httpDo(http.MethodPost, "/environments/"+f.staging.ID.String()+"/services", processesBody())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	v := decodeView(t, rec)

	// The stored config carries them, sorted by name, with the default process' name.
	require.Equal(t, "web", v.Config.ProcessName)
	require.Len(t, v.Config.Processes, 2)
	apiProc, worker := v.Config.Processes[0], v.Config.Processes[1]
	require.Equal(t, "api", apiProc.Name)
	require.Equal(t, 4000, apiProc.Port)
	require.Equal(t, "/up", apiProc.Healthcheck.Path)
	require.Equal(t, 4000, apiProc.Healthcheck.Port, "the health check follows the process' own port")
	require.Equal(t, "worker", worker.Name)
	require.Equal(t, []string{"bin/queue", "--fast"}, worker.StartCommand)
	require.Equal(t, 120, worker.StopGraceSeconds)
	require.Equal(t, 256, worker.MemoryMB)
	require.Equal(t, "none", worker.Healthcheck.Type, "a process without a port has no HTTP check")

	// The view lists the default process first, then the others.
	require.Len(t, v.Processes, 3)
	require.Equal(t, "web", v.Processes[0].Name)
	require.True(t, v.Processes[0].Default)
	require.Equal(t, 3000, v.Processes[0].Port)
	require.Equal(t, []string{"api", "worker"}, []string{v.Processes[1].Name, v.Processes[2].Name})
	require.False(t, v.Processes[1].Default)
	require.True(t, v.Processes[2].Pending == false && v.Processes[2].Removed == false, "a never-deployed service has nothing pending or removed")

	// The same service read back.
	rec = f.httpDo(http.MethodGet, "/services/"+v.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, decodeView(t, rec).Processes, 3)
}

func TestServiceWithoutProcessesViewsNone(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.staging.ID, "plain")
	rec := f.httpDo(http.MethodGet, "/services/"+sv.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.JSONEq(t, `[]`, string(raw["processes"]), "processes is always present, empty for a plain service")
	require.NotContains(t, rec.Body.String(), `"processName"`)
}

func TestCreateServiceRefusesInvalidProcesses(t *testing.T) {
	f := newTxFixture(t)
	path := "/environments/" + f.staging.ID.String() + "/services"
	mut := func(fn func(cfg map[string]any, procs []map[string]any) []map[string]any) map[string]any {
		b := processesBody()
		cfg := b["config"].(map[string]any)
		cfg["processes"] = fn(cfg, cfg["processes"].([]map[string]any))
		return b
	}
	manyProcs := func() []map[string]any {
		var out []map[string]any
		for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10"} {
			out = append(out, map[string]any{"name": n, "startCommand": []string{"x"}})
		}
		return out
	}
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"name pattern": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[0]["name"] = "Bad_Name"
			return p
		}), http.StatusUnprocessableEntity},
		"default process name pattern": {mut(func(c map[string]any, p []map[string]any) []map[string]any {
			c["processName"] = "Web!"
			return p
		}), http.StatusUnprocessableEntity},
		"too many for the schema": {mut(func(_ map[string]any, _ []map[string]any) []map[string]any { return manyProcs() }), http.StatusUnprocessableEntity},
		"no command": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[0]["startCommand"] = []string{}
			return p
		}), http.StatusUnprocessableEntity},
		"duplicate name": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[1]["name"] = p[0]["name"]
			return p
		}), http.StatusUnprocessableEntity},
		"default process name reused": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[0]["name"] = "web"
			return p
		}), http.StatusUnprocessableEntity},
		"port shared with the default process": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[1]["port"] = 3000
			return p
		}), http.StatusUnprocessableEntity},
		"port shared between processes": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[1]["port"] = 4000
			p[0]["port"] = 4000
			return p
		}), http.StatusUnprocessableEntity},
		"stop grace out of range": {mut(func(_ map[string]any, p []map[string]any) []map[string]any {
			p[0]["stopGraceSeconds"] = 99999
			return p
		}), http.StatusUnprocessableEntity},
	} {
		rec := f.httpDo(http.MethodPost, path, tc.body)
		require.Equal(t, tc.want, rec.Code, "%s: %s", name, rec.Body.String())
	}
	require.Zero(t, f.count("SELECT count(*) FROM services WHERE environment_id = $1", f.staging.ID), "nothing was created")

	// Processes need an app or a worker: a database or a stack is refused.
	rec := f.httpDo(http.MethodPost, path, map[string]any{
		"name": "db", "kind": "database", "sourceType": "image", "config": map[string]any{
			"image":     "postgres:16",
			"processes": []map[string]any{{"name": "worker", "startCommand": []string{"x"}}},
		},
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestPatchReplacesTheProcessList(t *testing.T) {
	f := newTxFixture(t)
	rec := f.httpDo(http.MethodPost, "/environments/"+f.staging.ID.String()+"/services", processesBody())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	v := decodeView(t, rec)

	// Singleton on the default process and one process fewer.
	body := processesBody()
	cfg := body["config"].(map[string]any)
	cfg["singleton"] = true
	cfg["processes"] = cfg["processes"].([]map[string]any)[:1]
	rec = f.httpDo(http.MethodPatch, "/services/"+v.ID.String(), map[string]any{"config": cfg}, "If-Match", `"`+itoa(v.Generation)+`"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeView(t, rec)
	require.True(t, got.Config.Singleton)
	require.Len(t, got.Config.Processes, 1)
	require.Equal(t, "worker", got.Config.Processes[0].Name)
	require.Equal(t, v.Generation+1, got.Generation)
	require.Len(t, got.Processes, 2)

	// A config without processes drops them all.
	delete(cfg, "processes")
	rec = f.httpDo(http.MethodPatch, "/services/"+v.ID.String(), map[string]any{"config": cfg}, "If-Match", `"`+itoa(got.Generation)+`"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, decodeView(t, rec).Config.Processes)
	require.Empty(t, decodeView(t, rec).Processes)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestRenameProcessesMovesScaleAndDomains(t *testing.T) {
	f := newTxFixture(t)
	rec := f.httpDo(http.MethodPost, "/environments/"+f.staging.ID.String()+"/services", processesBody())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	ctx := context.Background()

	// The api process has a stored replica count and a domain.
	_, err := f.q.EnsureProcessScale(ctx, db.EnsureProcessScaleParams{ServiceID: v.ID, Process: "api"})
	require.NoError(t, err)
	_, err = f.q.ApplyProcessScale(ctx, db.ApplyProcessScaleParams{ServiceID: v.ID, Process: "api", Replicas: 3, ExpectedReplicas: 1, LastScaleEvent: json.RawMessage(`null`)})
	require.NoError(t, err)
	dom, err := f.q.InsertDomain(ctx, db.InsertDomainParams{ServiceID: v.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)

	renamed := processesBody()
	cfg := renamed["config"].(map[string]any)
	procs := cfg["processes"].([]map[string]any)
	procs[1]["name"] = "gateway" // api -> gateway
	patch := func(gen int64, extra map[string]any) *httptest.ResponseRecorder {
		b := map[string]any{"config": cfg}
		for k, val := range extra {
			b[k] = val
		}
		return f.httpDo(http.MethodPatch, "/services/"+v.ID.String(), b, "If-Match", `"`+itoa(gen)+`"`)
	}

	// Without the rename the domain still points at a process that is gone.
	rec = patch(v.Generation, nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "process_in_use", errCode(t, rec))
	scales, err := f.q.ListProcessScales(ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "api", scales[0].Process, "the refused write changed nothing")

	// With renameProcesses the process keeps its replicas and its domain.
	rec = patch(v.Generation, map[string]any{"renameProcesses": map[string]string{"api": "gateway"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	scales, err = f.q.ListProcessScales(ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, scales, 1)
	require.Equal(t, "gateway", scales[0].Process)
	require.EqualValues(t, 3, scales[0].Replicas)
	doms, err := f.q.ListServiceDomains(ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)
	require.Equal(t, dom.ID, doms[0].ID)
	require.Equal(t, "gateway", doms[0].Component)
	require.EqualValues(t, 4000, doms[0].TargetPort)
	require.NotContains(t, rec.Body.String(), "renameProcesses", "the rename is not stored")
}

func TestPortChangeLeavesNamedProcessDomainsAlone(t *testing.T) {
	f := newTxFixture(t)
	rec := f.httpDo(http.MethodPost, "/environments/"+f.staging.ID.String()+"/services", processesBody())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	ctx := context.Background()
	_, err := f.q.InsertDomain(ctx, db.InsertDomainParams{ServiceID: v.ID, Hostname: "www.example.com", TargetPort: 3000})
	require.NoError(t, err)
	_, err = f.q.InsertDomain(ctx, db.InsertDomainParams{ServiceID: v.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)

	body := processesBody()
	cfg := body["config"].(map[string]any)
	cfg["port"] = 3100
	rec = f.httpDo(http.MethodPatch, "/services/"+v.ID.String(), map[string]any{"config": cfg}, "If-Match", `"`+itoa(v.Generation)+`"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ports := map[string]int32{}
	doms, err := f.q.ListServiceDomains(ctx, v.ID)
	require.NoError(t, err)
	for _, d := range doms {
		ports[d.Hostname] = d.TargetPort
	}
	require.Equal(t, map[string]int32{"www.example.com": 3100, "api.example.com": 4000}, ports, "only the default process' domain follows the service port")
}

func TestMetricsAndLogsValidateTheProcess(t *testing.T) {
	f := newTxFixture(t)
	rec := f.httpDo(http.MethodPost, "/environments/"+f.staging.ID.String()+"/services", processesBody())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	plain := f.imageService(f.staging.ID, "plain")
	base := "/services/" + v.ID.String()

	// Metrics: no process = the total, a known process = its series, an unknown one is refused.
	rec = f.httpDo(http.MethodGet, base+"/metrics?range=1h", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.httpDo(http.MethodGet, base+"/metrics?range=1h&process=worker", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.httpDo(http.MethodGet, base+"/metrics?range=1h&process=web", nil)
	require.Equal(t, http.StatusOK, rec.Code, "the default process is a process too")
	rec = f.httpDo(http.MethodGet, base+"/metrics?range=1h&process=cron", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "unknown_process", errCode(t, rec))
	rec = f.httpDo(http.MethodGet, "/services/"+plain.ID.String()+"/metrics?range=1h&process=web", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a service without processes has no process filter")
	require.Equal(t, "validation_failed", errCode(t, rec))

	// Logs: the filters are checked before the agent is asked for anything.
	rec = f.httpDo(http.MethodGet, base+"/logs?process=cron", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "unknown_process", errCode(t, rec))
	rec = f.httpDo(http.MethodGet, base+"/logs?process=worker&component=web", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))
	rec = f.httpDo(http.MethodGet, "/services/"+plain.ID.String()+"/logs?process=worker", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))
}

func TestDeleteInternalTxRunsTheDeleteCascade(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.staging.ID, "doomed")
	ctx := context.Background()
	_, err := f.q.InsertDomain(ctx, db.InsertDomainParams{ServiceID: sv.ID, Hostname: "doomed.example.com", TargetPort: 80})
	require.NoError(t, err)
	combine := uuid.New()

	// A rolled-back transaction deletes nothing.
	err = f.inTx(false, func(tx pgx.Tx) error {
		_, err := f.svcs.DeleteInternalTx(f.ctx, tx, sv.ID, services.DeleteOptions{ByCombine: combine})
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, f.row(sv.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1", sv.ID))

	var after func(context.Context)
	err = f.inTx(true, func(tx pgx.Tx) error {
		var err error
		after, err = f.svcs.DeleteInternalTx(f.ctx, tx, sv.ID, services.DeleteOptions{Reason: "combined", ByCombine: combine})
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, after)
	after(ctx)

	_, err = services.LoadScopeInternal(ctx, f.q, sv.ID)
	require.Error(t, err, "the service is gone")
	require.Zero(t, f.count("SELECT count(*) FROM domains WHERE service_id = $1", sv.ID))
	require.Equal(t, 1, f.count(
		"SELECT count(*) FROM audit_events WHERE action = 'service.delete' AND resource_id = $1 AND metadata->>'combine' = $2 AND metadata->>'reason' = 'combined'",
		sv.ID.String(), combine.String()))
}
