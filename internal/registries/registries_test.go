package registries

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ---------------------------------------------------------------------------

// rowDispatcher stores commands like the hub does (status pending) and remembers them.
type rowDispatcher struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
	ids  []uuid.UUID
}

func (d *rowDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	_, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	d.mu.Lock()
	d.cmds, d.ids = append(d.cmds, c), append(d.ids, id)
	d.mu.Unlock()
	return id, err
}

func (*rowDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *rowDispatcher) last() (agents.NewCommand, uuid.UUID, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.cmds) == 0 {
		return agents.NewCommand{}, uuid.Nil, false
	}
	return d.cmds[len(d.cmds)-1], d.ids[len(d.ids)-1], true
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, hdr http.Header) *http.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: hdr, Body: io.NopCloser(strings.NewReader(""))}
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	ws   db.Workspace
	user db.CreateUserRow
	srv  db.Server
	ctx  context.Context
	box  *secretbox.Box
	svc  *Service
	disp *rowDispatcher

	mu   sync.Mutex
	skew time.Duration
	// httpFn answers the control plane's requests to the registry (nil: unreachable).
	httpFn func(*http.Request) (*http.Response, error)
}

func (f *fixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Now().Add(f.skew)
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.skew += d
	f.mu.Unlock()
}

func scopesCtx(ws uuid.UUID, user uuid.UUID, scopes ...string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Role: auth.RoleOwner, Label: "dev@example.com", Scopes: scopes,
	})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "dev@example.com", Name: "Dev", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "build-1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)

	f := &fixture{t: t, pool: pool, q: q, ws: ws, user: u, srv: srv, box: box, disp: &rowDispatcher{}}
	f.svc = NewService(Deps{
		Pool: pool, Bus: events.NewBus(), Box: box, Dispatcher: f.disp, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: f.now, BuiltinURL: "http://registry.test:5000",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if f.httpFn == nil {
				return nil, io.EOF
			}
			return f.httpFn(r)
		})},
	})
	f.ctx = scopesCtx(ws.ID, u.ID, policy.AllScopes...)
	return f
}

// createExternal adds an external registry through the service.
func (f *fixture) createExternal(name string) RegistryView {
	f.t.Helper()
	v, err := f.svc.Create(f.ctx, CreateInput{Name: name, Host: "ghcr.io", Namespace: "acme", PushUsername: "deploy-bot", PushPassword: "ghp_pushsecret123"})
	require.NoError(f.t, err)
	return v
}

// builtinRegistry creates a team's built-in registries row and, once, the
// instance service row of the registry (ready, with a sealed signing key), like
// the instance services' provisioning does. It returns the key too.
func (f *fixture) builtinRegistry(name, host string) (db.Registry, ManagedSecrets) {
	f.t.Helper()
	return f.builtinRegistryIn(f.ws.ID, name, host)
}

// otherTeam is a second team with its own principal (a team has one built-in registry).
func (f *fixture) otherTeam() (context.Context, uuid.UUID) {
	f.t.Helper()
	ws, err := f.q.CreateWorkspace(context.Background(), "team-"+uuid.NewString()[:8])
	require.NoError(f.t, err)
	u, err := f.q.CreateUser(context.Background(), dbCreateUser(ws.ID, uuid.NewString()[:8]+"@example.com"))
	require.NoError(f.t, err)
	return scopesCtx(ws.ID, u.ID, policy.AllScopes...), ws.ID
}

func (f *fixture) builtinRegistryIn(ws uuid.UUID, name, host string) (db.Registry, ManagedSecrets) {
	f.t.Helper()
	bg := context.Background()
	ms, ok, err := LoadManagedSecrets(bg, f.q, f.box)
	require.NoError(f.t, err)
	if !ok {
		ms, err = GenerateManagedSecrets()
		require.NoError(f.t, err)
		enc, err := SealManagedSecrets(f.box, ms)
		require.NoError(f.t, err)
		_, err = f.q.UpsertInstanceService(bg, db.UpsertInstanceServiceParams{Kind: instanceKind, Status: "ready"})
		require.NoError(f.t, err)
		require.NoError(f.t, f.q.SetInstanceServiceSecrets(bg, db.SetInstanceServiceSecretsParams{Kind: instanceKind, SecretsEnc: enc}))
	}
	reg, err := f.q.InsertRegistry(bg, db.InsertRegistryParams{
		WorkspaceID: ws, Name: name, Kind: KindBuiltin, Host: host, Namespace: ws.String(), ManageRetention: true,
	})
	require.NoError(f.t, err)
	return reg, ms
}

// setBuiltinStatus marks the registry instance service ready or not.
func (f *fixture) setBuiltinStatus(status string) {
	f.t.Helper()
	require.NoError(f.t, f.q.SetInstanceServiceStatus(context.Background(), db.SetInstanceServiceStatusParams{Kind: instanceKind, Status: status, Message: "down for the test"}))
}

func code(err error) string {
	if err == nil {
		return ""
	}
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	return err.Error()
}

// verifyToken parses a registry token the way the registry does (ES256, key
// from the JWKS file by kid) and returns its claims.
func verifyToken(t *testing.T, raw string, ms ManagedSecrets, service string) (jwt.Claims, []tokenAccess, string) {
	t.Helper()
	jwks, err := ms.JWKS()
	require.NoError(t, err)
	var set jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal([]byte(jwks), &set))
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	require.Len(t, parsed.Headers, 1)
	kid := parsed.Headers[0].KeyID
	keys := set.Key(kid)
	require.Len(t, keys, 1, "the token's kid must name a key of the JWKS file")
	var std jwt.Claims
	var extra struct {
		Access []tokenAccess `json:"access"`
	}
	require.NoError(t, parsed.Claims(keys[0].Key, &std, &extra))
	require.NoError(t, std.Validate(jwt.Expected{Issuer: ManagedIssuer, AnyAudience: jwt.Audience{service}, Time: time.Now()}))
	return std, extra.Access, kid
}

func fmtScope(repo string, actions ...string) string {
	return fmt.Sprintf("repository:%s:%s", repo, strings.Join(actions, ","))
}

func dbCreateUser(ws uuid.UUID, email string) db.CreateUserParams {
	return db.CreateUserParams{WorkspaceID: ws, Email: email, Name: "Other", PasswordHash: "x"}
}

func mustUUID(s string) uuid.UUID { return uuid.MustParse(s) }

func newTestUUID() uuid.UUID { return uuid.New() }

func newUUID() string { return uuid.NewString() }

func jsonB64(seg string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(seg) }

func (f *fixture) commandVisible(id uuid.UUID) bool {
	_, err := f.q.GetAgentCommand(context.Background(), id)
	return err == nil
}

func dbCommand(payload json.RawMessage) db.AgentCommand {
	return db.AgentCommand{Kind: agentprotocol.KindRegistryCheck, Payload: payload}
}
