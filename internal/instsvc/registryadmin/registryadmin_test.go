package registryadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// rowDispatcher stores commands like the hub does (status pending).
type rowDispatcher struct {
	mu  sync.Mutex
	ids []uuid.UUID
	cmd []agents.NewCommand
}

func (d *rowDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	_, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, Kind: c.Kind, Payload: c.Payload, IdempotencyKey: c.IdempotencyKey, ExpiresAt: time.Now().Add(time.Hour),
	})
	d.mu.Lock()
	d.ids, d.cmd = append(d.ids, id), append(d.cmd, c)
	d.mu.Unlock()
	return id, err
}

func (*rowDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	b    *Backend
	disp *rowDispatcher
	box  *secretbox.Box
	dir  string
	jwks string

	mu  sync.Mutex
	now time.Time
	gau int
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func newFixture(t *testing.T, url string) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	f := &fixture{t: t, pool: pool, q: q, box: box, disp: &rowDispatcher{}, dir: t.TempDir(), now: time.Date(2026, 10, 30, 2, 30, 0, 0, time.UTC)}
	f.jwks = filepath.Join(f.dir, "auth", "jwks.json")
	f.b = New(Config{URL: url, JWKSPath: f.jwks, Pool: pool, Box: box, Dispatcher: f.disp, Now: f.clock})
	f.b.SetWriteGauge(func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.gau })
	_, err = q.UpsertInstanceService(context.Background(), db.UpsertInstanceServiceParams{Kind: "registry", Status: "ready"})
	require.NoError(t, err)
	// The control plane's host: a server with an agent online.
	ws, err := q.CreateWorkspace(context.Background(), "ops")
	require.NoError(t, err)
	srv, err := q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: ws.ID, Name: "cp"})
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), "UPDATE servers SET status = 'online', is_control_plane = true WHERE id = $1", srv.ID)
	require.NoError(t, err)
	return f
}

func (f *fixture) state() registries.ManagedState {
	f.t.Helper()
	row, err := f.q.GetInstanceService(context.Background(), "registry")
	require.NoError(f.t, err)
	return registries.DecodeManagedState(row.Options)
}

func TestPrepareMakesTheKeyOnceAndPublishesTheJWKS(t *testing.T) {
	f := newFixture(t, "http://registry.invalid")
	ctx := context.Background()

	require.NoError(t, f.b.Prepare(ctx))
	raw, err := os.ReadFile(f.jwks)
	require.NoError(t, err)
	info, err := os.Stat(f.jwks)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the registry runs as another user and only reads the public key")

	ms, ok, err := registries.LoadManagedSecrets(ctx, f.q, f.box)
	require.NoError(t, err)
	require.True(t, ok, "the signing key is sealed in instance_services")
	row, err := f.q.GetInstanceService(ctx, "registry")
	require.NoError(t, err)
	require.NotContains(t, string(row.SecretsEnc), "PRIVATE", "sealed, not plain")
	want, err := ms.JWKS()
	require.NoError(t, err)
	require.JSONEq(t, want, string(raw))
	require.NotContains(t, string(raw), `"d"`, "only the public half is published")

	// Idempotent: the same key, the file untouched; a deleted file comes back.
	require.NoError(t, f.b.Prepare(ctx))
	again, _, err := registries.LoadManagedSecrets(ctx, f.q, f.box)
	require.NoError(t, err)
	require.Equal(t, ms.RegistryKeyID, again.RegistryKeyID)
	require.NoError(t, os.Remove(f.jwks))
	require.NoError(t, f.b.Prepare(ctx))
	back, err := os.ReadFile(f.jwks)
	require.NoError(t, err)
	require.JSONEq(t, want, string(back))

	// A token it signs verifies against the published file, as the registry would.
	tok, err := f.b.SignRepoToken(ctx, registries.RepoAccess{Name: "t/app", Actions: []string{"pull"}})
	require.NoError(t, err)
	var set jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal(back, &set))
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	var std jwt.Claims
	require.NoError(t, parsed.Claims(set.Key(parsed.Headers[0].KeyID)[0].Key, &std))
	require.NoError(t, std.Validate(jwt.Expected{Issuer: registries.ManagedIssuer, AnyAudience: jwt.Audience{registries.BuiltinService}, Time: f.clock()}))

	// No path configured is an error a person can act on.
	f.b.cfg.JWKSPath = ""
	require.ErrorContains(t, f.b.Prepare(ctx), "NEBULACTRL_REGISTRY_JWKS_PATH")
}

// The installer's registry.yaml and compose file name what this package signs with.
func TestInstallerConfigMatchesTheSigner(t *testing.T) {
	var reg struct {
		Auth struct {
			Token struct {
				Service           string   `yaml:"service"`
				Issuer            string   `yaml:"issuer"`
				SigningAlgorithms []string `yaml:"signingalgorithms"`
			} `yaml:"token"`
		} `yaml:"auth"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(testutil.RepoFile(t, "deploy/install/instance/registry.yaml")), &reg))
	require.Equal(t, registries.BuiltinService, reg.Auth.Token.Service)
	require.Equal(t, registries.ManagedIssuer, reg.Auth.Token.Issuer)
	require.Equal(t, []string{registries.SigningAlgorithm}, reg.Auth.Token.SigningAlgorithms)

	var compose struct {
		Services map[string]struct {
			ContainerName string `yaml:"container_name"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(testutil.RepoFile(t, "deploy/install/docker-compose.yml")), &compose))
	require.Equal(t, agentprotocol.RegistryContainerName, compose.Services["registry"].ContainerName,
		"the agent runs garbage collection in the container the compose file names")
}

func TestHealthNeedsATokenChallengeAndAcceptsOurToken(t *testing.T) {
	var withToken int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="x"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		withToken++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	f := newFixture(t, srv.URL)
	ctx := context.Background()
	require.NoError(t, f.b.Prepare(ctx))
	require.NoError(t, f.b.Health(ctx))
	require.Equal(t, 1, withToken)

	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer open.Close()
	f.b.cfg.URL = open.URL
	require.ErrorContains(t, f.b.Health(ctx), "token authentication is not enabled")

	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer refuse.Close()
	f.b.cfg.URL = refuse.URL
	require.ErrorContains(t, f.b.Health(ctx), "key file or issuer does not match")
}

func TestReadOnlyFollowsTheStateAndFailsClosed(t *testing.T) {
	f := newFixture(t, "http://registry.invalid")
	ctx := context.Background()
	require.False(t, f.b.ReadOnly(ctx))
	require.NoError(t, f.b.patch(ctx, map[string]any{"readOnly": true}))
	require.True(t, f.b.ReadOnly(ctx))
	require.NoError(t, f.b.patch(ctx, map[string]any{"readOnly": nil}))
	require.False(t, f.b.ReadOnly(ctx))

	// When the state cannot be read, refuse writes rather than risk a push during collection.
	b := New(Config{URL: "x", Pool: f.pool})
	f.pool.Close()
	require.True(t, b.ReadOnly(ctx))
}

// ---- the garbage-collection window -------------------------------------------------------------

func TestGCWindowDrainsCollectsAndAlwaysPutsWritesBack(t *testing.T) {
	f := newFixture(t, "http://registry.invalid")
	ctx := context.Background()
	require.NoError(t, f.b.Prepare(ctx))

	// Due only in the quiet hours, and once a week.
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 30, 0, 0, time.UTC) }
	require.True(t, gcDue(at(5, 3), registries.ManagedState{}), "never collected: due in the window")
	require.False(t, gcDue(at(5, 12), registries.ManagedState{}), "outside the quiet hours")
	last := at(1, 3)
	require.False(t, gcDue(at(5, 3), registries.ManagedState{LastGCAt: &last}), "collected four days ago")
	require.True(t, gcDue(at(8, 3), registries.ManagedState{LastGCAt: &last}))
	require.True(t, gcDue(at(2, 14), registries.ManagedState{Phase: phaseCollect}), "an interrupted window resumes at any hour")

	// Start: read-only at once, then wait for the forwarder's cached flag to expire.
	wait, done, err := f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, drainGrace, wait)
	st := f.state()
	require.True(t, st.ReadOnly)
	require.Equal(t, phaseDrain, st.Phase)
	require.True(t, f.b.ReadOnly(ctx))

	// A push still in flight holds the window.
	f.mu.Lock()
	f.gau = 2
	f.mu.Unlock()
	f.advance(drainGrace + time.Second)
	wait, done, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, drainPoll, wait)
	require.Equal(t, phaseDrain, f.state().Phase)
	require.Empty(t, f.disp.ids, "nothing is collected while a push runs")

	// Quiet: the instance operation goes to the control-plane host's agent.
	f.mu.Lock()
	f.gau = 0
	f.mu.Unlock()
	wait, done, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, gcPoll, wait)
	require.Equal(t, phaseCollect, f.state().Phase)
	require.Len(t, f.disp.cmd, 1)
	require.Equal(t, agentprotocol.KindInstanceOp, f.disp.cmd[0].Kind)
	var p agentprotocol.InstanceOpPayload
	require.NoError(t, json.Unmarshal(f.disp.cmd[0].Payload, &p))
	require.Equal(t, agentprotocol.InstanceOpRegistryGC, p.Op)
	cp, err := f.q.GetControlPlaneServer(ctx)
	require.NoError(t, err)
	require.Equal(t, cp.ID, f.disp.cmd[0].ServerID, "only the host that runs the registry container")

	// Still running: still read-only.
	_, done, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.False(t, done)
	require.True(t, f.state().ReadOnly)

	// Success ends the window: writes are back and the week starts again.
	finish(t, f, "succeeded", "")
	_, done, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.True(t, done)
	st = f.state()
	require.False(t, st.ReadOnly)
	require.Empty(t, st.Phase)
	require.NotNil(t, st.LastGCAt)
	require.False(t, f.b.ReadOnly(ctx))
}

func finish(t *testing.T, f *fixture, status, msg string) {
	t.Helper()
	f.disp.mu.Lock()
	id := f.disp.ids[len(f.disp.ids)-1]
	f.disp.mu.Unlock()
	cmd, err := f.q.GetAgentCommand(context.Background(), id)
	require.NoError(t, err)
	_, err = f.q.FinishAgentCommand(context.Background(), db.FinishAgentCommandParams{ID: id, ServerID: cmd.ServerID, Status: status, Result: json.RawMessage(`{}`), Error: msg})
	require.NoError(t, err)
}

func TestGCWindowFailureReleasesWritesAndKeepsTheReason(t *testing.T) {
	f := newFixture(t, "http://registry.invalid")
	ctx := context.Background()
	require.NoError(t, f.b.Prepare(ctx))
	_, _, err := f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	f.advance(drainGrace + time.Second)
	_, _, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.Equal(t, phaseCollect, f.state().Phase)

	finish(t, f, "failed", "registry exited with status 1: boom")
	_, done, err := f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.True(t, done)
	st := f.state()
	require.False(t, st.ReadOnly, "a failed collection never leaves the registry read-only")
	require.Empty(t, st.Phase)
	require.Contains(t, st.LastGCMessage, "boom")
	require.Contains(t, st.Error, "boom")
	require.Nil(t, st.LastGCAt, "a failed window is retried tomorrow night, not next week")
}

func TestGCWindowEndsAtOnceWithoutAnAgentOrWhenPushesNeverStop(t *testing.T) {
	f := newFixture(t, "http://registry.invalid")
	ctx := context.Background()
	require.NoError(t, f.b.Prepare(ctx))

	// The control plane's agent is offline: nothing is sent, writes come back.
	_, err := f.q.GetControlPlaneServer(ctx)
	require.NoError(t, err)
	_, err = f.b.q.GetInstanceService(ctx, "registry")
	require.NoError(t, err)
	require.NoError(t, f.setCPStatus("offline"))
	_, _, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	f.advance(drainGrace + time.Second)
	_, done, err := f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.True(t, done)
	require.False(t, f.state().ReadOnly)
	require.Contains(t, f.state().LastGCMessage, "offline")
	require.Empty(t, f.disp.ids)

	// A push that never ends: after two hours the night is skipped, writes come back.
	require.NoError(t, f.setCPStatus("online"))
	f.mu.Lock()
	f.gau = 1
	f.mu.Unlock()
	_, _, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	f.advance(drainDeadline + time.Minute)
	_, done, err = f.b.AdvanceGC(ctx)
	require.NoError(t, err)
	require.True(t, done)
	require.False(t, f.state().ReadOnly)
	require.Contains(t, f.state().LastGCMessage, "Pushes were still running")
	require.Empty(t, f.disp.ids)
}

func (f *fixture) setCPStatus(status string) error {
	cp, err := f.q.GetControlPlaneServer(context.Background())
	if err != nil {
		return err
	}
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = $2 WHERE id = $1", cp.ID, status)
	return err
}

// ---- purge ---------------------------------------------------------------------------------------

// fakeRegistry is a token-checking registry with a few repositories.
type fakeRegistry struct {
	mu      sync.Mutex
	ms      registries.ManagedSecrets
	repos   map[string]map[string]string // repo -> tag -> digest
	bodies  map[string]string            // digest -> manifest body
	deleted []string                     // repo@digest
	t       *testing.T
}

func (r *fakeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	jwks, _ := r.ms.JWKS()
	var set jose.JSONWebKeySet
	_ = json.Unmarshal([]byte(jwks), &set)
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var std jwt.Claims
	var extra struct {
		Access []struct {
			Type    string   `json:"type"`
			Name    string   `json:"name"`
			Actions []string `json:"actions"`
		} `json:"access"`
	}
	if err := parsed.Claims(set.Keys[0].Key, &std, &extra); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	allowed := func(typ, name, action string) bool {
		for _, a := range extra.Access {
			if a.Type == typ && a.Name == name {
				for _, x := range a.Actions {
					if x == action || x == "*" {
						return true
					}
				}
			}
		}
		return false
	}
	p := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case p == "_catalog":
		if !allowed("registry", "catalog", "*") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var names []string
		for n := range r.repos {
			names = append(names, n)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": names})
	case strings.HasSuffix(p, "/tags/list"):
		repo := strings.TrimSuffix(p, "/tags/list")
		if !allowed("repository", repo, "pull") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var tags []string
		for tg := range r.repos[repo] {
			tags = append(tags, tg)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
	case strings.Contains(p, "/manifests/"):
		repo, ref, _ := strings.Cut(p, "/manifests/")
		switch req.Method {
		case http.MethodGet:
			if !allowed("repository", repo, "pull") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			d, ok := r.repos[repo][ref]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", d)
			_, _ = io.WriteString(w, r.bodies[d])
		case http.MethodDelete:
			if !allowed("repository", repo, "delete") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			r.deleted = append(r.deleted, repo+"@"+ref)
			w.WriteHeader(http.StatusAccepted)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPurgeDeletesOnlyTheTeamsManifestsIncludingIndexChildren(t *testing.T) {
	teamA, teamB := uuid.New().String(), uuid.New().String()
	ms, err := registries.GenerateManagedSecrets()
	require.NoError(t, err)
	fr := &fakeRegistry{ms: ms, t: t, repos: map[string]map[string]string{
		teamA + "/web":  {"v1": "sha256:aaa", "v2": "sha256:bbb"},
		teamA + "/api":  {"latest": "sha256:index"},
		teamB + "/web":  {"v1": "sha256:ccc"},
		"library/other": {"v1": "sha256:ddd"},
	}, bodies: map[string]string{
		"sha256:index": `{"manifests":[{"digest":"sha256:child1"},{"digest":"sha256:child2"}]}`,
		"sha256:aaa":   `{"layers":[]}`,
		"sha256:bbb":   `{"layers":[]}`,
	}}
	srv := httptest.NewServer(fr)
	defer srv.Close()

	f := newFixture(t, srv.URL)
	f.b.keys, f.b.keysAt = ms, f.clock()
	// secrets() reads the key from the cache for a minute; the same key signs the fake's expectations.
	require.NoError(t, f.b.Purge(context.Background(), instsvc.Slice{Kind: "registry", Ref: teamA + "/"}))

	require.ElementsMatch(t, []string{
		teamA + "/web@sha256:aaa", teamA + "/web@sha256:bbb",
		teamA + "/api@sha256:child1", teamA + "/api@sha256:child2", teamA + "/api@sha256:index",
	}, fr.deleted, "the team's manifests go, an index's children with it, and nobody else's")

	require.Error(t, f.b.Purge(context.Background(), instsvc.Slice{Kind: "registry", Ref: ""}), "an empty prefix would match every team")
}
