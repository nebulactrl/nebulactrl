package combine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func (f *fixture) serve() func(p *auth.Principal, method, path string, body any) (int, map[string]any) {
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
	return func(p *auth.Principal, method, path string, body any) (int, map[string]any) {
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
		var out map[string]any
		if rec.Body.Len() > 0 && rec.Body.Bytes()[0] == '{' {
			require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		}
		return rec.Code, out
	}
}

func TestCombineEndpointsFollowTheLifecycle(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o, _ := auth.PrincipalFrom(f.ctx)
	base := "/environments/" + f.env.ID.String() + "/services/combine"

	// Plan: 200 with what would happen; nothing changes.
	target := f.shop.ID
	planReq := CombinePlanRequest{ServiceIDs: f.ids(f.shop, f.worker, f.api), TargetServiceID: &target}
	code, body := do(o, http.MethodPost, base+"/plan", planReq)
	require.Equal(t, http.StatusOK, code, body)
	require.Empty(t, body["blocking"], "nothing prevents the start")
	require.NotEmpty(t, body["fingerprint"])
	require.Len(t, body["members"], 3)
	require.Zero(t, f.count("SELECT count(*) FROM service_combines"))
	code, body = do(o, http.MethodPost, base+"/plan", CombinePlanRequest{ServiceIDs: f.ids(f.shop)})
	require.Equal(t, http.StatusUnprocessableEntity, code, "a single service cannot be combined: %v", body)

	// Start: 202 with the staged combine; a stale plan is 409.
	req := f.request(f.plan())
	stale := req
	stale.Fingerprint = "0000"
	code, body = do(o, http.MethodPost, base, stale)
	require.Equal(t, http.StatusConflict, code, body)
	require.Equal(t, "plan_stale", body["code"])
	code, body = do(o, http.MethodPost, base, req)
	require.Equal(t, http.StatusAccepted, code, body)
	require.Equal(t, StateStaged, body["state"])
	id := body["id"].(string)

	code, body = do(o, http.MethodGet, "/combines/"+id, nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, StateStaged, body["state"])
	code, _ = do(o, http.MethodGet, "/combines/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, code)

	// A running deployment cannot be retried; abort takes a staged combine back.
	code, body = do(o, http.MethodPost, "/combines/"+id+"/retry", nil)
	require.Equal(t, http.StatusConflict, code, body)
	require.Equal(t, CodeInvalidState, body["code"])
	code, body = do(o, http.MethodPost, "/combines/"+id+"/abort", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, StateAborted, body["state"])

	// A failed combine can be retried (a fresh team: the aborted one above left its staging as it was).
	f = newFixture(t)
	do = f.serve()
	o, _ = auth.PrincipalFrom(f.ctx)
	v := f.started()
	f.advance(v.ID)
	f.setStatus(*f.combine(v.ID).DeploymentID, "failed", "worker never became healthy")
	f.advance(v.ID)
	code, body = do(o, http.MethodGet, "/combines/"+v.ID.String(), nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, StateFailed, body["state"])
	require.Contains(t, body["error"], "worker never became healthy")
	code, body = do(o, http.MethodPost, "/combines/"+v.ID.String()+"/retry", nil)
	require.Equal(t, http.StatusAccepted, code, body)
	require.Equal(t, StateStaged, body["state"])

	// Scopes.
	ro := *o
	ro.Scopes = []string{policy.ScopeProjectsRead}
	code, _ = do(&ro, http.MethodGet, "/combines/"+v.ID.String(), nil)
	require.Equal(t, http.StatusOK, code)
	code, _ = do(&ro, http.MethodPost, base+"/plan", planReq)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(nil, http.MethodGet, "/combines/"+v.ID.String(), nil)
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestWorkerSnoozesWhileTheDeploymentRunsAndFinishesTheCombine(t *testing.T) {
	f := newFixture(t)
	v := f.started()
	w := &worker{s: f.svc}
	job := func(attempt, max int) *river.Job[Args] {
		return &river.Job[Args]{JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: max}, Args: Args{CombineID: v.ID}}
	}

	// Deploying: the job asks to be run again later instead of spinning.
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, w.Work(context.Background(), job(1, 5)), &snooze)
	require.Positive(t, snooze.Duration)
	require.Equal(t, StateDeploying, f.combine(v.ID).State)
	f.succeed(v.ID)
	require.NoError(t, w.Work(context.Background(), job(1, 5)))
	require.Equal(t, StateDone, f.combine(v.ID).State, "the job carried the combine through the cutover")

	// A combine that no longer exists ends its job quietly.
	gone := &river.Job[Args]{JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5}, Args: Args{CombineID: uuid.New()}}
	require.NoError(t, w.Work(context.Background(), gone))
}

func TestWorkerRetriesErrorsAndRecordsTheFailureOnTheLastAttempt(t *testing.T) {
	f := newFixture(t)
	v := f.started()
	w := &worker{s: f.svc}
	f.dep.err = errors.New("database is down")
	job := func(attempt int) *river.Job[Args] {
		return &river.Job[Args]{JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: 3}, Args: Args{CombineID: v.ID}}
	}

	err := w.Work(context.Background(), job(1))
	require.ErrorContains(t, err, "database is down", "River retries a step that failed")
	require.Equal(t, StateStaged, f.combine(v.ID).State)
	require.NoError(t, w.Work(context.Background(), job(3)), "the last attempt does not return the error again")
	c := f.combine(v.ID)
	require.Equal(t, StateFailed, c.State, "nothing stays half-done unseen")
	require.Contains(t, c.Error, "gave up")
	require.Contains(t, c.Error, "database is down")
	require.NotNil(t, c.FinishedAt)

	// A finished combine is not touched by a late giving up.
	f.svc.giveUp(context.Background(), v.ID, errors.New("again"))
	require.NotContains(t, f.combine(v.ID).Error, "again")
	f.svc.giveUp(context.Background(), uuid.New(), errors.New("nobody"))
}

func TestClipAndActor(t *testing.T) {
	require.Equal(t, "abc", clip("abcdef", 3))
	require.Equal(t, "ab", clip("ab", 3))
	require.Equal(t, "", clip("", 3))
}

func TestRetryOfAProductionCombineIsGuardedForAgents(t *testing.T) {
	f := newFixture(t)
	for _, s := range []*db.Service{&f.shop, &f.worker, &f.api} {
		_, err := f.pool.Exec(context.Background(), "UPDATE services SET environment_id = $2 WHERE id = $1", s.ID, f.prod.ID)
		require.NoError(t, err)
	}
	_, err := f.pool.Exec(context.Background(), "UPDATE domains SET hostname = hostname || '.prod'")
	require.NoError(t, err)
	p, err := f.svc.Plan(f.ctx, f.prod.ID, CombinePlanRequest{ServiceIDs: f.ids(f.shop, f.worker, f.api), TargetServiceID: &f.shop.ID})
	require.NoError(t, err)
	v, err := f.svc.Start(f.ctx, f.prod.ID, f.request(p))
	require.NoError(t, err)
	f.advance(v.ID)
	f.setStatus(*f.combine(v.ID).DeploymentID, "failed", "boom")
	f.advance(v.ID)
	require.Equal(t, StateFailed, f.combine(v.ID).State)

	// An agent needs a person's approval (none is installed: the guard fails closed) and nothing is retried.
	mcp := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeMCP, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "agent", Scopes: policy.AllScopes,
	})
	_, err = f.svc.Retry(mcp, v.ID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Code, "approval", "%v", err)
	require.Equal(t, StateFailed, f.combine(v.ID).State)

	// Not a writer, not a combine of the team, not failed.
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleViewer, Label: "v", Scopes: []string{policy.ScopeProjectsRead},
	})
	_, err = f.svc.Retry(reader, v.ID)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Abort(reader, v.ID)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Retry(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")

	// A person is not asked.
	rv, err := f.svc.Retry(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StateStaged, rv.State)
}
