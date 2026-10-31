package clone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// suffixRe is a valid domain suffix: a hyphen, then letters, digits and hyphens.
var suffixRe = regexp.MustCompile(`^-[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// normalizeSuffix returns the suffix to use: the caller's (lowercased, with a
// leading hyphen added), else def.
func normalizeSuffix(in, def string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	if s == "" {
		s = def
	}
	if !strings.HasPrefix(s, "-") {
		s = "-" + s
	}
	if !suffixRe.MatchString(s) {
		return "", apierr.Unprocessable("validation_failed", "domainSuffix may only contain letters, digits and hyphens")
	}
	return s, nil
}

// normalizeMode validates the domain mode (default drop).
func normalizeMode(m string) (string, error) {
	switch m {
	case "", DomainsDrop:
		return DomainsDrop, nil
	case DomainsSuffix:
		return DomainsSuffix, nil
	}
	return "", apierr.Unprocessable("validation_failed", "domains must be drop or suffix")
}

// pendingRef is a reference found in a cloned variable, resolved once every
// cloned service exists.
type pendingRef struct {
	service, key string
	ref          varexpr.Ref
}

// run is one clone inside one transaction.
type run struct {
	s      *Service
	ctx    context.Context
	tx     pgx.Tx
	q      *db.Queries
	target services.EnvScope
	mode   string
	suffix string
	server *uuid.UUID
	res    *CloneResult

	taken     map[string]bool // hostnames claimed by this run
	conflicts []string
	refs      []pendingRef
}

func (s *Service) newRun(ctx context.Context, tx pgx.Tx, target services.EnvScope, mode, suffix string, server *uuid.UUID, res *CloneResult) *run {
	return &run{
		s: s, ctx: ctx, tx: tx, q: db.New(tx), target: target, mode: mode, suffix: suffix, server: server, res: res,
		taken: map[string]bool{},
	}
}

// createRow creates the service row of src in the target environment: the row,
// its sealed credentials and, for databases, a fresh database. It returns false
// when src was skipped (the reason is in the result). keepSlug gives the clone
// the source's slug (used for whole environments, where it is free).
func (r *run) createRow(src db.Service, name string, keepSlug bool) (db.Service, bool, error) {
	if why := cloneable(src); why != "" {
		r.res.Skipped = append(r.res.Skipped, CloneSkipped{Slug: src.Slug, Reason: why})
		return db.Service{}, false, nil
	}
	cfg := services.DecodeConfig(src.Config)
	server := src.ServerID
	if r.server != nil {
		server = r.server
	}
	var row db.Service
	fresh := cfg.Database != nil
	if fresh {
		if r.s.Databases == nil {
			return row, false, apierr.Unprocessable("databases_unavailable", "this server build cannot create managed databases")
		}
		var err error
		row, err = r.s.Databases.CreateTx(r.ctx, r.tx, databases.CreateInput{
			EnvironmentID: r.target.Env.ID, Name: name, Engine: cfg.Database.Engine, Version: cfg.Database.Version,
			Flavor: cfg.Database.Flavor, DatabaseName: cfg.Database.DatabaseName, Username: cfg.Database.Username, ServerID: server, AcceptLicense: true,
		})
		if err != nil {
			return row, false, fmt.Errorf("clone database %s: %w", src.Slug, err)
		}
	} else {
		cfg.Tailnet = nil // one tagged tailnet device per service: the clone does not take the source's
		var err error
		row, err = r.s.Services.CreateInTx(r.ctx, r.tx, r.target, services.CreateInput{
			Name: name, Kind: src.Kind, SourceType: src.SourceType, Config: cfg, ServerID: server,
		})
		if err != nil {
			return row, false, err
		}
	}
	if keepSlug && row.Slug != src.Slug {
		if err := r.q.UpdateServiceSlug(r.ctx, db.UpdateServiceSlugParams{ID: row.ID, Slug: src.Slug}); err != nil {
			return row, false, fmt.Errorf("set slug: %w", err)
		}
		row.Slug = src.Slug
	}
	if fresh {
		// Keep the tuning of the source (limits, Docker options, egress, ...);
		// the database section is the one the new database was created with.
		newCfg := cfg
		newCfg.Database = services.DecodeConfig(row.Config).Database
		newCfg.Tailnet = nil
		raw, err := json.Marshal(newCfg)
		if err != nil {
			return row, false, err
		}
		if row, err = r.q.SetServiceConfigBumped(r.ctx, db.SetServiceConfigBumpedParams{ID: row.ID, Config: raw}); err != nil {
			return row, false, fmt.Errorf("set config: %w", err)
		}
		if keepSlug {
			row.Slug = src.Slug
		}
	} else {
		// Sealed credentials are bound to their row: open with the source's id and
		// seal again for the clone's.
		gitTok, err := r.reseal(secretbox.ServicesGitToken, src.ID.String(), row.ID.String(), src.GitTokenEnc)
		if err != nil {
			return row, false, fmt.Errorf("copy git token of %s: %w", src.Slug, err)
		}
		regAuth, err := r.reseal(secretbox.ServicesRegistryAuth, src.ID.String(), row.ID.String(), src.RegistryAuthEnc)
		if err != nil {
			return row, false, fmt.Errorf("copy registry auth of %s: %w", src.Slug, err)
		}
		if gitTok != nil || regAuth != nil {
			if err := r.q.SetServiceCredentials(r.ctx, db.SetServiceCredentialsParams{ID: row.ID, GitTokenEnc: gitTok, RegistryAuthEnc: regAuth}); err != nil {
				return row, false, fmt.Errorf("store credentials: %w", err)
			}
		}
	}
	r.res.Services = append(r.res.Services, ClonedService{SourceID: src.ID, ID: row.ID, Name: row.Name, Slug: row.Slug, Kind: row.Kind, Fresh: fresh})
	if fresh {
		r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("%s is a new, empty database with new credentials. Restore a dump into it to bring the data over (Backups tab, Restore from a file).", row.Name))
	}
	return row, true, nil
}

// reseal opens a sealed column value of one row and seals it for another row
// of the same column. Empty input stays empty.
func (r *run) reseal(col secretbox.Column, fromRow, toRow string, enc []byte) ([]byte, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	plain, err := r.s.Box.Open(col, fromRow, enc)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(plain)
	return r.s.Box.Seal(col, toRow, plain)
}

// fill copies what hangs off a service row: volumes, variables, the repository
// link and the domains. slugMap rewrites references (old slug to new slug) in
// the variables of the clone.
func (r *run) fill(src, row db.Service, slugMap map[string]string) error {
	isDB := services.DecodeConfig(src.Config).Database != nil
	if !isDB {
		// New, empty volumes: only the definitions (name, mount path) are copied.
		if err := r.q.CloneServiceVolumes(r.ctx, db.CloneServiceVolumesParams{NewServiceID: row.ID, OldServiceID: src.ID}); err != nil {
			return fmt.Errorf("clone volumes: %w", err)
		}
	}
	if err := r.copyVariables(src, row, slugMap, isDB); err != nil {
		return err
	}
	if src.SourceType == services.SourceGit {
		if err := r.copyGit(src, row); err != nil {
			return err
		}
	}
	return r.copyDomains(src, row)
}

// copyVariables copies the variables of src to row. Generated variables are
// generated again. A database keeps only the variables its creation did not
// already write (its credentials are new).
func (r *run) copyVariables(src, row db.Service, slugMap map[string]string, isDB bool) error {
	vars, err := r.q.ListServiceVariables(r.ctx, src.ID)
	if err != nil {
		return fmt.Errorf("list variables: %w", err)
	}
	have := map[string]bool{}
	if isDB {
		cur, err := r.q.ListServiceVariables(r.ctx, row.ID)
		if err != nil {
			return fmt.Errorf("list variables: %w", err)
		}
		for _, v := range cur {
			have[v.Key] = true
		}
	}
	sc := services.ScopeOf(r.target, row)
	for _, v := range vars {
		if isDB && (have[v.Key] || v.Source == services.SourceGenerated) {
			continue
		}
		val, err := r.s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(src.ID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return fmt.Errorf("open %s.%s: %w", src.Slug, v.Key, err)
		}
		w := services.VarWrite{Key: v.Key, Value: rewriteRefs(val, slugMap), Secret: v.IsSecret, Scope: v.Scope, Source: v.Source}
		comment := v.Comment
		w.Comment = &comment
		if v.RotateEveryDays != nil {
			n := int(*v.RotateEveryDays)
			w.RotateEveryDays = &n
		}
		if len(v.TemplateEnc) > 0 {
			tmpl, err := r.s.Box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(src.ID.String(), v.Key), v.TemplateEnc)
			if err != nil {
				return fmt.Errorf("open template of %s.%s: %w", src.Slug, v.Key, err)
			}
			tmpl = rewriteRefs(tmpl, slugMap)
			// A generated value is generated again: a clone never shares a secret
			// with its source.
			if m, err := varexpr.Materialize(tmpl); err == nil {
				w.Value, w.Template, w.GenCalls, w.Secret = m.Stored, tmpl, m.Calls, v.IsSecret || m.Secret
			}
		}
		if _, err := services.WriteServiceVariable(r.ctx, r.q, r.s.Box, sc, w); err != nil {
			return fmt.Errorf("write %s.%s: %w", row.Slug, v.Key, err)
		}
		r.collectRefs(row.Slug, v.Key, w.Value)
	}
	return nil
}

// rewriteRefs applies the slug map (old to new) to every reference in value.
func rewriteRefs(value string, slugMap map[string]string) string {
	for from, to := range slugMap {
		if from != to {
			value, _ = varexpr.RewriteRefSlug(value, from, to)
		}
	}
	return value
}

func (r *run) collectRefs(service, key, value string) {
	t, err := varexpr.Parse(value)
	if err != nil {
		return
	}
	for _, ref := range t.Refs() {
		r.refs = append(r.refs, pendingRef{service: service, key: key, ref: ref})
	}
}

// copyGit links a git clone to its source's repository (auto-deploy off) and
// gives it its own copy of the deploy key and the pinned host keys.
func (r *run) copyGit(src, row db.Service) error {
	if r.s.Git != nil {
		if err := r.s.Git.CopyServiceGitTx(r.ctx, r.tx, src.ID, row.ID); err != nil {
			return fmt.Errorf("link %s to its repository: %w", row.Slug, err)
		}
	}
	k, err := r.q.GetServiceSSHKey(r.ctx, src.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("get deploy key: %w", err)
	}
	enc, err := r.reseal(secretbox.ServiceSSHKeysPrivateKey, src.ID.String(), row.ID.String(), k.PrivateKeyEnc)
	if err != nil {
		return fmt.Errorf("copy deploy key: %w", err)
	}
	if err := r.q.InsertServiceSSHKeyIfAbsent(r.ctx, db.InsertServiceSSHKeyIfAbsentParams{
		ServiceID: row.ID, PrivateKeyEnc: enc, PublicKey: k.PublicKey, Fingerprint: k.Fingerprint, Source: k.Source,
	}); err != nil {
		return fmt.Errorf("copy deploy key: %w", err)
	}
	if err := r.q.CopyServiceSSHHosts(r.ctx, db.CopyServiceSSHHostsParams{ToServiceID: row.ID, FromServiceID: src.ID}); err != nil {
		return fmt.Errorf("copy ssh host keys: %w", err)
	}
	return nil
}

// copyDomains drops or re-creates the domains of src for row. Hostnames are
// globally unique, so the clone never gets the source's own: a suffixed
// hostname that is taken is reported as a conflict.
func (r *run) copyDomains(src, row db.Service) error {
	doms, err := r.q.ListServiceDomains(r.ctx, src.ID)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	if len(doms) == 0 {
		return nil
	}
	out := func(d db.Domain, to, action, reason string) {
		r.res.Domains = append(r.res.Domains, CloneDomainOutcome{Service: row.Slug, Hostname: d.Hostname, To: to, Action: action, Reason: reason})
	}
	if r.mode == DomainsDrop {
		for _, d := range doms {
			out(d, "", ActionDropped, "Attach new domains in the Networking tab.")
		}
		return nil
	}
	var srv *db.Server
	if row.ServerID != nil {
		if s, err := r.q.LoadServerRow(r.ctx, db.LoadServerRowParams{ID: *row.ServerID}); err == nil {
			srv = &s
		}
	}
	for _, d := range doms {
		if d.Kind != "serve" || d.Component != "" {
			out(d, "", ActionSkipped, "Only public serve domains are cloned; redirects and stack components are not.")
			continue
		}
		var host, action string
		switch {
		case d.Generated:
			// The generated name embeds the environment: make the clone its own.
			base, ok := "", false
			if srv != nil {
				base, ok = domains.BaseDomain(*srv)
			}
			if !ok {
				out(d, "", ActionSkipped, "The clone's server has neither a base domain nor a public IP to generate a hostname from.")
				continue
			}
			host, action = domains.GenerateHostname(row.Slug, r.target.Env.Slug, r.target.ProjectSlug, base), ActionGenerated
		default:
			h, reason := suffixHost(d.Hostname, r.suffix)
			if reason != "" {
				out(d, "", ActionSkipped, reason)
				continue
			}
			host, action = h, ActionSuffixed
		}
		host, err := domains.ValidateHostname(host)
		if err != nil {
			out(d, "", ActionSkipped, err.Error())
			continue
		}
		taken := r.taken[host]
		if !taken {
			n, err := r.q.CountDomainsByHostname(r.ctx, host)
			if err != nil {
				return fmt.Errorf("check hostname: %w", err)
			}
			taken = n > 0
		}
		if taken {
			r.conflicts = append(r.conflicts, host)
			out(d, host, ActionConflict, host+" is already used by another domain; choose another suffix or drop the domains.")
			continue
		}
		r.taken[host] = true
		msg := ""
		if srv != nil {
			msg = domains.DNSGuidance(host, srv.PublicIp)
		}
		tlsMode := "auto" // a custom certificate does not cover the new hostname
		if d.TlsMode == "none" {
			tlsMode = "none"
		}
		nd, err := r.q.InsertDomain(r.ctx, db.InsertDomainParams{
			ServiceID: row.ID, Hostname: host, TargetPort: d.TargetPort, Generated: d.Generated, StatusMessage: msg,
			Kind: "serve", PathPrefix: d.PathPrefix, StripPrefix: d.StripPrefix, AddPrefix: d.AddPrefix, PreservePath: d.PreservePath,
			TlsMode: tlsMode, Options: d.Options, DnsResolver: d.DnsResolver,
		})
		if err != nil {
			return fmt.Errorf("insert domain: %w", err)
		}
		if err := r.copyAuthUsers(d.ID, nd.ID); err != nil {
			return err
		}
		if r.s.Jobs != nil {
			if err := r.s.Jobs.InsertTx(r.ctx, r.tx, domains.DNSCheckArgs{DomainID: &nd.ID}, nil); err != nil {
				return fmt.Errorf("enqueue dns check: %w", err)
			}
		}
		out(d, host, action, "")
	}
	return nil
}

// copyAuthUsers gives a cloned domain the basic-auth users of its source, so a
// protected site is not public in the clone. The hashes are sealed again.
func (r *run) copyAuthUsers(from, to uuid.UUID) error {
	rows, err := r.q.ListDomainAuthHashes(r.ctx, []uuid.UUID{from})
	if err != nil {
		return fmt.Errorf("list domain users: %w", err)
	}
	for _, u := range rows {
		enc, err := r.reseal(secretbox.DomainAuthUsersHash, secretbox.RowID(from.String(), u.Username), secretbox.RowID(to.String(), u.Username), u.HashEnc)
		if err != nil {
			return fmt.Errorf("copy domain user: %w", err)
		}
		if err := r.q.UpsertDomainAuthUser(r.ctx, db.UpsertDomainAuthUserParams{DomainID: to, Username: u.Username, HashEnc: enc}); err != nil {
			return fmt.Errorf("copy domain user: %w", err)
		}
	}
	return nil
}

// suffixHost adds suffix to the first label of host. reason is non-empty when
// the host cannot be suffixed (an apex domain, a label that gets too long).
func suffixHost(host, suffix string) (string, string) {
	labels := strings.Split(host, ".")
	if len(labels) < 3 {
		return "", "An apex domain cannot be suffixed; attach the clone's own domain."
	}
	labels[0] += suffix
	if len(labels[0]) > 63 {
		return "", "The suffixed hostname label would be longer than 63 characters."
	}
	return strings.Join(labels, "."), ""
}

// resolveRefs checks the references of every cloned variable against the target
// environment (its services and shared variables) and records what does not
// resolve.
func (r *run) resolveRefs() error {
	if len(r.refs) == 0 {
		return nil
	}
	svcs, err := r.q.ListAllEnvironmentServices(r.ctx, r.target.Env.ID)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	slugs := map[string]bool{}
	for _, s := range svcs {
		slugs[s.Slug] = true
	}
	shared, err := sharedKeys(r.ctx, r.q, r.target.Env.ProjectID, r.target.Env.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range r.refs {
		var msg string
		switch p.ref.NS {
		case "", varexpr.NSTeam:
			continue
		case varexpr.NSShared:
			if !shared[p.ref.Key] {
				msg = "the shared variable " + p.ref.Key + " does not exist in " + r.target.Env.Name
			}
		default:
			if !slugs[p.ref.NS] {
				msg = "there is no service " + p.ref.NS + " in " + r.target.Env.Name + "; add it or change the reference"
			}
		}
		if msg == "" {
			continue
		}
		k := p.service + "/" + p.key + "/" + p.ref.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		r.res.References = append(r.res.References, ReferenceFinding{Service: p.service, Key: p.key, Ref: p.ref.String(), Status: RefUnresolved, Message: msg})
	}
	sort.SliceStable(r.res.References, func(i, j int) bool {
		a, b := r.res.References[i], r.res.References[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Key < b.Key
	})
	return nil
}

// sharedKeys returns the keys of the shared variables an environment sees
// (project layer and environment layer).
func sharedKeys(ctx context.Context, q *db.Queries, projectID, envID uuid.UUID) (map[string]bool, error) {
	rows, err := q.ListEffectiveSharedVariables(ctx, db.ListEffectiveSharedVariablesParams{ProjectID: projectID, EnvironmentID: &envID})
	if err != nil {
		return nil, fmt.Errorf("list shared variables: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, v := range rows {
		out[v.Key] = true
	}
	return out, nil
}

// conflictError is the 409 of a clone whose suffixed hostnames are taken.
func (r *run) conflictError() error {
	if len(r.conflicts) == 0 {
		return nil
	}
	return apierr.Conflict("domain_conflict", "these hostnames are already in use: "+strings.Join(r.conflicts, ", ")+". Choose another domainSuffix or clone with domains dropped.").
		WithDetails(map[string]any{"hostnames": r.conflicts})
}
