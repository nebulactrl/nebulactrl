package auth_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/auth/totptest"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const (
	gpaPassword = "correct horse battery"
	gpaBase     = "https://nebula.example.com"
)

// gpaHooks records what the sign-up hooks were asked to do and fails on demand.
type gpaHooks struct {
	checkErr, acceptErr, openErr error

	checked  []string // invite tokens checked
	accepted []string // invite tokens consumed
	acceptBy []uuid.UUID
	opened   []uuid.UUID
	openName []string
}

func (h *gpaHooks) CheckInvite(_ context.Context, token, _ string) error {
	h.checked = append(h.checked, token)
	return h.checkErr
}

func (h *gpaHooks) AcceptInvite(_ context.Context, _ pgx.Tx, token string, userID uuid.UUID, _ string) error {
	h.accepted = append(h.accepted, token)
	h.acceptBy = append(h.acceptBy, userID)
	return h.acceptErr
}

func (h *gpaHooks) OpenSignup(_ context.Context, _ pgx.Tx, userID uuid.UUID, name string) error {
	h.opened = append(h.opened, userID)
	h.openName = append(h.openName, name)
	return h.openErr
}

// gpaRig is a migrated schema, a service with a secret box, a controllable clock
// and an in-memory session manager.
type gpaRig struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *db.Queries
	svc   *auth.Service
	sm    *scs.SessionManager
	box   *secretbox.Box
	clock *time.Time
	hooks *gpaHooks
	user  *auth.User // first user: owner of the first team, signs in with gpaPassword
}

func newGpaRig(t *testing.T) *gpaRig {
	t.Helper()
	pool := testutil.NewPool(t)
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	require.NoError(t, err)
	now := time.Now()
	r := &gpaRig{t: t, pool: pool, q: db.New(pool), box: box, clock: &now, sm: scs.New(), hooks: &gpaHooks{}}
	r.svc = auth.NewService(pool, policy.ScopesForRole)
	r.svc.SetClock(func() time.Time { return *r.clock })
	r.svc.Configure(box, gpaBase)
	r.svc.SetSignupHooks(r.hooks)
	u, err := r.svc.Setup(context.Background(), "ada@example.com", gpaPassword, "Ada", "")
	require.NoError(t, err)
	r.user = u
	return r
}

func (r *gpaRig) advance(d time.Duration) { *r.clock = r.clock.Add(d) }

// session returns a context carrying a fresh anonymous session.
func (r *gpaRig) session() context.Context {
	r.t.Helper()
	ctx, err := r.sm.Load(context.Background(), "")
	require.NoError(r.t, err)
	return ctx
}

// signedIn returns a session context signed in as u.
func (r *gpaRig) signedIn(u *auth.User) context.Context {
	ctx := r.session()
	r.sm.Put(ctx, auth.SessionUserKey, u.ID.String())
	return ctx
}

func (r *gpaRig) inTx(fn func(tx pgx.Tx) error) error {
	return r.svc.InTx(context.Background(), fn)
}

// newUser inserts an account; password "" makes a passwordless one (signs in
// with a provider or a passkey only).
func (r *gpaRig) newUser(email, password string) *auth.User {
	r.t.Helper()
	hash := ""
	if password != "" {
		var err error
		hash, err = argon2id.CreateHash(password, argon2id.DefaultParams)
		require.NoError(r.t, err)
	}
	u, err := r.q.InsertUser(context.Background(), db.InsertUserParams{Email: email, Name: "Test User", PasswordHash: hash})
	require.NoError(r.t, err)
	return &auth.User{ID: u.ID, Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt}
}

// enrollTOTP turns TOTP on for u and returns the shared secret and the recovery
// codes. The enrolment consumed the time step of the current clock, so the next
// code needs gpaRig.totpCode.
func (r *gpaRig) enrollTOTP(u *auth.User) ([]byte, []string) {
	r.t.Helper()
	ctx := context.Background()
	enrol, err := r.svc.StartTOTPEnrolment(ctx, u)
	require.NoError(r.t, err)
	require.NotEmpty(r.t, enrol.Secret)
	secret := r.totpSecret(u)
	var codes []string
	err = r.inTx(func(tx pgx.Tx) error {
		var err error
		codes, err = r.svc.EnableTOTP(ctx, tx, u.ID, totptest.Code(secret, *r.clock))
		return err
	})
	require.NoError(r.t, err)
	return secret, codes
}

// totpSecret reads u's stored TOTP secret back through the secret box.
func (r *gpaRig) totpSecret(u *auth.User) []byte {
	r.t.Helper()
	row, err := r.q.GetUserTOTP(context.Background(), u.ID)
	require.NoError(r.t, err)
	secret, err := r.box.Open(secretbox.UserTOTPSecret, u.ID.String(), row.SecretEnc)
	require.NoError(r.t, err)
	return secret
}

// totpCode moves the clock to the next 30 s step and returns the app's code for
// it, so it is a code the account has not accepted yet.
func (r *gpaRig) totpCode(secret []byte) string {
	r.advance(auth.TOTPPeriod * time.Second)
	return totptest.Code(secret, *r.clock)
}

// recoveryLeft counts u's unused recovery codes.
func (r *gpaRig) recoveryLeft(u *auth.User) int {
	r.t.Helper()
	n, err := r.q.CountRecoveryCodesLeft(context.Background(), u.ID)
	require.NoError(r.t, err)
	return int(n)
}

// seedRecoveryCodes stores recovery codes for an account with no TOTP (passkeys
// only) and returns them.
func (r *gpaRig) seedRecoveryCodes(u *auth.User) []string {
	r.t.Helper()
	codes, err := auth.NewRecoveryCodes()
	require.NoError(r.t, err)
	for _, c := range codes {
		require.NoError(r.t, r.q.InsertRecoveryCode(context.Background(), db.InsertRecoveryCodeParams{UserID: u.ID, CodeHash: auth.HashRecoveryCode(c)}))
	}
	return codes
}

// gpaRequireAPIError asserts err is an apierr with the given status and code.
func gpaRequireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	require.Error(t, err)
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	require.Equal(t, status, ae.Status, "%v", err)
	require.Equal(t, code, ae.Code, "%v", err)
}

// gpaProviderOpts describes an identity provider row inserted by gpaRig.provider.
type gpaProviderOpts struct {
	Kind      string
	Name      string
	IssuerURL string
	ClientID  string
	Secret    string
	Domains   []string
	Scopes    []string
	TrustMFA  bool
	Disabled  bool
}

// provider inserts an identity provider with its client secret sealed like the
// service does.
func (r *gpaRig) provider(o gpaProviderOpts) db.AuthProvider {
	r.t.Helper()
	if o.ClientID == "" {
		o.ClientID = "nebula-client"
	}
	if o.Secret == "" {
		o.Secret = "nebula-secret"
	}
	if o.Name == "" {
		o.Name = "Provider"
	}
	id := uuid.New()
	enc, err := r.box.SealString(secretbox.AuthProvidersClientSecret, id.String(), o.Secret)
	require.NoError(r.t, err)
	row, err := r.q.CreateAuthProvider(context.Background(), db.CreateAuthProviderParams{
		ID: id, Kind: o.Kind, Name: o.Name, IssuerUrl: o.IssuerURL, ClientID: o.ClientID, ClientSecretEnc: enc,
		Scopes: append([]string{}, o.Scopes...), AllowedDomains: append([]string{}, o.Domains...), TrustMfa: o.TrustMFA, Enabled: !o.Disabled,
	})
	require.NoError(r.t, err)
	return row
}

// insertPasskey stores a passkey row for u. It is a stand-in credential (no real
// key): enough for the paths that only count, list, rename or delete passkeys.
func (r *gpaRig) insertPasskey(u *auth.User, name string) db.UserPasskey {
	r.t.Helper()
	row, err := r.q.CreateUserPasskey(context.Background(), db.CreateUserPasskeyParams{
		UserID: u.ID, Name: name, CredentialID: []byte(uuid.NewString()), Credential: []byte(`{}`),
	})
	require.NoError(r.t, err)
	return row
}
