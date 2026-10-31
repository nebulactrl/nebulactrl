package services

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// serviceSlugRe is the shape of a service slug.
var serviceSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// CreateInTx inserts a service inside the caller's transaction. The caller has
// already authorised the environment (LoadEnvironment with projects:write) and
// owns audit, events and everything attached to the service (volumes,
// variables, domains). It validates like Create but does not touch presets.
// A git token or registry credential in in is sealed and stored with the row.
// in.Slug, when set, is used verbatim: it must be a valid, unreserved slug
// (422 validation_failed) that no live service of the environment holds
// (409 slug_conflict); empty derives the slug from the name.
func (s *Service) CreateInTx(ctx context.Context, tx pgx.Tx, env EnvScope, in CreateInput) (db.Service, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return db.Service{}, apierr.Unprocessable("validation_failed", "name is required")
	}
	kind, sourceType, cfg := in.Kind, in.SourceType, in.Config
	kind, sourceType, err := resolveKindSource(kind, sourceType)
	if err != nil {
		return db.Service{}, err
	}
	if sourceType == "" {
		sourceType = SourceImage
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(sourceType); err != nil {
		return db.Service{}, ConfigError(err)
	}
	if err := checkProcessKind(kind, cfg); err != nil {
		return db.Service{}, err
	}
	q := db.New(tx)
	if in.ServerID != nil {
		srv, err := s.checkServer(ctx, q, *in.ServerID)
		if err != nil {
			return db.Service{}, err
		}
		if err := CheckDockerPlacement(cfg, srv.Unrestricted); err != nil {
			return db.Service{}, err
		}
	}
	if kind == KindStack {
		if cfg, err = s.checkStackWrite(ctx, q, cfg, in.ServerID); err != nil {
			return db.Service{}, err
		}
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return db.Service{}, err
	}
	var slug string
	if in.Slug != "" {
		slug = in.Slug
		if !serviceSlugRe.MatchString(slug) {
			return db.Service{}, apierr.Unprocessable("validation_failed", "slug must be lowercase letters, digits and '-', start with a letter or digit and have at most 63 characters")
		}
		if IsReservedSlug(slug) {
			return db.Service{}, apierr.Unprocessable("validation_failed", "the slug "+slug+" is reserved for variable references")
		}
		live, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: env.Env.ID, Base: slug})
		if err != nil {
			return db.Service{}, err
		}
		for _, t := range live {
			if t == slug {
				return db.Service{}, apierr.Conflict("slug_conflict", "this environment already has a service with the slug "+slug)
			}
		}
	} else {
		base := projects.Slugify(name, "service")
		taken, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: env.Env.ID, Base: base})
		if err != nil {
			return db.Service{}, err
		}
		// shared, team and vault are variable namespaces, never service slugs.
		for _, r := range []string{"shared", "team", "vault"} {
			if IsReservedSlug(r) {
				taken = append(taken, r)
			}
		}
		slug = projects.UniqueSlug(base, taken)
	}
	svcID := uuid.New()
	var gitTok []byte
	if in.GitToken != "" {
		if gitTok, err = s.box.SealString(secretbox.ServicesGitToken, svcID.String(), in.GitToken); err != nil {
			return db.Service{}, fmt.Errorf("seal git token: %w", err)
		}
	}
	regAuth, err := s.sealRegistryAuth(svcID, in.RegistryAuth)
	if err != nil {
		return db.Service{}, fmt.Errorf("seal registry auth: %w", err)
	}
	row, err := q.InsertService(ctx, db.InsertServiceParams{
		ID: svcID, GitTokenEnc: gitTok, RegistryAuthEnc: regAuth, EnvironmentID: env.Env.ID, Name: name, Slug: slug, Kind: kind, SourceType: sourceType,
		Config: cfgJSON, ServerID: in.ServerID,
	})
	if in.Slug != "" && IsUniqueViolation(err) {
		return db.Service{}, apierr.Conflict("slug_conflict", "this environment already has a service with the slug "+in.Slug)
	}
	if err != nil {
		return db.Service{}, fmt.Errorf("insert service: %w", err)
	}
	return row, nil
}

// ScopeOf builds the Scope of a service row of env, for events.
func ScopeOf(env EnvScope, row db.Service) Scope {
	return Scope{
		Service: row, WorkspaceID: env.WorkspaceID, ProjectID: env.Env.ProjectID, EnvironmentID: env.Env.ID,
		ProjectSlug: env.ProjectSlug, EnvSlug: env.Env.Slug, EnvName: env.Env.Name, IsProduction: env.Env.IsProduction,
	}
}
