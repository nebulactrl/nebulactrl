package networks_test

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
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestOutboundHintsJudgeTheHostOfAURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://api.example.com/v1":         true,
		"https://api.example.com./v1":        true,
		"https://API.Example.COM":            true,
		"http://[2001:4860:4860::8888]/dns":  true,
		"http://93.184.216.34:8080":          true,
		"http://10.0.0.5:8080":               false,
		"http://192.168.1.1":                 false,
		"http://127.0.0.1:3000":              false,
		"http://169.254.169.254/latest":      false,
		"http://0.0.0.0":                     false,
		"http://[::1]:80":                    false,
		"http://cache:6379":                  false,
		"http://cache.internal/x":            false,
		"http://printer.lan":                 false,
		"http://nas.local":                   false,
		"http://app.localhost":               false,
		"http://router.home.arpa":            false,
		"http:///no-host":                    false,
		"http://%zz":                         false,
		"not a url":                          false,
		"  https://api.example.com  ":        true,
		"redis://user:pass@cache.example.io": true,
	} {
		got := networks.OutboundHints(map[string]string{"TARGET": url})
		require.Equal(t, want, len(got) == 1, url)
	}
	// Variable names of typically external services count without a URL, unless empty or blank.
	require.Equal(t, []string{"AWS_ACCESS_KEY_ID", "SMTP_HOST"}, networks.OutboundHints(map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIA", "SMTP_HOST": "smtp", "MAIL_FROM": "   ", "MAILGUN_KEY": "",
	}))
	require.Empty(t, networks.OutboundHints(nil))
}

func TestValidNamesAreTrimmedAndBounded(t *testing.T) {
	got, err := networks.ValidName("  Back end_1.a-b  ")
	require.NoError(t, err)
	require.Equal(t, "Back end_1.a-b", got)
	for _, bad := range []string{"", "   ", "-lead", ".lead", "emoji 🙂", "slash/y", string(bytes.Repeat([]byte("n"), 64))} {
		_, err := networks.ValidName(bad)
		requireStatus(t, err, http.StatusUnprocessableEntity, "invalid_network_name")
	}
	_, err = networks.ValidName(string(bytes.Repeat([]byte("n"), 63)))
	require.NoError(t, err)
}

func TestCreateNetworkRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := f.a.ctx

	_, err := f.nets.Create(ctx, "bad/name", f.a.server.ID)
	requireStatus(t, err, http.StatusUnprocessableEntity, "invalid_network_name")
	_, err = f.nets.Create(ctx, "Backend", uuid.New())
	requireStatus(t, err, http.StatusUnprocessableEntity, "server_not_found")
	_, err = f.nets.Create(ctx, "Backend", f.b.server.ID)
	requireStatus(t, err, http.StatusUnprocessableEntity, "server_not_found")

	f.exec(`UPDATE servers SET status = 'revoked' WHERE id = $1`, f.a.server.ID)
	_, err = f.nets.Create(ctx, "Backend", f.a.server.ID)
	requireStatus(t, err, http.StatusUnprocessableEntity, "server_unavailable")
	f.exec(`UPDATE servers SET status = 'online' WHERE id = $1`, f.a.server.ID)

	_, err = f.nets.Create(ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	_, err = f.nets.Create(ctx, "Backend", f.a.server.ID)
	requireStatus(t, err, http.StatusConflict, "network_name_taken")
	require.Equal(t, []string{"network.create"}, f.auditActions(f.a), "only the network that was made is audited")
	// Names are per team: another team may use the same one.
	_, err = f.nets.Create(f.b.ctx, "Backend", f.b.server.ID)
	require.NoError(t, err)
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func TestListAndGetShowOnlyTheTeamsNetworks(t *testing.T) {
	f := newFixture(t)
	second, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.a.ws.ID, Name: "alpha-2"})
	require.NoError(t, err)
	one, err := f.nets.Create(f.a.ctx, "One", f.a.server.ID)
	require.NoError(t, err)
	two, err := f.nets.Create(f.a.ctx, "Two", second.ID)
	require.NoError(t, err)
	_, err = f.nets.Create(f.b.ctx, "Foreign", f.b.server.ID)
	require.NoError(t, err)

	all, err := f.nets.List(f.a.ctx, nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
	onFirst, err := f.nets.List(f.a.ctx, &f.a.server.ID)
	require.NoError(t, err)
	require.Len(t, onFirst, 1)
	require.Equal(t, one.ID, onFirst[0].ID)
	require.Equal(t, "alpha-1", onFirst[0].ServerName)
	nowhere, err := f.nets.List(f.a.ctx, new(uuid.New()))
	require.NoError(t, err)
	require.Empty(t, nowhere)

	got, err := f.nets.Get(f.a.ctx, two.ID)
	require.NoError(t, err)
	require.Equal(t, "Two", got.Name)
	require.Equal(t, "alpha-2", got.ServerName)
	require.NotNil(t, got.Services, "an empty network lists no services, not null")

	_, err = f.nets.Get(f.a.ctx, uuid.New())
	requireStatus(t, err, http.StatusNotFound, "not_found")
	_, err = f.nets.Get(f.b.ctx, one.ID)
	requireStatus(t, err, http.StatusNotFound, "not_found")

	noServers := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.a.user, WorkspaceID: f.a.ws.ID, Scopes: []string{policy.ScopeProjectsRead}})
	_, err = f.nets.List(noServers, nil)
	requireStatus(t, err, http.StatusForbidden, "forbidden")
	_, err = f.nets.Get(noServers, one.ID)
	requireStatus(t, err, http.StatusForbidden, "forbidden")
}

func TestDeleteNetwork(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	_, err = f.nets.Attach(f.a.ctx, api.ID, n.ID)
	require.NoError(t, err)

	err = f.nets.Delete(f.a.ctx, n.ID)
	requireStatus(t, err, http.StatusConflict, "network_in_use")
	ae, _ := apierr.As(err)
	require.EqualValues(t, 1, ae.Details["attached"])
	_, err = f.nets.Get(f.a.ctx, n.ID)
	require.NoError(t, err, "a network in use stays")

	requireStatus(t, f.nets.Delete(f.b.ctx, n.ID), http.StatusNotFound, "not_found")
	requireStatus(t, f.nets.Delete(f.a.ctx, uuid.New()), http.StatusNotFound, "not_found")

	require.NoError(t, f.nets.Detach(f.a.ctx, api.ID, n.ID))
	require.NoError(t, f.nets.Delete(f.a.ctx, n.ID))
	_, err = f.nets.Get(f.a.ctx, n.ID)
	requireStatus(t, err, http.StatusNotFound, "not_found")
	require.Equal(t, []string{"network.create", "network.attach", "network.detach", "network.delete"}, f.auditActions(f.a))
}

func TestDetachRefusals(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})

	requireStatus(t, f.nets.Detach(f.a.ctx, api.ID, n.ID), http.StatusNotFound, "not_found")
	requireStatus(t, f.nets.Detach(f.a.ctx, api.ID, uuid.New()), http.StatusNotFound, "not_found")
	requireStatus(t, f.nets.Detach(f.a.ctx, uuid.New(), n.ID), http.StatusNotFound, "not_found")
	requireStatus(t, f.nets.Detach(f.b.ctx, api.ID, n.ID), http.StatusNotFound, "not_found")
	_, err = f.nets.Attach(f.a.ctx, uuid.New(), n.ID)
	requireStatus(t, err, http.StatusNotFound, "not_found")
	_, err = f.nets.Attach(f.a.ctx, api.ID, uuid.New())
	requireStatus(t, err, http.StatusNotFound, "not_found")
	require.Equal(t, int64(1), f.generation(api.ID), "refused changes stage nothing")
}

func TestOnlyContainersJoinSharedNetworks(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	raw, err := json.Marshal(services.Config{Route: &services.RouteConfig{Protocol: "http"}}.WithDefaults())
	require.NoError(t, err)
	route, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.a.env.ID, Name: "edge", Slug: "edge", Kind: services.KindRoute, SourceType: services.SourceExternal, Config: raw, ServerID: &f.a.server.ID,
	})
	require.NoError(t, err)

	_, err = f.nets.Attach(f.a.ctx, route.ID, n.ID)
	require.Error(t, err, "a route has no container to attach")
	status, _ := apierr.As(err)
	require.NotNil(t, status)
	require.Equal(t, http.StatusUnprocessableEntity, status.Status)
	require.Zero(t, f.attachments(n.ID))
}

// ---- membership ----

func TestResolveKeepsOnlyLiveNetworksOfTheServer(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	second, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.a.ws.ID, Name: "alpha-2"})
	require.NoError(t, err)
	here, err := f.nets.Create(f.a.ctx, "Here", f.a.server.ID)
	require.NoError(t, err)
	there, err := f.nets.Create(f.a.ctx, "There", second.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	attached := []services.SnapshotNetwork{
		{ID: here.ID, Name: "Here", Alias: "api.production.shop"},
		{ID: there.ID, Name: "There", Alias: "api.production.shop"},
		{ID: uuid.New(), Name: "Deleted", Alias: "api.production.shop"},
	}

	m, err := networks.Resolve(bg, f.q, f.a.env.ID, f.a.server.ID, api.ID, false, attached)
	require.NoError(t, err)
	require.False(t, m.BlockEgress)
	require.False(t, m.EnvHasEgressBlocked)
	require.Equal(t, []agentprotocol.SharedNetworkAttachment{{Name: agentprotocol.SharedNetworkName(here.ID.String()), Alias: "api.production.shop"}}, m.SharedNetworks,
		"a network of another server and a deleted one are dropped, so a rollback does not fail on them")

	none, err := networks.Resolve(bg, f.q, f.a.env.ID, f.a.server.ID, api.ID, false, nil)
	require.NoError(t, err)
	require.Empty(t, none.SharedNetworks)
}

func TestResolveNeverPutsAnEgressBlockedServiceOnASharedNetwork(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	attached := []services.SnapshotNetwork{{ID: n.ID, Name: "Backend", Alias: "api.production.shop"}}

	m, err := networks.Resolve(bg, f.q, f.a.env.ID, f.a.server.ID, api.ID, true, attached)
	require.NoError(t, err)
	require.True(t, m.BlockEgress)
	require.Empty(t, m.SharedNetworks, "a shared bridge would give it the internet back")
	require.True(t, m.EnvHasEgressBlocked)
}

func TestResolveReportsASiblingThatBlocksEgress(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})

	m, err := networks.Resolve(bg, f.q, f.a.env.ID, f.a.server.ID, api.ID, false, nil)
	require.NoError(t, err)
	require.False(t, m.EnvHasEgressBlocked)

	f.service(f.a, "db", &f.a.server.ID, services.Config{Image: "postgres:17", Port: 5432, BlockEgress: true})
	m, err = networks.Resolve(bg, f.q, f.a.env.ID, f.a.server.ID, api.ID, false, nil)
	require.NoError(t, err)
	require.True(t, m.EnvHasEgressBlocked, "the environment's network is shared with a service that must not reach the internet")
	require.False(t, m.BlockEgress)

	// On another server the sibling does not matter.
	second, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.a.ws.ID, Name: "alpha-2"})
	require.NoError(t, err)
	m, err = networks.Resolve(bg, f.q, f.a.env.ID, second.ID, api.ID, false, nil)
	require.NoError(t, err)
	require.False(t, m.EnvHasEgressBlocked)
}

// ---- HTTP ----

func TestHTTPNetworkLifecycle(t *testing.T) {
	f := newFixture(t)
	handler := func(ctx context.Context) http.Handler {
		p, _ := auth.PrincipalFrom(ctx)
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
			})
		})
		cfg := huma.DefaultConfig("test", "1")
		cfg.CreateHooks = nil
		networks.Register(humachi.New(r, cfg), f.nets)
		return r
	}
	h := handler(f.a.ctx)
	do := func(method, path string, body any) (int, map[string]any) {
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m map[string]any
		if rec.Body.Len() > 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
		}
		return rec.Code, m
	}

	status, body := do(http.MethodPost, "/networks", map[string]any{"name": "Backend", "serverId": f.a.server.ID.String()})
	require.Equal(t, http.StatusCreated, status, body)
	id := body["id"].(string)
	require.Equal(t, agentprotocol.SharedNetworkName(id), body["dockerName"])
	status, body = do(http.MethodPost, "/networks", map[string]any{"name": "Backend", "serverId": f.a.server.ID.String()})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "network_name_taken", body["code"])

	status, body = do(http.MethodGet, "/networks/"+id, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "Backend", body["name"])
	status, body = do(http.MethodGet, "/networks", nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)
	status, body = do(http.MethodGet, "/networks?serverId="+f.b.server.ID.String(), nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["items"], "the filter narrows to the server")

	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	status, body = do(http.MethodPut, "/services/"+api.ID.String()+"/networks/"+id, nil)
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "api.production.shop", body["alias"])
	status, body = do(http.MethodDelete, "/networks/"+id, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "network_in_use", body["code"])
	status, _ = do(http.MethodDelete, "/services/"+api.ID.String()+"/networks/"+id, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = do(http.MethodDelete, "/networks/"+id, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = do(http.MethodGet, "/networks/"+id, nil)
	require.Equal(t, http.StatusNotFound, status)
}
