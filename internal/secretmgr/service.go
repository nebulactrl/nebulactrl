package secretmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// maxGrants bounds the allow-list of one integration.
const maxGrants = 200

// Audit actions of the secret access log. `secret.fetch` is one event per
// operation that read external secrets (a deploy's command, a task, a database
// credential, a backup, a log-masking refetch): the service, who started it and
// the references read, never a value. `secret.fetch.denied` is the same event
// when an integration's scope refused at least one reference.
const (
	ActionFetch       = "secret.fetch"
	ActionFetchDenied = "secret.fetch.denied"
)

// Service manages the team's secret manager integrations and creates the
// fetchers that resolve references at deploy time.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	box  *secretbox.Box
	log  *slog.Logger
}

var _ services.ExternalSecrets = (*Service)(nil)

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, box *secretbox.Box, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{pool: pool, box: box, log: logger.With("component", "secretmgr")}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// Input creates or updates an integration. An empty Secret keeps the stored
// credential on update; an empty Scope keeps the stored scope (and ignores
// Grants) on update and means ScopeTeam on create.
type Input struct {
	Name   string
	Kind   string
	Config SecretManagerConfig
	// Secret is the Vault token (token auth), the AppRole secret id, the
	// Infisical client secret or the Doppler token.
	Secret string
	Scope  string
	Grants []ScopeGrant
}

// grantsByIntegration loads the team's grants, with project and environment names.
func (s *Service) grantsByIntegration(ctx context.Context, q *db.Queries, ws uuid.UUID) (map[uuid.UUID][]ScopeGrant, error) {
	rows, err := q.ListSecretIntegrationGrants(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list integration grants: %w", err)
	}
	out := map[uuid.UUID][]ScopeGrant{}
	for _, r := range rows {
		g := ScopeGrant{ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID, ProjectName: r.ProjectName}
		if r.EnvironmentName != nil {
			g.EnvironmentName = *r.EnvironmentName
		}
		out[r.IntegrationID] = append(out[r.IntegrationID], g)
	}
	return out, nil
}

// List returns the team's integrations.
func (s *Service) List(ctx context.Context) ([]SecretManager, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return nil, err
	}
	ws := policy.WorkspaceID(ctx)
	rows, err := s.q.ListSecretIntegrations(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list secret integrations: %w", err)
	}
	grants, err := s.grantsByIntegration(ctx, s.q, ws)
	if err != nil {
		return nil, err
	}
	out := make([]SecretManager, len(rows))
	for i, r := range rows {
		out[i] = view(r, grants[r.ID])
	}
	return out, nil
}

func (s *Service) get(ctx context.Context, q *db.Queries, id uuid.UUID) (db.SecretIntegration, error) {
	r, err := q.GetSecretIntegration(ctx, db.GetSecretIntegrationParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SecretIntegration{}, apierr.NotFound("secret manager")
	}
	if err != nil {
		return db.SecretIntegration{}, fmt.Errorf("get secret integration: %w", err)
	}
	return r, nil
}

// checkScope validates a scope and its grants for the team and returns the
// grants to store: none for ScopeTeam; for ScopeSelected at least one, every
// project in the team, every environment in its project, duplicates dropped and
// environment grants of a project that is allowed as a whole folded into it.
func checkScope(ctx context.Context, q *db.Queries, ws uuid.UUID, scope string, grants []ScopeGrant) ([]ScopeGrant, error) {
	switch scope {
	case ScopeTeam:
		return nil, nil
	case ScopeSelected:
	default:
		return nil, unprocessable("invalid_scope", "scope must be team or selected")
	}
	if len(grants) == 0 {
		return nil, unprocessable("grants_required", "choose at least one project or environment, or make the integration available to the whole team")
	}
	if len(grants) > maxGrants {
		return nil, unprocessable("invalid_grants", fmt.Sprintf("at most %d projects and environments", maxGrants))
	}
	whole := map[uuid.UUID]bool{}
	for _, g := range grants {
		if g.EnvironmentID == nil {
			whole[g.ProjectID] = true
		}
	}
	type key struct{ p, e uuid.UUID }
	seen := map[key]bool{}
	var out []ScopeGrant
	for _, g := range grants {
		if _, err := q.SecretGrantProject(ctx, db.SecretGrantProjectParams{ID: g.ProjectID, WorkspaceID: ws}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, unprocessable("invalid_grant", fmt.Sprintf("project %s was not found in this team", g.ProjectID))
			}
			return nil, fmt.Errorf("check grant project: %w", err)
		}
		k := key{p: g.ProjectID}
		if g.EnvironmentID != nil {
			if _, err := q.SecretGrantEnvironment(ctx, db.SecretGrantEnvironmentParams{ID: *g.EnvironmentID, ProjectID: g.ProjectID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, unprocessable("invalid_grant", fmt.Sprintf("environment %s was not found in project %s", *g.EnvironmentID, g.ProjectID))
				}
				return nil, fmt.Errorf("check grant environment: %w", err)
			}
			if whole[g.ProjectID] {
				continue
			}
			k.e = *g.EnvironmentID
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ScopeGrant{ProjectID: g.ProjectID, EnvironmentID: g.EnvironmentID})
	}
	return out, nil
}

// storeGrants replaces the grants of an integration.
func storeGrants(ctx context.Context, q *db.Queries, id uuid.UUID, grants []ScopeGrant) error {
	if err := q.DeleteSecretIntegrationGrants(ctx, id); err != nil {
		return fmt.Errorf("clear integration grants: %w", err)
	}
	for _, g := range grants {
		if err := q.InsertSecretIntegrationGrant(ctx, db.InsertSecretIntegrationGrantParams{
			IntegrationID: id, ProjectID: g.ProjectID, EnvironmentID: g.EnvironmentID,
		}); err != nil {
			return fmt.Errorf("store integration grant: %w", err)
		}
	}
	return nil
}

// grantMeta renders grants for an audit entry: ids only.
func grantMeta(grants []ScopeGrant) []map[string]string {
	out := make([]map[string]string, 0, len(grants))
	for _, g := range grants {
		m := map[string]string{"projectId": g.ProjectID.String()}
		if g.EnvironmentID != nil {
			m["environmentId"] = g.EnvironmentID.String()
		}
		out = append(out, m)
	}
	return out
}

// Create adds an integration. Nothing contacts the manager until Test or the
// first deploy that references it.
func (s *Service) Create(ctx context.Context, in Input) (SecretManager, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return SecretManager{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if !nameRe.MatchString(in.Name) {
		return SecretManager{}, unprocessable("invalid_name", "name is 1-63 lowercase letters, digits and dashes (it appears in references as vault:<name>/… or doppler:<name>/…)")
	}
	cfg, err := normalize(in.Kind, in.Config)
	if err != nil {
		return SecretManager{}, err
	}
	if in.Secret == "" {
		return SecretManager{}, unprocessable("secret_required", "the credential is required")
	}
	if err := checkSecret(in.Secret); err != nil {
		return SecretManager{}, err
	}
	if in.Scope == "" {
		in.Scope = ScopeTeam
	}
	id := uuid.New()
	sealed, err := s.box.SealString(secretbox.SecretIntegrationsCredentials, id.String(), in.Secret)
	if err != nil {
		return SecretManager{}, fmt.Errorf("seal credential: %w", err)
	}
	cfgJSON, _ := json.Marshal(cfg)
	ws := policy.WorkspaceID(ctx)
	var row db.SecretIntegration
	var grants []ScopeGrant
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		stored, err := checkScope(ctx, q, ws, in.Scope, in.Grants)
		if err != nil {
			return err
		}
		row, err = q.InsertSecretIntegration(ctx, db.InsertSecretIntegrationParams{
			ID: id, WorkspaceID: ws, Name: in.Name, Kind: in.Kind, Config: cfgJSON, CredentialsEnc: sealed, Scope: in.Scope,
		})
		if err != nil {
			return err
		}
		if err := storeGrants(ctx, q, id, stored); err != nil {
			return err
		}
		if grants, err = s.grantsOf(ctx, q, ws, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "secret_manager.create", ResourceType: "secret_manager", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": in.Name, "kind": in.Kind, "scope": in.Scope, "grants": grantMeta(stored)},
		})
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return SecretManager{}, apierr.Conflict("name_taken", fmt.Sprintf("an integration named %q already exists", in.Name))
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return SecretManager{}, err
		}
		return SecretManager{}, fmt.Errorf("create secret integration: %w", err)
	}
	return view(row, grants), nil
}

// grantsOf returns the grants of one integration, with names.
func (s *Service) grantsOf(ctx context.Context, q *db.Queries, ws, id uuid.UUID) ([]ScopeGrant, error) {
	all, err := s.grantsByIntegration(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	return all[id], nil
}

// sameGrants reports whether two grant sets allow the same projects and environments.
func sameGrants(a, b []ScopeGrant) bool {
	key := func(g ScopeGrant) string {
		if g.EnvironmentID == nil {
			return g.ProjectID.String()
		}
		return g.ProjectID.String() + "/" + g.EnvironmentID.String()
	}
	set := map[string]bool{}
	for _, g := range a {
		set[key(g)] = true
	}
	if len(set) != len(b) {
		return false
	}
	for _, g := range b {
		if !set[key(g)] {
			return false
		}
	}
	return true
}

// Update changes the connection settings, the scope and, when Secret is given,
// the credential. The name and kind are fixed: references use the name.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in Input) (SecretManager, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return SecretManager{}, err
	}
	ws := policy.WorkspaceID(ctx)
	var out db.SecretIntegration
	var grants []ScopeGrant
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := s.get(ctx, q, id)
		if err != nil {
			return err
		}
		if in.Kind != "" && in.Kind != cur.Kind {
			return unprocessable("kind_fixed", "the kind of an integration cannot change; create a new one")
		}
		cfg, err := normalize(cur.Kind, in.Config)
		if err != nil {
			return err
		}
		var old SecretManagerConfig
		_ = json.Unmarshal(cur.Config, &old)
		sealed := cur.CredentialsEnc
		changed := []string{}
		switch {
		case in.Secret != "":
			if err := checkSecret(in.Secret); err != nil {
				return err
			}
			if sealed, err = s.box.SealString(secretbox.SecretIntegrationsCredentials, cur.ID.String(), in.Secret); err != nil {
				return fmt.Errorf("seal credential: %w", err)
			}
			changed = append(changed, "secret")
		case identityChanged(cur.Kind, old, cfg):
			return unprocessable("secret_required", "changing the address, auth method or identity requires the credential again")
		}
		cfgJSON, _ := json.Marshal(cfg)
		if string(cfgJSON) != string(mustJSON(old)) {
			changed = append(changed, "config")
		}
		scope := cur.Scope
		before, err := s.grantsOf(ctx, q, ws, id)
		if err != nil {
			return err
		}
		meta := map[string]any{"name": cur.Name, "kind": cur.Kind}
		if in.Scope != "" {
			stored, err := checkScope(ctx, q, ws, in.Scope, in.Grants)
			if err != nil {
				return err
			}
			if in.Scope != cur.Scope || !sameGrants(before, stored) {
				changed = append(changed, "scope")
				meta["scopeFrom"], meta["scope"], meta["grants"] = cur.Scope, in.Scope, grantMeta(stored)
				if err := storeGrants(ctx, q, id, stored); err != nil {
					return err
				}
			}
			scope = in.Scope
		}
		if out, err = q.UpdateSecretIntegration(ctx, db.UpdateSecretIntegrationParams{
			ID: id, WorkspaceID: ws, Kind: cur.Kind, Config: cfgJSON, CredentialsEnc: sealed, Scope: scope,
		}); err != nil {
			return err
		}
		if grants, err = s.grantsOf(ctx, q, ws, id); err != nil {
			return err
		}
		meta["changed"] = changed
		return audit.Record(ctx, tx, audit.Entry{
			Action: "secret_manager.update", ResourceType: "secret_manager", ResourceID: id.String(), Metadata: meta,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return SecretManager{}, err
		}
		return SecretManager{}, fmt.Errorf("update secret integration: %w", err)
	}
	return view(out, grants), nil
}

func mustJSON(c SecretManagerConfig) []byte {
	b, _ := json.Marshal(c)
	return b
}

// Delete removes an integration. It is refused while a variable references it.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return err
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := s.get(ctx, q, id)
		if err != nil {
			return err
		}
		users, err := s.usage(ctx, q, cur.WorkspaceID, cur.Name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			shown := users
			if len(shown) > 5 {
				shown = shown[:5]
			}
			return apierr.Conflict("integration_in_use", fmt.Sprintf("%d variable(s) reference %q (%s); remove those references first", len(users), cur.Name, strings.Join(shown, ", "))).
				WithDetails(map[string]any{"variables": users})
		}
		if _, err := q.DeleteSecretIntegration(ctx, db.DeleteSecretIntegrationParams{ID: id, WorkspaceID: cur.WorkspaceID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "secret_manager.delete", ResourceType: "secret_manager", ResourceID: id.String(),
			Metadata: map[string]any{"name": cur.Name, "kind": cur.Kind},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete secret integration: %w", err)
	}
	return nil
}

// usage lists the variables ("service-slug KEY", "team KEY") whose value
// references the integration.
func (s *Service) usage(ctx context.Context, q *db.Queries, ws uuid.UUID, name string) ([]string, error) {
	uses := func(value string) bool {
		tpl, err := varexpr.Parse(value)
		if err != nil {
			return false
		}
		for _, r := range tpl.ExtRefs() {
			if r.Integration == name {
				return true
			}
		}
		return false
	}
	var out []string
	envs, err := q.ListWorkspaceEnvInfos(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		svcs, err := q.ListAllEnvironmentServices(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		slug := map[uuid.UUID]string{}
		for _, sv := range svcs {
			slug[sv.ID] = e.ProjectName + "/" + e.Name + "/" + sv.Slug
		}
		vars, err := q.ListEnvironmentVariables(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range vars {
			val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(v.ServiceID.String(), v.Key), v.ValueEnc)
			if err != nil {
				return nil, err
			}
			if uses(val) {
				out = append(out, slug[v.ServiceID]+" "+v.Key)
			}
		}
	}
	shared, err := q.ListWorkspaceSharedVariables(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, v := range shared {
		val, err := s.box.OpenString(secretbox.SharedVariablesValue, v.ID.String(), v.ValueEnc)
		if err != nil {
			return nil, err
		}
		if uses(val) {
			layer := "team"
			switch {
			case v.EnvironmentID != nil:
				layer = "environment shared"
			case v.ProjectID != nil:
				layer = "project shared"
			}
			out = append(out, layer+" "+v.Key)
		}
	}
	sort.Strings(out)
	return out, nil
}

// TestInput optionally names a secret to read, to check the path and the
// permissions end to end.
type TestInput struct {
	Path string
	Key  string
}

// SecretManagerTestResult is the outcome of a connection test.
type SecretManagerTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	// Read is true when a secret was read (the value is never returned).
	Read bool `json:"read"`
}

// testRef builds the reference a test reads, in the syntax of the integration's
// kind: `vault:<name>/<path>#<key>` or `doppler:<name>[/<project>/<config>]#<key>`.
func testRef(row db.SecretIntegration, in TestInput) (*varexpr.ExtRef, error) {
	path, key := strings.Trim(strings.TrimSpace(in.Path), "/"), strings.TrimSpace(in.Key)
	if path == "" && key == "" {
		return nil, nil
	}
	var raw string
	if row.Kind == KindDoppler {
		raw = "doppler:" + row.Name
		if path != "" {
			raw += "/" + path
		}
		raw += "#" + key
	} else {
		raw = fmt.Sprintf("vault:%s/%s#%s", row.Name, path, key)
	}
	r, err := varexpr.ParseExtRef(raw)
	if err != nil {
		return nil, unprocessable("invalid_reference", err.Error())
	}
	return &r, nil
}

// Test authenticates to the manager and, when in names a secret, reads it. The
// outcome is stored on the integration; a secret's value is discarded. A read is
// audited with the reference.
func (s *Service) Test(ctx context.Context, id uuid.UUID, in TestInput) (SecretManagerTestResult, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return SecretManagerTestResult{}, err
	}
	row, err := s.get(ctx, s.q, id)
	if err != nil {
		return SecretManagerTestResult{}, err
	}
	ref, err := testRef(row, in)
	if err != nil {
		return SecretManagerTestResult{}, err
	}
	p, err := s.providerFor(row)
	if err != nil {
		return SecretManagerTestResult{}, err
	}
	res := SecretManagerTestResult{}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout+5*time.Second)
	defer cancel()
	if err := p.check(ctx); err != nil {
		res.Message = err.Error()
	} else if ref != nil {
		if _, err := p.fetch(ctx, *ref); err != nil {
			res.Message = fmt.Sprintf("signed in, but reading ${{%s}} failed: %s", ref, err)
		} else {
			res.OK, res.Read, res.Message = true, true, fmt.Sprintf("Signed in and read ${{%s}}.", ref)
		}
	} else {
		res.OK, res.Message = true, "Signed in. Enter a path and key to check read access to a secret."
	}
	errText := ""
	if !res.OK {
		errText = res.Message
	}
	meta := map[string]any{"name": row.Name, "ok": res.OK, "read": res.Read}
	if ref != nil {
		meta["reference"] = ref.String()
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).SetSecretIntegrationTest(ctx, db.SetSecretIntegrationTestParams{ID: id, LastTestOk: &res.OK, LastTestError: errText}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "secret_manager.test", ResourceType: "secret_manager", ResourceID: id.String(), Metadata: meta,
		})
	})
	if err != nil {
		return SecretManagerTestResult{}, fmt.Errorf("record test: %w", err)
	}
	return res, nil
}

func (s *Service) providerFor(r db.SecretIntegration) (provider, error) {
	var cfg SecretManagerConfig
	if err := json.Unmarshal(r.Config, &cfg); err != nil {
		return nil, fmt.Errorf("decode integration %s: %w", r.Name, err)
	}
	secret, err := s.box.OpenString(secretbox.SecretIntegrationsCredentials, r.ID.String(), r.CredentialsEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential of %s: %w", r.Name, err)
	}
	p, err := newProvider(r.Kind, cfg, secret)
	if err != nil {
		return nil, fmt.Errorf("integration %s: %w", r.Name, err)
	}
	return p, nil
}

// Fetcher implements services.ExternalSecrets: a fetcher for one operation on one
// service. It refuses references to integrations the service's project and
// environment may not use (before any request leaves), logs in at most once per
// integration and reads each secret once; everything it learned is dropped with
// it. Flush records what it read in the audit log.
func (s *Service) Fetcher(scope services.ExtScope) services.ExtFetcher {
	return &session{s: s, scope: scope, integrations: map[string]*sessionIntegration{}, values: map[string]string{}, noted: map[string]bool{}}
}

// sessionIntegration is one integration as a session loaded it. The checks that
// depend on the reference (its kind, the wording of an error) run per reference.
type sessionIntegration struct {
	row db.SecretIntegration
	p   provider
	// denied is set when the integration's scope refuses the session's project
	// and environment: nothing is ever requested from the manager.
	denied bool
	// unusable is why the stored credentials cannot be used.
	unusable error
}

type session struct {
	s     *Service
	scope services.ExtScope

	mu           sync.Mutex
	integrations map[string]*sessionIntegration
	values       map[string]string

	// The access log: references read, refused by scope, or that failed. noted
	// dedupes them (a failed reference may be asked for repeatedly).
	noted  map[string]bool
	read   []string
	denied []string
	failed []failedRef
}

type failedRef struct {
	Reference string `json:"reference"`
	Error     string `json:"error"`
}

var _ services.ExtFlusher = (*session)(nil)

// integration loads the named integration for the session (once) and checks,
// for this reference, that the session's project and environment may use it and
// that its kind takes this kind of reference.
func (se *session) integration(ctx context.Context, ref varexpr.ExtRef) (*sessionIntegration, error) {
	it, ok := se.integrations[ref.Integration]
	if !ok {
		row, err := se.s.q.GetSecretIntegrationByName(ctx, db.GetSecretIntegrationByNameParams{Name: ref.Integration, WorkspaceID: se.scope.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, refError(ref, fmt.Sprintf("no secret manager integration named %q (Team settings → Integrations → Secret managers)", ref.Integration))
		}
		if err != nil {
			return nil, fmt.Errorf("load integration %s: %w", ref.Integration, err)
		}
		it = &sessionIntegration{row: row}
		if row.Scope == ScopeSelected {
			allowed, err := se.s.q.SecretIntegrationAllowed(ctx, db.SecretIntegrationAllowedParams{
				ID: row.ID, ProjectID: se.scope.ProjectID, EnvironmentID: se.scope.EnvironmentID,
			})
			if err != nil {
				return nil, fmt.Errorf("check integration scope %s: %w", ref.Integration, err)
			}
			it.denied = !allowed
		}
		if !it.denied {
			it.p, it.unusable = se.s.providerFor(row)
		}
		se.integrations[ref.Integration] = it
	}
	switch {
	case it.denied:
		return it, apierr.Unprocessable("integration_not_allowed",
			fmt.Sprintf("reference ${{%s}}: the secret manager integration %q is not available to %s (an admin can allow it under Team settings → Integrations → Secret managers)", ref, ref.Integration, se.where())).
			WithDetails(map[string]any{"integration": ref.Integration, "reference": ref.String()})
	case !services.ExtKindMatches(ref, it.row.Kind):
		return it, refError(ref, fmt.Sprintf("the integration %q is a %s integration, which does not take %s: references", ref.Integration, it.row.Kind, ref.SchemeName()))
	case it.unusable != nil:
		return it, refError(ref, "the integration cannot be used: "+it.unusable.Error())
	}
	return it, nil
}

// where names the project and environment of the session for a message.
func (se *session) where() string {
	if se.scope.Project != "" && se.scope.Environment != "" {
		return fmt.Sprintf("project %q environment %q", se.scope.Project, se.scope.Environment)
	}
	return "this project and environment"
}

// note adds id to list once.
func (se *session) note(list *[]string, kind, id string) {
	if k := kind + "\x00" + id; !se.noted[k] {
		se.noted[k] = true
		*list = append(*list, id)
	}
}

// noteFailed records a reference that could not be read, once.
func (se *session) noteFailed(id string, err error) {
	if k := "failed\x00" + id; !se.noted[k] {
		se.noted[k] = true
		se.failed = append(se.failed, failedRef{Reference: id, Error: err.Error()})
	}
}

// Fetch reads one secret. Errors are 422 `external_secret_failed` (or
// `integration_not_allowed` when scoping refused the integration) and start with
// the reference.
func (se *session) Fetch(ctx context.Context, ref varexpr.ExtRef) (string, error) {
	se.mu.Lock()
	defer se.mu.Unlock()
	id := ref.String()
	if v, ok := se.values[id]; ok {
		return v, nil
	}
	it, err := se.integration(ctx, ref)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Code == "integration_not_allowed" {
			se.note(&se.denied, "denied", id)
		} else {
			se.noteFailed(id, err)
		}
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*requestTimeout)
	defer cancel()
	v, err := it.p.fetch(ctx, ref)
	if err != nil {
		se.noteFailed(id, err)
		return "", refError(ref, err.Error()).WithDetails(map[string]any{"integration": ref.Integration, "reference": id})
	}
	se.values[id] = v
	se.note(&se.read, "read", id)
	return v, nil
}

// Flush records the session's access in the audit log, once: one event for
// the operation, with the references it read, was refused or failed to read.
// It names the service, the purpose and (for a deploy) the deployment and who
// started it; never a value. The write survives a cancelled or timed-out
// operation, and a failure to write is logged, not returned.
func (se *session) Flush(ctx context.Context) {
	se.mu.Lock()
	read, denied, failed := se.read, se.denied, se.failed
	se.read, se.denied, se.failed = nil, nil, nil
	se.noted = map[string]bool{}
	se.mu.Unlock()
	if len(read)+len(denied)+len(failed) == 0 {
		return
	}
	sort.Strings(read)
	sort.Strings(denied)
	meta := map[string]any{
		"service": se.scope.Service, "project": se.scope.Project, "environment": se.scope.Environment,
		"purpose": se.scope.Purpose, "count": len(read),
	}
	if len(read) > 0 {
		meta["references"] = read
	}
	if len(denied) > 0 {
		meta["denied"] = denied
	}
	if len(failed) > 0 {
		meta["failed"] = failed
	}
	if se.scope.DeploymentID != "" {
		meta["deployment"] = se.scope.DeploymentID
	}
	if se.scope.StartedBy != "" {
		meta["startedBy"] = se.scope.StartedBy
	}
	action := ActionFetch
	if len(denied) > 0 {
		action = ActionFetchDenied
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	projectID := se.scope.ProjectID
	err := db.InTx(ctx, se.s.pool, func(tx pgx.Tx) error {
		e := audit.Entry{
			Action: action, ResourceType: "service", ResourceID: se.scope.ServiceID.String(),
			Metadata: meta, WorkspaceID: se.scope.WorkspaceID,
		}
		if projectID != uuid.Nil {
			e.ProjectID = &projectID
		}
		return audit.Record(ctx, tx, e)
	})
	if err != nil {
		se.s.log.Error("record secret access", "service", se.scope.Service, "purpose", se.scope.Purpose, "err", err)
	}
}
