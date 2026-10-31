package previews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// cloneResult describes the services of a freshly cloned preview environment.
type cloneResult struct {
	All      []uuid.UUID
	Affected []uuid.UUID
	Warnings []string
}

// clone copies the base environment of a preview into env inside tx:
//
//   - services whose repository is the PR's build the head commit (a fork PR
//     builds the fork's clone URL); every other service is cloned as is;
//   - databases are created fresh (new credentials, empty volume) through the
//     databases domain; their own variables are never copied;
//   - variables, volumes, links and the environment's shared variables are
//     copied; generator variables are generated again;
//   - generated domains `<service>-pr-<n>.<base>` replace the base domains;
//   - a fork preview gets no secrets: no secret variable, no variable that
//     references a secret shared variable, no git token and no registry auth.
func (s *Service) clone(ctx context.Context, tx pgx.Tx, es services.EnvScope, base db.Environment, set db.ListPreviewTargetsRow, matching map[uuid.UUID]bool, ev PullRequest) (cloneResult, error) {
	q := db.New(tx)
	sys := systemCtx(ctx, es.WorkspaceID, es.Env.ProjectID)
	var res cloneResult

	baseServices, err := q.ListAllEnvironmentServices(ctx, base.ID)
	if err != nil {
		return res, fmt.Errorf("list base services: %w", err)
	}
	excluded := map[string]bool{}
	for _, slug := range set.ExcludedServices {
		excluded[slug] = true
	}
	shared, err := q.ListEffectiveSharedVariables(ctx, db.ListEffectiveSharedVariablesParams{ProjectID: es.Env.ProjectID, EnvironmentID: &base.ID})
	if err != nil {
		return res, fmt.Errorf("list shared variables: %w", err)
	}
	sharedSecret := map[string]bool{}
	for _, sv := range shared {
		sharedSecret[sv.Key] = sv.IsSecret
	}

	newID := map[uuid.UUID]uuid.UUID{}
	for _, b := range baseServices {
		if b.Kind == services.KindRoute || b.Kind == services.KindStack || excluded[b.Slug] { // a stack is skipped: its compose file is not cloned
			continue
		}
		row, err := s.cloneService(sys, tx, es, b, matching[b.ID], ev)
		if err != nil {
			return res, err
		}
		newID[b.ID] = row.ID
		res.All = append(res.All, row.ID)
		if matching[b.ID] {
			res.Affected = append(res.Affected, row.ID)
		}
		sc := services.ScopeOf(es, row)
		isDB := services.DecodeConfig(b.Config).Database != nil
		if !isDB {
			if err := q.CloneServiceVolumes(ctx, db.CloneServiceVolumesParams{NewServiceID: row.ID, OldServiceID: b.ID}); err != nil {
				return res, fmt.Errorf("clone volumes: %w", err)
			}
			if err := s.cloneVariables(ctx, q, sc, b, ev.FromFork, sharedSecret); err != nil {
				return res, err
			}
		}
		warn, err := s.cloneDomain(ctx, tx, es, b, row, ev.Number)
		if err != nil {
			return res, err
		}
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		if !ev.FromFork && s.Git != nil {
			if err := s.Git.CopyServiceGitTx(ctx, tx, b.ID, row.ID); err != nil {
				return res, fmt.Errorf("link %s to its repository: %w", b.Slug, err)
			}
		}
	}

	links, err := q.ListEnvironmentLinks(ctx, base.ID)
	if err != nil {
		return res, fmt.Errorf("list links: %w", err)
	}
	for _, l := range links {
		from, okF := newID[l.FromServiceID]
		to, okT := newID[l.ToServiceID]
		if okF && okT {
			if _, err := q.InsertLink(ctx, db.InsertLinkParams{EnvironmentID: es.Env.ID, FromServiceID: from, ToServiceID: to}); err != nil {
				return res, fmt.Errorf("clone link: %w", err)
			}
		}
	}

	envShared, err := q.ListEnvironmentSharedVariablesRaw(ctx, &base.ID)
	if err != nil {
		return res, fmt.Errorf("list environment shared variables: %w", err)
	}
	for _, v := range envShared {
		if ev.FromFork && v.IsSecret {
			continue
		}
		newID := uuid.New()
		valueEnc, err := s.reseal(secretbox.SharedVariablesValue, v.ID.String(), newID.String(), v.ValueEnc)
		if err != nil {
			return res, fmt.Errorf("copy shared variable %s: %w", v.Key, err)
		}
		templateEnc, err := s.reseal(secretbox.SharedVariablesTemplate, v.ID.String(), newID.String(), v.TemplateEnc)
		if err != nil {
			return res, fmt.Errorf("copy shared variable %s template: %w", v.Key, err)
		}
		if err := q.CopySharedVariable(ctx, db.CopySharedVariableParams{
			ID: newID, WorkspaceID: v.WorkspaceID, ProjectID: v.ProjectID, EnvironmentID: &es.Env.ID, Key: v.Key, ValueEnc: valueEnc,
			IsSecret: v.IsSecret, Source: v.Source, TemplateEnc: templateEnc, GenCalls: v.GenCalls, Comment: v.Comment,
		}); err != nil {
			return res, fmt.Errorf("copy shared variable: %w", err)
		}
	}

	if err := canvas.CloneLayout(ctx, q, base.ID, es.Env.ID, newID); err != nil {
		return res, err
	}
	return res, nil
}

// cloneService creates the service row of one base service. Databases go
// through the databases domain so their credentials are generated afresh.
func (s *Service) cloneService(sys context.Context, tx pgx.Tx, es services.EnvScope, b db.Service, matches bool, ev PullRequest) (db.Service, error) {
	q := db.New(tx)
	cfg := services.DecodeConfig(b.Config)
	if cfg.Database != nil {
		if s.Databases == nil {
			return db.Service{}, apierr.Unprocessable("databases_unavailable", "this server build cannot create managed databases")
		}
		row, err := s.Databases.CreateTx(sys, tx, databases.CreateInput{
			EnvironmentID: es.Env.ID, Name: b.Name, Engine: cfg.Database.Engine, Version: cfg.Database.Version,
			Flavor: cfg.Database.Flavor, DatabaseName: cfg.Database.DatabaseName, ServerID: b.ServerID, AcceptLicense: true,
		})
		if err != nil {
			return db.Service{}, fmt.Errorf("create preview database %s: %w", b.Slug, err)
		}
		if row.Slug != b.Slug {
			// References (${{slug.KEY}}) name the base service's slug.
			if err := q.UpdateServiceSlug(sys, db.UpdateServiceSlugParams{ID: row.ID, Slug: b.Slug}); err != nil {
				return db.Service{}, fmt.Errorf("set slug: %w", err)
			}
			row.Slug = b.Slug
		}
		return row, nil
	}
	if matches && b.SourceType == services.SourceGit {
		cfg.GitBranch = ev.HeadBranch
		if ev.FromFork && ev.HeadCloneURL != "" {
			cfg.GitURL = ev.HeadCloneURL
		}
	}
	// A preview is never put on the tailnet: one tagged device per pull request
	// would eat the device quota, and Funnel would publish it.
	cfg.Tailnet = nil
	raw, err := json.Marshal(cfg)
	if err != nil {
		return db.Service{}, err
	}
	// Sealed credentials are bound to their row: the clone's copies are opened
	// with the base service's id and sealed again for the new service's id.
	newID := uuid.New()
	var gitTok, regAuth []byte
	if !ev.FromFork {
		var err error
		if gitTok, err = s.reseal(secretbox.ServicesGitToken, b.ID.String(), newID.String(), b.GitTokenEnc); err != nil {
			return db.Service{}, fmt.Errorf("copy git token of %s: %w", b.Slug, err)
		}
		if regAuth, err = s.reseal(secretbox.ServicesRegistryAuth, b.ID.String(), newID.String(), b.RegistryAuthEnc); err != nil {
			return db.Service{}, fmt.Errorf("copy registry auth of %s: %w", b.Slug, err)
		}
	}
	row, err := q.InsertService(sys, db.InsertServiceParams{
		ID: newID, EnvironmentID: es.Env.ID, Name: b.Name, Slug: b.Slug, Kind: b.Kind, SourceType: b.SourceType, Config: raw,
		GitTokenEnc: gitTok, RegistryAuthEnc: regAuth, ServerID: b.ServerID,
	})
	if err != nil {
		return db.Service{}, fmt.Errorf("insert preview service %s: %w", b.Slug, err)
	}
	if !ev.FromFork && b.SourceType == services.SourceGit {
		// The preview clones the same repository: it uses the same deploy key and
		// pinned host keys (a fork's code never gets them).
		if err := s.copySSHKey(sys, q, b.ID, row.ID); err != nil {
			return db.Service{}, fmt.Errorf("copy deploy key: %w", err)
		}
		if err := q.CopyServiceSSHHosts(sys, db.CopyServiceSSHHostsParams{ToServiceID: row.ID, FromServiceID: b.ID}); err != nil {
			return db.Service{}, fmt.Errorf("copy ssh host keys: %w", err)
		}
	}
	return row, nil
}

// reseal opens a sealed column value of one row and seals it for another row
// of the same column. Empty input stays empty.
func (s *Service) reseal(col secretbox.Column, fromRow, toRow string, enc []byte) ([]byte, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	plain, err := s.Box.Open(col, fromRow, enc)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(plain)
	return s.Box.Seal(col, toRow, plain)
}

// copySSHKey gives the service `to` its own copy of the deploy key of `from`
// (nothing when `from` has none).
func (s *Service) copySSHKey(ctx context.Context, q *db.Queries, from, to uuid.UUID) error {
	k, err := q.GetServiceSSHKey(ctx, from)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	enc, err := s.reseal(secretbox.ServiceSSHKeysPrivateKey, from.String(), to.String(), k.PrivateKeyEnc)
	if err != nil {
		return err
	}
	return q.InsertServiceSSHKeyIfAbsent(ctx, db.InsertServiceSSHKeyIfAbsentParams{
		ServiceID: to, PrivateKeyEnc: enc, PublicKey: k.PublicKey, Fingerprint: k.Fingerprint, Source: k.Source,
	})
}

// cloneVariables copies the service's variables. Fork previews skip every
// secret and every variable that references a secret shared or vault value.
func (s *Service) cloneVariables(ctx context.Context, q *db.Queries, sc services.Scope, b db.Service, fork bool, sharedSecret map[string]bool) error {
	vars, err := q.ListServiceVariables(ctx, b.ID)
	if err != nil {
		return fmt.Errorf("list variables: %w", err)
	}
	for _, v := range vars {
		val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(b.ID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return fmt.Errorf("decrypt %s.%s: %w", b.Slug, v.Key, err)
		}
		if fork && (v.IsSecret || referencesSecret(val, sharedSecret)) {
			continue
		}
		w := services.VarWrite{Key: v.Key, Value: val, Secret: v.IsSecret, Scope: v.Scope, Source: v.Source}
		comment := v.Comment
		w.Comment = &comment
		if len(v.TemplateEnc) > 0 {
			tmpl, err := s.Box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(b.ID.String(), v.Key), v.TemplateEnc)
			if err != nil {
				return fmt.Errorf("decrypt template of %s.%s: %w", b.Slug, v.Key, err)
			}
			// Generated values are generated again: a preview never shares a
			// generated secret with the base environment.
			if m, err := varexpr.Materialize(tmpl); err == nil {
				w.Value, w.Template, w.GenCalls, w.Secret = m.Stored, tmpl, m.Calls, v.IsSecret || m.Secret
			}
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.Box, sc, w); err != nil {
			return fmt.Errorf("write %s.%s: %w", b.Slug, v.Key, err)
		}
	}
	return nil
}

// referencesSecret reports whether a variable value references a secret
// shared variable or anything from an external secret manager.
func referencesSecret(value string, sharedSecret map[string]bool) bool {
	tpl, err := varexpr.Parse(value)
	if err != nil {
		return false
	}
	if len(tpl.ExtRefs()) > 0 {
		return true // external secret manager values are secrets
	}
	for _, r := range tpl.Refs() {
		if r.NS == varexpr.NSShared && sharedSecret[r.Key] {
			return true
		}
	}
	return false
}

// cloneDomain gives the clone a generated domain when the base service has a
// public one. warn is non-empty when none could be generated.
func (s *Service) cloneDomain(ctx context.Context, tx pgx.Tx, es services.EnvScope, b, row db.Service, number int) (warn string, err error) {
	q := db.New(tx)
	doms, err := q.ListServiceDomains(ctx, b.ID)
	if err != nil {
		return "", fmt.Errorf("list domains: %w", err)
	}
	var first *db.Domain
	for i := range doms {
		if doms[i].Kind == "serve" {
			first = &doms[i]
			break
		}
	}
	if first == nil {
		return "", nil
	}
	if row.ServerID == nil {
		return "No domain was generated for " + b.Name + ": the service has no server.", nil
	}
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *row.ServerID})
	if err != nil {
		return "", fmt.Errorf("load server: %w", err)
	}
	base, ok := domains.BaseDomain(srv)
	if !ok {
		return "No domain was generated for " + b.Name + ": the server has neither a base domain nor a public IP.", nil
	}
	host, err := s.freeHostname(ctx, q, b.Slug, es.ProjectSlug, number, base)
	if err != nil {
		return "", err
	}
	dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
		ServiceID: row.ID, Hostname: host, TargetPort: first.TargetPort, Generated: true, StatusMessage: domains.DNSGuidance(host, srv.PublicIp),
	})
	if err != nil {
		return "", fmt.Errorf("insert domain: %w", err)
	}
	if _, err := q.BumpServiceGeneration(ctx, row.ID); err != nil {
		return "", err
	}
	if s.Jobs != nil {
		if err := s.Jobs.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
			return "", fmt.Errorf("enqueue dns check: %w", err)
		}
	}
	return "", nil
}

// freeHostname returns `<service>-pr-<n>.<base>`, falling back to a name that
// includes the project slug and then a counter when the hostname is taken.
func (s *Service) freeHostname(ctx context.Context, q *db.Queries, serviceSlug, projectSlug string, number int, base string) (string, error) {
	first := serviceSlug + "-pr-" + strconv.Itoa(number)
	labels := []string{first, first + "-" + projectSlug}
	for i := 2; i < 10; i++ {
		labels = append(labels, first+"-"+projectSlug+"-"+strconv.Itoa(i))
	}
	for _, l := range labels {
		host := truncateLabel(l) + "." + base
		n, err := q.CountDomainsByHostname(ctx, host)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return host, nil
		}
	}
	return "", apierr.Conflict("domain_conflict", "no free preview hostname for "+serviceSlug)
}

func truncateLabel(l string) string {
	if len(l) <= 63 {
		return l
	}
	return strings.Trim(l[:63], "-")
}

// uniqueEnvSlug picks the slug of a preview environment: `pr-<n>`, made unique
// within the project.
func uniqueEnvSlug(ctx context.Context, q *db.Queries, projectID uuid.UUID, number int) (string, error) {
	base := "pr-" + strconv.Itoa(number)
	taken, err := q.ListEnvironmentSlugsLike(ctx, db.ListEnvironmentSlugsLikeParams{ProjectID: projectID, Base: base})
	if err != nil {
		return "", err
	}
	return projects.UniqueSlug(base, taken), nil
}
