package mesh_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/tailscale/hujson"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// userPolicy is a tailnet policy a person maintains: comments, rules of their
// own (a tag rule, a wildcard rule, a rule with a proto over literal IPs) and an
// outdated NebulaCtrl rule of a node that no longer exists.
const userPolicy = `// Our tailnet policy. Do not break it.
{
  // who owns which tag
  "tagOwners": {
    "tag:prod": ["autogroup:admin"],
  },
  "acls": [
    // everybody reaches the wiki
    {"action": "accept", "src": ["*"], "dst": ["tag:prod:443"]},
    // the on-call group reaches ssh everywhere
    {"action": "accept", "src": ["group:oncall"], "dst": ["*:22"]},
    // a rule over literal IPs with a proto: never NebulaCtrl's
    {"action": "accept", "proto": "tcp", "src": ["100.64.0.50"], "dst": ["100.64.0.51:22"]},
    // an old NebulaCtrl rule of a node that was re-provisioned
    {"action": "accept", "src": ["100.64.0.99"], "dst": ["100.64.0.2:15000"]},
  ],
}
`

// The rule the seeded state generates: the link of the consumer environment to
// the provider's mesh port on server B.
const (
	linkIP      = "100.64.0.10"
	generatedIP = "100.64.0.2"
)

var generatedDst = generatedIP + ":5432"

// tsFixture is a team on Tailscale (OAuth) with two mesh-enabled servers, one
// service link and its grant, so that a policy rule is generated.
type tsFixture struct {
	*fixture
	fake     *meshtest.Tailscale
	nonAdmin context.Context
	ref      uuid.UUID
}

func newTailscaleFixture(t *testing.T, opts ...meshtest.TailscaleOption) *tsFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	f := &fixture{t: t, pool: pool, q: q, ws: ws.ID}
	f.env1, err = q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	f.env2, err = q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	f.a, err = q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	f.b, err = q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent2"})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $1", facts)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	jm := jobs.New(pool, nil)
	fake := meshtest.NewTailscale(t, opts...)
	f.ts = tailscale.NewService(pool, box, jm, nil)
	f.ts.SetBaseURL(fake.URL, fake.Client())
	tailscale.RegisterJobs(jm, f.ts)
	f.svc = mesh.NewService(mesh.Deps{Pool: pool, Bus: events.NewBus(), Jobs: jm, Dispatcher: realDisp{}, Tailscale: f.ts})
	mesh.RegisterJobs(jm, f.svc)
	f.admin = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: policy.AllScopes})
	_, err = f.ts.Put(f.admin, tailscale.PutInput{
		ClientID: meshtest.TailscaleClientID, ClientSecret: meshtest.TailscaleClientSecret,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", mesh.DefaultMeshTag},
	})
	require.NoError(t, err)

	tf := &tsFixture{fixture: f, fake: fake}
	tf.nonAdmin = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: []string{policy.ScopeServersRead, policy.ScopeServersWrite}})
	tf.seedRule()
	return tf
}

// seedRule enables the mesh on both servers and grants a service link, then
// gives the nodes the tailnet addresses the rule is made of.
func (f *tsFixture) seedRule() {
	pg1, _ := f.twoServices()
	f.ref = uuid.New()
	require.NoError(f.t, db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		exp, err := f.svc.EnsureExposure(bg, tx, pg1, agentprotocol.MeshPurposeService, 5432)
		if err != nil {
			return err
		}
		link, err := f.svc.EnsureLink(bg, tx, f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeService)
		if err != nil {
			return err
		}
		return f.svc.Grant(bg, tx, link.ID, exp.ID, f.ref, "reference")
	}))
	for srv, ip := range map[uuid.UUID]string{f.a.ID: "100.64.0.1", f.b.ID: generatedIP} {
		_, err := f.pool.Exec(bg, `UPDATE server_mesh SET ips = $2, state = 'online' WHERE server_id = $1`, srv, []string{ip})
		require.NoError(f.t, err)
	}
	_, err := f.pool.Exec(bg, `UPDATE mesh_links SET ips = $1, state = 'online'`, []string{linkIP})
	require.NoError(f.t, err)
}

func (f *tsFixture) setMode(ctx context.Context, mode string) (mesh.View, error) {
	return f.svc.UpdateSettings(ctx, mesh.SettingsInput{ACLMode: &mode})
}

func (f *tsFixture) mustSetMode(mode string) mesh.View {
	f.t.Helper()
	v, err := f.setMode(f.admin, mode)
	require.NoError(f.t, err)
	require.Equal(f.t, mode, v.ACLMode)
	return v
}

func (f *tsFixture) settings() db.MeshSetting {
	s, err := f.q.GetMeshSettings(bg, f.ws)
	require.NoError(f.t, err)
	return s
}

// posts returns the policy writes the fake saw (POST /acl, validate excluded).
func (f *tsFixture) posts() []meshtest.TailscaleRequest {
	var out []meshtest.TailscaleRequest
	for _, r := range f.fake.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/acl") {
			out = append(out, r)
		}
	}
	return out
}

func (f *tsFixture) policyRequests() int {
	n := 0
	for _, r := range f.fake.Requests() {
		if strings.Contains(r.Path, "/acl") {
			n++
		}
	}
	return n
}

func (f *tsFixture) audits(action string) int {
	var n int
	require.NoError(f.t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = $1`, action).Scan(&n))
	return n
}

// std parses a HuJSON policy into its JSON form for assertions on structure.
func std(t *testing.T, doc string) (tagOwners map[string][]string, acls []map[string]any) {
	t.Helper()
	b, err := hujson.Standardize([]byte(doc))
	require.NoError(t, err, "the stored policy must stay valid HuJSON:\n%s", doc)
	var p struct {
		TagOwners map[string][]string `json:"tagOwners"`
		ACLs      []map[string]any    `json:"acls"`
	}
	require.NoError(t, json.Unmarshal(b, &p))
	return p.TagOwners, p.ACLs
}

func hasRule(acls []map[string]any, src, dst string) bool {
	for _, r := range acls {
		if r["action"] != "accept" || r["proto"] != nil {
			continue
		}
		s, _ := r["src"].([]any)
		d, _ := r["dst"].([]any)
		if len(s) == 1 && len(d) == 1 && s[0] == src && d[0] == dst {
			return true
		}
	}
	return false
}

func requireAPICode(t *testing.T, err error, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	require.Equal(t, code, ae.Code)
}

// ---- advisory mode -------------------------------------------------------------------

// Advisory is the default and never writes, even when the OAuth client holds the
// write scope: it shows the snippet and compares the stored policy with the rules.
func TestAdvisoryModeNeverWritesAndDetectsDrift(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices, meshtest.ScopePolicy))
	require.Equal(t, mesh.ModeAdvisory, f.settings().AclMode)

	// The user's policy lacks the generated rule: drift, with what is missing.
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s := f.settings()
	require.Equal(t, mesh.StateDrift, s.AclState)
	require.Contains(t, s.AclMessage, "1 generated rule(s) are missing")
	v, err := f.svc.View(f.admin)
	require.NoError(t, err)
	require.Equal(t, mesh.ModeAdvisory, v.ACLMode)
	require.Empty(t, v.ACLManagedWarning, "the managed warning belongs to managed mode")
	require.Contains(t, v.Snippet, linkIP)
	require.Contains(t, v.Snippet, generatedDst)
	require.Contains(t, v.Snippet, mesh.DefaultMeshTag)

	// The preview says what a managed push would add and remove.
	pv, err := f.svc.PreviewACL(f.admin)
	require.NoError(t, err)
	require.NotNil(t, pv.Diff)
	require.Len(t, pv.Diff.Add, 1)
	require.Equal(t, []string{generatedDst}, pv.Diff.Add[0].Dst)
	require.Len(t, pv.Diff.Remove, 1, "the outdated NebulaCtrl rule")
	require.Equal(t, []string{"100.64.0.99"}, pv.Diff.Remove[0].Src)

	// The user pastes the snippet's rule: the drift check turns green.
	pasted := strings.Replace(userPolicy, `"acls": [`, `"acls": [
    {"action": "accept", "src": ["`+linkIP+`"], "dst": ["`+generatedDst+`"]},`, 1)
	f.fake.SetPolicyExternally(pasted)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s = f.settings()
	require.Equal(t, mesh.StateApplied, s.AclState)
	require.Contains(t, s.AclMessage, "present in your tailnet policy")

	// And back to drift when someone removes it.
	f.fake.SetPolicyExternally(userPolicy)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Equal(t, mesh.StateDrift, f.settings().AclState)

	require.Empty(t, f.posts(), "advisory mode never writes")
	require.Empty(t, f.fake.PolicyWrites())
}

// Without the policy_file:read scope the drift check cannot run: the state is
// advisory with the scope named; still nothing is written.
func TestAdvisoryModeWithoutPolicyScopeSaysSo(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy))
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s := f.settings()
	require.Equal(t, mesh.StateAdvisory, s.AclState)
	require.Contains(t, s.AclMessage, "policy_file:read")
	require.Empty(t, f.fake.PolicyWrites())

	// With the read scope only, drift is detected and still nothing is written.
	f.fake.GrantScope(meshtest.ScopePolicyRead)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Equal(t, mesh.StateDrift, f.settings().AclState)
	require.Empty(t, f.fake.PolicyWrites())
}

// An unreadable tailnet (API down) is advisory, not an error that blocks anything.
func TestAdvisoryModeWhenThePolicyCannotBeReadFallsBackToTheSnippet(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopePolicyRead))
	f.fake.SetDown(true)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s := f.settings()
	require.Equal(t, mesh.StateAdvisory, s.AclState)
	require.Contains(t, s.AclMessage, "Add the generated rules")
}

func TestModeOffAndPausedTouchNothing(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopePolicy))
	f.mustSetMode(mesh.ModeOff)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Equal(t, mesh.StateAdvisory, f.settings().AclState)
	require.Zero(t, f.policyRequests(), "off reads and writes nothing")

	f.mustSetMode(mesh.ModeManaged)
	before := f.policyRequests()
	require.NoError(t, f.q.SetMeshPaused(bg, db.SetMeshPausedParams{WorkspaceID: f.ws, Paused: true, PausedReason: "Headscale is being migrated"}))
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Equal(t, before, f.policyRequests(), "no ACL write while the mesh is paused")
	require.Empty(t, f.fake.PolicyWrites()[1:], "only the opt-in probe ever wrote")
}

// ---- switching to managed --------------------------------------------------------------

// Managed on Tailscale is an opt-in: it needs the admin scope, the OAuth client's
// write scope and a policy that can be patched, it is refused on a shared tailnet,
// and it is audited. Every refusal leaves the mode advisory and the policy alone.
func TestSwitchingToManagedIsAnExplicitAuditedOptIn(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices))

	// Not an admin: forbidden before anything is read.
	_, err := f.setMode(f.nonAdmin, mesh.ModeManaged)
	requireAPICode(t, err, "forbidden")
	require.Zero(t, f.policyRequests())

	// No policy scope at all.
	_, err = f.setMode(f.admin, mesh.ModeManaged)
	requireAPICode(t, err, apierr.CodeMeshPolicyScopeMissing)
	require.Contains(t, err.Error(), "policy_file")

	// policy_file:read reads and validates, but the push needs the write scope.
	f.fake.GrantScope(meshtest.ScopePolicyRead)
	_, err = f.setMode(f.admin, mesh.ModeManaged)
	requireAPICode(t, err, apierr.CodeMeshPolicyScopeMissing)

	// Another team on the same tailnet.
	f.fake.GrantScope(meshtest.ScopePolicy)
	other := f.otherTeamOnTheTailnet()
	_, err = f.setMode(f.admin, mesh.ModeManaged)
	requireAPICode(t, err, apierr.CodeMeshACLSharedTailnet)
	_, err = f.pool.Exec(bg, `DELETE FROM tailnet_integrations WHERE workspace_id = $1`, other)
	require.NoError(t, err)

	// A policy that is not HuJSON cannot be patched.
	good, _ := f.fake.Policy()
	f.fake.SetPolicyExternally(`{"acls": [`)
	_, err = f.setMode(f.admin, mesh.ModeManaged)
	require.Error(t, err)
	f.fake.SetPolicyExternally(good)

	require.Equal(t, mesh.ModeAdvisory, f.settings().AclMode, "every refusal left the mode alone")
	require.Zero(t, f.audits("mesh.acl.managed.enable"))
	require.Empty(t, f.fake.PolicyWrites(), "and wrote nothing")
	v, err := f.svc.View(f.admin)
	require.NoError(t, err)
	require.Empty(t, v.ACLManagedWarning)

	// The opt-in: audited, and the warning is surfaced.
	v, err = f.setMode(f.admin, mesh.ModeManaged)
	require.NoError(t, err)
	require.Equal(t, mesh.ModeManaged, v.ACLMode)
	require.Contains(t, v.ACLManagedWarning, "rewrite the whole tailnet policy")
	require.Contains(t, v.ACLManagedWarning, "policy_file")
	require.Equal(t, 1, f.audits("mesh.acl.managed.enable"))
	var meta string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE action = 'mesh.acl.managed.enable'`).Scan(&meta))
	require.Contains(t, meta, "tailscale")

	// The write-scope probe saved the document it had just read, unchanged.
	require.Equal(t, []string{userPolicy}, f.fake.PolicyWrites())
	doc, _ := f.fake.Policy()
	require.Equal(t, userPolicy, doc)

	// Asking again is not a new opt-in.
	_, err = f.setMode(f.admin, mesh.ModeManaged)
	require.NoError(t, err)
	require.Equal(t, 1, f.audits("mesh.acl.managed.enable"))
}

// otherTeamOnTheTailnet gives a second team the same tailnet and returns its id.
func (f *tsFixture) otherTeamOnTheTailnet() uuid.UUID {
	ws2, err := f.q.CreateWorkspace(bg, "other")
	require.NoError(f.t, err)
	u2, err := f.q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws2.ID, Email: "b@example.com", Name: "B", PasswordHash: "x"})
	require.NoError(f.t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u2.ID, WorkspaceID: ws2.ID, Label: "b", Scopes: policy.AllScopes})
	_, err = f.ts.Put(ctx, tailscale.PutInput{ClientID: meshtest.TailscaleClientID, ClientSecret: meshtest.TailscaleClientSecret, DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc"}})
	require.NoError(f.t, err)
	return ws2.ID
}

// ---- managed push ----------------------------------------------------------------------

// The push replaces only what NebulaCtrl owns: comments, the user's own rules and
// tagOwners entries survive; the generated rule is added, the outdated NebulaCtrl
// rule is removed; the write is guarded by the ETag that was read; a second apply
// writes nothing.
func TestManagedPushPatchesOnlyNebulaCtrlRules(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices, meshtest.ScopePolicy))
	f.mustSetMode(mesh.ModeManaged)
	_, etagBefore := f.fake.Policy()
	probes := len(f.fake.PolicyWrites())

	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s := f.settings()
	require.Equal(t, mesh.StateApplied, s.AclState, s.AclMessage)
	writes := f.fake.PolicyWrites()
	require.Len(t, writes, probes+1)
	doc, etag := f.fake.Policy()
	require.Equal(t, writes[len(writes)-1], doc)
	require.Equal(t, etag, s.AclVersion, "the version of what was written is remembered")
	post := f.posts()[len(f.posts())-1]
	require.Equal(t, etagBefore, post.IfMatch, "the write presented the ETag it read")

	// What the user wrote is untouched.
	for _, keep := range []string{
		"// Our tailnet policy. Do not break it.", "// who owns which tag", "// everybody reaches the wiki",
		"// the on-call group reaches ssh everywhere", "// a rule over literal IPs with a proto: never NebulaCtrl's",
	} {
		require.Contains(t, doc, keep)
	}
	owners, acls := std(t, doc)
	require.Equal(t, []string{"autogroup:admin"}, owners["tag:prod"])
	require.Equal(t, []string{"autogroup:admin"}, owners[mesh.DefaultMeshTag], "the mesh tag is owned")
	require.True(t, hasRuleAny(acls, []string{"*"}, []string{"tag:prod:443"}))
	require.True(t, hasRuleAny(acls, []string{"group:oncall"}, []string{"*:22"}))
	var proto map[string]any
	for _, r := range acls {
		if r["proto"] == "tcp" {
			proto = r
		}
	}
	require.NotNil(t, proto, "the rule with a proto is the user's")
	require.Equal(t, []any{"100.64.0.51:22"}, proto["dst"])
	// What is NebulaCtrl's was replaced.
	require.True(t, hasRule(acls, linkIP, generatedDst))
	require.NotContains(t, doc, "100.64.0.99", "the outdated NebulaCtrl rule is gone")
	require.Len(t, acls, 4, "three of the user's rules and the generated one")

	// Applying again is a no-op (nothing is written, the state stays applied).
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Len(t, f.fake.PolicyWrites(), probes+1)
	require.Equal(t, mesh.StateApplied, f.settings().AclState)

	// The page shows the warning while managed.
	v, err := f.svc.View(f.admin)
	require.NoError(t, err)
	require.NotEmpty(t, v.ACLManagedWarning)
	require.Empty(t, v.Snippet, "nothing to paste in managed mode")
}

func hasRuleAny(acls []map[string]any, src, dst []string) bool {
	for _, r := range acls {
		if r["proto"] != nil {
			continue
		}
		sb, _ := json.Marshal(r["src"])
		dbs, _ := json.Marshal(r["dst"])
		wantS, _ := json.Marshal(src)
		wantD, _ := json.Marshal(dst)
		if bytes.Equal(sb, wantS) && bytes.Equal(dbs, wantD) {
			return true
		}
	}
	return false
}

// When the grant goes away the generated rule is replaced (removed), everything
// else of the user's policy stays; when someone deletes our rule by hand the next
// apply puts it back (managed means the rules are ours to keep).
func TestManagedPushFollowsTheMeshAndRepairsDrift(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices, meshtest.ScopePolicy))
	f.mustSetMode(mesh.ModeManaged)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	doc, _ := f.fake.Policy()
	_, acls := std(t, doc)
	require.True(t, hasRule(acls, linkIP, generatedDst))

	// Someone deletes the generated rule in the console.
	deleted := strings.Replace(doc, `"`+linkIP+`"`, `"100.64.0.250"`, 1) // keeps the document valid, breaks the rule
	require.NotEqual(t, doc, deleted)
	f.fake.SetPolicyExternally(deleted)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	doc, _ = f.fake.Policy()
	_, acls = std(t, doc)
	require.True(t, hasRule(acls, linkIP, generatedDst), "the rule is back")
	require.Equal(t, mesh.StateApplied, f.settings().AclState)

	// The last grant is released: the rule disappears, the user's rules stay.
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.Release(bg, tx, f.ref) }))
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	doc, _ = f.fake.Policy()
	_, acls = std(t, doc)
	require.False(t, hasRule(acls, linkIP, generatedDst))
	require.True(t, hasRuleAny(acls, []string{"*"}, []string{"tag:prod:443"}))
	require.Contains(t, doc, "// everybody reaches the wiki")
	require.Len(t, acls, 3)
}

// A human saves the policy between NebulaCtrl's read and its write: the write
// answers 412, nothing is overwritten, the apply fails (River retries it), and the
// retry patches the human's version, keeping their edit.
func TestManagedPushNeverOverwritesAConcurrentEdit(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices, meshtest.ScopePolicy))
	f.mustSetMode(mesh.ModeManaged)
	probes := len(f.fake.PolicyWrites())

	human := strings.Replace(userPolicy, "// everybody reaches the wiki", "// everybody reaches the wiki AND THE HANDBOOK (edited just now)", 1)
	f.fake.ExternalEditAfterNextRead(human)
	err := f.svc.ApplyACL(bg, f.ws)
	require.ErrorIs(t, err, tailscale.ErrPolicyConflict, "the apply fails so that it is retried")
	doc, _ := f.fake.Policy()
	require.Equal(t, human, doc, "the human's edit was not overwritten")
	require.Len(t, f.fake.PolicyWrites(), probes, "no write was accepted")
	require.NotEqual(t, mesh.StateApplied, f.settings().AclState)

	// The retry reads the new version and keeps the edit.
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	doc, _ = f.fake.Policy()
	require.Contains(t, doc, "AND THE HANDBOOK (edited just now)")
	_, acls := std(t, doc)
	require.True(t, hasRule(acls, linkIP, generatedDst))
	require.Equal(t, mesh.StateApplied, f.settings().AclState)
}

// Things that make a push unsafe stop it and say why, without writing.
func TestManagedPushRefusesWhenItIsNotSafe(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices, meshtest.ScopePolicy))
	f.mustSetMode(mesh.ModeManaged)
	probes := len(f.fake.PolicyWrites())

	// Another team joins the tailnet later: NebulaCtrl will not rewrite its policy.
	other := f.otherTeamOnTheTailnet()
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s := f.settings()
	require.Equal(t, mesh.StateError, s.AclState)
	require.Contains(t, s.AclMessage, "Another team uses this tailnet")
	require.Len(t, f.fake.PolicyWrites(), probes)
	_, err := f.pool.Exec(bg, `DELETE FROM tailnet_integrations WHERE workspace_id = $1`, other)
	require.NoError(t, err)

	// The policy is no longer valid HuJSON (someone broke it): nothing is patched.
	f.fake.SetPolicyExternally(`{"acls": [`)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	s = f.settings()
	require.Equal(t, mesh.StateError, s.AclState)
	require.Contains(t, s.AclMessage, "HuJSON")
	require.Len(t, f.fake.PolicyWrites(), probes)

	// The policy cannot be read (API down): the apply fails and is retried; nothing is written.
	f.fake.SetPolicyExternally(userPolicy)
	f.fake.SetDown(true)
	require.Error(t, f.svc.ApplyACL(bg, f.ws), "the policy cannot be read: retried")
	f.fake.SetDown(false)
	require.Len(t, f.fake.PolicyWrites(), probes)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Equal(t, mesh.StateApplied, f.settings().AclState)
}

// Leaving managed mode stops the writes; the rules that were pushed stay in the
// tailnet policy (nothing is removed behind the user's back).
func TestSwitchingBackToAdvisoryStopsWritingAndKeepsTheRules(t *testing.T) {
	f := newTailscaleFixture(t, meshtest.WithPolicy(userPolicy), meshtest.WithScopes(meshtest.ScopeAuthKeys, meshtest.ScopeDevices, meshtest.ScopePolicy))
	f.mustSetMode(mesh.ModeManaged)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	writes := len(f.fake.PolicyWrites())

	f.mustSetMode(mesh.ModeAdvisory)
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.Release(bg, tx, f.ref) }))
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Len(t, f.fake.PolicyWrites(), writes, "advisory never writes")
	doc, _ := f.fake.Policy()
	_, acls := std(t, doc)
	require.True(t, hasRule(acls, linkIP, generatedDst), "the pushed rule stays until the user removes it")
	require.Equal(t, mesh.StateApplied, f.settings().AclState, "no rules are needed any more")
}
