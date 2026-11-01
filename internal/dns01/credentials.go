package dns01

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DNSCredentialField is one variable of a credential as the API shows it. A secret
// variable never carries its value.
type DNSCredentialField struct {
	Env      string  `json:"env"`
	Label    string  `json:"label"`
	Secret   bool    `json:"secret"`
	Required bool    `json:"required"`
	Set      bool    `json:"set"`
	Value    *string `json:"value,omitempty" doc:"Only non-secret variables, and only for administrators"`
}

// DNSCredentialUse is a server whose resolver uses the credential.
type DNSCredentialUse struct {
	ServerID   uuid.UUID `json:"serverId"`
	ServerName string    `json:"serverName"`
}

// DNSCredential is the API view of a DNS provider credential.
type DNSCredential struct {
	ID           uuid.UUID            `json:"id"`
	Scope        string               `json:"scope" enum:"team,server" doc:"team: usable on every server of the team; server: only on ServerID"`
	ServerID     *uuid.UUID           `json:"serverId"`
	Name         string               `json:"name"`
	Provider     string               `json:"provider" pattern:"^[a-z0-9][a-z0-9-]{0,39}$" doc:"The lego provider code"`
	ProviderName string               `json:"providerName"`
	Fields       []DNSCredentialField `json:"fields"`
	Rev          int                  `json:"rev" doc:"Advances whenever the values change"`
	UsedBy       []DNSCredentialUse   `json:"usedBy"`
	CreatedAt    time.Time            `json:"createdAt"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

// CredentialInput creates a credential.
type CredentialInput struct {
	Name     string
	Provider string
	// ServerID scopes the credential to one server; nil = the whole team.
	ServerID *uuid.UUID
	// Values maps the provider's variable names to their values.
	Values map[string]string
}

// CredentialPatch updates a credential. Nil/empty fields are unchanged.
type CredentialPatch struct {
	Name *string
	// Values replaces the named variables; an empty value keeps the stored one.
	Values map[string]string
	// Clear removes optional variables.
	Clear []string
}

func requireAdmin(ctx context.Context) error { return policy.Require(ctx, policy.ScopeAdmin, nil) }

func validName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 80 {
		return "", apierr.Unprocessable("validation_failed", "name must be 1-80 characters")
	}
	return n, nil
}

// checkValues validates variable names and values against the provider catalog:
// only variables lego reads for the provider, single-line bounded values, every
// required variable and one complete authentication method where there are
// several.
func checkValues(spec agentprotocol.DNSProviderSpec, values map[string]string) error {
	known := map[string]bool{}
	for _, f := range spec.Fields {
		known[f.Env] = true
	}
	for env, v := range values {
		if !known[env] {
			hint := ""
			if spec.Generic {
				hint = fmt.Sprintf(" (lego lists its variables at %s)", spec.Docs)
			}
			return apierr.Unprocessable("invalid_credential", fmt.Sprintf("%s is not a %s credential variable%s", env, spec.Name, hint))
		}
		if err := agentprotocol.ValidateDNSValue(v); err != nil {
			return apierr.Unprocessable("invalid_credential", fmt.Sprintf("%s: %v", env, err))
		}
	}
	if err := agentprotocol.CheckDNSVariables(spec, envNames(values)); err != nil {
		return apierr.Unprocessable("invalid_credential", err.Error())
	}
	return nil
}

// normalizeValues tidies pasted values (whitespace, a service account key on
// one line) and drops variables left blank.
func normalizeValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		if v = agentprotocol.NormalizeDNSValue(k, v); v != "" {
			out[k] = v
		}
	}
	return out
}

func envNames(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for k := range values {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (s *Service) sealValues(id uuid.UUID, values map[string]string) ([]byte, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return s.box.SealString(secretbox.DNSCredentialsSecrets, id.String(), string(raw))
}

func (s *Service) openValues(id uuid.UUID, enc []byte) (map[string]string, error) {
	return openValues(s.box, id, enc)
}

func openValues(box *secretbox.Box, id uuid.UUID, enc []byte) (map[string]string, error) {
	plain, err := box.OpenString(secretbox.DNSCredentialsSecrets, id.String(), enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt dns credential: %w", err)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, fmt.Errorf("decode dns credential: %w", err)
	}
	return out, nil
}

// credentialView renders a row. Non-secret values are shown to administrators only.
func (s *Service) credentialView(ctx context.Context, c db.DnsCredential, uses []DNSCredentialUse) (DNSCredential, error) {
	spec, _ := agentprotocol.DNSProvider(c.Provider)
	v := DNSCredential{
		ID: c.ID, Scope: "team", ServerID: c.ServerID, Name: c.Name, Provider: c.Provider, ProviderName: spec.Name,
		Rev: int(c.Rev), UsedBy: nonNil(uses), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if c.ServerID != nil {
		v.Scope = "server"
	}
	showPlain := false
	if p, err := policy.Authenticated(ctx); err == nil {
		showPlain = policy.HasScope(p, policy.ScopeAdmin)
	}
	var values map[string]string
	if showPlain {
		var err error
		if values, err = s.openValues(c.ID, c.SecretsEnc); err != nil {
			return DNSCredential{}, err
		}
	}
	for _, f := range spec.Fields {
		set := slices.Contains(c.EnvNames, f.Env)
		if spec.Generic && !set {
			// A generic provider has dozens of variables; list the ones in use.
			continue
		}
		cf := DNSCredentialField{Env: f.Env, Label: f.Label, Secret: f.Secret, Required: f.Required, Set: set}
		if !f.Secret && cf.Set && showPlain {
			if val, ok := values[f.Env]; ok {
				cf.Value = &val
			}
		}
		v.Fields = append(v.Fields, cf)
	}
	return v, nil
}

// Providers returns the curated DNS providers and their credential variables.
// Every other lego provider is LegoProviders.
func (s *Service) Providers(ctx context.Context) ([]agentprotocol.DNSProviderSpec, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return nil, err
	}
	return agentprotocol.DNSProviders, nil
}

// LegoProviders returns every lego DNS provider without a curated form, for the
// "any lego provider" form: the code, the lego docs link and the variables
// lego reads for it.
func (s *Service) LegoProviders(ctx context.Context) ([]agentprotocol.DNSProviderSpec, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return nil, err
	}
	return agentprotocol.LegoProviders(), nil
}

// ListCredentials returns the team's credentials; with serverID only the team-wide
// ones and that server's.
func (s *Service) ListCredentials(ctx context.Context, serverID *uuid.UUID) ([]DNSCredential, error) {
	if err := policy.Require(ctx, policy.ScopeProxyWrite, nil); err != nil {
		return nil, err
	}
	ws := policy.WorkspaceID(ctx)
	if serverID != nil {
		if _, err := s.loadServer(ctx, s.q, policy.ScopeServersRead, *serverID); err != nil {
			return nil, err
		}
	}
	rows, err := s.q.ListDNSCredentials(ctx, db.ListDNSCredentialsParams{WorkspaceID: ws, ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("list dns credentials: %w", err)
	}
	usage, err := s.usageByCredential(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]DNSCredential, 0, len(rows))
	for _, r := range rows {
		v, err := s.credentialView(ctx, r, usage[r.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) usageByCredential(ctx context.Context, ws uuid.UUID) (map[uuid.UUID][]DNSCredentialUse, error) {
	rows, err := s.q.DNSCredentialUsage(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("dns credential usage: %w", err)
	}
	out := map[uuid.UUID][]DNSCredentialUse{}
	for _, r := range rows {
		out[r.CredentialID] = append(out[r.CredentialID], DNSCredentialUse{ServerID: r.ServerID, ServerName: r.ServerName})
	}
	return out, nil
}

// CreateCredential seals and stores a credential. Administrators only.
func (s *Service) CreateCredential(ctx context.Context, in CredentialInput) (DNSCredential, error) {
	if err := requireAdmin(ctx); err != nil {
		return DNSCredential{}, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return DNSCredential{}, err
	}
	spec, ok := agentprotocol.DNSProvider(in.Provider)
	if !ok {
		return DNSCredential{}, providerError(in.Provider)
	}
	in.Values = normalizeValues(in.Values)
	if err := checkValues(spec, in.Values); err != nil {
		return DNSCredential{}, err
	}
	ws := policy.WorkspaceID(ctx)
	if in.ServerID != nil {
		if _, err := s.loadServer(ctx, s.q, policy.ScopeAdmin, *in.ServerID); err != nil {
			return DNSCredential{}, err
		}
	}
	credID := uuid.New()
	sealed, err := s.sealValues(credID, in.Values)
	if err != nil {
		return DNSCredential{}, fmt.Errorf("seal dns credential: %w", err)
	}
	var row db.DnsCredential
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertDNSCredential(ctx, db.InsertDNSCredentialParams{
			ID: credID, WorkspaceID: ws, ServerID: in.ServerID, Name: name, Provider: in.Provider, SecretsEnc: sealed, EnvNames: envNames(in.Values),
		})
		if err != nil {
			return err
		}
		// Variable names only: never a value.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "dns_credential.create", ResourceType: "dns_credential", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": name, "provider": in.Provider, "scope": scopeOf(in.ServerID), "variables": envNames(in.Values)},
		})
	})
	if isUnique(err) {
		return DNSCredential{}, apierr.Conflict("credential_name_taken", fmt.Sprintf("a credential named %q already exists", name))
	}
	if err != nil {
		return DNSCredential{}, fmt.Errorf("create dns credential: %w", err)
	}
	return s.credentialView(ctx, row, nil)
}

func scopeOf(serverID *uuid.UUID) string {
	if serverID == nil {
		return "team"
	}
	return "server"
}

// UpdateCredential renames a credential and/or changes variables. Changed
// values advance rev and stage "Deploy proxy" on every server whose resolver
// uses it. Administrators only.
func (s *Service) UpdateCredential(ctx context.Context, id uuid.UUID, in CredentialPatch) (DNSCredential, error) {
	if err := requireAdmin(ctx); err != nil {
		return DNSCredential{}, err
	}
	ws := policy.WorkspaceID(ctx)
	cur, err := s.q.GetDNSCredential(ctx, db.GetDNSCredentialParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return DNSCredential{}, apierr.NotFound("credential")
	}
	if err != nil {
		return DNSCredential{}, fmt.Errorf("get dns credential: %w", err)
	}
	spec, _ := agentprotocol.DNSProvider(cur.Provider)
	name := cur.Name
	if in.Name != nil {
		if name, err = validName(*in.Name); err != nil {
			return DNSCredential{}, err
		}
	}
	var sealed []byte
	var names []string
	if len(in.Values) > 0 || len(in.Clear) > 0 {
		values, err := s.openValues(cur.ID, cur.SecretsEnc)
		if err != nil {
			return DNSCredential{}, err
		}
		next := make(map[string]string, len(values))
		for k, v := range values {
			next[k] = v
		}
		for k, v := range normalizeValues(in.Values) {
			next[k] = v
		}
		for _, k := range in.Clear {
			delete(next, k)
		}
		if err := checkValues(spec, next); err != nil {
			return DNSCredential{}, err
		}
		if !mapsEqual(values, next) {
			if sealed, err = s.sealValues(cur.ID, next); err != nil {
				return DNSCredential{}, fmt.Errorf("seal dns credential: %w", err)
			}
			names = envNames(next)
		}
	}
	var row db.DnsCredential
	var staged []uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		row, err = q.UpdateDNSCredential(ctx, db.UpdateDNSCredentialParams{ID: id, WorkspaceID: ws, Name: name, SecretsEnc: sealed, EnvNames: names})
		if err != nil {
			return err
		}
		if sealed != nil {
			uses, err := q.DNSCredentialUsage(ctx, ws)
			if err != nil {
				return err
			}
			for _, u := range uses {
				if u.CredentialID == id {
					if err := q.BumpServerProxyGeneration(ctx, u.ServerID); err != nil {
						return err
					}
					staged = append(staged, u.ServerID)
				}
			}
		}
		meta := map[string]any{"name": name, "provider": cur.Provider, "valuesChanged": sealed != nil}
		if sealed != nil {
			meta["variables"] = names
		}
		return audit.Record(ctx, tx, audit.Entry{Action: "dns_credential.update", ResourceType: "dns_credential", ResourceID: id.String(), Metadata: meta})
	})
	if isUnique(err) {
		return DNSCredential{}, apierr.Conflict("credential_name_taken", fmt.Sprintf("a credential named %q already exists", name))
	}
	if err != nil {
		return DNSCredential{}, fmt.Errorf("update dns credential: %w", err)
	}
	for _, sid := range staged {
		s.publish(sid)
	}
	usage, err := s.usageByCredential(ctx, ws)
	if err != nil {
		return DNSCredential{}, err
	}
	return s.credentialView(ctx, row, usage[id])
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// DeleteCredential removes a credential that no resolver (desired or applied)
// uses. Administrators only.
func (s *Service) DeleteCredential(ctx context.Context, id uuid.UUID) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	ws := policy.WorkspaceID(ctx)
	cur, err := s.q.GetDNSCredential(ctx, db.GetDNSCredentialParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("credential")
	}
	if err != nil {
		return fmt.Errorf("get dns credential: %w", err)
	}
	usage, err := s.usageByCredential(ctx, ws)
	if err != nil {
		return err
	}
	if u := usage[id]; len(u) > 0 {
		return apierr.Conflict("credential_in_use", "the resolver of "+u[0].ServerName+" uses this credential; remove or change the resolver first")
	}
	applied, err := s.q.DNSCredentialAppliedOn(ctx, db.DNSCredentialAppliedOnParams{WorkspaceID: ws, CredentialID: id.String()})
	if err != nil {
		return fmt.Errorf("dns credential applied on: %w", err)
	}
	if len(applied) > 0 {
		return apierr.Conflict("credential_in_use", "the proxy of "+applied[0].Name+" still runs with this credential; deploy the proxy without the resolver first")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeleteDNSCredential(ctx, db.DeleteDNSCredentialParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "dns_credential.delete", ResourceType: "dns_credential", ResourceID: id.String(),
			Metadata: map[string]any{"name": cur.Name, "provider": cur.Provider},
		})
	})
	if err != nil {
		return fmt.Errorf("delete dns credential: %w", err)
	}
	return nil
}
