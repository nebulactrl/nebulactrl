package deploykeys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/deploykeys"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/gitsshtest"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type fixture struct {
	pool *pgxpool.Pool
	q    *db.Queries
	box  *secretbox.Box
	svc  *deploykeys.Service
	ctx  context.Context
	env  db.Environment
	ws   db.Workspace
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "alpha")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "a", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a", Scopes: policy.AllScopes})
	return &fixture{pool: pool, q: q, box: box, svc: deploykeys.NewService(pool, events.NewBus(), box), ctx: ctx, env: env, ws: ws}
}

func (f *fixture) gitService(t *testing.T, slug, gitURL string) db.Service {
	t.Helper()
	raw, err := json.Marshal(services.Config{GitURL: gitURL, GitBranch: "main"}.WithDefaults())
	require.NoError(t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceGit, Config: raw,
	})
	require.NoError(t, err)
	return sv
}

func (f *fixture) actions(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT action FROM audit_events WHERE workspace_id = $1 AND action LIKE 'service.ssh_%' ORDER BY id`, f.ws.ID)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		out = append(out, a)
	}
	return out
}

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func TestGenerateStoresTheKeyEncryptedAndNeverReturnsIt(t *testing.T) {
	f := newFixture(t)
	sv := f.gitService(t, "web", "git@github.com:acme/shop.git")

	st, err := f.svc.Status(f.ctx, sv.ID)
	require.NoError(t, err)
	require.True(t, st.UsesSSHURL)
	require.Equal(t, "github.com", st.Host)
	require.Nil(t, st.Key)
	require.NotNil(t, st.DeployKeyLink)
	require.Equal(t, "https://github.com/acme/shop/settings/keys/new", st.DeployKeyLink.URL)

	st, err = f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{})
	require.NoError(t, err)
	require.NotNil(t, st.Key)
	require.Equal(t, deploykeys.SourceGenerated, st.Key.Source)
	require.Equal(t, "ssh-ed25519", st.Key.Type)
	require.True(t, strings.HasPrefix(st.Key.PublicKey, "ssh-ed25519 AAAA"))
	require.True(t, strings.HasSuffix(st.Key.PublicKey, " nebulactrl-web"))

	// Nothing the API returns carries the private key...
	body, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(body), "PRIVATE KEY")
	require.NotContains(t, strings.ToLower(string(body)), "privatekey")

	// ...and at rest it is ciphertext that opens to the matching private key.
	row, err := f.q.GetServiceSSHKey(context.Background(), sv.ID)
	require.NoError(t, err)
	require.NotContains(t, string(row.PrivateKeyEnc), "PRIVATE KEY")
	pem, err := f.box.Open(secretbox.ServiceSSHKeysPrivateKey, sv.ID.String(), row.PrivateKeyEnc)
	require.NoError(t, err)
	signer, err := ssh.ParsePrivateKey(pem)
	require.NoError(t, err)
	require.Equal(t, st.Key.Fingerprint, ssh.FingerprintSHA256(signer.PublicKey()))

	require.Equal(t, []string{deploykeys.ActionKeyCreate}, f.actions(t))
}

func TestSetKeyConflictRotateUploadAndValidation(t *testing.T) {
	f := newFixture(t)
	sv := f.gitService(t, "web", "ssh://git@git.example.com/acme/shop.git")
	first, err := f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{})
	require.NoError(t, err)

	_, err = f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{})
	requireAPIError(t, err, 409, "ssh_key_exists")

	rotated, err := f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{Rotate: true})
	require.NoError(t, err)
	require.NotEqual(t, first.Key.Fingerprint, rotated.Key.Fingerprint)
	require.NotNil(t, rotated.Key.RotatedAt)

	own, err := gitssh.Generate("mine")
	require.NoError(t, err)
	uploaded, err := f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{PrivateKey: string(own.PrivatePEM), Rotate: true})
	require.NoError(t, err)
	require.Equal(t, deploykeys.SourceUploaded, uploaded.Key.Source)
	require.Equal(t, own.Fingerprint, uploaded.Key.Fingerprint)

	_, err = f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{PrivateKey: "not a key", Rotate: true})
	requireAPIError(t, err, 422, "invalid_private_key")
	st, err := f.svc.Status(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Equal(t, own.Fingerprint, st.Key.Fingerprint, "a rejected upload leaves the key alone")

	require.Equal(t, []string{deploykeys.ActionKeyCreate, deploykeys.ActionKeyRotate, deploykeys.ActionKeyRotate}, f.actions(t))

	// Not offered for image services.
	raw, _ := json.Marshal(services.Config{Image: "nginx"}.WithDefaults())
	img, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "img", Slug: "img", Kind: services.KindApp, SourceType: services.SourceImage, Config: raw,
	})
	require.NoError(t, err)
	_, err = f.svc.SetKey(f.ctx, img.ID, deploykeys.SetKeyInput{})
	requireAPIError(t, err, 422, "not_git_source")
}

func TestKeysAreScopedToTheTeamAndTheScope(t *testing.T) {
	f := newFixture(t)
	sv := f.gitService(t, "web", "git@github.com:acme/shop.git")

	// Another team gets a 404 for everything.
	other, err := f.q.CreateWorkspace(context.Background(), "beta")
	require.NoError(t, err)
	ou, err := f.q.CreateUser(context.Background(), db.CreateUserParams{WorkspaceID: other.ID, Email: "b@example.com", Name: "b", PasswordHash: "x"})
	require.NoError(t, err)
	octx := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: ou.ID, WorkspaceID: other.ID, Label: "b", Scopes: policy.AllScopes})
	_, err = f.svc.Status(octx, sv.ID)
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.SetKey(octx, sv.ID, deploykeys.SetKeyInput{})
	requireAPIError(t, err, 404, "not_found")

	// A read-only principal may look but not change.
	ro := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: f.ws.ID, Label: "ro", Scopes: []string{policy.ScopeProjectsRead}})
	_, err = f.svc.Status(ro, sv.ID)
	require.NoError(t, err)
	_, err = f.svc.SetKey(ro, sv.ID, deploykeys.SetKeyInput{})
	requireAPIError(t, err, 403, "forbidden")
}

func TestCredentialsFailWithoutAKeyAndSkipOtherURLs(t *testing.T) {
	f := newFixture(t)
	sv := f.gitService(t, "web", "ssh://git@127.0.0.1:1/acme/shop.git")

	_, err := f.svc.Credentials(context.Background(), sv.ID, "ssh://git@127.0.0.1:1/acme/shop.git")
	require.ErrorIs(t, err, gitssh.ErrNoKey)

	c, err := f.svc.Credentials(context.Background(), sv.ID, "https://github.com/acme/shop.git")
	require.NoError(t, err)
	require.Nil(t, c, "an https URL needs no SSH credentials")
}

func TestFirstFetchPinsTheHostKeyAndAChangedKeyIsRefusedUntilAccepted(t *testing.T) {
	f := newFixture(t)
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	root, sha := bareRepo(t)

	// A service with a deploy key the test server accepts.
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, root, host)
	url := "ssh://git@" + srv.Addr + "/app.git"
	sv := f.gitService(t, "web", url)
	st, err := f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{})
	require.NoError(t, err)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(st.Key.PublicKey))
	require.NoError(t, err)
	srv.Authorize(pub)

	bg := context.Background()
	// 1. TOFU: the first fetch records the key and succeeds.
	got, err := f.svc.Resolve(bg, sv.ID, url, "main")
	require.NoError(t, err)
	require.Equal(t, sha, got)
	st, err = f.svc.Status(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Len(t, st.HostKeys, 1)
	hostName := "[127.0.0.1]:" + itoa(srv.Port)
	require.Equal(t, hostName, st.HostKeys[0].Host)
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), st.HostKeys[0].Fingerprint)
	require.Nil(t, st.HostKeys[0].Mismatch)

	// Credentials hand the agent exactly that pin.
	creds, err := f.svc.Credentials(bg, sv.ID, url)
	require.NoError(t, err)
	require.Len(t, creds.KnownHosts, 1)
	require.True(t, strings.HasPrefix(creds.KnownHosts[0], hostName+" ssh-ed25519 "), creds.KnownHosts[0])

	// 2. The host presents another key: the fetch fails loudly and the refused key is recorded.
	changed := gitsshtest.NewHostSigner(t)
	srv.SetHostKey(changed)
	_, err = f.svc.Resolve(bg, sv.ID, url, "main")
	var mm *gitssh.MismatchError
	require.ErrorAs(t, err, &mm)
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), mm.Pinned[0].Fingerprint)
	require.Equal(t, ssh.FingerprintSHA256(changed.PublicKey()), mm.Offered.Fingerprint)

	st, err = f.svc.Status(f.ctx, sv.ID)
	require.NoError(t, err)
	require.NotNil(t, st.HostKeys[0].Mismatch)
	require.Equal(t, ssh.FingerprintSHA256(changed.PublicKey()), st.HostKeys[0].Mismatch.Fingerprint)
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), st.HostKeys[0].Fingerprint, "the pin is untouched")

	// The agent would still be given the OLD pin, so its clone fails too.
	creds, err = f.svc.Credentials(bg, sv.ID, url)
	require.NoError(t, err)
	require.Contains(t, creds.KnownHosts[0], gitssh.NewHostKey(hostName, host.PublicKey()).Key)

	// The connection test reports the same.
	tr, err := f.svc.Test(f.ctx, sv.ID)
	require.NoError(t, err)
	require.False(t, tr.OK)
	require.Equal(t, "host_key_changed", tr.Code)

	// 3. Accepting needs the fingerprint the user reviewed.
	_, err = f.svc.AcceptHostKey(f.ctx, sv.ID, "SHA256:something-else")
	requireAPIError(t, err, 409, "host_key_changed")
	st, err = f.svc.AcceptHostKey(f.ctx, sv.ID, ssh.FingerprintSHA256(changed.PublicKey()))
	require.NoError(t, err)
	require.Equal(t, ssh.FingerprintSHA256(changed.PublicKey()), st.HostKeys[0].Fingerprint)
	require.Nil(t, st.HostKeys[0].Mismatch)

	got, err = f.svc.Resolve(bg, sv.ID, url, "main")
	require.NoError(t, err)
	require.Equal(t, sha, got)
	tr, err = f.svc.Test(f.ctx, sv.ID)
	require.NoError(t, err)
	require.True(t, tr.OK, tr.Message)
	require.Equal(t, sha, tr.CommitSHA)

	require.Equal(t, []string{
		deploykeys.ActionKeyCreate, deploykeys.ActionHostPin, deploykeys.ActionHostClash, deploykeys.ActionHostAllow,
	}, f.actions(t))

	// 4. Removing the key drops the pins with it.
	require.NoError(t, f.svc.DeleteKey(f.ctx, sv.ID))
	st, err = f.svc.Status(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Nil(t, st.Key)
	require.Empty(t, st.HostKeys)
	requireAPIError(t, f.svc.DeleteKey(f.ctx, sv.ID), 404, "not_found")
}

func TestRejectedDeployKeyIsExplained(t *testing.T) {
	f := newFixture(t)
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	root, _ := bareRepo(t)
	srv := gitsshtest.Start(t, root, gitsshtest.NewHostSigner(t)) // authorizes nobody
	url := "ssh://git@" + srv.Addr + "/app.git"
	sv := f.gitService(t, "web", url)
	_, err := f.svc.SetKey(f.ctx, sv.ID, deploykeys.SetKeyInput{})
	require.NoError(t, err)

	tr, err := f.svc.Test(f.ctx, sv.ID)
	require.NoError(t, err)
	require.False(t, tr.OK)
	require.Equal(t, "git_unreachable", tr.Code)
	require.Contains(t, tr.Message, "deploy key was rejected")
	require.Len(t, tr.Status.HostKeys, 1, "the host key was still pinned on first contact")
}

func TestCopyKeyAndPinsToAPreviewService(t *testing.T) {
	f := newFixture(t)
	a := f.gitService(t, "web", "git@github.com:acme/shop.git")
	b := f.gitService(t, "web2", "git@github.com:acme/shop.git")
	_, err := f.svc.SetKey(f.ctx, a.ID, deploykeys.SetKeyInput{})
	require.NoError(t, err)
	bg := context.Background()
	hk := gitssh.NewHostKey("github.com", gitsshtest.NewHostSigner(t).PublicKey())
	_, err = f.q.PinServiceSSHHost(bg, db.PinServiceSSHHostParams{ServiceID: a.ID, Host: hk.Host, KeyType: hk.Type, PublicKey: hk.Authorized(), Fingerprint: hk.Fingerprint})
	require.NoError(t, err)

	// The key is bound to its service row: a copy opens it and seals it again for the new service.
	srcKey, err := f.q.GetServiceSSHKey(bg, a.ID)
	require.NoError(t, err)
	srcPEM, err := f.box.Open(secretbox.ServiceSSHKeysPrivateKey, a.ID.String(), srcKey.PrivateKeyEnc)
	require.NoError(t, err)
	copyEnc, err := f.box.Seal(secretbox.ServiceSSHKeysPrivateKey, b.ID.String(), srcPEM)
	require.NoError(t, err)
	require.NoError(t, f.q.InsertServiceSSHKeyIfAbsent(bg, db.InsertServiceSSHKeyIfAbsentParams{
		ServiceID: b.ID, PrivateKeyEnc: copyEnc, PublicKey: srcKey.PublicKey, Fingerprint: srcKey.Fingerprint, Source: srcKey.Source,
	}))
	_, err = f.box.Open(secretbox.ServiceSSHKeysPrivateKey, a.ID.String(), copyEnc)
	require.ErrorIs(t, err, secretbox.ErrDecrypt, "the copy is bound to the new service")
	require.NoError(t, f.q.CopyServiceSSHHosts(bg, db.CopyServiceSSHHostsParams{ToServiceID: b.ID, FromServiceID: a.ID}))
	st, err := f.svc.Status(f.ctx, b.ID)
	require.NoError(t, err)
	srcSt, _ := f.svc.Status(f.ctx, a.ID)
	require.Equal(t, srcSt.Key.Fingerprint, st.Key.Fingerprint)
	require.Len(t, st.HostKeys, 1)
}
