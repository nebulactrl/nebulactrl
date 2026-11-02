package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// principal is the fixture's signed-in owner.
func (f *fixture) principal() *auth.Principal {
	p, _ := auth.PrincipalFrom(f.ctx)
	return p
}

// withScopes is the owner limited to scopes.
func (f *fixture) withScopes(scopes ...string) context.Context {
	p := *f.principal()
	p.Scopes = scopes
	return auth.WithPrincipal(context.Background(), &p)
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// serve returns a caller of the registered task routes.
func (f *fixture) serve() func(p *auth.Principal, method, path string, body any) (int, []byte) {
	r := chi.NewRouter()
	var cur *auth.Principal
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if cur != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), cur))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc)
	return func(p *auth.Principal, method, path string, body any) (int, []byte) {
		f.t.Helper()
		cur = p
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(f.t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	return m
}

func TestTaskEndpoints(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	do := f.serve()
	o := f.principal()
	base := "/services/" + sv.ID.String()

	code, raw := do(o, http.MethodPost, base+"/tasks", map[string]any{
		"name": "nightly", "schedule": "30 3 * * *", "timezone": "Europe/Berlin", "command": []string{"rake", "cleanup"}, "timeoutSeconds": 120, "concurrency": "allow",
	})
	require.Equal(t, http.StatusCreated, code, string(raw))
	task := decode(t, raw)
	id := task["id"].(string)
	require.Equal(t, "Europe/Berlin", task["timezone"])
	require.Equal(t, "allow", task["concurrency"])
	require.Equal(t, true, task["enabled"])
	require.Equal(t, float64(120), task["timeoutSeconds"])
	require.NotEmpty(t, task["nextRunAt"])
	code, raw = do(o, http.MethodPost, base+"/tasks", map[string]any{"name": "nightly", "schedule": "30 3 * * *", "command": []string{"x"}})
	require.Equal(t, http.StatusConflict, code, string(raw))
	code, raw = do(o, http.MethodPost, base+"/tasks", map[string]any{"name": "bad", "schedule": "nope", "command": []string{"x"}})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))

	code, raw = do(o, http.MethodGet, base+"/tasks", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Len(t, decode(t, raw)["items"], 1)

	code, raw = do(o, http.MethodPatch, "/tasks/"+id, map[string]any{"enabled": false, "schedule": "0 4 * * *"})
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, false, decode(t, raw)["enabled"])
	require.Equal(t, "0 4 * * *", decode(t, raw)["schedule"])
	code, raw = do(o, http.MethodPatch, "/tasks/"+uuid.NewString(), map[string]any{"enabled": true})
	require.Equal(t, http.StatusNotFound, code, string(raw))

	// Run now: 202 with the queued run; a stopped-server service answers 409.
	code, raw = do(o, http.MethodPost, "/tasks/"+id+"/run", nil)
	require.Equal(t, http.StatusAccepted, code, string(raw))
	run := decode(t, raw)
	runID := run["id"].(string)
	require.Equal(t, "queued", run["status"])
	require.Equal(t, SourceManual, run["source"])
	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindRunTask, cmd.Kind)

	code, raw = do(o, http.MethodPost, base+"/run", map[string]any{"command": []string{"ls", "-la"}, "timeoutSeconds": 60})
	require.Equal(t, http.StatusAccepted, code, string(raw))
	require.Equal(t, SourceOneOff, decode(t, raw)["source"])
	code, raw = do(o, http.MethodPost, base+"/run", map[string]any{"command": []string{""}})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	require.Equal(t, "invalid_command", decode(t, raw)["code"])

	// The result and the output arrive from the agent; the API shows them.
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{
		TaskRunID: runID, Lines: []agentprotocol.LogLine{{Text: "hello " + canary}, {Text: "second"}},
	}))
	require.NoError(t, f.svc.HandleResult(context.Background(), cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.RunTaskResult{
		StartedAt: f.clock, FinishedAt: f.clock.Add(2 * time.Second), ExitCode: 0,
	})))
	code, raw = do(o, http.MethodGet, "/task-runs/"+runID, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, "succeeded", decode(t, raw)["status"])
	require.Equal(t, float64(0), decode(t, raw)["exitCode"])

	code, raw = do(o, http.MethodGet, "/task-runs/"+runID+"/logs", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	lines := decode(t, raw)["items"].([]any)
	require.Len(t, lines, 2)
	require.NotContains(t, string(raw), canary, "stored output is redacted")
	require.Equal(t, "second", lines[1].(map[string]any)["line"])

	// Following a finished run streams its lines and then the end event.
	code, raw = do(o, http.MethodGet, "/task-runs/"+runID+"/logs?follow=true", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	body := string(raw)
	require.Contains(t, body, `"line":"second"`)
	require.NotContains(t, body, canary)
	require.Contains(t, body, "event: end")
	require.Contains(t, body, `"status":"succeeded"`)
	require.Less(t, strings.Index(body, `"line":"second"`), strings.Index(body, "event: end"), "all lines come before the end")

	code, raw = do(o, http.MethodGet, base+"/task-runs?taskId="+id+"&limit=1", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Len(t, decode(t, raw)["items"], 1)
	code, raw = do(o, http.MethodGet, base+"/task-runs", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Len(t, decode(t, raw)["items"], 2)
	code, raw = do(o, http.MethodGet, base+"/task-runs?taskId=not-a-uuid", nil)
	require.GreaterOrEqual(t, code, 400, string(raw))
	code, _ = do(o, http.MethodGet, "/task-runs/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = do(o, http.MethodGet, "/task-runs/"+uuid.NewString()+"/logs", nil)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = do(o, http.MethodGet, "/task-runs/"+uuid.NewString()+"/logs?follow=true", nil)
	require.Equal(t, http.StatusNotFound, code)

	// Schedule preview needs no service, and says why an expression is wrong.
	code, raw = do(o, http.MethodPost, "/task-schedules/preview", map[string]any{"schedule": "*/30 * * * *", "timezone": "Europe/Berlin", "count": 2})
	require.Equal(t, http.StatusOK, code, string(raw))
	pv := decode(t, raw)
	require.Equal(t, true, pv["valid"])
	require.Len(t, pv["nextRuns"], 2)
	code, raw = do(o, http.MethodPost, "/task-schedules/preview", map[string]any{"schedule": "nope"})
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, false, decode(t, raw)["valid"])
	code, raw = do(o, http.MethodPost, "/task-schedules/preview", map[string]any{"schedule": "@daily"})
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Len(t, decode(t, raw)["nextRuns"], 5, "five by default")

	code, _ = do(o, http.MethodDelete, "/tasks/"+id, nil)
	require.Equal(t, http.StatusNoContent, code)
	code, _ = do(o, http.MethodDelete, "/tasks/"+id, nil)
	require.Equal(t, http.StatusNotFound, code)

	// Signed out, and scopes: reading is not running.
	code, _ = do(nil, http.MethodGet, base+"/tasks", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	ro := *o
	ro.Scopes = []string{policy.ScopeProjectsRead}
	code, _ = do(&ro, http.MethodGet, base+"/tasks", nil)
	require.Equal(t, http.StatusOK, code)
	code, _ = do(&ro, http.MethodPost, base+"/run", map[string]any{"command": []string{"ls"}})
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(&ro, http.MethodPost, base+"/tasks", map[string]any{"name": "x", "schedule": "* * * * *", "command": []string{"ls"}})
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(&ro, http.MethodGet, "/task-runs/"+runID+"/logs", nil)
	require.Equal(t, http.StatusForbidden, code, "output needs logs:read")
}

func TestTickWorkerAndRegistration(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	require.Equal(t, "task_tick", TaskTickArgs{}.Kind())
	require.Equal(t, time.Minute, TickInterval)

	// The worker runs a tick: a due task starts.
	task := f.task(sv.ID, "* * * * *")
	f.clock = f.clock.Add(2 * time.Minute)
	w := &tickWorker{s: f.svc}
	require.NoError(t, w.Work(context.Background(), &river.Job[TaskTickArgs]{}))
	runs := f.runs(sv.ID)
	require.Len(t, runs, 1)
	require.Equal(t, SourceCron, runs[0].Source)
	require.Equal(t, task.ID, *runs[0].TaskID)

	// Registering adds the worker and the one-minute sweep to a job client.
	jm := jobs.New(f.pool, f.svc.log)
	RegisterJobs(jm, f.svc)
	_, err := jm.Client()
	require.NoError(t, err)
}

type failingDispatcher struct{ realDispatcher }

func (d *failingDispatcher) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.Nil, errors.New("queue is down")
}

func TestTickMovesPastATaskThatCannotStartInsteadOfRetryingEveryMinute(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	broken := f.task(sv.ID, "* * * * *", func(in *TaskInput) { in.Name = "broken" })
	healthy := f.task(sv.ID, "* * * * *", func(in *TaskInput) { in.Name = "second" })
	f.svc.disp = &failingDispatcher{}
	f.clock = f.clock.Add(2 * time.Minute)

	require.NoError(t, f.svc.Tick(context.Background()), "one broken task does not fail the tick")
	require.Empty(t, f.runs(sv.ID), "nothing was recorded for tasks whose start failed")
	for _, id := range []uuid.UUID{broken.ID, healthy.ID} {
		var next *time.Time
		var last *time.Time
		require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT next_run_at, last_run_at FROM service_tasks WHERE id=$1`, id).Scan(&next, &last))
		require.NotNil(t, next)
		require.True(t, next.After(f.clock), "the task moved on to its next minute")
		require.NotNil(t, last)
	}
}

func TestTickSkipsAStoppedServiceAndRecordsTheReasonOnce(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	f.task(sv.ID, "* * * * *")
	f.exec(`UPDATE services SET desired_state = 'stopped' WHERE id = $1`, sv.ID)

	for range 3 {
		f.clock = f.clock.Add(time.Minute)
		require.NoError(t, f.svc.Tick(context.Background()))
	}
	runs := f.runs(sv.ID)
	require.Len(t, runs, 1, "a streak of identical skips is one entry")
	require.Equal(t, "skipped", runs[0].Status)
	require.Equal(t, "the service is stopped", runs[0].Error)
	require.Empty(t, f.disp.cmds)

	// A deleted service advances the task and records nothing.
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, sv.ID)
	f.clock = f.clock.Add(time.Minute)
	require.NoError(t, f.svc.Tick(context.Background()))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_runs`))
}

func TestHandlesOnlyRunTaskCommands(t *testing.T) {
	require.True(t, Handles(agentprotocol.KindRunTask))
	require.False(t, Handles(agentprotocol.KindDeploy))
	require.False(t, Handles(""))
}

func TestSecretsForCommand(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	run, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"env"}, 0)
	require.NoError(t, err)
	cmd, _ := f.disp.last()
	_ = run
	ctx := context.Background()

	// Other kinds carry no secrets here.
	got, err := f.svc.SecretsForCommand(ctx, agentprotocol.KindDeploy, cmd.Payload)
	require.NoError(t, err)
	require.Nil(t, got)

	// The values of the release's variables are attached under env:KEY, never the build-only ones.
	var p agentprotocol.RunTaskPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	got, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, canary, got[agentprotocol.SecretEnvPrefix+"API_KEY"])
	require.Equal(t, "production", got[agentprotocol.SecretEnvPrefix+"MODE"])
	require.NotContains(t, got, agentprotocol.SecretEnvPrefix+"BUILD_ONLY")
	require.Equal(t, p.DeploymentID, got[agentprotocol.SecretEnvPrefix+services.VarDeploymentID])
	require.Equal(t, p.ReleaseID, got[agentprotocol.SecretEnvPrefix+services.VarReleaseID])
	require.NotContains(t, string(cmd.Payload), canary, "the payload itself never carries the values")
	require.NotContains(t, got, agentprotocol.SecretRegistryAuth)

	// Registry credentials of an image source ride along when the service has them.
	regAuth := []byte(`{"username":"u","password":"` + canaryPass + `"}`)
	enc, err := f.box.Seal(secretbox.ServicesRegistryAuth, sv.ID.String(), regAuth)
	require.NoError(t, err)
	f.exec(`UPDATE services SET registry_auth_enc = $2 WHERE id = $1`, sv.ID, enc)
	_, err = f.svc.RunCommand(f.ctx, sv.ID, []string{"env"}, 0)
	require.NoError(t, err)
	cmd, _ = f.disp.last()
	got, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, cmd.Payload)
	require.NoError(t, err)
	dec, err := base64.URLEncoding.DecodeString(got[agentprotocol.SecretRegistryAuth])
	require.NoError(t, err)
	require.Equal(t, regAuth, dec)

	// Broken payloads fail loudly instead of sending nothing.
	_, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, []byte(`nope`))
	require.ErrorContains(t, err, "decode run_task payload")
	_, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, []byte(`{"releaseId":"x"}`))
	require.ErrorContains(t, err, "release id")
	_, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, []byte(`{"releaseId":"`+uuid.NewString()+`"}`))
	require.ErrorContains(t, err, "get release")
	bad, _ := json.Marshal(agentprotocol.RunTaskPayload{ReleaseID: p.ReleaseID, ServiceID: "nope", HasRegistryAuth: true})
	_, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, bad)
	require.ErrorContains(t, err, "service id")
	f.exec(`UPDATE releases SET env_enc = $2 WHERE id = $1`, uuid.MustParse(p.ReleaseID), []byte("garbage-not-sealed"))
	_, err = f.svc.SecretsForCommand(ctx, agentprotocol.KindRunTask, cmd.Payload)
	require.ErrorContains(t, err, "decrypt release env")
}

func TestResultsAreInterpretedFromWhatTheAgentReports(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	start := func() (RunView, uuid.UUID) {
		v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"true"}, 0)
		require.NoError(t, err)
		_, id := f.disp.last()
		return v, id
	}
	statusOf := func(id uuid.UUID) RunView {
		v, err := f.svc.GetRun(f.ctx, id)
		require.NoError(t, err)
		return v
	}
	started, finished := f.clock, f.clock.Add(3*time.Second)

	// A failed run that never started has no exit code and the agent's reason.
	v, cmd := start()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "image pull failed"}))
	got := statusOf(v.ID)
	require.Equal(t, "failed", got.Status)
	require.Nil(t, got.ExitCode)
	require.Equal(t, "image pull failed", got.Error)

	// The message may come from the data; an empty one gets a default.
	v, cmd = start()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, f.result(cmd, agentprotocol.OutcomeFailed, "", agentprotocol.RunTaskResult{Message: "no such container"})))
	require.Equal(t, "no such container", statusOf(v.ID).Error)
	v, cmd = start()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}))
	require.Equal(t, "the agent reported a failed run", statusOf(v.ID).Error)

	// A container that ran keeps its exit code even when it failed.
	v, cmd = start()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, f.result(cmd, agentprotocol.OutcomeFailed, "exit status 3", agentprotocol.RunTaskResult{StartedAt: started, FinishedAt: finished, ExitCode: 3})))
	got = statusOf(v.ID)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, 3, *got.ExitCode)
	require.NotNil(t, got.StartedAt)
	require.NotNil(t, got.FinishedAt)

	// Timeouts are their own status; a success drops any message.
	v, cmd = start()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, f.result(cmd, agentprotocol.OutcomeFailed, "killed", agentprotocol.RunTaskResult{TimedOut: true, StartedAt: started, ExitCode: 137})))
	got = statusOf(v.ID)
	require.Equal(t, "timed_out", got.Status)
	require.Equal(t, 137, *got.ExitCode)
	v, cmd = start()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, f.result(cmd, agentprotocol.OutcomeSucceeded, "ignored note", agentprotocol.RunTaskResult{StartedAt: started, FinishedAt: finished})))
	got = statusOf(v.ID)
	require.Equal(t, "succeeded", got.Status)
	require.Equal(t, 0, *got.ExitCode)
	require.Empty(t, got.Error)

	// A repeat of the result is a no-op; a result for a command that is not a run is ignored.
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "late"}))
	require.Equal(t, "succeeded", statusOf(v.ID).Status)
	require.NoError(t, f.svc.HandleResult(context.Background(), uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}))
	require.NoError(t, f.svc.HandleUndelivered(context.Background(), uuid.New(), "expired"))

	// A command that never reached its agent fails the run, once.
	v, cmd = start()
	require.NoError(t, f.svc.HandleUndelivered(context.Background(), cmd, "  the server did not pick the command up  "))
	got = statusOf(v.ID)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "the server did not pick the command up", got.Error)
	require.NoError(t, f.svc.HandleUndelivered(context.Background(), cmd, "again"))
	require.Equal(t, "the server did not pick the command up", statusOf(v.ID).Error)
}

type brokenRedactor struct{}

func (brokenRedactor) RedactorFor(context.Context, uuid.UUID) (func(string) string, error) {
	return nil, errors.New("secrets unavailable")
}

func TestAgentMessagesThatCannotBeRedactedAreDropped(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"true"}, 0)
	require.NoError(t, err)
	_, cmd := f.disp.last()
	f.svc.redactor = brokenRedactor{}
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "boom " + canary}))
	got, err := f.svc.GetRun(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.NotContains(t, got.Error, canary, "fail closed")
	require.Empty(t, got.Error)

	// Without a redactor nothing is stored of the task's output.
	f.svc.redactor = nil
	second, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"true"}, 0)
	require.NoError(t, err)
	require.Error(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{TaskRunID: second.ID.String(), Lines: []agentprotocol.LogLine{{Text: "x"}}}))
	lines, err := f.svc.readLines(context.Background(), second.ID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, lines)
}

func TestFailuresOfUnattendedRunsAreNotified(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "* * * * *")
	f.clock = f.clock.Add(2 * time.Minute)
	require.NoError(t, f.svc.Tick(context.Background()))
	_, cmd := f.disp.last()
	started := f.clock
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, f.result(cmd, agentprotocol.OutcomeFailed, "exit status 2", agentprotocol.RunTaskResult{
		StartedAt: started, FinishedAt: started.Add(4 * time.Second), ExitCode: 2,
	})))
	require.Equal(t, []string{notifications.TypeTaskFailed}, f.notif.types())
	ev := f.notif.evs[0]
	require.Equal(t, `Task "nightly" failed on api`, ev.Title)
	require.Equal(t, "exit status 2", ev.Body)
	require.Equal(t, "2", ev.Fields["Exit code"])
	require.Equal(t, "4s", ev.Fields["Duration"])
	require.Equal(t, SourceCron, ev.Fields["Source"])
	require.Equal(t, notifications.SeverityError, ev.Severity)
	require.Equal(t, sv.ID, *ev.ServiceID)

	// A timeout says so; a run somebody started by hand notifies nobody.
	f.clock = f.clock.Add(2 * time.Minute)
	require.NoError(t, f.svc.Tick(context.Background()))
	_, cmd = f.disp.last()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, f.result(cmd, agentprotocol.OutcomeFailed, "", agentprotocol.RunTaskResult{TimedOut: true})))
	require.Len(t, f.notif.evs, 2)
	require.True(t, strings.HasPrefix(f.notif.evs[1].Body, "The task timed out and was stopped."))
	require.NotContains(t, f.notif.evs[1].Fields, "Exit code")

	_, err := f.svc.RunTaskNow(f.ctx, task.ID)
	require.NoError(t, err)
	_, cmd = f.disp.last()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "nope"}))
	require.Len(t, f.notif.evs, 2, "a manual run's failure is shown to the person who started it")

	// No notifier configured: failing runs are simply recorded.
	f.svc.notifier = nil
	f.clock = f.clock.Add(2 * time.Minute)
	require.NoError(t, f.svc.Tick(context.Background()))
	_, cmd = f.disp.last()
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "quiet"}))
}

func TestResultOfARunWhoseServiceWasDeletedIsStillStored(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"true"}, 0)
	require.NoError(t, err)
	_, cmd := f.disp.last()
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, sv.ID)
	require.NoError(t, f.svc.HandleResult(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "late"}))
	status, _, msg, err := f.svc.RunStatus(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", status)
	require.Equal(t, "late", msg)
	_, _, _, err = f.svc.RunStatus(context.Background(), uuid.New())
	requireAPIError(t, err, "not_found")
}

func TestRunHistoryAndLogsAreScopedAndPaged(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "0 4 * * *")
	r1, err := f.svc.RunTaskNow(f.ctx, task.ID)
	require.NoError(t, err)
	r2, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"ls"}, 0)
	require.NoError(t, err)

	all, err := f.svc.ListRuns(f.ctx, sv.ID, nil, 0)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, r2.ID, all[0].ID, "newest first")
	one, err := f.svc.ListRuns(f.ctx, sv.ID, &task.ID, 500)
	require.NoError(t, err)
	require.Len(t, one, 1)
	require.Equal(t, r1.ID, one[0].ID)
	limited, err := f.svc.ListRuns(f.ctx, sv.ID, nil, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	_, err = f.svc.ListRuns(f.ctx, uuid.New(), nil, 10)
	requireAPIError(t, err, "not_found")

	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{
		TaskRunID: r1.ID.String(), Lines: []agentprotocol.LogLine{{Text: "one"}, {Text: "two"}, {Text: "three"}},
	}))
	lines, err := f.svc.RunLogs(f.ctx, r1.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, lines, 3)
	rest, err := f.svc.RunLogs(f.ctx, r1.ID, lines[0].ID, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"two", "three"}, []string{rest[0].Line, rest[1].Line})
	capped, err := f.svc.RunLogs(f.ctx, r1.ID, 0, 2)
	require.NoError(t, err)
	require.Len(t, capped, 2)

	// Output from the wrong server, for an unknown run, or without lines is ignored.
	other := uuid.New()
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), other, agentprotocol.LogFrame{TaskRunID: r1.ID.String(), Lines: []agentprotocol.LogLine{{Text: "spoof"}}}))
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{TaskRunID: uuid.NewString(), Lines: []agentprotocol.LogLine{{Text: "x"}}}))
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{TaskRunID: "nope", Lines: []agentprotocol.LogLine{{Text: "x"}}}))
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{TaskRunID: r1.ID.String()}))
	lines, err = f.svc.RunLogs(f.ctx, r1.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, lines, 3)

	// Authorization: a run of another team is invisible; output needs logs:read.
	_, err = f.svc.GetRun(f.withScopes(policy.ScopeServersRead), r1.ID)
	requireAPIError(t, err, "forbidden")
	_, err = f.svc.RunLogs(f.withScopes(policy.ScopeProjectsRead), r1.ID, 0, 10)
	requireAPIError(t, err, "forbidden")
	got, err := f.svc.GetRun(f.withScopes(policy.ScopeProjectsRead), r1.ID)
	require.NoError(t, err)
	require.Equal(t, r1.ID, got.ID)
	ws2, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	foreign := *f.principal()
	foreign.WorkspaceID = ws2.ID
	_, err = f.svc.GetRun(auth.WithPrincipal(context.Background(), &foreign), r1.ID)
	requireAPIError(t, err, "not_found")
	_, err = f.svc.FollowLogs(auth.WithPrincipal(context.Background(), &foreign), r1.ID)
	requireAPIError(t, err, "not_found")
}

func TestRunCommandValidation(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	for name, c := range map[string]struct {
		cmd     []string
		timeout int
	}{
		"empty":            {nil, 0},
		"empty program":    {[]string{""}, 0},
		"timeout too long": {[]string{"ls"}, agentprotocol.MaxTaskTimeoutSeconds + 1},
		"negative timeout": {[]string{"ls"}, -5},
	} {
		_, err := f.svc.RunCommand(f.ctx, sv.ID, c.cmd, c.timeout)
		require.Error(t, err, name)
		ae, ok := apierr.As(err)
		require.True(t, ok, name)
		require.Equal(t, 422, ae.Status, name)
	}
	require.Empty(t, f.disp.cmds, "a refused command is never sent")

	// The default and the cap on timeouts.
	_, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"ls"}, 0)
	require.NoError(t, err)
	cmd, _ := f.disp.last()
	var p agentprotocol.RunTaskPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, agentprotocol.DefaultTaskTimeoutSeconds, p.TimeoutSeconds)
	require.Equal(t, agentprotocol.MaxTaskTimeoutSeconds, normalizeTimeout(agentprotocol.MaxTaskTimeoutSeconds+100))
	require.Equal(t, agentprotocol.DefaultTaskTimeoutSeconds, normalizeTimeout(-1))
	require.Equal(t, 90, normalizeTimeout(90))

	// A service that never deployed, or whose server is offline, cannot run anything.
	f.exec(`UPDATE servers SET status = 'offline' WHERE id = $1`, f.server.ID)
	_, err = f.svc.RunCommand(f.ctx, sv.ID, []string{"ls"}, 0)
	requireAPIError(t, err, "server_offline")
	_, err = f.svc.RunCommand(f.withScopes(policy.ScopeProjectsWrite), sv.ID, []string{"ls"}, 0)
	requireAPIError(t, err, "forbidden")
}

func TestRunOneOffForInternalCallersChecksTheServiceFirst(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	bg := context.Background()
	run := func(cmd []string, opts RunOpts, svcID uuid.UUID) (uuid.UUID, error) {
		var id uuid.UUID
		err := pgx.BeginFunc(bg, f.svc.pool, func(tx pgx.Tx) error {
			var err error
			id, err = f.svc.RunOneOff(f.ctx, tx, svcID, cmd, opts)
			return err
		})
		return id, err
	}
	_, err := run(nil, RunOpts{}, sv.ID)
	requireAPIError(t, err, "invalid_command")
	_, err = run([]string{"ls"}, RunOpts{}, uuid.New())
	requireAPIError(t, err, "not_found")
	id, err := run([]string{"seed"}, RunOpts{Source: SourceCron, ActorLabel: "preview", Timeout: 90 * time.Second}, sv.ID)
	require.NoError(t, err)
	v, err := f.svc.GetRun(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, SourceCron, v.Source)
	require.Equal(t, "preview", v.ActorLabel)
	require.Equal(t, 90, v.TimeoutSeconds)
	id, err = run([]string{"seed"}, RunOpts{}, sv.ID)
	require.NoError(t, err)
	v, _ = f.svc.GetRun(f.ctx, id)
	require.Equal(t, SourceOneOff, v.Source)
	require.Equal(t, "owner@example.com", v.ActorLabel, "the caller's own label when none is given")
	require.Equal(t, agentprotocol.DefaultTaskTimeoutSeconds, v.TimeoutSeconds)
}

func TestRunsInProductionNeedAnApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	sv := f.deployed()
	rest := f.restToken(policy.AllScopes)
	task := f.task(sv.ID, "0 4 * * *")

	// A one-off command: the approver sees the whole argv; an approval is bound to that command.
	_, err := f.svc.RunCommand(rest, sv.ID, []string{"rails", "console"}, 0)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, "Run a one-off command in api in production (Shop / production)", ar.Plan.Summary)
	require.Contains(t, strings.Join(ar.Plan.Effects, "\n"), "`rails console`")
	require.Empty(t, f.disp.cmds)
	f.approve(g, err)
	_, err = f.svc.RunCommand(rest, sv.ID, []string{"rails", "console"}, 0)
	require.NoError(t, err)
	_, err = f.svc.RunCommand(rest, sv.ID, []string{"rails", "console"}, 0)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "an approval is used once")
	_, err = f.svc.RunCommand(rest, sv.ID, []string{"rails", "runner", "drop"}, 0)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "another command is another call")

	// A saved task is guarded by its id, not its command.
	_, err = f.svc.RunTaskNow(rest, task.ID)
	ar, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, "Run the task nightly of api in production (Shop / production)", ar.Plan.Summary)
	f.approve(g, err)
	_, err = f.svc.RunTaskNow(rest, task.ID)
	require.NoError(t, err)

	// A person in the dashboard is not asked.
	_, err = f.svc.RunCommand(f.ctx, sv.ID, []string{"rails", "console"}, 0)
	require.NoError(t, err)
}

func TestChangingATaskCommandInProductionNeedsTheRunScope(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "0 4 * * *")
	writer := f.withScopes(policy.ScopeProjectsRead, policy.ScopeProjectsWrite)

	_, err := f.svc.CreateTask(writer, sv.ID, TaskInput{Name: "new", Schedule: "0 5 * * *", Command: []string{"id"}})
	requireAPIError(t, err, "forbidden")
	cmd := []string{"id"}
	_, err = f.svc.UpdateTask(writer, task.ID, TaskPatch{Command: &cmd})
	requireAPIError(t, err, "forbidden")
	name := "renamed"
	_, err = f.svc.UpdateTask(writer, task.ID, TaskPatch{Name: &name})
	require.NoError(t, err, "a change that keeps the command needs no run scope")
	deployer := f.withScopes(policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite)
	_, err = f.svc.UpdateTask(deployer, task.ID, TaskPatch{Command: &cmd})
	require.NoError(t, err)
}

func TestDescribeMoreSchedules(t *testing.T) {
	for expr, want := range map[string]string{
		"0 0 15 * *":   "On day 15 of every month at 00:00 UTC",
		"*/10 * 1 * *": "Every 10 minutes on day 1 of every month (UTC)",
		"0 9 * * 7":    "Every Sunday at 09:00 UTC",
		"0 9 * * 1-3":  "Every Monday to Wednesday at 09:00 UTC",
		"*/15 * * * 1": "Every 15 minutes on Monday (UTC)",
		"0 0 1 3 *":    "On March 1 at 00:00 UTC",
		"0 0 1 13 *":   "Custom schedule: 0 0 1 13 * (UTC)",
		"*/5 * 1 3 *":  "Every 5 minutes on March 1 (UTC)",
		"0 0 * * 8":    "Custom schedule: 0 0 * * 8 (UTC)",
		"0 0 * * */2":  "Custom schedule: 0 0 * * */2 (UTC)",
		"0 0 * * 1,x":  "Custom schedule: 0 0 * * 1,x (UTC)",
		"@hourly":      "Every hour, on the hour (UTC)",
		"@yearly":      "On January 1 at 00:00 UTC",
		"0 0 1,15 * *": "Custom schedule: 0 0 1,15 * * (UTC)",
		"1 2 3 4":      "Custom schedule: 1 2 3 4 (UTC)",
		"15 3 * * 1-5": "Every weekday (Monday to Friday) at 03:15 UTC",
		"* 3 * * *":    "Custom schedule: * 3 * * * (UTC)",
		"0 */1 * * *":  "Every hour at minute 0 (UTC)",
		"5 */2 * * *":  "Every 2 hours at minute 5 (UTC)",
	} {
		require.Equal(t, want, Describe(expr, ""), expr)
	}
}
