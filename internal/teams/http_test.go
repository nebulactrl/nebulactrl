package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// hx serves the team routes the way the control plane does (a session
// manager around the router); who the request is made as is set per call.
type hx struct {
	*tf
	sm      *scs.SessionManager
	handler http.Handler
	as      *auth.Principal
	cookies []*http.Cookie
}

func (f *tf) serve() *hx {
	f.t.Helper()
	h := &hx{tf: f, sm: scs.New()}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if h.as != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), h.as))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc, h.sm)
	h.handler = h.sm.LoadAndSave(r)
	return h
}

// asUser makes the next requests as a signed-in user acting in team.
func (h *hx) asUser(user, team uuid.UUID) {
	p, _ := auth.PrincipalFrom(h.tf.as(user, team))
	h.as = p
}

func (h *hx) asToken(user, team uuid.UUID, role string, scopes ...string) {
	p, _ := auth.PrincipalFrom(h.tf.token(user, team, role, scopes...))
	h.as = p
}

func (h *hx) do(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var rd io.Reader = bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(h.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range h.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if cs := rec.Result().Cookies(); len(cs) > 0 {
		h.cookies = cs
	}
	return rec.Code, rec.Body.Bytes()
}

func (h *hx) call(want int, method, path string, body any) map[string]any {
	h.t.Helper()
	code, raw := h.do(method, path, body)
	require.Equal(h.t, want, code, "%s %s: %s", method, path, raw)
	var m map[string]any
	if len(raw) > 0 && raw[0] == '{' {
		require.NoError(h.t, json.Unmarshal(raw, &m), string(raw))
	}
	return m
}

func (h *hx) failure(want int, code, method, path string, body any) {
	h.t.Helper()
	m := h.call(want, method, path, body)
	require.Equal(h.t, code, m["code"], "%s %s: %v", method, path, m)
}

func items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, i := range m["items"].([]any) {
		out = append(out, i.(map[string]any))
	}
	return out
}

func TestTeamEndpointsLifecycle(t *testing.T) {
	f := newTf(t)
	h := f.serve()
	h.asUser(f.ada.ID, f.team)

	list := h.call(http.StatusOK, http.MethodGet, "/teams", nil)
	require.Len(t, items(t, list), 1)
	require.Equal(t, "Home", items(t, list)[0]["name"])
	require.Equal(t, true, items(t, list)[0]["current"])

	created := h.call(http.StatusCreated, http.MethodPost, "/teams", map[string]any{"name": "Platform"})
	id := created["id"].(string)
	require.Equal(t, "platform", created["slug"])
	require.Equal(t, "owner", created["role"])
	require.Equal(t, false, created["current"])
	h.failure(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/teams", map[string]any{"name": " "})

	got := h.call(http.StatusOK, http.MethodGet, "/teams/"+id, nil)
	require.Equal(t, "Platform", got["name"])
	h.failure(http.StatusNotFound, "not_found", http.MethodGet, "/teams/"+uuid.NewString(), nil)

	renamed := h.call(http.StatusOK, http.MethodPatch, "/teams/"+id, map[string]any{"name": "Platform Team"})
	require.Equal(t, "Platform Team", renamed["name"])
	require.Equal(t, "platform", renamed["slug"])

	// Switching answers with the team as current and starts carrying it in the session.
	sw := h.call(http.StatusOK, http.MethodPost, "/teams/"+id+"/switch", nil)
	require.Equal(t, true, sw["current"])
	require.NotEmpty(t, h.cookies, "the session now holds the team")
	h.failure(http.StatusNotFound, "not_found", http.MethodPost, "/teams/"+uuid.NewString()+"/switch", nil)
	h.asToken(f.ada.ID, f.team, auth.RoleOwner, policy.ScopeAdmin)
	h.failure(http.StatusForbidden, "forbidden", http.MethodPost, "/teams/"+id+"/switch", nil)

	// Signed out: every team route is a 401.
	h.as = nil
	h.failure(http.StatusUnauthorized, "unauthorized", http.MethodGet, "/teams", nil)
	h.failure(http.StatusUnauthorized, "unauthorized", http.MethodPost, "/teams", map[string]any{"name": "X"})
}

func TestTeamDeletionEndpoints(t *testing.T) {
	f := newTf(t)
	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	RegisterJobs(jm, f.svc)
	f.svc.jobs = jm
	h := f.serve()
	h.asUser(f.ada.ID, f.team)
	doomed := h.call(http.StatusCreated, http.MethodPost, "/teams", map[string]any{"name": "Doomed"})
	id, slug := doomed["id"].(string), doomed["slug"].(string)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	f.join(uuid.MustParse(id), bob, auth.RoleAdmin)

	// The plan counts what is in the team and deletes nothing.
	plan := h.call(http.StatusOK, http.MethodGet, "/teams/"+id+"/deletion-plan", nil)
	require.Equal(t, slug, plan["team"].(map[string]any)["slug"])
	require.Equal(t, float64(0), plan["projects"])
	require.Equal(t, false, plan["hostsControlPlane"])
	require.Equal(t, true, plan["backupDestinationsKept"])

	// Owners only: an admin is refused, a stranger sees nothing, a token is refused.
	h.asUser(bob, uuid.MustParse(id))
	h.failure(http.StatusForbidden, "forbidden", http.MethodGet, "/teams/"+id+"/deletion-plan", nil)
	h.failure(http.StatusForbidden, "forbidden", http.MethodDelete, "/teams/"+id+"?confirm="+slug, nil)
	h.asUser(f.user("eve@example.com", ""), f.team)
	h.failure(http.StatusNotFound, "not_found", http.MethodGet, "/teams/"+id+"/deletion-plan", nil)
	h.asToken(f.ada.ID, uuid.MustParse(id), auth.RoleOwner, policy.ScopeAdmin)
	h.failure(http.StatusForbidden, "forbidden", http.MethodDelete, "/teams/"+id+"?confirm="+slug, nil)
	h.asUser(f.ada.ID, f.team)

	// The slug must be typed; a team that hosts the control plane cannot go.
	h.failure(http.StatusUnprocessableEntity, "confirmation_mismatch", http.MethodDelete, "/teams/"+id+"?confirm=nope", nil)
	srv, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: uuid.MustParse(id), Name: "cp"})
	require.NoError(t, err)
	f.exec(`UPDATE servers SET is_control_plane=true WHERE id=$1`, srv.ID)
	require.Equal(t, true, h.call(http.StatusOK, http.MethodGet, "/teams/"+id+"/deletion-plan", nil)["hostsControlPlane"])
	h.failure(http.StatusConflict, "team_hosts_control_plane", http.MethodDelete, "/teams/"+id+"?confirm="+slug, nil)
	f.exec(`UPDATE servers SET is_control_plane=false WHERE id=$1`, srv.ID)
	require.Zero(t, f.audits("team.delete.start"))

	// Deleting answers 202 with the state, queues the job and records it; a second request conflicts.
	deleted := h.call(http.StatusAccepted, http.MethodDelete, "/teams/"+id+"?confirm="+slug+"&keepDnsRecords=true", nil)
	del := deleted["deletion"].(map[string]any)
	require.Equal(t, DeletionDeleting, del["state"])
	require.Equal(t, StepProjects, del["step"])
	require.Equal(t, true, del["keepDnsRecords"])
	require.Equal(t, 1, f.count(`SELECT count(*) FROM river_job WHERE kind='delete_team' AND args->>'teamId'=$1`, id))
	require.Equal(t, 1, f.audits("team.delete.start"))
	h.failure(http.StatusConflict, "deletion_in_progress", http.MethodDelete, "/teams/"+id+"?confirm="+slug, nil)
	listed := h.call(http.StatusOK, http.MethodGet, "/teams", nil)
	var seen bool
	for _, tm := range items(t, listed) {
		if tm["id"] == id {
			seen = true
			require.Equal(t, DeletionDeleting, tm["deletion"].(map[string]any)["state"])
		}
	}
	require.True(t, seen)
}

func TestDeleteTeamWithoutAJobManagerFails(t *testing.T) {
	f := newTf(t)
	team, err := f.svc.Create(f.asAda(), "Doomed")
	require.NoError(t, err)
	_, err = f.svc.Delete(f.asAda(), team.ID, DeleteInput{Confirm: team.Slug})
	require.ErrorContains(t, err, "no job manager")
	// The failed start left no mark on the team.
	got, err := f.svc.Get(f.asAda(), team.ID)
	require.NoError(t, err)
	require.Nil(t, got.Deletion)
}

func TestMemberEndpoints(t *testing.T) {
	f := newTf(t)
	p1 := f.project("One")
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	h := f.serve()
	h.asUser(bob, f.team)
	base := "/teams/" + f.team.String() + "/members/"

	members := items(t, h.call(http.StatusOK, http.MethodGet, "/teams/"+f.team.String()+"/members", nil))
	require.Len(t, members, 3)

	// Restrict, then lift with allProjects, then change the role.
	m := h.call(http.StatusOK, http.MethodPatch, base+cy.String(), map[string]any{"projectIds": []string{p1.String()}})
	require.Equal(t, []any{p1.String()}, m["projectIds"])
	m = h.call(http.StatusOK, http.MethodPatch, base+cy.String(), map[string]any{"allProjects": true})
	require.Nil(t, m["projectIds"])
	m = h.call(http.StatusOK, http.MethodPatch, base+cy.String(), map[string]any{"role": "viewer"})
	require.Equal(t, "viewer", m["role"])
	m = h.call(http.StatusOK, http.MethodPatch, base+cy.String(), map[string]any{"projectIds": []string{}})
	require.Nil(t, m["projectIds"], "an empty list is omitted from the body")
	got, err := f.q.GetMember(context.Background(), db.GetMemberParams{WorkspaceID: f.team, UserID: cy})
	require.NoError(t, err)
	require.NotNil(t, got.ProjectIds)
	require.Empty(t, got.ProjectIds)
	h.failure(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, base+cy.String(), map[string]any{"role": "root"})
	h.failure(http.StatusForbidden, "forbidden", http.MethodPatch, base+f.ada.ID.String(), map[string]any{"role": "viewer"})
	h.failure(http.StatusNotFound, "not_found", http.MethodPatch, base+uuid.NewString(), map[string]any{"role": "viewer"})

	// Password reset link.
	link := h.call(http.StatusCreated, http.MethodPost, base+cy.String()+"/password-reset", nil)
	require.True(t, strings.HasPrefix(link["token"].(string), auth.ResetTokenPrefix))
	require.Contains(t, link["link"], link["token"])
	h.failure(http.StatusUnprocessableEntity, "own_password", http.MethodPost, base+bob.String()+"/password-reset", nil)

	// Remove and leave answer 204.
	code, raw := h.do(http.MethodDelete, base+cy.String(), nil)
	require.Equal(t, http.StatusNoContent, code, string(raw))
	require.False(t, f.isMember(cy, f.team))
	h.failure(http.StatusNotFound, "not_found", http.MethodDelete, base+cy.String(), nil)
	h.failure(http.StatusForbidden, "forbidden", http.MethodDelete, base+f.ada.ID.String(), nil)
	h.asUser(f.ada.ID, f.team)
	h.failure(http.StatusConflict, "last_owner", http.MethodDelete, base+f.ada.ID.String(), nil)
	h.asUser(bob, f.team)
	code, _ = h.do(http.MethodDelete, base+bob.String(), nil)
	require.Equal(t, http.StatusNoContent, code)
	require.False(t, f.isMember(bob, f.team))
	// No longer a member: the team is gone from their point of view.
	h.failure(http.StatusNotFound, "not_found", http.MethodGet, "/teams/"+f.team.String()+"/members", nil)
}

func TestInviteEndpointsWithSignedInAcceptance(t *testing.T) {
	f := newTf(t)
	f.withPublicURL("https://nebula.example.com")
	h := f.serve()
	h.asUser(f.ada.ID, f.team)
	base := "/teams/" + f.team.String() + "/invites"

	created := h.call(http.StatusCreated, http.MethodPost, base, map[string]any{"role": "developer", "maxUses": 2, "expiresInDays": 3})
	token := created["token"].(string)
	inv := created["invite"].(map[string]any)
	require.Equal(t, "https://nebula.example.com/invite/"+token, created["link"])
	require.Equal(t, "link_only", created["delivery"])
	require.Equal(t, float64(2), inv["maxUses"])
	expires, err := time.Parse(time.RFC3339Nano, inv["expiresAt"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, f.now.Add(72*time.Hour), expires, time.Minute)

	mailed := h.call(http.StatusCreated, http.MethodPost, base, map[string]any{"role": "viewer", "email": "new@example.com"})
	require.Equal(t, "sent", mailed["delivery"])
	require.Equal(t, "new@example.com", mailed["invite"].(map[string]any)["email"])
	h.failure(http.StatusUnprocessableEntity, "invalid_email", http.MethodPost, base, map[string]any{"role": "viewer", "email": "nope"})
	h.failure(http.StatusConflict, "already_member", http.MethodPost, base, map[string]any{"role": "viewer", "email": "ada@example.com"})

	list := items(t, h.call(http.StatusOK, http.MethodGet, base, nil))
	require.Len(t, list, 2)

	// The public preview needs no sign-in.
	h.as = nil
	pv := h.call(http.StatusOK, http.MethodGet, "/invites/"+token, nil)
	require.Equal(t, "Home", pv["teamName"])
	require.Equal(t, "developer", pv["role"])
	require.Equal(t, "Ada", pv["invitedBy"])
	h.failure(http.StatusNotFound, "invite_invalid", http.MethodGet, "/invites/nci_unknown", nil)

	// A signed-in user accepts: they join and the team becomes the session's.
	eve := f.user("eve@example.com", "")
	h.asUser(eve, uuid.Nil)
	acc := h.call(http.StatusOK, http.MethodPost, "/invites/"+token+"/accept", nil)
	require.Equal(t, f.team.String(), acc["team"].(map[string]any)["id"])
	require.Equal(t, false, acc["createdAccount"])
	require.True(t, f.isMember(eve, f.team))
	h.failure(http.StatusConflict, "already_member", http.MethodPost, "/invites/"+token+"/accept", nil)

	// An admin revokes the other invite.
	h.asUser(f.ada.ID, f.team)
	code, raw := h.do(http.MethodDelete, base+"/"+list[1]["id"].(string), nil)
	require.Equal(t, http.StatusNoContent, code, string(raw))
	h.failure(http.StatusNotFound, "not_found", http.MethodDelete, base+"/"+uuid.NewString(), nil)
	require.Len(t, items(t, h.call(http.StatusOK, http.MethodGet, base, nil)), 1)
	// Developers manage no invites.
	h.asUser(eve, f.team)
	h.failure(http.StatusForbidden, "forbidden", http.MethodGet, base, nil)
	h.failure(http.StatusForbidden, "forbidden", http.MethodPost, base, map[string]any{"role": "viewer"})
}

func TestAcceptInviteWithSignupOverHTTP(t *testing.T) {
	f := newTf(t)
	h := f.serve()
	c := f.invite(InviteInput{Role: auth.RoleDeveloper, MaxUses: 5})
	path := "/invites/" + c.Token + "/accept"

	// Nobody signed in and nothing to sign up with: 401.
	h.failure(http.StatusUnauthorized, "unauthorized", http.MethodPost, path, nil)
	h.failure(http.StatusUnauthorized, "unauthorized", http.MethodPost, path, map[string]any{"email": "n@example.com", "name": "N"})
	// Refusals pass through with their codes and create nothing.
	h.failure(http.StatusUnprocessableEntity, "weak_password", http.MethodPost, path, map[string]any{"email": "n@example.com", "name": "N", "password": "short"})
	h.failure(http.StatusConflict, "account_exists", http.MethodPost, path, map[string]any{"email": "ada@example.com", "name": "N", "password": covPassword})
	require.Zero(t, f.count(`SELECT count(*) FROM users WHERE email='n@example.com'`))

	// Sign-up creates the account, joins and starts the session.
	acc := h.call(http.StatusOK, http.MethodPost, path, map[string]any{"email": "n@example.com", "name": "Nina", "password": covPassword})
	require.Equal(t, true, acc["createdAccount"])
	require.Equal(t, "developer", acc["team"].(map[string]any)["role"])
	require.NotEmpty(t, h.cookies, "the new user is signed in")
	_, err := f.au.Login(context.Background(), "n@example.com", covPassword)
	require.NoError(t, err)
}

func TestInviteRoutesAreRateLimitedPerClient(t *testing.T) {
	f := newTf(t)
	h := f.serve()
	c := f.invite(InviteInput{Role: auth.RoleViewer})

	for i := range 60 {
		code, raw := h.do(http.MethodGet, "/invites/"+c.Token, nil)
		require.Equal(t, http.StatusOK, code, "preview %d: %s", i, raw)
	}
	h.failure(http.StatusTooManyRequests, "rate_limited", http.MethodGet, "/invites/"+c.Token, nil)

	// Accepting is limited much tighter (an attempt each, signed in or not).
	for i := range 10 {
		code, raw := h.do(http.MethodPost, "/invites/"+c.Token+"/accept", nil)
		require.Equal(t, http.StatusUnauthorized, code, "accept %d: %s", i, raw)
	}
	h.failure(http.StatusTooManyRequests, "rate_limited", http.MethodPost, "/invites/"+c.Token+"/accept", nil)
}

func TestInstanceEndpoints(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleOwner)
	h := f.serve()

	h.asUser(bob, f.team)
	view := h.call(http.StatusOK, http.MethodGet, "/instance", nil)
	require.Equal(t, true, view["allowTeamCreation"])
	require.Equal(t, false, view["canManage"])
	require.NotContains(t, view, "smtpHost")
	h.failure(http.StatusForbidden, "forbidden", http.MethodPatch, "/instance", map[string]any{"allowTeamCreation": false})

	h.asUser(f.ada.ID, f.team)
	upd := h.call(http.StatusOK, http.MethodPatch, "/instance", map[string]any{
		"allowTeamCreation": false, "smtpHost": "smtp.example.com", "smtpPort": 587, "smtpTls": "starttls",
		"smtpUsername": "u", "smtpPassword": "pw", "smtpFrom": "no-reply@example.com", "smtpInsecureSkipVerify": false,
	})
	require.Equal(t, false, upd["allowTeamCreation"])
	require.Equal(t, true, upd["canManage"])
	require.Equal(t, true, upd["smtpConfigured"])
	require.Equal(t, "smtp.example.com", upd["smtpHost"])
	require.Equal(t, float64(587), upd["smtpPort"])
	require.Equal(t, true, upd["smtpPasswordSet"])
	require.NotContains(t, upd, "smtpPassword", "the password is write-only")
	h.failure(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, "/instance", map[string]any{"smtpPort": 0})
	h.failure(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, "/instance", map[string]any{"smtpTls": "ssl"})
	h.failure(http.StatusUnprocessableEntity, "invalid_email", http.MethodPatch, "/instance", map[string]any{"smtpFrom": "nope"})
	require.Equal(t, "smtp.example.com", h.call(http.StatusOK, http.MethodGet, "/instance", nil)["smtpHost"], "refused patches changed nothing")

	// With team creation off the member can no longer create teams.
	h.asUser(bob, f.team)
	h.failure(http.StatusForbidden, "team_creation_disabled", http.MethodPost, "/teams", map[string]any{"name": "Blocked"})

	h.as = nil
	h.failure(http.StatusUnauthorized, "unauthorized", http.MethodGet, "/instance", nil)
}
