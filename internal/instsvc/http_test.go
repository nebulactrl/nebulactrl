package instsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// caller is who a request is made as; nil principal = no sign-in.
type caller struct {
	p *auth.Principal
}

func (f *fixture) owner(tm team) caller {
	return caller{&auth.Principal{Type: auth.TypeUser, UserID: tm.User, WorkspaceID: tm.ID, Label: tm.Name, Scopes: policy.AllScopes}}
}

func (f *fixture) token(tm team, scopes ...string) caller {
	id := uuid.New()
	return caller{&auth.Principal{Type: auth.TypeToken, TokenID: &id, UserID: tm.User, WorkspaceID: tm.ID, Label: "tok", Scopes: scopes}}
}

func (f *fixture) do(c caller, method, path string, body any) (int, []byte) {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if c.p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), c.p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	Register(humachi.New(r, cfg), f.svc)
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

func TestAdminRoutesAreForTheInstanceAdministratorOnly(t *testing.T) {
	f := newFixture(t)
	f.provision()
	sliceID := f.slice(f.b, KindS3).ID
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/admin/instance-services", nil},
		{http.MethodPut, "/admin/instance-services", map[string]any{"defaultQuotaBytes": 100 << 30}},
		{http.MethodPost, "/admin/instance-services/check", nil},
		{http.MethodPut, "/admin/team-slices/" + sliceID.String() + "/quota", map[string]any{"quotaBytes": 5 << 30}},
		{http.MethodDelete, "/admin/team-slices/" + f.b.ID.String() + "?confirm=Beta", nil},
	}
	nonAdminOwner := f.owner(f.b) // owner of team b, not the instance administrator
	for _, rt := range routes {
		code, body := f.do(nonAdminOwner, rt.method, rt.path, rt.body)
		require.Equal(t, http.StatusForbidden, code, "%s %s: %s", rt.method, rt.path, body)
		require.Contains(t, string(body), "instance_admin_required")

		// A token of the administrator without the admin scope does not pass either.
		code, _ = f.do(f.token(f.a, policy.ScopeProjectsRead, policy.ScopeServersRead), rt.method, rt.path, rt.body)
		require.Equal(t, http.StatusForbidden, code, "%s %s with a read-only token", rt.method, rt.path)

		code, _ = f.do(caller{}, rt.method, rt.path, rt.body)
		require.Equal(t, http.StatusUnauthorized, code, "%s %s without sign-in", rt.method, rt.path)
	}
	// None of it changed anything.
	s3svc, err := f.q.GetInstanceService(context.Background(), KindS3)
	require.NoError(t, err)
	require.Equal(t, DefaultQuotaBytes, defaultQuotaOf(s3svc.Options))
	require.Nil(t, f.slice(f.b, KindS3).QuotaBytes)
	require.Equal(t, 3, f.sliceRows(f.b))

	// The administrator can.
	admin := f.owner(f.a)
	code, body := f.do(admin, http.MethodGet, "/admin/instance-services", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	code, _ = f.do(f.token(f.a, policy.ScopeAdmin), http.MethodGet, "/admin/instance-services", nil)
	require.Equal(t, http.StatusOK, code, "the administrator's admin-scope token passes")
}

func TestInstanceServicesViewAndDefaultQuota(t *testing.T) {
	f := newFixture(t)
	f.provision()
	f.setQuota(f.b, 7<<30)
	f.setUsed(f.a, 1234)
	admin := f.owner(f.a)

	code, body := f.do(admin, http.MethodGet, "/admin/instance-services", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var v InstanceServicesView
	require.NoError(t, json.Unmarshal(body, &v))
	require.EqualValues(t, DefaultQuotaBytes, v.DefaultQuotaBytes)
	require.Len(t, v.Services, 3)
	for _, s := range v.Services {
		require.Equal(t, StateReady, s.Status, s.Kind)
		require.Equal(t, 2, s.Teams)
	}
	require.EqualValues(t, 1234, v.Services[0].UsedBytes)
	require.Len(t, v.Slices, 6)
	byTeam := map[string]SliceView{}
	for _, s := range v.Slices {
		if s.Kind == KindS3 {
			byTeam[s.TeamName] = s
		}
	}
	require.EqualValues(t, DefaultQuotaBytes, byTeam["Alpha"].EffectiveQuotaBytes)
	require.Nil(t, byTeam["Alpha"].QuotaBytes)
	require.EqualValues(t, 7<<30, byTeam["Beta"].EffectiveQuotaBytes)
	require.EqualValues(t, 7<<30, *byTeam["Beta"].QuotaBytes)
	for _, secretish := range []string{f.slice(f.a, KindS3).AccessKey, f.slice(f.a, KindS3).Ref, f.slice(f.b, KindS3).AccessKey} {
		require.NotContains(t, string(body), secretish, "the admin view carries no key or bucket")
	}

	// The default must be at least 1 GiB.
	code, _ = f.do(admin, http.MethodPut, "/admin/instance-services", map[string]any{"defaultQuotaBytes": 1 << 20})
	require.Equal(t, http.StatusUnprocessableEntity, code)

	// A new default applies to teams without their own quota, natively too.
	code, body = f.do(admin, http.MethodPut, "/admin/instance-services", map[string]any{"defaultQuotaBytes": 100 << 30})
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &v))
	require.EqualValues(t, 100<<30, v.DefaultQuotaBytes)
	calls := f.s3.calls(f.slice(f.a, KindS3).Ref)
	require.EqualValues(t, 100<<30, calls[len(calls)-1].Quota)
	callsB := f.s3.calls(f.slice(f.b, KindS3).Ref)
	require.EqualValues(t, DefaultQuotaBytes, callsB[len(callsB)-1].Quota, "team b has its own quota: the default does not touch it")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionDefaultQuota))
	slc, err := f.svc.ForTeam(context.Background(), f.a.ID)
	require.NoError(t, err)
	require.EqualValues(t, 100<<30, slc.S3.QuotaBytes)
}

func TestSliceQuotaRoute(t *testing.T) {
	f := newFixture(t)
	f.provision()
	admin := f.owner(f.a)
	s3 := f.slice(f.b, KindS3)
	path := "/admin/team-slices/" + s3.ID.String() + "/quota"

	code, body := f.do(admin, http.MethodPut, path, map[string]any{"quotaBytes": 5 << 30})
	require.Equal(t, http.StatusOK, code, string(body))
	var v SliceView
	require.NoError(t, json.Unmarshal(body, &v))
	require.EqualValues(t, 5<<30, v.EffectiveQuotaBytes)
	require.EqualValues(t, 5<<30, *v.QuotaBytes)
	calls := f.s3.calls(s3.Ref)
	require.EqualValues(t, 5<<30, calls[len(calls)-1].Quota, "the native quota follows")

	// Lowering the quota under the use only blocks new writes.
	f.setUsed(f.b, 6<<30)
	code, body = f.do(admin, http.MethodPut, path, map[string]any{"quotaBytes": 2 << 30})
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &v))
	require.Equal(t, StateFull, v.State)
	require.EqualValues(t, 6<<30, v.UsedBytes, "nothing was deleted")
	_, err := f.svc.Admit(context.Background(), f.b.ID, 1)
	require.ErrorIs(t, err, ErrQuotaExceeded)

	// Raising it makes the slice ready again; null resets to the default.
	code, body = f.do(admin, http.MethodPut, path, map[string]any{"quotaBytes": 10 << 30})
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &v))
	require.Equal(t, StateReady, v.State)
	code, body = f.do(admin, http.MethodPut, path, map[string]any{"quotaBytes": nil})
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &v))
	require.Nil(t, v.QuotaBytes)
	require.EqualValues(t, DefaultQuotaBytes, v.EffectiveQuotaBytes)
	require.Equal(t, 4, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionSliceQuota), "each change is audited")

	// Too small, not an S3 slice, unknown.
	code, _ = f.do(admin, http.MethodPut, path, map[string]any{"quotaBytes": 1024})
	require.Equal(t, http.StatusUnprocessableEntity, code)
	code, body = f.do(admin, http.MethodPut, "/admin/team-slices/"+f.slice(f.b, KindRegistry).ID.String()+"/quota", map[string]any{"quotaBytes": 5 << 30})
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Contains(t, string(body), "quota_not_supported")
	code, _ = f.do(admin, http.MethodPut, "/admin/team-slices/"+uuid.NewString()+"/quota", map[string]any{"quotaBytes": 5 << 30})
	require.Equal(t, http.StatusNotFound, code)
}

func TestCheckRunsEnsureNow(t *testing.T) {
	f := newFixture(t)
	code, body := f.do(f.owner(f.a), http.MethodPost, "/admin/instance-services/check", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	require.Equal(t, 6, f.count(`SELECT count(*) FROM team_service_slices`), "three kinds for each of the two teams")
	var v InstanceServicesView
	require.NoError(t, json.Unmarshal(body, &v))
	for _, s := range v.Services {
		require.Equal(t, StateReady, s.Status)
	}
}

func TestPurgeRouteRefusesAnExistingTeam(t *testing.T) {
	f := newFixture(t)
	f.provision()
	code, body := f.do(f.owner(f.a), http.MethodDelete, "/admin/team-slices/"+f.b.ID.String()+"?confirm=Beta", nil)
	require.Equal(t, http.StatusConflict, code, string(body))
	require.Contains(t, string(body), "team_still_exists")
	require.Equal(t, 3, f.sliceRows(f.b))
}

func TestTeamSliceShowsSpaceNeverPlumbing(t *testing.T) {
	f := newFixture(t)
	f.provision()
	f.setQuota(f.a, 10<<30)
	f.setUsed(f.a, 4<<30)
	f.setUsed(f.b, 1<<30)
	// A live image of team a's registry.
	reg, err := f.q.GetBuiltinRegistry(context.Background(), f.a.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `
		INSERT INTO registry_images (registry_id, repository, digest, platform, build_hash, size_bytes, state)
		VALUES ($1, 'x/app', 'sha256:aaa', 'linux/amd64', 'h', 12345, 'live')`, reg.ID)
	require.NoError(t, err)

	code, body := f.do(f.owner(f.a), http.MethodGet, "/team-slice", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var v TeamSliceView
	require.NoError(t, json.Unmarshal(body, &v))
	require.EqualValues(t, 4<<30, v.Backup.UsedBytes)
	require.EqualValues(t, 10<<30, v.Backup.QuotaBytes)
	require.Equal(t, 40, v.Backup.Percent)
	require.Equal(t, StateReady, v.Backup.State)
	require.EqualValues(t, 12345, v.Registry.UsedBytes)
	require.Equal(t, 7, v.Logs.RetentionDays)

	// Never a key, bucket, prefix, tenant, endpoint or secret, whoever asks and
	// whatever the team's own state.
	sa, sb := f.slice(f.a, KindS3), f.slice(f.b, KindS3)
	for _, c := range []caller{f.owner(f.a), f.token(f.a, policy.ScopeProjectsRead)} {
		code, body = f.do(c, http.MethodGet, "/team-slice", nil)
		require.Equal(t, http.StatusOK, code, string(body))
		text := string(body)
		for _, banned := range []string{sa.AccessKey, sa.Ref, sb.AccessKey, sb.Ref, f.s3.secrets[sa.Ref], f.a.ID.String(), "127.0.0.1", "http://", "accessKey", "secret", "bucket", "endpoint", "ref"} {
			require.NotContains(t, strings.ToLower(text), strings.ToLower(banned), "the team's slice view leaked %q", banned)
		}
	}
	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.ElementsMatch(t, []string{"backup", "registry", "logs"}, keysOf(raw, "$schema"))
	require.ElementsMatch(t, []string{"usedBytes", "quotaBytes", "percent", "state"}, keysOf(raw["backup"].(map[string]any)))

	// Team b sees its own numbers, never a's.
	code, body = f.do(f.owner(f.b), http.MethodGet, "/team-slice", nil)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(body, &v))
	require.EqualValues(t, 1<<30, v.Backup.UsedBytes)
	require.EqualValues(t, 0, v.Registry.UsedBytes)

	// It needs the read scope, and shows a full slice as full.
	code, _ = f.do(f.token(f.a, policy.ScopeServersRead), http.MethodGet, "/team-slice", nil)
	require.Equal(t, http.StatusForbidden, code)
	f.setUsed(f.a, 10<<30)
	_, err = f.svc.Admit(context.Background(), f.a.ID, 1)
	require.ErrorIs(t, err, ErrQuotaExceeded)
	code, body = f.do(f.owner(f.a), http.MethodGet, "/team-slice", nil)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(body, &v))
	require.Equal(t, StateFull, v.Backup.State)
	require.Equal(t, 100, v.Backup.Percent)
}

func TestTeamSliceWithoutBuiltinServices(t *testing.T) {
	f := newFixture(t)
	code, body := f.do(f.owner(f.a), http.MethodGet, "/team-slice", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var v TeamSliceView
	require.NoError(t, json.Unmarshal(body, &v))
	require.Equal(t, "unavailable", v.Backup.State)
	require.EqualValues(t, 0, v.Backup.QuotaBytes)
}

func keysOf(m map[string]any, skip ...string) []string {
	var out []string
	for k := range m {
		if len(skip) > 0 && k == skip[0] {
			continue
		}
		out = append(out, k)
	}
	return out
}
